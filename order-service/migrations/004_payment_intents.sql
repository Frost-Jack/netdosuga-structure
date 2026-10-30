CREATE TABLE IF NOT EXISTS business_payment_attempts(order_id text PRIMARY KEY,amount_cents bigint NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
