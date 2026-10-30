package observe

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Writable rejects a demoted server and session-level read-only settings. Retire
// existing sessions so the next acquisition uses the read-write connect validator.
func Writable(ctx context.Context, pool *pgxpool.Pool) error {
	var writable bool
	if err := pool.QueryRow(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&writable); err != nil {
		return err
	}
	if !writable {
		pool.Reset()
		return fmt.Errorf("writable database unavailable")
	}
	return nil
}
