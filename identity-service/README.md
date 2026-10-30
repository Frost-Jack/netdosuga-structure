# identity-service

Independent Go module/Docker context serving HTTP `:8084`. Required: `DATABASE_URL`. Optional: `HTTP_ADDR`, `MVP_SEED_PASSWORD` (local default `demo12345`), `OTEL_EXPORTER_OTLP_ENDPOINT` (default `jaeger:4317`). Tables belong to the `identity` schema.

POST `/v1/register` accepts email/password/name and customer or vendor role. POST `/v1/login` returns user, opaque session_token and expires_at. GET `/v1/session` resolves a bearer; POST `/v1/logout` revokes it. Passwords use bcrypt; only SHA-256 hashes of random 256-bit tokens are persisted, with 24-hour expiry. Seed accounts preserve existing IDs/data and never overwrite credentials. Registration cannot create admins. Bodies/fields and login attempts are bounded; login throttles by normalized account, registration by peer.

GET `/healthz`, `/readyz`, `/metrics` provide diagnostics. Database connects require read-write mode; readiness rejects standby/read-only sessions and retires pooled connections so the next acquisition reconnects. Auth bodies and headers never enter logs or trace payloads. Spans cover password work, sessions and actual SQL; operation labels are fixed. `go test ./...` runs unit tests. `TEST_DATABASE_URL=... go test -count=1 ./...` adds registration/login/logout/expiry/hash-only storage checks inside a disposable schema.
