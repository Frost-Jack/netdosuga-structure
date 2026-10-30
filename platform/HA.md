# Local HA demonstration (ND-403/404)

`compose.yaml` remains the single-instance baseline. `compose.ha.yaml` is a separate, standalone Compose project named `netdosuga-ha`, with separate PostgreSQL, Kafka, Grafana and Prometheus volumes. Startup enables synchronous replication after the standby begins streaming; no setup or sync script is required for ordinary startup. The old `netdosuga-demo_postgres-data` volume and its receipts are untouched. Fresh PostgreSQL volumes bootstrap the explicit primary recorded in `ha/postgres.cfg`, so a saved promoted route is also valid on a new machine. Existing PGDATA is always reused and never automatically promoted. HA starts a fresh demo database; it does **not** claim to migrate historical baseline records. Do not run both projects on the same host ports.

```powershell
# Stop baseline containers if running; never use down -v.
docker compose -f platform/compose.yaml stop
docker compose -f platform/compose.ha.yaml up -d --build --wait
# Existing contracts/metrics/traces, then business workflow and controlled failures.
./platform/scripts/verify.ps1 -HA
docker compose -f platform/compose.tests.yaml run --rm business
./platform/scripts/verify-ha.ps1
```


Two gateway processes receive HTTP roundrobin requests through HAProxy. A second HAProxy routes gRPC with HTTP/2 (`proto h2`) roundrobin per request to two order processes; a separate HTTP listener supplies readiness checks. `X-Gateway-Instance` and `X-Order-Instance` identify the responding processes. These free local HAProxy 3.2.12 instances demonstrate the balancing role drawn as F5; no F5 product, license, hardware, or production equivalence is claimed. PostgreSQL and each load balancer are internal-only; public host ports are 8080, 3000, 9090, 16686 and read-only Kafka UI 8086 on loopback.

PostgreSQL 16.10 uses actual `pg_basebackup -R` physical streaming replication, `synchronous_commit=remote_apply`, and one synchronous standby. The `sync` command verifies `pg_stat_replication` reports streaming/sync. A successful commit is replayed on the standby before acknowledgment. A standby outage can block writes; no standby-loss availability guarantee is claimed. No election/quorum service is present.

## Manual primary failover and recovery

```powershell
./platform/scripts/ha-postgres.ps1 failover
./platform/scripts/ha-postgres.ps1 rejoin
```

`failover` reads the persisted router target, verifies the candidate is a standby, stops the old primary with Docker, and requires its actual container state to be stopped before promotion. If fencing cannot be proven it refuses promotion. It calls `pg_promote`, disables the synchronous standby requirement for explicitly degraded single-copy operation, persists the new router target in `ha/postgres.cfg`, and restarts only the database router. Existing connection pools reconnect. This manual operation causes interruption; it is not automatic failover. Never manually start the stopped old primary before `rejoin`.

`rejoin` stops the former primary, archives its PGDATA under `/data/before-rejoin-<timestamp>` on its own HA volume, then replaces its working directory with a fresh base backup from the authoritative primary. It starts this node as a standby and reenables/verifies synchronous replication. No volume is deleted. Archived copies consume disk space and need operator retention management. The promoted topology and route survive subsequent `up`/restart. Run `sync` after startup to verify the intended guarantee before using the app.

This is a single Docker host demo. Host failure, network partitions beyond Docker-controlled fencing, external operator restarts, load balancer failure, multiple simultaneous faults, backup restoration and production failover orchestration are not covered. Automatic fencing/election would require an independently designed quorum system. During recovery there is one durable database copy, so a second failure may lose newly accepted writes.

## Evidence and repeatability

`verify-ha.ps1 -DatabaseOnly` checks a committed unique marker on the actual recovery-mode standby, fences/promotes primary, checks marker survival and a post-failover write, reclones former primary, verifies both markers on the new standby, restarts both PostgreSQL nodes and rechecks synchronous topology. Full `verify-ha.ps1` additionally checks both HTTP/gRPC instances via headers, stops each first application instance and verifies surviving requests, stops the single Kafka broker while checkout commits into the transactional outbox, restarts Kafka and requires the matching durable notification receipt and publication marker, and submits a real business API write after primary failover. Cleanup runs in `finally` and attempts database rejoin and application/Kafka recovery even when an assertion fails.

JSON evidence: `test-results/ha-failures.json`, `test-results/business.json`, `test-results/smoke.json`. Test history consists of uniquely named demo offers/orders and HA marker rows. It is deliberately preserved, not erased to make tests pass. Kafka is one persistent broker with replication factor one: backlog recovery is demonstrated, broker HA is not.

Sources: [PostgreSQL 16 streaming/synchronous replication](https://www.postgresql.org/docs/16/warm-standby.html), [pg_basebackup](https://www.postgresql.org/docs/16/app-pgbasebackup.html), [replication configuration](https://www.postgresql.org/docs/16/runtime-config-replication.html). Versions are pinned tags, not floating latest. Existing baseline images remain unchanged; the added HAProxy tag was pulled and executed during verification.

Database application connections require `target_session_attrs=read-write`; readiness checks verify a writable primary session and discard stale read-only sessions. PostgreSQL router shutdowns close affected sessions. After topology restart the fault test waits for both order instances, identity, payment mock and notification readiness before making one business write. This avoids treating a successful standby ping as application recovery.
