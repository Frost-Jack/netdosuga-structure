# order-service

Local demo gRPC service with a legacy technical receipt flow and a persistent business shop flow backed by a transactional outbox. This folder is an independent Go module and Docker build context.

Build/test from this folder: `go test ./...`, `docker build -t netdosuga-order-service .`. Docker runs tests before compiling. Integration startup and smoke checks belong to the sibling `platform/` project.

Startup requires `APP_ENV=local`, `MOCK_MODE=true`, and `DATABASE_URL`. Defaults: `GRPC_ADDR=:50051`, `HTTP_ADDR=:8081`, `MOCK_BASE_URL=http://integration-mock:8083`, `MOCK_HTTP_TIMEOUT=1s`, `KAFKA_BROKERS=kafka:9092` (comma-separated), `KAFKA_TOPIC=netdosuga.demo.orders.v1`. Timeouts must be positive. OTLP/gRPC defaults to `jaeger:4317` through `OTEL_EXPORTER_OTLP_ENDPOINT`; `OTEL_SERVICE_NAME` defaults to `order-service`.

`api/order.proto` defines legacy `CreateDemoOrder` and business `Shop`; gateway keeps an identical source copy with generated module-specific Go imports. Scenarios are `success`, `delay`, `error`, and `timeout`. Only successful payment fixtures produce a receipt and event. Dependency errors use gRPC Unavailable; payment timeouts use DeadlineExceeded. `api/demo-order.schema.json` defines the event. Migration 001 creates legacy `demo_receipts`; migration 002 adds the business tables described below.

For legacy `CreateDemoOrder`, the database insert and Kafka publish are sequential and non-atomic. A publish failure can leave a receipt; retries can produce new event IDs. That legacy flow has no outbox or exactly-once guarantee. Business `Shop` commands use the transactional outbox described below.

`GET /healthz` reports liveness. `/readyz` and gRPC health check PostgreSQL and mock readiness. Kafka availability is excluded so business writes can queue in the outbox during an outage. `/metrics` exports HTTP and gRPC counters/histograms plus Go/process metrics. OpenTelemetry spans cover gRPC, mock HTTP, insert, and Kafka publish; W3C headers propagate with the event. JSON stdout logs include timestamp, level, service, message and operation trace/span IDs. SIGTERM bounds gRPC/HTTP shutdown and flushes traces. Container UID is 10001.

Business demo includes integer-cent totals and commission arithmetic. Real providers, personal data and production order processing remain outside scope. `tools/Dockerfile.proto` contains the pinned protobuf generation toolchain; generated files are checked in for independent builds.


## Business demo (sprint 2)

`Shop` adds the fixed command allowlist in `api/order.proto`; HTTP mapping lives in the gateway. Persistent applications, offers, carts, orders, items, tickets and outbox use migration 002. Migration startup is serialized with a PostgreSQL transaction advisory lock and recorded in `schema_migrations`. Three public synthetic offers seed once without resetting existing data.

Server identities: customer-demo/customer-two, vendor-demo/vendor-two, admin-demo. Roles and ownership are checked in the service. Public catalog accepts an empty actor; unknown identities return 403. These actors are demonstration switches, not authentication. Amounts are integer cents; commission is 10%, rounded down per item line. Quantity is 1..20. Cart additions replace the offer quantity. Checkout requires a customer-scoped idempotency key. Checkout locks offers in ID order; payment, cancellation and redemption serialize on the order. Failed mock payment/refund rolls back every SQL change. Paid orders can cancel only before any redemption. Stock stays reserved until explicit cancellation: reservation expiry is future work.

`BUSINESS_KAFKA_TOPIC=netdosuga.business.events.v1` is separate from the unchanged legacy topic. Outbox inserts commit with state, publishers claim rows with `FOR UPDATE SKIP LOCKED`, and retry after Kafka outages. Delivery is at least once: publish success before DB commit can replay. Notification deduplicates event IDs durably. Event order is best effort across concurrent publishers; notification sends status facts and does not reconstruct order state. W3C context is saved in outbox and restored for a producer span. Readiness requires DB and mock; Kafka outage queues business events while the legacy immediate-publish command can fail.

Mock payments/refunds are deterministic synthetic fixtures without an external financial effect. Their HTTP call runs inside the order transaction; a database failure after mock success may repeat the fixture on retry. This is not a production payment saga or provider idempotency implementation.

Run SQL concurrency/ownership/idempotency/refund tests against an isolated schema with `TEST_DATABASE_URL=... go test -run TestBusinessDatabase -v`. The test creates and drops only a unique test schema. Normal builds run the remaining tests and skip that test when no URL is supplied.

