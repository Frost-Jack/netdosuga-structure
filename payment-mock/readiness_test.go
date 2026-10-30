package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestWritableReadinessRejectsAndDiscardsReadOnlySession(t *testing.T) {
	url := os.Getenv("READINESS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set READINESS_TEST_DATABASE_URL for isolated PostgreSQL readiness regression")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, "SET default_transaction_read_only=on"); err != nil {
		t.Fatal(err)
	}
	if writableDB(ctx, pool) == nil {
		t.Fatal("read-only session incorrectly marked ready")
	}
	if err = writableDB(ctx, pool); err != nil {
		t.Fatal("stale read-only session was not discarded", err)
	}
}
