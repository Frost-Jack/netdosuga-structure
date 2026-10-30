package main

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ping succeeds on a standby. A ready writer must hold a writable primary
// session; discard a stale standby session so the next pool acquire can recover.
func writableDB(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var writable bool
	err = conn.QueryRow(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&writable)
	if err != nil {
		return err
	}
	if !writable {
		conn.Hijack().Close(ctx)
		return fmt.Errorf("writable primary unavailable")
	}
	return nil
}
