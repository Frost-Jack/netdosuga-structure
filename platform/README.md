# Local integration platform

This is a synthetic demo: no real payments, email recipients, orders, or production deployment. Each application builds from its own sibling directory. Keep `platform`, `gateway`, `order-service`, `notification-service`, and `integration-mock` beside each other after splitting repositories. Docker Desktop must run Linux containers; Docker Compose v2 and PowerShell are required.

From the workspace root, run these exact PowerShell commands:

```powershell
Set-Location platform
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\setup.ps1
docker compose config --quiet
docker compose up -d --build --wait --wait-timeout 240
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify.ps1
```

Setup creates random local PostgreSQL/Grafana passwords in ignored `.env`, refuses to overwrite an existing file, and prints no credentials. `config --quiet` validates without printing resolved credentials. Keep `.env` between restarts. `.env.example` documents names only. Grafana username is `demo`; read its generated password locally from `.env` when logging into the UI. These credentials are local demo credentials only.

Only four loopback ports are published: gateway http://127.0.0.1:8080, Prometheus http://127.0.0.1:9090, Grafana http://127.0.0.1:3000, and Jaeger http://127.0.0.1:16686. PostgreSQL, Kafka, OTLP and mock APIs are internal. No container mounts a Docker socket. Every application must reject startup unless `APP_ENV=local` and `MOCK_MODE=true`.

```powershell
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/api/v1/demo/orders -ContentType application/json -Body '{"scenario":"success"}'
docker compose ps
docker compose logs --tail 100 gateway order-service notification-service integration-mock
```

The returned `trace_id` identifies the complete request, payment, Kafka consumer and mock email trace in Jaeger. Search application JSON stdout by it:

```powershell
$traceId = 'paste_returned_trace_id'
docker compose logs --no-color gateway order-service notification-service integration-mock | Select-String $traceId
```

The provisioned Grafana dashboard **Netdosuga technical demo** uses the provisioned Prometheus datasource. Eight panels show HTTP request rate, HTTP p95 latency, gRPC counts, consumer outcomes, HTTP 5xx rate, availability, process memory and gRPC p95 latency. Prometheus `/targets` should show four application targets UP in the baseline, or six targets in the HA stack (two gateway, two order, notification and integration mock). Metrics are scraped every five seconds. Smoke waits for enough scrape samples and executes each panel query through Grafana's datasource proxy.

Verification runs the Python standard-library container on the Compose network, then checks PostgreSQL receipts and trace-correlated JSON logs with Compose CLI. Reports are saved under ignored `test-results/`: `smoke.json` includes response IDs, trace evidence, Prometheus series and actual panel query results; `kafka-contracts.json` validates the actual current events against producer and consumer schemas; `receipts.csv`, `correlated-logs.json`, and `verification.json` add database/log evidence. Gateway response and mock delivery payloads are validated against checked-in OpenAPI schemas, and protobuf copies must match. The smoke container sees no Docker socket. A failed check exits nonzero; inspect the report and `docker compose logs`.

Repeat the required lifecycle check:

```powershell
docker compose stop
docker compose up -d --wait --wait-timeout 240
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify.ps1
```

Stop without deleting stored demo data:

```powershell
docker compose down
```

PostgreSQL, Kafka, Prometheus and Grafana use named volumes. Jaeger uses memory and loses traces on restart; application logs use Docker JSON logging with 10 MB rotation and three files per container. Permanent log collection, retention, alerts, backup restoration, production HA orchestration and production authentication remain backlog work. The legacy `/api/v1/demo/orders` path still inserts a receipt and publishes Kafka separately, and its notification delivery can repeat. The business workflow uses a transactional PostgreSQL outbox, retrying Kafka publication, and durable notification event-ID deduplication; mock email also deduplicates event IDs. The separate [HA demonstration](HA.md) implements two gateway/order instances, synchronous PostgreSQL replication and verified manual fenced failover, with its degraded-mode and single-host limits documented explicitly.

Pinned image tags are recorded in `compose.yaml`; no floating `latest` tags. Application images use local `sprint1` tags and are built from sibling source folders. PostgreSQL 16, single Kafka 3.9 KRaft combined node, Jaeger all-in-one 1.76 with OTLP/gRPC, Prometheus 3.7, Grafana 12.2 and Python 3.13 are deliberately small local components. Kafka uses PLAINTEXT only inside the local Docker network. A one-shot volume initializer sets ownership on only its named data volume; the broker then runs as uid 1000. Two further one-shot initializers create the separate legacy demo and business topics idempotently. All three initializers should finish with exit code 0. The Kafka/Jaeger version-specific official deployment references are https://kafka.apache.org/39/getting-started/docker/ and https://www.jaegertracing.io/docs/1.76/deployment/.

If a service stays unhealthy, use `docker compose ps` then `docker compose logs <service>`. PostgreSQL password changes require matching existing database credentials; regenerating `.env` does not change credentials in an existing volume. Port conflicts require freeing the four loopback ports. Builds need registry/module network access. Never diagnose by printing `docker compose config` or dumping environment variables, because resolved configuration contains the local passwords.

HA-демонстрация с отдельными томами, двумя gateway/order, synchronous streaming PostgreSQL и ручным fenced failover: [HA.md](HA.md). Проверки: scripts/verify.ps1 -HA, tests/business.py, scripts/verify-ha.ps1.
