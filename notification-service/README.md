# notification-service

Local demo Kafka consumers for legacy receipts and business events, with an HTTP email adapter client and durable business-event deduplication. This folder is a standalone Go module and Docker build context.

Build/test: `go test ./...`, `docker build -t netdosuga-notification-service .`.
Startup requires `APP_ENV=local MOCK_MODE=true` and `DATABASE_URL`. Defaults: `HTTP_ADDR=:8082`, `KAFKA_BROKERS=kafka:9092` (comma-separated), `KAFKA_TOPIC=netdosuga.demo.orders.v1`, `KAFKA_GROUP_ID=netdosuga-demo-notification-v1`, `MOCK_BASE_URL=http://integration-mock:8083`.

Both consumer groups start at the earliest offset. In the legacy receipt reader, processing is sequential; offset commits happen only after HTTP 200 from `/mock/emails`. Network errors and invalid events retry the same record every second until shutdown. A poison event therefore blocks the legacy reader. Legacy delivery can repeat after a commit failure or restart. The separate business reader quarantines poison records and durably deduplicates IDs, as described below. Neither flow claims exactly-once external email.

`/healthz` reports process liveness. `/readyz` checks PostgreSQL, legacy Kafka topic metadata and mock readiness under bounded timeouts. `/metrics` exports finite HTTP labels, `netdosuga_events_total{service,outcome}` with bounded success/error/duplicate/poison outcomes, and Go/process metrics. Kafka W3C headers establish the consumer parent; HTTP client spans inject the same trace into the mock. OTLP/gRPC defaults to `jaeger:4317` (`OTEL_EXPORTER_OTLP_ENDPOINT`), service name `notification-service` (`OTEL_SERVICE_NAME`). JSON stdout logs carry timestamp/level/service/message and operation trace/span IDs.

SIGTERM cancels fetch/retry/HTTP operations, bounds shutdown to 5s plus a 3s telemetry flush. Container UID is 10001. Only synthetic IDs are sent, without recipient addresses or real providers. `event.schema.json` fixes the legacy consumed event; `openapi.json` describes operational endpoints. Integration checks run from the sibling platform folder.

## Business events (sprint 2)

A separate reader consumes `BUSINESS_KAFKA_TOPIC=netdosuga.business.events.v1`, group `netdosuga-business-notification-v1`. `DATABASE_URL` is required. Startup creates durable `business_notifications` and `business_poison` tables under an advisory migration lock. Business event IDs serialize delivery and are recorded after mock HTTP success; replays skip delivered IDs across restarts. Malformed/unsupported business records are quarantined by topic/partition/offset and committed, so they cannot block subsequent records. Transient failures retry once per second without skipping records. The legacy reader and contract remain unchanged.

Business mail uses `/mock/business-emails`, synthetic opaque order IDs, and W3C trace headers. The adapter deduplicates event IDs in its bounded in-memory delivery list, covering a consumer crash after HTTP success before SQL commit while the adapter remains running. SQL dedup is durable, but the mock endpoint's delivery list is volatile; simultaneous adapter restart and that crash window may repeat a synthetic delivery. This does not claim exactly-once external email.

Business delivery evidence: GET /mock/business-deliveries; the legacy delivery endpoint is unchanged.

