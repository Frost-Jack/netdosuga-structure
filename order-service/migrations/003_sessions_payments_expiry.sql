ALTER TABLE business_orders ADD COLUMN IF NOT EXISTS expires_at timestamptz;
UPDATE business_orders SET expires_at=created_at+interval '15 minutes' WHERE expires_at IS NULL;
ALTER TABLE business_orders ALTER COLUMN expires_at SET DEFAULT (now()+interval '15 minutes');
ALTER TABLE business_orders ADD COLUMN IF NOT EXISTS payment_id text;
ALTER TABLE business_orders ADD COLUMN IF NOT EXISTS refund_id text;
CREATE INDEX IF NOT EXISTS business_reserved_expiry ON business_orders(expires_at) WHERE status='RESERVED';

