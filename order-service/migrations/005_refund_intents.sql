CREATE TABLE IF NOT EXISTS business_refund_attempts(order_id text PRIMARY KEY,attempt_count bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now());
