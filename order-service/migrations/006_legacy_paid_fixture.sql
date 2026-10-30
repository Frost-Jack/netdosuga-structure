ALTER TABLE business_orders ADD COLUMN IF NOT EXISTS legacy_fixture_payment boolean NOT NULL DEFAULT false;
UPDATE business_orders SET legacy_fixture_payment=true WHERE status='PAID' AND payment_id IS NULL;
