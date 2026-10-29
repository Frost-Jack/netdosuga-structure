# integration-mock

Local synthetic HTTP adapter fixture, with no SMTP or payment-provider connection. This folder is a standalone Go module and Docker build context.

Build/test: `go test ./...`, `docker build -t netdosuga-integration-mock .`.
Run with `APP_ENV=local MOCK_MODE=true`; other environments fail startup.
HTTP defaults to `:8083` (`HTTP_ADDR`). `MOCK_DELAY=250ms` and `MOCK_TIMEOUT=1500ms` accept positive durations up to 5s.

`POST /mock/payments` accepts scenario success/error/delay/timeout. Error returns 503. Delay and timeout sleep for their configured duration; a cancelled caller produces an error span.
Legacy `POST /mock/emails` accepts only synthetic mock/event_id/order_id fields with the fixed demo order ID. Legacy `GET /mock/deliveries` returns the last 100 legacy deliveries, oldest first, including the propagated trace ID. History is volatile and may include duplicates. No recipient addresses are accepted.

`/healthz` and `/readyz` report process readiness: this in-memory fixture has no runtime dependencies. `/metrics` exports bounded HTTP labels and Go/process metrics. OTLP/gRPC defaults to `jaeger:4317` (`OTEL_EXPORTER_OTLP_ENDPOINT`), service name `integration-mock` (`OTEL_SERVICE_NAME`). W3C context propagates from HTTP headers. Logs are JSON stdout with operation trace/span IDs. SIGTERM allows 5s HTTP shutdown plus 3s telemetry flush. Docker runs UID 10001.

See `openapi.json` for the HTTP contract. The parent platform supplies the local Compose network and automated integration smoke.

Business demo adds `/mock/refunds` with the same success/error/delay/timeout fixture behavior as payments and `/mock/business-emails` accepting opaque synthetic order IDs. Business email event IDs are deduplicated in the bounded in-memory delivery list; no SMTP or refund provider is called. The list is demonstration evidence, not durable delivery storage. Legacy endpoints remain compatible.

Business delivery evidence is exposed by GET /mock/business-deliveries. Legacy GET /mock/deliveries remains limited to the legacy fixture and its original schema.

