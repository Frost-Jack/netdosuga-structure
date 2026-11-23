package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"strings"
	"testing"
)

func TestSQLTemplate(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT column1 FROM users WHERE id=$1 AND email='SECRET'", "SELECT column1 FROM users WHERE id=$1 AND email=?"},
		{"SELECT 'SE''CRET', E'SEC\\'RET', 123, -1.2e-9, .5, 0xdeadbeef", "SELECT ?, E?, ?, -?, ?, ?"},
		{"SELECT $$SECRET$$, $tag$SECRET$tag$, $tag9$SECRET$tag9$, $метка$SECRET$метка$, $2", "SELECT ?, ?, ?, ?, $2"},
		{"/* SECRET /* nested */ other */ SELECT 1 -- SECRET\n FROM users", "SELECT ? FROM users"},
		{`SELECT "SECRET", "SEC""RET" FROM users`, `SELECT "?", "?" FROM users`},
		{"SELECT 'unterminated SECRET", "SELECT ?"},
		{"SELECT 'ambiguous\\', 'SECRET'", "[query omitted: ambiguous string escape]"},
	} {
		if got := sqlTemplate(tc.sql); got != tc.want {
			t.Errorf("template = %q, want %q", got, tc.want)
		}
	}
	for _, sql := range []string{strings.Repeat("SELECT column1, ", 1000), strings.Repeat("x", 65537)} {
		if got := sqlTemplate(sql); len(got) > 2048 {
			t.Fatal("unbounded template")
		}
	}
}
func TestQueryTraceDetailsAndRedaction(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	parentCtx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	tracer := QueryTracer{}
	ctx := tracer.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{SQL: "SELECT id FROM users WHERE email=$1 AND password='SECRET_SQL'", Args: []any{"SECRET_ARG"}})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 2")})
	errCtx := tracer.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{SQL: "UPDATE users SET email=$1", Args: []any{"SECRET_ARG"}})
	tracer.TraceQueryEnd(errCtx, nil, pgx.TraceQueryEndData{Err: errors.New("SECRET_ERROR")})
	parent.End()
	spans := recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("got %d spans", len(spans))
	}
	if spans[0].Name() != "db.SELECT" || spans[0].Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("span name/parent lost")
	}
	values := map[string]string{}
	for _, span := range spans {
		for _, a := range span.Attributes() {
			if strings.Contains(a.Value.Emit(), "SECRET") {
				t.Fatal("secret in attributes")
			}
			if span.Name() == "db.SELECT" {
				values[string(a.Key)] = a.Value.Emit()
			}
		}
		if strings.Contains(span.Status().Description, "SECRET") || len(span.Events()) != 0 {
			t.Fatal("raw error exported")
		}
	}
	if values["db.query.text"] != "SELECT id FROM users WHERE email=$1 AND password=?" || values["db.query.parameter_count"] != "1" || values["db.response.returned_rows"] != "2" {
		t.Fatalf("details missing: %v", values)
	}
	if spans[1].Status().Code != codes.Error || spans[1].Status().Description != "database error" {
		t.Fatal("error status missing")
	}
}
