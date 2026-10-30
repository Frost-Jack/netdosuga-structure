# Local MVP platform

Start the HA platform from the workspace root in one command:

```powershell
docker compose -f platform/compose.ha.yaml up -d --build --wait
```

Docker Desktop must run Linux containers with Compose v2. The command builds each independent Go service, reuses existing named volumes, initializes fresh PostgreSQL/Kafka data, and starts two API gateways, two order instances, a synchronous PostgreSQL pair, identity, payment mock, notifications and diagnostics. `platform/.env` remains the location for saved credentials; existing values take precedence. With no file, local PostgreSQL and Grafana use `demo12345`. Grafana user is `demo`. Existing PostgreSQL volumes retain their existing password: do not replace their matching `.env`.

The baseline remains available with `docker compose -f platform/compose.yaml up -d --build --wait`. It uses its original project and volume names. The baseline and HA publish the same local ports, so run one at a time. Stop either project with its matching Compose file and `down`; omit `--volumes` to retain data.

| Local address | Purpose |
| --- | --- |
| http://localhost:8080 | Ticket service and real sign-in |
| http://localhost:3000 | Grafana operation/error/latency panels |
| http://localhost:16686 | Jaeger traces |
| http://localhost:9090 | Prometheus targets and queries |
| http://localhost:8086 | Read-only Kafbat Kafka UI |

Seed logins are buyer@netdosuga.local, buyer2@netdosuga.local, vendor@netdosuga.local, vendor2@netdosuga.local and admin@netdosuga.local. The initial password is `demo12345`, configurable through MVP_SEED_PASSWORD before first initialization. The payment and email providers are explicitly local mocks. No card data is accepted. Kafka, PostgreSQL and provider APIs stay on the internal network.

Run acceptance tests in the separate test Compose project:

```powershell
docker compose -f platform/compose.tests.yaml up --abort-on-container-exit --exit-code-from tests
```

The single runner executes legacy smoke, business purchase/refund, then MVP authentication/payment/trace checks sequentially. Its exit status is the test result. Reports remain in `platform/test-results`. To target baseline, set STACK_NETWORK=netdosuga-demo_default and EXPECTED_PROM_TARGETS=6; HA defaults to netdosuga-ha_default and 8 targets. Individual suites can run with `docker compose -f platform/compose.tests.yaml run --rm business` (or smoke/mvp). The additional PowerShell verification checks SQL receipts, Kafka contracts and correlated logs: `./platform/scripts/verify.ps1 -HA`. Manual failover/rejoin and fault tests are described in [HA.md](HA.md).

Telemetry uses service names api-gateway, orders-service, identity-service, payment-mock, notification-worker and mail-mock. The HA proxy keys are http-router, grpc-router and postgres-router; old gateway/order-service/postgres names remain network aliases. Instances appear as api-gateway-a/b and orders-service-a/b. The original pg-a/pg-b keys, project names and named volumes are preserved.

Take the trace_id from an API response and paste it into Jaeger's trace search, or choose a service and operation. Expand operation and internal child spans; app.request/app.response attributes contain sanitized machine fields. Auth bodies, credentials, cookies, user text and ticket codes are excluded. The provider spans show validate → idempotency.lookup → provider.simulate → persist; notification spans show validate → deduplicate → send → commit. Grafana links to Jaeger and plots request/error rates and p95 latency by service/operation. Business errors are visible even when the gRPC transport returns OK.

Detailed JSON logs include service, instance, operation, status, duration_ms and trace/span IDs. Inspect a trace locally:

```powershell
$traceId = 'paste_trace_id'
docker compose -f platform/compose.ha.yaml logs --no-color --no-log-prefix gateway-a gateway-b order-a order-b identity-service payment-mock notification-service integration-mock | Select-String $traceId
```

Logs use bounded allowlisted request/response data (2048 bytes, arrays up to three entries, strings up to 128 characters). Jaeger stores traces in memory, so its restart removes trace history. There is no centralized log search service; Docker JSON logs are the log source. Logs rotate at 10MB × 3 files. Kafka retains up to 24 hours/128MB per topic partition, uses a 384MB JVM heap with a 768MB container budget; Kafbat uses a 256MB heap/512MB budget. Broker startup repairs only its named data directory ownership and drops to uid1000. Topics are created by Kafka on first access; no one-shot initialization containers are needed. PostgreSQL startup enables synchronous replication once the replica streams; manual fenced failover still supports explicit degraded operation until rejoin.

All image tags are pinned. Kafbat v1.5.0 comes from its [official releases](https://github.com/kafbat/kafka-ui/releases); its health check uses /actuator/health. Builds use one image for each service, reused by both replicas. Do not print resolved Compose configuration or environment values when diagnosing credentials. `docker compose ... config --quiet` validates configuration without displaying secrets.

To verify a completely fresh installation without publishing host ports or reading saved .env credentials, run `./platform/scripts/verify-bootstrap.ps1`. It creates a UUID Compose project with empty-volume defaults, starts the full stack in one up command, runs the same sequential acceptance runner, and removes only that disposable project and its volumes afterward. The working HA project and its data stay untouched.
