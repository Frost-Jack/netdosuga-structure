"""Sequential resource faults and deterministic pre-commit interruption points."""

from contextlib import contextmanager
import base64
from datetime import datetime, timezone
import json
import os
import shutil
import time
from uuid import uuid4

from acceptance_cases import case, quote, request, until, variants
from acceptance_run import PLATFORM, command

TOPIC = "netdosuga.business.events.v1"
GROUP = "netdosuga-business-notification-v1"


def recover(t, *services, timeout=75):
    t.stack.dc("up", "-d", "--no-build", "--no-deps", "--wait",
               "--wait-timeout", str(timeout - 5), *services, timeout=timeout)


@contextmanager
def stopped(t, *services):
    t.stack.dc("stop", "-t", "2", *services, timeout=20)
    try:
        yield
    finally:
        recover(t, *services)


@variants("H-01", {"gateway": ("gateway-a", "X-Gateway-Instance", "api-gateway-b"),
                    "orders": ("order-a", "X-Order-Instance", "orders-service-b")}, "faults")
def loses_replica(t, fixture):
    service, header, endpoint = fixture
    a, b = t.control_cart()
    failures = []
    with stopped(t, service):
        def read_remaining():
            try:
                status, _, headers = request(t.base + "/api/v1/shop/catalog", raw=True, timeout=3)
                if status == 200 and headers[header] == endpoint:
                    return True
                failures.append(status)
            except (OSError, TimeoutError) as error:
                failures.append(type(error).__name__)
            return False

        def checkout_during_loss():
            try:
                return t.checkout()
            except (AssertionError, ValueError, OSError) as error:
                failures.append("checkout " + type(error).__name__)
                return None

        first = checkout_during_loss()
        until(read_remaining, 15)
        order = t.checkout()
        if first:
            t.assertEqual(first["id"], order["id"])
        t.assertEqual(t.checkout()["id"], order["id"])
        t.assertEqual(t.stock(a, b), (0, 1))
        t.assertEqual(len(t.events(order["id"], "order.created")), 1)
        t.stack.observations[t.name] = {"order_id": order["id"], "transient_errors": failures}


def postgres(t, action, timeout=55):
    # Invoke the same operational script with the disposable Compose project and route.
    executable = shutil.which("pwsh") or shutil.which("powershell")
    if not executable:
        raise RuntimeError("The PostgreSQL failover cases require PowerShell")
    env = dict(t.stack.env, AT_PG_SCRIPT=(PLATFORM / "scripts/ha-postgres.ps1").as_posix(),
               AT_PG_ACTION=action, AT_PG_PLATFORM=PLATFORM.as_posix(), AT_PG_PROJECT=t.stack.project)
    script = """
    $ErrorActionPreference='Stop'
    $platform = $env:AT_PG_PLATFORM
    & $env:AT_PG_SCRIPT -Action $env:AT_PG_ACTION -ProjectName $env:AT_PG_PROJECT `
      -ComposeFile @("$platform/compose.ha.yaml","$platform/compose.bootstrap.yaml","$platform/compose.acceptance.yaml") `
      -EnvFile "$platform/tests/bootstrap.env" -RoutePath "$env:ACCEPTANCE_HA_DIR/postgres.cfg"
    """
    encoded = base64.b64encode(script.encode("utf-16-le")).decode("ascii")
    command([executable, "-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded], env=env, timeout=timeout)


def standby(t):
    return "pg-b" if t.stack.primary == "pg-a" else "pg-a"


@variants("H-02", {"fenced": False}, "faults")
@variants("H-03a", {"rejoin": True}, "faults")
def postgres_transition(t, verify_rejoin):
    old = t.stack.primary
    new = standby(t)
    _, _, original = t.control_order("PAID")
    t.assertEqual(t.stack.sql("SELECT pg_is_in_recovery();", new), "t")
    before = time.monotonic()
    postgres(t, "failover")
    t.stack.primary = new
    joined = False
    evidence = t.stack.observations[t.name] = {"former_primary": old, "primary": new}
    try:
        t.assertEqual(t.stack.sql("SELECT pg_is_in_recovery();", new), "f")
        read = until(lambda: read_order(t, original["id"]), 25)
        evidence["read_recovery_seconds"] = round(time.monotonic() - before, 3)
        t.assertEqual(read, original)
        second_a, second_b, second = t.control_order(suffix="-post-failover")
        evidence["write_recovery_seconds"] = round(time.monotonic() - before, 3)
        t.assertEqual(t.stock(second_a, second_b), (0, 1))
        t.assertEqual(read_order(t, second["id"]), second)
        # Normal startup must leave the fenced previous primary stopped.
        t.stack.dc("up", "-d", "--no-build", "--no-deps", old, timeout=15)
        container = t.stack.dc("ps", "-aq", old).strip()
        until(lambda: command(["docker", "inspect", "--format", "{{.State.Running}}", container]).strip() == "false", 12)
        t.assertIn("Refusing to start fenced former primary", t.stack.dc("logs", "--no-color", old))
        if verify_rejoin:
            postgres(t, "rejoin")
            joined = True
            _, _, marker = t.control_order(suffix="-replication-check")
            t.assertEqual(t.stack.sql("SELECT pg_is_in_recovery();", old), "t")
            observed = until(lambda: t.stack.rows(
                f"SELECT id,total_cents FROM business_orders WHERE id={quote(marker['id'])}", old), 10)
            t.assertEqual(observed, [{"id": marker["id"], "total_cents": 390000}])
            t.assertEqual(t.stack.sql(f"SELECT sync_state FROM pg_stat_replication WHERE application_name={quote(old)};"), "sync")
            evidence["replication_recovery_seconds"] = round(time.monotonic() - before, 3)
    finally:
        if not joined:
            postgres(t, "rejoin")


def read_order(t, oid):
    try:
        return t.shop("orders/" + oid, actor="c1")["order"]
    except (AssertionError, ValueError, OSError):
        return None


@case("H-03b", "faults")
def restart_everything(t):
    _, _, paid = t.control_order("PAID")
    _, _, order = t.control_order("PAID", suffix="-returned")
    cancelled = t.order_change(order, "cancel")["order"]
    original = {row["id"]: t.effects(row["id"]) for row in (paid, cancelled)}
    start = time.monotonic()
    with stopped(t):
        pass
    t.stack.refresh_urls()
    t.assertEqual(t.http("/api/v1/auth/session", actor="c1")[0]["user"]["id"], t.actor_ids["c1"])
    t.assertEqual(t.order_change(paid, "pay")["order"], paid)
    t.assertEqual(t.order_change(cancelled, "cancel")["order"], cancelled)
    t.assertEqual({row["id"]: t.effects(row["id"]) for row in (paid, cancelled)}, original)
    until(lambda: t.stack.sql(f"SELECT sync_state FROM pg_stat_replication WHERE application_name={quote(standby(t))};") == "sync", 12)
    t.stack.observations[t.name] = {"seconds_to_verify_restart": round(time.monotonic() - start, 3)}


@case("H-04", "faults")
def repeat_bootstrap(t):
    baseline = (t.rows("SELECT * FROM schema_migrations ORDER BY version"),
                t.rows("SELECT id FROM identity.users WHERE id NOT LIKE 'test-%' ORDER BY id"),
                t.rows("SELECT id FROM business_offers WHERE id LIKE 'offer-demo-%' ORDER BY id"))
    t.assertEqual([row["version"] for row in baseline[0]], list(range(1, 7)))
    t.assertEqual(len(baseline[1]), 5)
    t.assertTrue(baseline[2])
    t.stack.dc("up", "-d", "--build", "--wait", "--wait-timeout", "90", timeout=120)
    t.stack.refresh_urls()
    t.assertEqual((t.rows("SELECT * FROM schema_migrations ORDER BY version"),
                   t.rows("SELECT id FROM identity.users WHERE id NOT LIKE 'test-%' ORDER BY id"),
                   t.rows("SELECT id FROM business_offers WHERE id LIKE 'offer-demo-%' ORDER BY id")), baseline)
    t.assertEqual(request(t.base + "/readyz", raw=True)[0], 200)


def kafka(t, executable, *args, **kwargs):
    return t.stack.dc("exec", "-T", "kafka", "/opt/kafka/bin/" + executable,
                      "--bootstrap-server", "kafka:9092", *args, **kwargs)


def publish(t, *payloads):
    kafka(t, "kafka-console-producer.sh", "--topic", TOPIC, stdin="\n".join(payloads) + "\n", timeout=15)


def event_payload(t, event_type="order.created"):
    return {"schema_version": 1, "demo": True, "event_id": str(uuid4()), "event_type": event_type,
            "order_id": t.prefix, "occurred_at": datetime.now(timezone.utc).isoformat()}


def mail_count(t, eid):
    status, response, _ = request(t.stack.urls["integration-mock"] + "/mock/business-deliveries")
    t.assertEqual(status, 200)
    return sum(row["event_id"] == eid for row in response["deliveries"])


def notification_count(t, eid):
    return int(t.sql(f"SELECT count(*) FROM business_notifications WHERE event_id={quote(eid)};"))


@case("N-18a", "faults")
def duplicate_delivery(t):
    _, _, order = t.control_order()
    event = t.mail(order["id"], "order.created")
    before = read_order(t, order["id"])
    sentinel = event_payload(t)
    publish(t, json.dumps(event["payload"]), json.dumps(event["payload"]), json.dumps(sentinel))
    until(lambda: notification_count(t, sentinel["event_id"]) == 1, 20)
    t.assertEqual(notification_count(t, event["event_id"]), 1)
    t.assertEqual(mail_count(t, event["event_id"]), 1)
    t.assertEqual(read_order(t, order["id"]), before)


@variants("N-18b", {"malformed": "malformed", "type": "type"}, "faults")
def poison_delivery(t, invalid):
    bogus = event_payload(t, "unknown.event")
    valid = event_payload(t)
    prior = t.rows("SELECT topic,partition,message_offset FROM business_poison")
    publish(t, "{" if invalid == "malformed" else json.dumps(bogus), json.dumps(valid))
    until(lambda: notification_count(t, valid["event_id"]) == 1, 20)
    after = t.rows("SELECT topic,partition,message_offset,reason FROM business_poison")
    new, = [row for row in after if {key: row[key] for key in ("topic", "partition", "message_offset")} not in prior]
    t.assertEqual((new["topic"], new["reason"]), (TOPIC, "INVALID_EVENT"))
    t.assertGreaterEqual(new["partition"], 0)
    t.assertGreaterEqual(new["message_offset"], 0)
    t.assertEqual(mail_count(t, bogus["event_id"]), 0)
    t.assertEqual(mail_count(t, valid["event_id"]), 1)


@case("N-19", "faults")
def kafka_outage(t):
    a, b = t.control_cart()
    with stopped(t, "kafka"):
        order = t.checkout()
        oid = order["id"]
        event, = t.events(oid, "order.created")
        t.assertIsNone(event["published_at"])
        t.assertEqual(t.stock(a, b), (0, 1))
    t.assertEqual(t.mail(oid, "order.created", seconds=30)["event_id"], event["event_id"])
    t.assertEqual(t.checkout()["id"], oid)


@contextmanager
def reject_write(t, operation, table, predicate):
    name = "test_" + uuid4().hex
    t.sql(f"""CREATE FUNCTION {name}() RETURNS trigger LANGUAGE plpgsql AS $$
              BEGIN IF {predicate} THEN RAISE EXCEPTION 'acceptance rollback'; END IF; RETURN NEW; END $$;
              CREATE TRIGGER {name} BEFORE {operation} ON {table} FOR EACH ROW EXECUTE FUNCTION {name}();""")
    try:
        yield
    finally:
        t.sql(f"DROP TRIGGER {name} ON {table}; DROP FUNCTION {name}();")


@case("N-20a", "faults")
def publish_before_marker(t):
    t.control_cart()
    with reject_write(t, "UPDATE", "business_outbox", f"NEW.payload->>'order_id' IN (SELECT id FROM business_orders WHERE idempotency_key={quote(t.prefix)})"):
        order = t.checkout()
        event, = t.events(order["id"], "order.created")
        until(lambda: mail_count(t, event["event_id"]) == 1, 20)
        t.assertIsNone(t.events(order["id"], "order.created")[0]["published_at"])
        t.stack.dc("stop", "-t", "2", "order-a", "order-b", timeout=15)
    recover(t, "order-a", "order-b")
    t.assertEqual(t.mail(order["id"], "order.created")["event_id"], event["event_id"])


@case("N-20b", "faults")
def send_before_commit(t):
    event = event_payload(t)
    eid = event["event_id"]
    with reject_write(t, "INSERT", "business_notifications", f"NEW.event_id = {quote(eid)}"):
        publish(t, json.dumps(event))
        until(lambda: mail_count(t, eid) == 1, 20)
        t.assertEqual(notification_count(t, eid), 0)
        t.stack.dc("stop", "-t", "2", "notification-service", timeout=15)
    recover(t, "notification-service")
    until(lambda: notification_count(t, eid) == 1, 20)
    t.assertEqual(mail_count(t, eid), 1)
    # A caught-up group's committed offset proves that redelivery finished.
    def caught_up():
        rows = kafka(t, "kafka-consumer-groups.sh", "--describe", "--group", GROUP, timeout=10).splitlines()
        offsets = [row.split() for row in rows if row.split()[:2] == [GROUP, TOPIC]]
        return offsets and all(len(row) > 5 and row[5] == "0" for row in offsets)
    until(caught_up, 15)


@variants("N-12b", {"payment-rollback": "pay"}, "faults")
@variants("N-13b", {"refund-rollback": "cancel"}, "faults")
def failed_commit(t, action):
    a, b, order = t.control_order("PAID" if action == "cancel" else "RESERVED")
    target = "CANCELLED" if action == "cancel" else "PAID"
    scope = f"NEW.id={quote(order['id'])} AND NEW.status={quote(target)}"
    with reject_write(t, "UPDATE", "business_orders", scope):
        t.order_change(order, action, expected=503, code="UNAVAILABLE")
    if action == "cancel":
        ticket = next(row for row in order["tickets"] if row["offer_id"] == a)
        t.redeem(ticket, expected=409, code="REFUND_PENDING")
    t.assertEqual(t.effects(order["id"])[0]["kind"], "payment")
    cancelled = t.order_change(order, "cancel")["order"]
    t.assertEqual(cancelled["status"], "CANCELLED")
    t.assertEqual(t.stock(a, b), (2, 2))
    t.assertEqual([ticket["status"] for ticket in cancelled["tickets"]], ["REVOKED"] * (3 if action == "cancel" else 0))
    effects = [effect for effect in t.effects(order["id"]) if effect["kind"] in ("payment", "refund")]
    t.assertEqual([(effect["kind"], effect["amount_cents"]) for effect in effects], [("payment", 390000), ("refund", 390000)])
    t.assertEqual(len(t.events(order["id"], "order.cancelled")), 1)
