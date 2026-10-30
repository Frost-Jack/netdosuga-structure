# Payment mock

Independent local-only HTTP mock on :8085. Requires APP_ENV=local, MOCK_MODE=true and DATABASE_URL; owns only payment_mock.effects in PostgreSQL. No cards or external provider calls.

POST /v1/payments with order_id, amount_cents, currency RUB, scenario success/error/delay/timeout and Idempotency-Key payment:<order_id>. POST /v1/refunds with order_id, amount_cents, scenario and refund:<order_id>. Success responds mock:true, provider ID, order_id, amount_cents, status SUCCEEDED. Error returns 503; timeout waits up to ten seconds and never persists an effect, including after client cancellation. Delay waits 300ms. Repeat success returns the persisted response across restarts; mismatch returns 409. Refund requires an existing payment for the same amount. Calls serialize by order through PostgreSQL advisory transaction locks.

`go test ./...` checks strict input rejection. Set PAYMENT_TEST_DATABASE_URL to run unique-row PostgreSQL checks for error/timeout side effects, restart durability, refund and mismatch behavior; the test removes only its own UUID order rows.

GET /v1/payments/{order_id} resolves a committed payment or404 under the same per-order lock. POST /v1/payments/{order_id}/cancel with {amount_cents} and Idempotency-Key refund:<order_id> atomically refunds any persisted payment and writes a terminal cancellation fence. Its response is mock:true/status:SUCCEEDED/cancelled:true with refund_id only when an actual payment existed. The fence persists across restarts and rejects every later authorization with409, including a delayed first request. This endpoint is used when an order has a durable payment intent but its own transaction did not commit the provider ID.
