"""Isolated business walkthrough with real response traces; standard library only."""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import time
import urllib.error
import urllib.request
import uuid


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class Demo:
    def __init__(self, args):
        self.args = args
        self.private_values = set()
        self.report = {"run_id": uuid.uuid4().hex, "started_at": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "steps": [], "passed": False}
        self.sessions = {actor: urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            for actor in ("buyer", "vendor", "admin")}

    def call(self, step, method, path, body=None, actor="buyer", expected=200, key=None):
        self.report["phase"] = step
        headers = {"Content-Type": "application/json"}
        if key:
            headers["Idempotency-Key"] = key
        request = urllib.request.Request(self.args.base.rstrip("/") + "/api/v1" + path,
            data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
        try:
            response = self.sessions[actor].open(request, timeout=12)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status = response.code
            value = json.load(response)
        trace_id = value.get("trace_id", "")
        entry = {"step": step, "status": status, "expected_status": expected,
                 "trace_id": trace_id, "jaeger_url": self.args.jaeger_ui.rstrip("/") + "/trace/" + trace_id}
        self.report["steps"].append(entry)
        print(f"{step}: HTTP {status}" + (" (expected error)" if expected >= 400 else "") + f" trace={trace_id}", flush=True)
        require(status == expected, f"{step}: expected HTTP {expected}, received {status}")
        require(bool(re.fullmatch(r"[0-9a-f]{32}", trace_id)) and int(trace_id, 16) != 0,
                f"{step}: missing or invalid response trace_id")
        return value

    def run(self):
        run = self.report["run_id"]
        for actor, role in (("buyer", "customer"), ("vendor", "vendor")):
            password = secrets.token_urlsafe(24)
            self.private_values.add(password)
            value = self.call("register_" + actor, "POST", "/auth/register",
                {"email": f"trace-{actor}-{run}@netdosuga.local", "password": password,
                 "name": "Trace demo " + actor, "role": role}, actor, 201)
            self.report[actor + "_id"] = value["user"]["id"]
        admin_password = os.getenv("MVP_SEED_PASSWORD", "demo12345")
        self.private_values.add(admin_password)
        self.call("login_admin", "POST", "/auth/login", {"email": "admin@netdosuga.local",
            "password": admin_password}, "admin")
        app = self.call("create_application", "POST", "/shop/applications",
            {"organization": "Trace " + run[:12], "title": "Trace demo offer", "city": "Moscow",
             "category": "test"}, "vendor")["application"]
        self.report["application_id"] = app["id"]
        offer = self.call("approve_draft", "POST", f"/shop/applications/{app['id']}/approve", {}, "admin")["offer"]
        oid = self.report["offer_id"] = offer["id"]
        require(offer["status"] == "DRAFT", "approval did not produce DRAFT")
        self.call("fill_offer", "PATCH", f"/shop/offers/{oid}", {"title": "Trace demo offer",
            "description": "Isolated trace walkthrough", "city": "Moscow", "category": "test",
            "price_cents": 10000, "capacity": 1}, "vendor")
        self.call("publish_offer", "POST", f"/shop/offers/{oid}/publish", {}, "vendor")
        self.call("add_cart", "POST", "/shop/cart/items", {"offer_id": oid, "quantity": 1})
        order = self.call("checkout_reserved", "POST", "/shop/orders", {}, key=run)["order"]
        order_id = self.report["order_id"] = order["id"]
        require(order["status"] == "RESERVED" and not order["tickets"], "checkout was not RESERVED without tickets")
        path = f"/shop/orders/{order_id}"
        self.call("payment_mock_error", "POST", path + "/pay", {"scenario": "error"}, expected=502)
        require(self.call("verify_reserved", "GET", path)["order"]["status"] == "RESERVED", "failed payment changed order state")
        value = self.call("payment_success", "POST", path + "/pay", {"scenario": "success"})
        self.report["payment_trace_id"] = value["trace_id"]
        paid = value["order"]
        require(paid["status"] == "PAID" and len(paid["tickets"]) == 1, "payment did not issue one ticket")
        repeated = self.call("payment_repeat", "POST", path + "/pay", {"scenario": "success"})["order"]
        require(repeated["status"] == "PAID" and repeated["tickets"] == paid["tickets"]
                and repeated.get("payment_id") == paid.get("payment_id"), "repeat payment changed payment or tickets")
        self.private_values.add(paid["tickets"][0]["code"])
        ticket_path = "/shop/tickets/" + paid["tickets"][0]["code"] + "/redeem"
        self.call("redeem_ticket", "POST", ticket_path, {}, "vendor")
        self.call("redeem_repeat", "POST", ticket_path, {}, "vendor", 409)
        self.evidence()
        self.report["passed"] = True
        self.report["phase"] = "complete"

    def fetch_trace(self, trace_id):
        with urllib.request.urlopen(self.args.jaeger.rstrip("/") + "/api/traces/" + trace_id, timeout=5) as response:
            traces = json.load(response).get("data") or []
        for trace in traces:
            require(trace["traceID"] == trace_id, "Jaeger returned another trace")
        return traces

    def detailed_evidence(self, traces, authentication=False):
        rows = {}
        for trace in traces:
            for span in trace["spans"]:
                rows[span["spanID"]] = (trace["processes"][span["processID"]]["serviceName"], span,
                    {tag["key"]: tag["value"] for tag in span.get("tags", [])})

        def ancestors(span):
            seen = set()
            pending = [r["spanID"] for r in span.get("references", []) if r.get("traceID") == span["traceID"]]
            while pending:
                sid = pending.pop()
                if sid in seen or sid not in rows:
                    continue
                seen.add(sid)
                yield rows[sid]
                pending.extend(r["spanID"] for r in rows[sid][1].get("references", [])
                               if r.get("traceID") == span["traceID"])

        sql = {}
        db_services = () if authentication else ("orders-service", "identity-service", "payment-mock", "notification-worker")
        for service in db_services:
            matches = [tags for owner, span, tags in rows.values() if owner == service
                and tags.get("db.system") in ("postgres", "postgresql")
                and tags.get("db.operation") and tags.get("db.query.text")
                and isinstance(tags.get("db.query.parameter_count"), int)
                and any(parent_owner == service for parent_owner, _, _ in ancestors(span))]
            require(bool(matches), "missing contextual SQL detail: " + service)
            require(any(re.search(r"\$[1-9][0-9]*", t["db.query.text"]) for t in matches),
                    "missing parameterized SQL detail: " + service)
            sql[service] = {"span_count": len(matches), "sample_template": matches[0]["db.query.text"]}

        def http_detail(tags, kind):
            return (tags.get("span.kind") == kind and tags.get("http.request.method")
                and tags.get("http.route") and isinstance(tags.get("http.response.status_code"), int)
                and (tags.get("server.address") or tags.get("url.full")))

        http = []
        http_services = (("api-gateway", "identity-service"),) if authentication else (
            ("orders-service", "identity-service"), ("orders-service", "payment-mock"),
            ("notification-worker", "mail-mock"))
        for client, server in http_services:
            pairs = [(ct, st) for owner, span, st in rows.values() if owner == server and http_detail(st, "server")
                     for parent_owner, _, ct in ancestors(span) if parent_owner == client and http_detail(ct, "client")
                     and ct["http.request.method"] == st["http.request.method"]
                     and ct["http.response.status_code"] == st["http.response.status_code"]]
            require(bool(pairs), "missing contextual HTTP detail: " + client + " -> " + server)
            ct, st = pairs[0]
            http.append({"client": client, "server": server, "pair_count": len(pairs),
                         "method": ct["http.request.method"], "route": ct["http.route"],
                         "status": st["http.response.status_code"]})
        if authentication:
            return {"http": http}
        grpc = [(owner, span, tags) for owner, span, tags in rows.values()
                if tags.get("rpc.system") == "grpc" and tags.get("rpc.service") == "netdosuga.demo.v1.OrderService"
                and tags.get("rpc.method") == "Shop" and "rpc.grpc.status_code" in tags]
        require(any(owner == "orders-service" and tags.get("span.kind") == "server"
                    and any(parent_owner == "api-gateway" and pt.get("span.kind") == "client"
                            and pt.get("rpc.method") == "Shop" and pt.get("rpc.system") == "grpc"
                            and pt.get("rpc.service") == tags["rpc.service"]
                            and "rpc.grpc.status_code" in pt for parent_owner, _, pt in ancestors(span))
                    for owner, span, tags in grpc), "missing contextual gRPC Shop detail")
        kafka = [(tags, parent_tags) for owner, span, tags in rows.values()
                 if owner == "notification-worker" and span["operationName"] == "business event consume"
                 and tags.get("messaging.system") == "kafka" and tags.get("messaging.operation.name") == "process"
                 and tags.get("messaging.message.type") == "order.paid"
                 for parent_owner, parent_span, parent_tags in ancestors(span)
                 if parent_owner == "orders-service" and parent_span["operationName"] == "outbox.publish"
                 and parent_tags.get("messaging.system") == "kafka"
                 and parent_tags.get("messaging.operation.name") == "publish"
                 and parent_tags.get("messaging.message.type") == "order.paid"
                 and tags.get("messaging.destination.name")
                 and tags["messaging.destination.name"] == parent_tags.get("messaging.destination.name")]
        require(bool(kafka), "missing contextual Kafka producer/consumer detail")
        return {"sql": sql, "http": http, "grpc_shop_span_count": len(grpc),
                "kafka": {"pair_count": len(kafka), "topic": kafka[0][0]["messaging.destination.name"],
                          "event_type": "order.paid"}}

    def evidence(self):
        self.report["phase"] = "wait_trace_and_notification"
        trace_id = self.report["payment_trace_id"]
        expected = {
            "api-gateway": set(),
            "orders-service": {"operation.orders.pay", "payment.authorize", "outbox.enqueue", "outbox.publish"},
            "identity-service": {"session.lookup"},
            "payment-mock": {"operation.payment", "validate", "idempotency.lookup", "provider.simulate", "persist"},
            "notification-worker": {"business event consume", "validate", "deduplicate", "send", "commit"},
            "mail-mock": {"POST /mock/business-emails"},
        }
        deadline = time.monotonic() + self.args.wait
        missing = []
        while time.monotonic() < deadline:
            try:
                traces = self.fetch_trace(trace_id)
                spans = {}
                for trace in traces:
                    require(trace["traceID"] == trace_id, "Jaeger returned another trace")
                    for span in trace["spans"]:
                        service = trace["processes"][span["processID"]]["serviceName"]
                        spans.setdefault(service, set()).add(span["operationName"])
                self.report["trace_spans"] = {service: sorted(names) for service, names in spans.items()}
                missing = [service + ":" + name for service, names in expected.items()
                           for name in (names - spans.get(service, set()))]
                missing += [service for service in expected if service not in spans]
                with urllib.request.urlopen(self.args.mock.rstrip("/") + "/mock/business-deliveries", timeout=5) as response:
                    deliveries = json.load(response)["deliveries"]
                matched = [d for d in deliveries if d["order_id"] == self.report["order_id"] and d.get("trace_id") == trace_id]
                self.report["notification"] = {"matching_delivery_count": len(matched), "trace_id": trace_id}
                if not missing and matched:
                    try:
                        detail = self.detailed_evidence(traces)
                        checked = list(traces)
                        for step in self.report["steps"]:
                            if step["step"] in ("login_admin", "payment_mock_error", "redeem_ticket"):
                                extra = self.fetch_trace(step["trace_id"])
                                require(any(t.get("spans") for t in extra), "missing error or redemption trace")
                                if step["step"] == "login_admin":
                                    authentication = self.detailed_evidence(extra, authentication=True)
                                    authentication["trace_id"] = step["trace_id"]
                                if step["step"] == "payment_mock_error":
                                    error_tags = [{tag["key"]: tag["value"] for tag in span.get("tags", [])}
                                                  for trace in extra for span in trace["spans"]]
                                    require(any(tags.get("span.kind") == "server"
                                                and tags.get("http.request.method") == "POST"
                                                and tags.get("http.route")
                                                and tags.get("http.response.status_code") == 502
                                                for tags in error_tags), "missing failed payment HTTP 502 detail")
                                checked.extend(extra)
                        for session in self.sessions.values():
                            for handler in session.handlers:
                                if isinstance(handler, urllib.request.HTTPCookieProcessor):
                                    self.private_values.update(cookie.value for cookie in handler.cookiejar)
                        serialized = json.dumps(checked)
                        require(not any(value and value in serialized for value in self.private_values),
                                "private value found in Jaeger trace")
                        detail["trace_id"] = trace_id
                        self.report["protocol_details"] = {"payment": detail, "authentication": authentication}
                        self.report["redaction"] = {"checked_trace_count": len(checked), "private_values_absent": True}
                    except RuntimeError as error:
                        missing = [str(error)]
                        time.sleep(min(1, max(0, deadline - time.monotonic())))
                        continue
                    print("Jaeger verified payment, Kafka consumer, and mock mail on the same trace", flush=True)
                    return
            except (urllib.error.URLError, TimeoutError, OSError, ValueError):
                # Export batches and consumers become visible asynchronously.
                pass
            time.sleep(min(1, max(0, deadline - time.monotonic())))
        self.report["missing_spans"] = missing
        raise RuntimeError("bounded wait expired: payment trace or matching notification evidence incomplete")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default=os.getenv("GATEWAY_URL", "http://gateway:8080"), help="gateway origin")
    parser.add_argument("--jaeger", default=os.getenv("JAEGER_URL", "http://jaeger:16686"), help="Jaeger query origin")
    parser.add_argument("--jaeger-ui", default=os.getenv("JAEGER_UI_URL", "http://localhost:16686"), help="browser Jaeger origin")
    parser.add_argument("--mock", default=os.getenv("MOCK_URL", "http://integration-mock:8083"), help="mail mock origin")
    parser.add_argument("--results", default=os.getenv("RESULTS_DIR", "/results"), help="report directory")
    parser.add_argument("--wait", type=float, default=90, help="bounded evidence polling seconds (1..300)")
    args = parser.parse_args()
    if not 1 <= args.wait <= 300:
        parser.error("--wait must be between 1 and 300")
    demo = Demo(args)
    try:
        demo.run()
    except Exception as error:
        # Exception text can contain URLs (including redemption codes); never persist it.
        demo.report["failure"] = {"type": type(error).__name__, "phase": demo.report.get("phase", "initialization")}
        if isinstance(error, RuntimeError):
            demo.report["failure"]["reason"] = str(error)
        print(f"FAILED in {demo.report['failure']['phase']} ({type(error).__name__}); see report", flush=True)
    finally:
        directory = Path(args.results)
        directory.mkdir(parents=True, exist_ok=True)
        target = directory / ("demo-trace-" + demo.report["run_id"] + ".json")
        target.write_text(json.dumps(demo.report, indent=2) + "\n", encoding="utf-8")
        print("Report: " + str(target), flush=True)
        for step in demo.report["steps"]:
            if step["step"] in ("create_application", "payment_mock_error", "payment_success", "redeem_ticket"):
                print(step["step"] + ": " + step["jaeger_url"], flush=True)
        trace_id = demo.report.get("payment_trace_id")
        if trace_id:
            print("PowerShell log correlation (HA stack, from repository root):", flush=True)
            print("docker compose -f platform/compose.ha.yaml logs --no-color gateway-a gateway-b order-a order-b identity-service payment-mock notification-service integration-mock | Select-String -SimpleMatch '" + trace_id + "'", flush=True)
    return 0 if demo.report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
