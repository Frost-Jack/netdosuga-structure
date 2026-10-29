CREATE TABLE IF NOT EXISTS demo_receipts (
  event_id text PRIMARY KEY,
  scenario text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
