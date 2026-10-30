package observe

import (
	"context"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func TestWritableRejectsAndRetiresReadOnlySession(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsReadWrite
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Writable(ctx, pool); err != nil {
		t.Fatal("primary rejected", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	poisonedPID := conn.Conn().PgConn().PID()
	if _, err = conn.Exec(ctx, "SET default_transaction_read_only=on"); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	if err = Writable(ctx, pool); err == nil {
		t.Fatal("read-only session reported ready")
	}
	if err = Writable(ctx, pool); err != nil {
		t.Fatal("pool did not recover", err)
	}
	conn, err = pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if conn.Conn().PgConn().PID() == poisonedPID {
		t.Fatal("read-only session remained pooled")
	}
}
