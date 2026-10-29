# gateway

Local demo HTTP entry point and gRPC client. This folder is an independent Go module and Docker build context; it needs no parent Go module.

Build/test from this folder: `go test ./...`, `docker build -t netdosuga-gateway .`. The Docker build runs tests before compiling. Full integration startup and smoke checks belong to the sibling `platform/` project.

Startup requires both `APP_ENV=local` and `MOCK_MODE=true`. Defaults: `HTTP_ADDR=:8080`, `ORDER_GRPC_ADDR=order-service:50051`, `ORDER_GRPC_TIMEOUT=3s`. The timeout must be positive. OTLP/gRPC uses `OTEL_EXPORTER_OTLP_ENDPOINT=jaeger:4317`; `OTEL_SERVICE_NAME` defaults to `gateway`.

`POST /api/v1/demo/orders` accepts `{"scenario":"success"}`; scenarios are `success`, `delay`, `error`, and `timeout`. Success/delay return HTTP 200 with `mock=true`, synthetic order ID, event ID, and trace ID. Dependency errors return 502, timeouts 504, and invalid input 400. `api/openapi.yaml` describes this HTTP contract. `api/order.proto` is copied from order-service and must stay identical; generated Go files have module-specific imports.

`GET /healthz` reports liveness, `/readyz` calls order-service gRPC health, and `/metrics` exports HTTP counters/histograms plus Go/process metrics. JSON stdout logs include service, timestamp, level, message and operation trace/span IDs. W3C trace context propagates through gRPC. SIGTERM triggers bounded HTTP shutdown and telemetry flush. The container runs as UID 10001.

The infrastructure and business workflows use demonstration data and mock integrations. There is no production authentication, real payment, or production routing.


The business demo panel is embedded at `/`; no npm build is needed. Switch among five explicit demo identities to submit and review vendor applications, edit and publish offers, browse the catalogue, create a cart and order, simulate payment outcomes, inspect tickets, cancel/refund, redeem tickets, and inspect sales. Money is stored in cents and displayed as rubles. These identities are not production authentication.

`/api/v1/shop/*` routes are a fixed allowlist translated to `Shop` RPC commands. `X-Demo-Actor`, opaque path IDs and `Idempotency-Key` are forwarded separately; only catalogue city/category query fields are forwarded. JSON object bodies are bounded to 64 KiB. Checkout requires an idempotency key. Business calls have at least a five-second gRPC deadline so the bounded payment mock deadline can return its own error. Old infrastructure demo behavior and deadline remain unchanged.

Business responses always include `demo:true` and the gateway trace ID. Domain HTTP status codes pass through; transport failures return 502/504. `X-Gateway-Instance` and `X-Order-Instance` expose the serving replicas without adding metric label cardinality. The panel links the latest request to local Jaeger. OpenAPI describes every business route and response shape.

