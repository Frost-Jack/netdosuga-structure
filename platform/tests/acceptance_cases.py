"""Independent HTTP/SQL acceptance cases for the implemented MVP."""

from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta
import hashlib
import http.cookies
import json
import re
import threading
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
from uuid import uuid4

cases = []


class CheckFailure(AssertionError):
    """An assertion whose message contains only result codes and safe metadata."""


def case(name, group="functional"):
    def register(function):
        cases.append((group, name, function))
        return function
    return register


def variants(name, values, group="functional"):
    def register(function):
        for label, value in values.items():
            case(f"{name}-{label}", group)(lambda test, v=value: function(test, v))
        return function
    return register


def quote(value):
    return "'" + value.replace("'", "''") + "'"


def until(probe, seconds=8):
    deadline = time.monotonic() + seconds
    while True:
        value = probe()
        if value:
            return value
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise AssertionError("condition did not become true before deadline")
        time.sleep(min(0.2, remaining))


def concurrent(*functions):
    ready = threading.Barrier(len(functions), timeout=5)

    def start(function):
        ready.wait()
        return function()

    with ThreadPoolExecutor(max_workers=len(functions)) as pool:
        return list(pool.map(start, functions))


def request(url, method="GET", body=None, headers=None, timeout=5, raw=False):
    payload = body if isinstance(body, bytes) else None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=payload, method=method,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        response = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        content = response.read()
        return response.status, content if raw else json.loads(content or "{}"), response.headers


class Scenario(unittest.TestCase):
    def __init__(self, name, function, stack, browser="chromium"):
        super().__init__()
        self.name = name
        self.function = function
        self.stack = stack
        self.browser = browser
        self.prefix = "test-" + uuid4().hex
        self.actor_ids = {}
        self.tokens = {}

    def id(self):
        return self.name

    def runTest(self):
        self.function(self)

    @property
    def base(self):
        return self.stack.urls["http-router"]

    def sql(self, statement):
        return self.stack.sql(statement)

    def rows(self, query):
        return self.stack.rows(query)

    def actors(self, *actors):
        users, sessions = [], []
        roles = {"c1": "customer", "c2": "customer", "v1": "vendor", "v2": "vendor", "admin": "admin"}
        for actor in actors:
            if actor in self.tokens:
                continue
            actor_id = self.prefix + "-" + actor
            token = uuid4().hex + uuid4().hex
            self.actor_ids[actor] = actor_id
            self.tokens[actor] = token
            role = "customer" if actor.startswith("load-") else roles[actor]
            users.append("(" + ",".join(map(quote, (actor_id, actor_id + "@example.test", actor, role, "fixture"))) + ")")
            sessions.append(f"({quote(hashlib.sha256(token.encode()).hexdigest())},{quote(actor_id)},now()+interval '24 hours')")
        if users:
            self.sql("BEGIN; INSERT INTO identity.users(id,email,name,role,password_hash) VALUES " + ",".join(users)
                     + "; INSERT INTO identity.sessions VALUES " + ",".join(sessions) + "; COMMIT;")

    def http(self, path, method="GET", body=None, actor=None, expected=200, code=None, headers=None, timeout=5):
        auth = {"Cookie": "nd_session=" + self.tokens[actor]} if actor else {}
        status, value, response_headers = request(self.base + path, method, body, {**auth, **(headers or {})}, timeout)
        statuses = (expected,) if isinstance(expected, int) else expected
        if status not in statuses:
            raise CheckFailure(f"{method}: HTTP {status}, expected {statuses}")
        if code is not None and value.get("code") != code:
            actual = value.get("code")
            if not isinstance(actual, str) or not re.fullmatch(r"[A-Z_]{1,80}", actual):
                actual = "<missing or invalid>"
            raise CheckFailure(f"code {actual}, expected {code}")
        return value, response_headers

    def shop(self, path, *args, **kwargs):
        value, _ = self.http("/api/v1/shop/" + path, *args, **kwargs)
        self.assertIs(value.get("demo"), True)
        self.assertRegex(value.get("trace_id", ""), r"^[0-9a-f]{32}$")
        return value

    def offer(self, vendor="v1", suffix="a", **fields):
        self.actors(vendor)
        oid = self.prefix + "-" + suffix
        values = {"title": oid, "description": "Тестовое событие", "city": self.prefix,
                  "category": "acceptance", "price_cents": 150000, "capacity": 2,
                  "available": 2, "status": "PUBLISHED", **fields}
        columns = ",".join(values)
        literals = ",".join(quote(v) if isinstance(v, str) else str(v) for v in values.values())
        self.sql(f"INSERT INTO business_offers(id,vendor_id,{columns}) VALUES({quote(oid)},{quote(self.actor_ids[vendor])},{literals});")
        return oid

    def application(self):
        self.actors("v1", "admin")
        return {"organization": self.prefix, "title": self.prefix, "city": self.prefix, "category": "acceptance"}

    def pending(self):
        return self.shop("applications", "POST", self.application(), actor="v1")["application"]

    def update_fields(self, **overrides):
        return {"title": self.prefix, "description": "Описание", "city": self.prefix,
                "category": "acceptance", "price_cents": 1000, "capacity": 2, **overrides}

    def catalog(self, **filters):
        return self.shop("catalog?" + urllib.parse.urlencode(filters))["offers"]

    def cart_item(self, offer, quantity, actor="c1"):
        return self.shop("cart/items", "POST", {"offer_id": offer, "quantity": quantity}, actor=actor)["cart"]

    def checkout(self, actor="c1", key=None):
        return self.shop("orders", "POST", {}, actor=actor, expected=(200, 201),
                         headers={"Idempotency-Key": key or self.prefix})["order"]

    def order_change(self, order, action, scenario="success", actor="c1", **kwargs):
        return self.shop("orders/" + order["id"] + "/" + action, "POST", {"scenario": scenario}, actor=actor, **kwargs)

    def control_cart(self, actor="c1", suffix=""):
        self.actors(actor)
        a = self.offer(suffix="a" + suffix)
        b = self.offer("v2", suffix="b" + suffix, price_cents=90000)
        self.cart_item(a, 2, actor)
        self.cart_item(b, 1, actor)
        return a, b

    def control_order(self, state="RESERVED", actor="c1", suffix=""):
        a, b = self.control_cart(actor, suffix)
        order = self.checkout(actor, self.prefix + suffix)
        if state == "PAID":
            order = self.order_change(order, "pay", actor=actor)["order"]
        return a, b, order

    def stock(self, *offers):
        rows = self.rows("SELECT id, available FROM business_offers WHERE id IN (" + ",".join(map(quote, offers)) + ")")
        by_id = {entry["id"]: entry["available"] for entry in rows}
        return tuple(by_id[offer] for offer in offers)

    def events(self, order_id, kind=None):
        condition = "" if kind is None else f" AND payload->>'event_type'={quote(kind)}"
        return self.rows(f"SELECT event_id,payload,headers,published_at FROM business_outbox WHERE payload->>'order_id'={quote(order_id)}{condition} ORDER BY event_id")

    def snapshot(self, oid, offers):
        order = self.shop("orders/" + oid, actor="c1")["order"]
        events = [event["event_id"] for event in self.events(oid)]
        return order, self.stock(*offers), self.effects(oid), events

    def effects(self, order_id):
        return self.rows(f"SELECT kind,amount_cents,result FROM payment_mock.effects WHERE order_id={quote(order_id)} ORDER BY kind")

    def mail(self, order_id, kind, trace_id=None, seconds=10):
        def delivered():
            event, = self.events(order_id, kind)
            if event["published_at"] is None:
                return None
            eid = event["event_id"]
            self.assertEqual((event["payload"]["event_id"], event["payload"]["order_id"]), (eid, order_id))
            rows = self.rows(f"SELECT delivered_at FROM business_notifications WHERE event_id={quote(eid)}")
            if not rows:
                return None
            self.assertEqual(len(rows), 1)
            self.assertTrue(rows[0]["delivered_at"])
            _, value, _ = request(self.stack.urls["integration-mock"] + "/mock/business-deliveries")
            mails = [mail for mail in value["deliveries"] if mail["event_id"] == eid]
            if not mails:
                return None
            self.assertEqual(len(mails), 1)
            self.assertEqual(mails[0]["order_id"], order_id)
            recorded_trace = event["headers"]["traceparent"].split("-")[1]
            self.assertEqual(mails[0]["trace_id"], recorded_trace)
            if trace_id:
                self.assertEqual(recorded_trace, trace_id)
            return event
        return until(delivered, seconds)

    def redeem(self, ticket, actor="v1", **kwargs):
        return self.shop("tickets/" + ticket["code"] + "/redeem", "POST", {}, actor=actor, **kwargs)


@variants("F-01", {"customer": "customer", "vendor": "vendor"})
def registration(t, role):
    email = t.prefix + "@example.test"
    password = "Test-password-" + t.prefix
    data, _ = t.http("/api/v1/auth/register", "POST", {"email": " " + email.upper() + " ", "name": "Имя", "password": password, "role": role}, expected=201)
    user = data["user"]
    rows = t.rows(f"SELECT id,email,role,(password_hash LIKE '$2%') AS bcrypt FROM identity.users WHERE email={quote(email)}")
    t.assertEqual(rows, [{"id": user["id"], "email": email, "role": role, "bcrypt": True}])
    t.assertNotIn("session_token", data)
    login, _ = t.http("/api/v1/auth/login", "POST", {"email": email, "password": password})
    t.assertEqual(login["user"]["id"], user["id"])


@case("F-02")
def shared_session(t):
    start = datetime.fromisoformat(t.sql("SELECT clock_timestamp();"))
    logged, headers = t.http("/api/v1/auth/login", "POST", {"email": "buyer@netdosuga.local", "password": "demo12345"})
    end = datetime.fromisoformat(t.sql("SELECT clock_timestamp();"))
    attributes = http.cookies.SimpleCookie(headers["Set-Cookie"])["nd_session"]
    t.assertTrue(attributes["httponly"])
    t.assertEqual(attributes["samesite"], "Lax")
    t.assertNotIn("session_token", logged)
    token = attributes.value
    session_hash = hashlib.sha256(token.encode()).hexdigest()
    row, = t.rows(f"SELECT token_hash,expires_at,user_id FROM identity.sessions WHERE token_hash={quote(session_hash)}")
    t.assertEqual((row["token_hash"], row["user_id"]), (session_hash, logged["user"]["id"]))
    expires = datetime.fromisoformat(row["expires_at"])
    t.assertTrue(start + timedelta(hours=24) <= expires <= end + timedelta(hours=24))
    for _ in range(6):
        session, other = t.http("/api/v1/auth/session", headers={"Cookie": "nd_session=" + token})
        t.assertEqual(session["user"], logged["user"])
        t.assertNotIn("session_token", session)
        if other["X-Gateway-Instance"] != headers["X-Gateway-Instance"]:
            break
    else:
        t.fail("second gateway did not resolve the session")


@case("F-03")
def application_created(t):
    expected = t.application()
    app = t.pending()
    t.assertEqual({key: app[key] for key in expected}, expected)
    t.assertEqual((app["status"], app["vendor_id"]), ("PENDING", t.actor_ids["v1"]))
    t.assertIn(app, t.shop("applications", actor="admin")["applications"])


@variants("F-04", {"approve": "approve"})
@variants("F-05", {"reject": "reject"})
def moderate(t, action):
    app = t.pending()
    body = {"reason": "Нет документов"} if action == "reject" else {}
    result = t.shop(f"applications/{app['id']}/{action}", "POST", body, actor="admin")
    expected = "APPROVED" if action == "approve" else "REJECTED"
    t.assertEqual(result["application"]["status"], expected)
    offers = t.rows(f"SELECT id,vendor_id,status FROM business_offers WHERE title={quote(t.prefix)}")
    if action == "approve":
        t.assertEqual(offers, [{"id": result["offer"]["id"], "vendor_id": app["vendor_id"], "status": "DRAFT"}])
        t.assertEqual(t.catalog(city=t.prefix), [])
    else:
        t.assertEqual(result["application"]["reason"], body["reason"])
        t.assertEqual(offers, [])


@case("F-06")
def publish(t):
    oid = t.offer(status="DRAFT")
    inputs = t.update_fields()
    t.shop(f"offers/{oid}", "PATCH", inputs, actor="v1")
    result = t.shop(f"offers/{oid}/publish", "POST", {}, actor="v1")["offer"]
    t.assertEqual(result["status"], "PUBLISHED")
    t.assertEqual({key: result[key] for key in inputs}, inputs)
    t.assertEqual(t.catalog(city=t.prefix), [result])


@variants("F-07", {key: key for key in ("all", "city", "category", "both", "unknown", "limit")})
def catalog_filters(t, mode):
    if mode == "limit":
        t.actors("v1")
        t.sql(f"""INSERT INTO business_offers(id,vendor_id,title,city,category,status)
               SELECT {quote(t.prefix)} || lpad(i::text,3,'0'),{quote(t.actor_ids['v1'])},'limit',
                      {quote(t.prefix)},'acceptance','PUBLISHED' FROM generate_series(1,201) i;""")
        t.assertEqual([item["id"] for item in t.catalog(city=t.prefix)], [f"{t.prefix}{i:03d}" for i in range(1, 201)])
        return
    a = t.offer(category=t.prefix)
    b = t.offer(suffix="b", category="different")
    c = t.offer(suffix="c", city="different", category=t.prefix)
    t.offer(suffix="d", status="DRAFT", category=t.prefix)
    filters, expected = {"all": ({}, {a, b, c}), "city": ({"city": t.prefix}, {a, b}),
                         "category": ({"category": t.prefix}, {a, c}), "both": ({"city": t.prefix, "category": t.prefix}, {a}),
                         "unknown": ({"city": t.prefix + "-missing"}, set())}[mode]
    actual = {offer["id"] for offer in t.catalog(**filters) if offer["id"].startswith(t.prefix)}
    t.assertEqual(actual, expected)


@variants("F-08", {"add": "add", "replace": "replace", "remove": "remove"})
def cart_mutation(t, mode):
    t.actors("c1")
    oid = t.offer()
    if mode != "add":
        t.cart_item(oid, 1)
    if mode == "remove":
        cart = t.shop("cart/items/" + oid, "DELETE", actor="c1")["cart"]
        t.assertEqual(cart, {"items": [], "total_cents": 0})
    else:
        quantity = 1 if mode == "add" else 2
        cart = t.cart_item(oid, quantity)
        t.assertEqual([(row["offer_id"], row["quantity"]) for row in cart["items"]], [(oid, quantity)])
        t.assertEqual(cart["total_cents"], quantity * 150000)


@case("F-09")
def reserve(t):
    a, b, order = t.control_order()
    t.assertEqual((order["status"], order["total_cents"], order["commission_cents"]), ("RESERVED", 390000, 39000))
    t.assertEqual({item["offer_id"]: item["quantity"] for item in order["items"]}, {a: 2, b: 1})
    t.assertEqual(t.stock(a, b), (0, 1))
    t.assertEqual(t.shop("cart", actor="c1")["cart"], {"items": [], "total_cents": 0})
    t.assertEqual(datetime.fromisoformat(order["expires_at"]) - datetime.fromisoformat(order["created_at"]), timedelta(minutes=15))
    t.assertEqual(len(t.events(order["id"], "order.created")), 1)


@case("F-10")
def repeat_checkout(t):
    a, b, order = t.control_order()
    before = t.snapshot(order["id"], (a, b))
    t.assertEqual(t.checkout(), order)
    t.assertEqual(t.snapshot(order["id"], (a, b)), before)
    t.assertEqual([entry["id"] for entry in t.shop("orders", actor="c1")["orders"]], [order["id"]])


@case("F-10-changed-cart")
def repeat_checkout_with_changed_cart(t):
    a, b, order = t.control_order()
    added = t.offer(suffix="next", price_cents=110000)
    cart = t.cart_item(added, 1)
    before = t.snapshot(order["id"], (a, b, added))
    t.assertEqual(t.checkout(), order)
    t.assertEqual(t.shop("cart", actor="c1")["cart"], cart)
    t.assertEqual(t.snapshot(order["id"], (a, b, added)), before)
    t.assertEqual([entry["id"] for entry in t.shop("orders", actor="c1")["orders"]], [order["id"]])


def assert_paid(t, order, a, b):
    t.assertEqual(order["status"], "PAID")
    tickets = order["tickets"]
    t.assertEqual(Counter((ticket["order_id"], ticket["offer_id"], ticket["status"]) for ticket in tickets),
                  Counter({(order["id"], a, "VALID"): 2, (order["id"], b, "VALID"): 1}))
    t.assertEqual(len({ticket["code"] for ticket in tickets}), 3)
    effect, = t.effects(order["id"])
    t.assertEqual((effect["kind"], effect["amount_cents"], effect["result"]["payment_id"]), ("payment", 390000, order["payment_id"]))
    t.assertEqual(len(t.events(order["id"], "order.paid")), 1)


@case("F-11")
def pay(t):
    a, b, order = t.control_order()
    assert_paid(t, t.order_change(order, "pay")["order"], a, b)


@case("F-12")
def repeat_pay(t):
    a, b, order = t.control_order("PAID")
    before = t.snapshot(order["id"], (a, b))
    t.assertEqual(t.order_change(order, "pay")["order"], order)
    t.assertEqual(t.snapshot(order["id"], (a, b)), before)


@case("F-13")
def read_owned(t):
    _, _, order = t.control_order("PAID")
    t.control_order(actor="c2", suffix="-other")
    t.assertEqual([entry["id"] for entry in t.shop("orders", actor="c1")["orders"]], [order["id"]])
    t.assertEqual(t.shop("orders/" + order["id"], actor="c1")["order"], order)


def assert_cancelled(t, order, offers, paid):
    t.assertEqual(order["status"], "CANCELLED")
    t.assertEqual(t.stock(*offers), (2, 2))
    t.assertEqual(len(t.events(order["id"], "order.cancelled")), 1)
    effects = t.effects(order["id"])
    if paid:
        t.assertEqual([ticket["status"] for ticket in order["tickets"]], ["REVOKED"] * 3)
        t.assertEqual([(effect["kind"], effect["amount_cents"]) for effect in effects], [("payment", 390000), ("refund", 390000)])
        t.assertEqual(effects[1]["result"]["refund_id"], order["refund_id"])
    else:
        t.assertEqual((order["tickets"], effects), ([], []))


@case("F-14")
def cancel_reserved(t):
    a, b, order = t.control_order()
    assert_cancelled(t, t.order_change(order, "cancel")["order"], (a, b), False)


@case("F-15")
def refund(t):
    a, b, order = t.control_order("PAID")
    assert_cancelled(t, t.order_change(order, "cancel")["order"], (a, b), True)


@case("F-16")
def repeat_refund(t):
    a, b, paid = t.control_order("PAID")
    order = t.order_change(paid, "cancel")["order"]
    before = t.snapshot(order["id"], (a, b))
    t.assertEqual(t.order_change(order, "cancel")["order"], order)
    t.assertEqual(t.snapshot(order["id"], (a, b)), before)


@variants("F-17", {"due": True, "future": False})
def expiry(t, due):
    a, b, order = t.control_order()
    oid = order["id"]
    if not due:
        t.assertEqual(t.shop("orders/" + oid, actor="c1")["order"]["status"], "RESERVED")
        t.assertEqual(t.stock(a, b), (0, 1))
        t.assertEqual(t.events(oid, "order.expired"), [])
        return
    t.sql(f"UPDATE business_orders SET expires_at=now() WHERE id={quote(oid)};")
    until(lambda: t.events(oid, "order.expired"), 12)
    t.assertEqual(t.shop("orders/" + oid, actor="c1")["order"]["status"], "EXPIRED")
    t.assertEqual(t.stock(a, b), (2, 2))
    t.assertEqual(len(t.events(oid, "order.expired")), 1)


@case("F-18")
def redeem(t):
    a, _, order = t.control_order("PAID")
    ticket = next(ticket for ticket in order["tickets"] if ticket["offer_id"] == a)
    t.assertEqual(t.redeem(ticket)["ticket"]["status"], "REDEEMED")
    after = t.shop("orders/" + order["id"], actor="c1")["order"]
    t.assertEqual(after["status"], "PAID")
    t.assertEqual({entry["code"]: entry["status"] for entry in after["tickets"]},
                  {entry["code"]: "REDEEMED" if entry == ticket else "VALID" for entry in order["tickets"]})
    t.assertEqual(len(t.events(order["id"], "ticket.redeemed")), 1)


@case("F-19")
def vendor_sales(t):
    a, b, order = t.control_order("PAID")
    _, _, other = t.control_order("PAID", suffix="-excluded")
    t.order_change(other, "cancel")
    for actor, offer, quantity, totals in (("v1", a, 2, (300000, 30000, 270000)),
                                           ("v2", b, 1, (90000, 9000, 81000))):
        sales = t.shop("sales", actor=actor)
        t.assertEqual(tuple(sales[key] for key in ("gross_cents", "commission_cents", "net_cents")), totals)
        t.assertEqual([(row["order_id"], row["vendor_id"], row["offer_id"], row["quantity"]) for row in sales["sales"]],
                      [(order["id"], t.actor_ids[actor], offer, quantity)])


@variants("F-20", {name: name for name in ("created", "paid", "cancelled", "expired", "redeemed")})
def business_delivery(t, state):
    from acceptance_observe import linked, tags, trace

    a, b, order = t.control_order("PAID" if state == "redeemed" else "RESERVED")
    trace_id = None
    kind = "order." + state
    if state in ("paid", "cancelled"):
        trace_id = t.order_change(order, "pay" if state == "paid" else "cancel")["trace_id"]
    if state == "expired":
        t.sql(f"UPDATE business_orders SET expires_at=now() WHERE id={quote(order['id'])};")
        until(lambda: t.events(order["id"], kind), 12)
    if state == "redeemed":
        kind = "ticket.redeemed"
        trace_id = t.redeem(next(ticket for ticket in order["tickets"] if ticket["offer_id"] == a))["trace_id"]
    event = t.mail(order["id"], kind, trace_id)
    recorded_trace = event["headers"]["traceparent"].split("-")[1]
    actual = trace(t, recorded_trace, {"outbox.publish", "business event consume"})
    publish, = [span for span in actual["spans"]
                if span["operationName"] == "outbox.publish" and tags(span).get("messaging.message.type") == kind]
    t.assertTrue(any(span["operationName"] == "business event consume"
                     and tags(span).get("messaging.message.type") == kind and linked(span, publish["spanID"])
                     for span in actual["spans"]))
