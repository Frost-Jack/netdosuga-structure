package main

import (
	"context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"testing"
)

func TestOutboxHeadersPropagateProducerContext(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(previous)
	provider := sdktrace.NewTracerProvider()
	defer provider.Shutdown(context.Background())
	tracer := provider.Tracer("test")
	enqueueCtx, enqueue := tracer.Start(context.Background(), "outbox.enqueue")
	defer enqueue.End()
	stored := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(enqueueCtx, stored)
	restored := otel.GetTextMapPropagator().Extract(context.Background(), stored)
	publishCtx, publish := tracer.Start(restored, "outbox.publish", trace.WithSpanKind(trace.SpanKindProducer))
	defer publish.End()
	received := propagation.MapCarrier{}
	for _, header := range outboxTraceHeaders(publishCtx) {
		received[header.Key] = string(header.Value)
	}
	consumerCtx := otel.GetTextMapPropagator().Extract(context.Background(), received)
	remote := trace.SpanContextFromContext(consumerCtx)
	if remote.TraceID() != enqueue.SpanContext().TraceID() || remote.SpanID() != publish.SpanContext().SpanID() || !remote.IsRemote() {
		t.Fatal("consumer must receive the producer span in the original trace")
	}
	if remote.SpanID() == enqueue.SpanContext().SpanID() {
		t.Fatal("enqueue context propagated instead of producer")
	}
}
