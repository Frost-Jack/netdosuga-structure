"""Sanity and telemetry checks tied to the request and event under test."""

import base64
import json
import re
from urllib.parse import urlencode, urlsplit

from acceptance_cases import case, quote, request, until


def query(t, expression, grafana=False):
    service = "grafana" if grafana else "prometheus"
    prefix = "/api/datasources/proxy/uid/prometheus" if grafana else ""
    status, value, _ = request(t.stack.urls[service] + prefix + "/api/v1/query?" + urlencode({"query": expression}),
                               headers=grafana_auth() if grafana else None)
    t.assertEqual((status, value.get("status")), (200, "success"))
    return value["data"]["result"]


def counter(t, expression):
    return sum(float(value["value"][1]) for value in query(t, expression))


def grafana_auth():
    return {"Authorization": "Basic " + base64.b64encode(b"demo:demo12345").decode()}


@case("S-01", "sanity")
def readiness(t):
    status, _, _ = request(t.base + "/readyz", raw=True)
    t.assertEqual(status, 200)
    services = {"gateway-a": 8080, "gateway-b": 8080, "order-a": 8081, "order-b": 8081,
                "identity-service": 8084, "payment-mock": 8085, "notification-service": 8082,
                "integration-mock": 8083}
    for service, port in services.items():
        t.stack.dc("exec", "-T", service, "wget", "-qO", "/dev/null", f"http://127.0.0.1:{port}/readyz", timeout=5)
    t.assertEqual(t.sql("SELECT pg_is_in_recovery();"), "f")
    oid = t.offer()
    t.assertEqual([offer["id"] for offer in t.catalog(city=t.prefix)], [oid])


@case("S-03", "sanity")
def logout(t):
    data, headers = t.http("/api/v1/auth/login", "POST", {"email": "buyer@netdosuga.local", "password": "demo12345"})
    raw_cookie = headers["Set-Cookie"].split(";", 1)[0]
    t.assertEqual((data["user"]["id"], data["user"]["role"]), ("customer-demo", "customer"))
    t.assertEqual(t.http("/api/v1/auth/session", headers={"Cookie": raw_cookie})[0]["user"], data["user"])
    t.http("/api/v1/auth/logout", "POST", {}, expected=204, headers={"Cookie": raw_cookie})
    t.shop("cart", headers={"Cookie": raw_cookie}, expected=401, code="UNAUTHENTICATED")


@case("S-04", "sanity")
def one_ticket(t):
    t.actors("c1")
    offer = t.offer(price_cents=1000)
    t.cart_item(offer, 1)
    order = t.order_change(t.checkout(), "pay")["order"]
    t.assertEqual((order["status"], order["total_cents"], order["commission_cents"]), ("PAID", 1000, 100))
    t.assertEqual([ticket["status"] for ticket in order["tickets"]], ["VALID"])
    t.assertEqual(t.stock(offer), (1,))
    t.assertEqual([(effect["kind"], effect["amount_cents"]) for effect in t.effects(order["id"])], [("payment", 1000)])


@case("S-05", "sanity")
def mail(t):
    _, _, order = t.control_order()
    paid = t.order_change(order, "pay")
    t.mail(order["id"], "order.paid", paid["trace_id"])


@case("S-06", "sanity")
def monitoring_ui(t):
    from acceptance_browser import page_for
    from playwright.sync_api import expect

    status, content, _ = request(t.base + "/swagger/", raw=True)
    t.assertEqual(status, 200)
    t.assertIn(b"SwaggerUIBundle", content)
    status, spec, _ = request(t.base + "/openapi.yaml")
    t.assertEqual(status, 200)
    t.assertIn("get", spec["paths"]["/api/v1/shop/catalog"])
    _, targets, _ = request(t.stack.urls["prometheus"] + "/api/v1/targets")
    t.assertEqual(len(targets["data"]["activeTargets"]), 8)
    until(lambda: len([series for series in query(t, "up") if series["value"][1] == "1"]) == 8, 25)
    expr = 'netdosuga_http_requests_total{service="api-gateway",route="/api/v1/shop/catalog",status="200"}'
    before = counter(t, expr)
    with page_for(t) as page:
        page.goto("/swagger/", timeout=5000)
        operation = page.locator(".opblock").filter(has=page.get_by_text("/api/v1/shop/catalog", exact=True))
        operation.get_by_role("button", name=re.compile("GET")).click()
        operation.get_by_role("button", name="Try it out").click()
        with page.expect_response(lambda response: urlsplit(response.url).path == "/api/v1/shop/catalog") as catalog:
            operation.get_by_role("button", name="Execute").click()
        t.assertTrue(catalog.value.ok, "Swagger catalog query failed")
        expect(operation.locator(".live-responses-table")).to_contain_text("200")
    until(lambda: counter(t, expr) > before, 18)
    status, dashboard, _ = request(t.stack.urls["grafana"] + "/api/dashboards/uid/netdosuga-demo", headers=grafana_auth())
    t.assertEqual(status, 200)
    target = dashboard["dashboard"]["panels"][0]["targets"][0]["expr"]
    until(lambda: query(t, target, grafana=True), 18)


def trace(t, trace_id, names, validate=None):
    incomplete = None

    def fetched():
        nonlocal incomplete
        status, value, _ = request(t.stack.urls["jaeger"] + "/api/traces/" + trace_id)
        results = value.get("data") or []
        if status == 200 and results:
            result = results[0]
            present = {span["operationName"] for span in result["spans"]}
            if names <= present:
                t.assertEqual(result["traceID"], trace_id)
                if validate:
                    try:
                        validate(result)
                    except RuntimeError as error:
                        incomplete = error
                        return None
                return result
        return None
    try:
        return until(fetched, 20)
    except AssertionError:
        if incomplete is not None:
            raise incomplete
        raise


def tags(span):
    return {tag["key"]: tag["value"] for tag in span.get("tags", [])}


def linked(span, parent_id):
    return any(ref["spanID"] == parent_id for ref in span.get("references", []))


@case("O-01")
def distributed_trace(t):
    from demo_trace import Demo

    _, _, order = t.control_order()
    paid = t.order_change(order, "pay")
    event = t.mail(order["id"], "order.paid", paid["trace_id"])
    root_name = "operation.orders.pay"
    names = {root_name, "payment.authorize", "transaction.commit", "outbox.enqueue",
             "outbox.publish", "business event consume", "session.lookup", "db.SELECT"}
    # Jaeger receives spans from the Go exporters on independent batch schedules.
    evidence = {}
    actual = trace(t, paid["trace_id"], names,
                   lambda result: evidence.update(Demo.detailed_evidence([result])))
    spans = actual["spans"]
    shop, = [span for span in spans if span["operationName"] == root_name]
    parents = {shop["spanID"]}
    for _ in spans:
        parents |= {span["spanID"] for span in spans if any(linked(span, parent_id) for parent_id in parents)}
    descendants = [span for span in spans if span["spanID"] in parents]
    t.assertTrue(names <= {span["operationName"] for span in descendants})
    enqueue_id = event["headers"]["traceparent"].split("-")[2]
    publisher, = [span for span in descendants if span["operationName"] == "outbox.publish"]
    consumer, = [span for span in descendants if span["operationName"] == "business event consume"]
    t.assertTrue(linked(publisher, enqueue_id))
    t.assertTrue(linked(consumer, publisher["spanID"]))
    for span in (publisher, consumer):
        t.assertEqual(tags(span)["messaging.destination.name"], "netdosuga.business.events.v1")
    sql = [tags(span)["db.statement"] for span in descendants if "db.statement" in tags(span)]
    t.assertTrue(any("$1" in statement for statement in sql))
    t.assertTrue(all(order["id"] not in statement and len(statement.encode()) <= 2048 for statement in sql))
    t.assertTrue(any(tags(span).get("span.kind") == "server"
                     and tags(span).get("http.route") == "/api/v1/shop/orders/{id}/pay"
                     and tags(span).get("http.request.method") == "POST"
                     and tags(span).get("http.response.status_code") == 200 for span in spans))
    t.assertTrue(any(tags(span).get("http.route") == "/v1/payments" and tags(span).get("http.request.method") == "POST"
                     for span in descendants))
    t.stack.observations[t.name] = evidence


def trace_logs(t, trace_id):
    raw = t.stack.dc("logs", "--no-color", "--no-log-prefix",
                     "gateway-a", "gateway-b", "order-a", "order-b", "identity-service",
                     "notification-service", "payment-mock", "integration-mock")
    return [json.loads(line) for line in raw.splitlines() if line.startswith("{") and trace_id in line]


def pay_counters(t):
    business, rpc_ok = 0, 0
    for replica in ("order-a", "order-b"):
        metrics = t.stack.dc("exec", "-T", replica, "wget", "-qO-", "http://127.0.0.1:8081/metrics", timeout=5)
        for line in metrics.splitlines():
            if line.startswith("netdosuga_operations_total{") and 'operation="orders.pay"' in line and 'outcome="error"' in line:
                business += float(line.rsplit(" ", 1)[-1])
            if line.startswith("netdosuga_grpc_requests_total{") and 'method="/netdosuga.demo.v1.OrderService/Shop"' in line and 'code="OK"' in line:
                rpc_ok += float(line.rsplit(" ", 1)[-1])
    return business, rpc_ok


@case("O-02")
def traced_decline(t):
    _, _, order = t.control_order()
    expr = 'netdosuga_operations_total{service="orders-service",operation="orders.pay",outcome="error"}'
    before = counter(t, expr)
    business_before, grpc_before = pay_counters(t)
    denied = t.order_change(order, "pay", "error", expected=502, code="MOCK_FAILED")
    t.assertEqual(pay_counters(t), (business_before + 1, grpc_before + 1))
    trace_id = denied["trace_id"]
    actual = trace(t, trace_id, {"operation.orders.pay", "payment.authorize"})
    span, = [entry for entry in actual["spans"] if entry["operationName"] == "operation.orders.pay"]
    t.assertIn(tags(span).get("otel.status_code"), ("ERROR", "Error"))
    logs = trace_logs(t, trace_id)
    t.assertTrue(any(row.get("trace_id") == trace_id and row.get("span_id") == span["spanID"] and row.get("method") == "Shop"
                     and row.get("status") == 502 for row in logs))
    until(lambda: counter(t, expr) > before, 15)


@case("O-03")
def redaction(t):
    a, _, order = t.control_order("PAID")
    ticket = next(entry for entry in order["tickets"] if entry["offer_id"] == a)
    secret = "diagnostic-" + t.prefix
    email = t.prefix + "-redaction@example.test"
    user, response_headers = t.http("/api/v1/auth/register", "POST", {
        "email": email, "password": secret, "name": "Telemetry", "role": "customer"}, expected=201)
    password_trace = user["trace_id"]
    cookie = response_headers["Set-Cookie"].split(";", 1)[0].split("=", 1)[1]
    paid = t.shop("orders/" + order["id"] + "/pay", "POST",
                  {"scenario": "success", "password": secret, "token": cookie, "email": email,
                   "code": ticket["code"], "description": secret * 60}, actor="c1")
    redeemed = t.redeem(ticket)
    for trace_id, names in ((password_trace, {"password.hash"}),
                            (paid["trace_id"], {"operation.orders.pay"}),
                            (redeemed["trace_id"], {"operation.tickets.redeem"})):
        data = trace(t, trace_id, names)
        records = trace_logs(t, trace_id)
        for token in (secret, email, cookie, ticket["code"], t.tokens["c1"]):
            t.assertFalse(token in json.dumps([data, records]), "sensitive value found in scoped telemetry")
        for span in data["spans"]:
            for key, value in tags(span).items():
                if key in {"app.request", "app.response", "db.statement"}:
                    t.assertLessEqual(len(value.encode()), 2048)
    series = query(t, '{__name__=~"netdosuga_.*"}')
    t.assertTrue(series)
    for row in series:
        t.assertTrue({"order_id", "trace_id", "email", "ticket_code"}.isdisjoint(row["metric"]))
        t.assertFalse(any(value in json.dumps(row["metric"]) for value in (order["id"], email, paid["trace_id"])))
