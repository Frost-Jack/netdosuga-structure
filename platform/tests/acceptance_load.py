"""L-01/L-02 user profiles with per-request deadlines and storage reconciliation."""

from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import json
import math
from statistics import mean
import subprocess
import threading
import time
from urllib.parse import urlencode

from acceptance_cases import case, quote, request, until
from acceptance_run import command


def latency(samples):
    sorted_samples = sorted(samples)
    if not sorted_samples:
        return {"samples": 0}
    return {
        "samples": len(sorted_samples), "mean_ms": round(mean(sorted_samples) * 1000, 2),
        **{f"p{percentile}_ms": round(1000 * sorted_samples[math.ceil(len(sorted_samples) * percentile / 100) - 1], 2)
           for percentile in (50, 95, 99)}, "max_ms": round(sorted_samples[-1] * 1000, 2),
    }


@contextmanager
def monitoring(t):
    out = t.stack.observations[t.name]
    report = json.loads(command(["docker", "info", "--format", "{{json .}}"]))
    out["docker"] = {key: report.get(key) for key in ("ServerVersion", "NCPU", "MemTotal", "OSType")}
    config = json.loads(t.stack.dc("config", "--format", "json"))
    out["services"] = {name: {"image": value.get("image"), "cpus": value.get("cpus"), "memory": value.get("mem_limit")}
                       for name, value in config["services"].items()}
    out["telemetry"] = {"scrape_interval_seconds": 5, "trace_export": "OTLP to Jaeger"}
    services = t.stack.dc("ps", "-q").split()
    samples = out["resources"] = []
    finished = threading.Event()

    def sample():
        while not finished.is_set():
            try:
                lines = command(["docker", "stats", "--no-stream", "--format", "{{json .}}", *services], timeout=10)
                samples.append({"elapsed": round(time.monotonic() - started, 2),
                                "containers": [json.loads(line) for line in lines.splitlines() if line.startswith("{")]})
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                samples.append({"error": type(error).__name__})
            finished.wait(15)

    started = time.monotonic()
    thread = threading.Thread(target=sample)
    thread.start()
    try:
        yield
    finally:
        finished.set()
        thread.join()


def add_replica_counts(totals, headers):
    for header in ("X-Gateway-Instance", "X-Order-Instance"):
        instance = headers.get(header)
        if instance:
            totals[header + ":" + instance] += 1


@case("L-01", "load-catalog")
def catalog_load(t):
    from acceptance_browser import launch
    from playwright.sync_api import sync_playwright

    t.actors("v1")
    fixture = t.prefix
    published = 1000 - int(t.sql("SELECT count(*) FROM business_offers WHERE status='PUBLISHED';"))
    t.sql(f"""INSERT INTO business_offers(id,vendor_id,title,description,city,category,price_cents,capacity,available,status)
          SELECT {quote(fixture)} || '-' || lpad(i::text,4,'0'),{quote(t.actor_ids['v1'])},'Load card','Load description',
                 {quote(fixture)} || '-' || (i % 4), CASE WHEN i % 3 = 0 THEN 'music' ELSE 'other' END,
                 500,10,10,CASE WHEN i <= {published} THEN 'PUBLISHED' ELSE 'DRAFT' END
          FROM generate_series(1,{published + 100}) i;""")
    out = t.stack.observations[t.name] = {"api_users": 99, "browser_users": 1, "published": 1000,
                                         "drafts": 100, "steady_seconds": 300, "http_deadline_seconds": 5}
    filters = [{}, {}, {"city": fixture + "-0"}, {"city": fixture + "-0", "category": "music"}]
    failures = out["errors"] = []
    with monitoring(t), sync_playwright() as playwright, ThreadPoolExecutor(max_workers=99) as pool:
        browser = launch(playwright, t.browser)
        out["browser"] = {"name": t.browser, "version": browser.version, "cache": "fresh context per navigation"}
        start = time.monotonic() + 1
        steady = start + 180
        end = steady + 300

        def worker(user):
            scheduled = start if user == 0 else start + 60 + 120 * user / 98
            time.sleep(max(0, scheduled - time.monotonic()))
            metrics, samples, errors = Counter(), [], []
            count = 0
            while (began := time.monotonic()) < end:
                parameters = filters[(user + count) % len(filters)]
                count += 1
                try:
                    status, data, headers = request(t.base + "/api/v1/shop/catalog?" + urlencode(parameters))
                    elapsed = time.monotonic() - began
                    assert status == 200 and elapsed <= 5, f"status {status} or deadline exceeded"
                    offers = data["offers"]
                    assert len(offers) <= 200 and offers, "unexpected result size"
                    assert all(row["status"] == "PUBLISHED" and all(row[key] == value for key, value in parameters.items()) for row in offers), "catalog mismatch"
                    if began >= steady:
                        samples.append(elapsed)
                        add_replica_counts(metrics, headers)
                except Exception as error:
                    errors.append({"user": user, "attempt": count, "reason": type(error).__name__ + ": " + str(error)})
            return metrics, samples, errors, count

        futures = [pool.submit(worker, user) for user in range(99)]
        ready = []
        for index in range(60):
            target = steady + index * 5
            time.sleep(max(0, target - time.monotonic()))
            began = time.monotonic()
            context = browser.new_context()
            try:
                page = context.new_page()
                try:
                    page.goto(t.base, wait_until="domcontentloaded", timeout=5000)
                    remaining = lambda: max(1, (began + 5 - time.monotonic()) * 1000)
                    page.locator("#filters").wait_for(state="visible", timeout=remaining())
                    page.locator("#content.catalog article").first.wait_for(state="visible", timeout=remaining())
                    elapsed = time.monotonic() - began
                    assert elapsed <= 5, "page readiness deadline exceeded"
                    ready.append(elapsed)
                except Exception as error:
                    failures.append({"browser_attempt": index + 1, "reason": type(error).__name__})
            finally:
                context.close()
        browser.close()
        replicas, api, requested = Counter(), [], 0
        for future in futures:
            counts, times, errors, attempts = future.result()
            replicas.update(counts)
            api.extend(times)
            failures.extend(errors)
            requested += attempts
    out.update(api=latency(api), browser_readiness=latency(ready), rps=round(len(api) / 300, 2), replicas=dict(replicas),
               api_attempts=requested, browser_attempts=60)
    out["backlog"] = reconcile_events(t, "o.customer_id LIKE " + quote(t.prefix + "%"))
    t.assertFalse(failures, f"{len(failures)} unexpected requests or navigations")
    t.assertEqual(len(ready), 60)
    t.assertLessEqual(mean(api), 2)
    t.assertLessEqual(mean(ready), 2)
    for header in ("X-Gateway-Instance", "X-Order-Instance"):
        t.assertEqual(len([key for key in replicas if key.startswith(header + ":")]), 2)


def reconcile_events(t, order_filter):
    query = f"""
    SELECT count(*) AS events, count(*) FILTER (WHERE e.published_at IS NULL) AS pending,
           count(*) FILTER (WHERE n.delivered_at IS NULL) AS undelivered
    FROM business_outbox e JOIN business_orders o ON e.payload->>'order_id'=o.id
    LEFT JOIN business_notifications n USING(event_id) WHERE {order_filter}
    """
    result = None

    def drained():
        nonlocal result
        result, = t.rows(query)
        return result if result["pending"] == result["undelivered"] == 0 else None

    try:
        return until(drained, 60)
    finally:
        t.stack.observations[t.name]["backlog"] = result


@case("L-02", "load-orders")
def checkout_load(t):
    users = [f"load-{index:02d}" for index in range(20)]
    t.actors(*users)
    oid = t.offer(capacity=5000, available=5000, price_cents=1000)
    out = t.stack.observations[t.name] = {"users": 20, "ramp_seconds": 60, "steady_seconds": 180,
                                         "http_deadline_seconds": 5, "iteration_cap": 5000, "pace_seconds": 1}
    errors = out["errors"] = []
    attempts = 0
    lock = threading.Lock()
    with monitoring(t), ThreadPoolExecutor(max_workers=20) as pool:
        start = time.monotonic()
        steady = start + 60
        end = steady + 180

        def worker(index, actor):
            nonlocal attempts
            time.sleep(max(0, start + 60 * index / 19 - time.monotonic()))
            valid, durations, replicas = set(), [], Counter()
            while (began := time.monotonic()) < end:
                with lock:
                    if attempts >= 5000:
                        errors.append({"user": actor, "reason": "iteration cap reached before end of steady stage"})
                        break
                    attempts += 1
                    sequence = attempts
                base = t.base + "/api/v1/shop/"
                headers = {"Cookie": "nd_session=" + t.tokens[actor], "Idempotency-Key": f"{t.prefix}-{sequence}"}
                try:
                    def call(path, method="GET", body=None):
                        tick = time.monotonic()
                        status, data, response_headers = request(base + path, method, body, headers)
                        elapsed = time.monotonic() - tick
                        if began >= steady:
                            durations.append(elapsed)
                            add_replica_counts(replicas, response_headers)
                        assert status in (200, 201) and elapsed <= 5, f"{path} status {status} or deadline exceeded"
                        return data

                    call("cart/items", "POST", {"offer_id": oid, "quantity": 1})
                    order = call("orders", "POST", {})["order"]
                    call("orders/" + order["id"] + "/pay", "POST", {"scenario": "success"})
                    paid = call("orders/" + order["id"])["order"]
                    assert (paid["status"], paid["total_cents"], paid["commission_cents"]) == ("PAID", 1000, 100)
                    assert len(paid["tickets"]) == 1 and paid["tickets"][0]["status"] == "VALID"
                    assert paid["id"] not in valid
                    valid.add(paid["id"])
                except Exception as error:
                    errors.append({"user": actor, "iteration": sequence, "reason": type(error).__name__})
                time.sleep(max(0, min(end, began + 1) - time.monotonic()))
            return valid, durations, replicas

        futures = [pool.submit(worker, index, actor) for index, actor in enumerate(users)]
        verified, samples, replicas = set(), [], Counter()
        for future in futures:
            ids, durations, counts = future.result()
            t.assertTrue(verified.isdisjoint(ids))
            verified.update(ids)
            samples.extend(durations)
            replicas.update(counts)
    out.update(iterations=attempts, paid=len(verified), api=latency(samples), rps=round(len(samples) / 180, 2), replicas=dict(replicas))
    filter_orders = "o.customer_id LIKE " + quote(t.prefix + "-load-%")
    rows = t.rows(f"""
        SELECT o.id,o.status,o.total_cents,o.commission_cents,
          (SELECT count(*) FROM business_tickets k WHERE k.order_id=o.id AND k.status='VALID' AND k.offer_id={quote(oid)}) AS tickets,
          (SELECT count(*) FROM payment_mock.effects p WHERE p.order_id=o.id AND p.kind='payment' AND p.amount_cents=1000) AS payments,
          (SELECT count(*) FROM business_refund_attempts f WHERE f.order_id=o.id) AS unresolved
        FROM business_orders o WHERE {filter_orders}
    """)
    backlog = reconcile_events(t, filter_orders)
    t.assertEqual({row["id"] for row in rows}, verified)
    t.assertTrue(all((row["status"], row["total_cents"], row["commission_cents"], row["tickets"], row["payments"], row["unresolved"])
                     == ("PAID", 1000, 100, 1, 1, 0) for row in rows))
    t.assertEqual(t.stock(oid), (5000 - len(verified),))
    t.assertEqual(backlog["events"], 2 * len(verified))
    t.assertEqual(len(verified), attempts)
    t.assertGreater(attempts, 0)
    t.assertFalse(errors, f"{len(errors)} iterations failed")
