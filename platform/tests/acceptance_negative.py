"""Validation and transactional invariants observed through the public API."""

from collections import Counter
import time

from acceptance_cases import case, concurrent, quote, request, until, variants


@variants("N-01a", {"same": str, "upper": str.upper, "spaces": lambda email: " " + email + " "})
def duplicate_registration(t, transform):
    email = t.prefix + "@example.test"
    body = {"email": email, "name": "Test", "role": "customer", "password": "test-password"}
    t.http("/api/v1/auth/register", "POST", body, expected=201)
    t.http("/api/v1/auth/register", "POST", {**body, "email": transform(email)}, expected=409, code="EMAIL_EXISTS")
    t.assertEqual(t.rows(f"SELECT count(*) AS users FROM identity.users WHERE email={quote(email)}"), [{"users": 1}])


@variants("N-01b", {"wrong-password": True, "unknown-email": False})
def wrong_login(t, known):
    if known:
        t.actors("c1")
    who = t.actor_ids.get("c1", t.prefix)
    before = t.rows(f"SELECT token_hash FROM identity.sessions WHERE user_id={quote(who)}")
    response, headers = t.http("/api/v1/auth/login", "POST", {"email": who + "@example.test", "password": "invalid-password"},
                               expected=401, code="INVALID_CREDENTIALS")
    t.assertEqual(set(response), {"message", "code", "trace_id"})
    t.assertIsNone(headers.get("Set-Cookie"))
    t.assertEqual(t.rows(f"SELECT token_hash FROM identity.sessions WHERE user_id={quote(who)}"), before)


@case("N-02a-admin")
def cannot_register_admin(t):
    email = t.prefix + "@example.test"
    t.http("/api/v1/auth/register", "POST", {"email": email, "name": "Test", "role": "admin", "password": "test-password"},
           expected=400, code="INVALID_CREDENTIALS")
    t.assertEqual(t.rows(f"SELECT id FROM identity.users WHERE email={quote(email)}"), [])


@case("N-02a-spoof")
def spoofed_vendor(t):
    app = t.pending()
    data = t.shop(f"applications/{app['id']}/approve", "POST", {"actor": t.actor_ids["admin"], "role": "admin", "user_id": t.actor_ids["admin"]},
                  actor="v1", headers={"X-Demo-Actor": "admin-demo"}, expected=403, code="FORBIDDEN")
    t.assertEqual(set(data), {"code", "demo", "message", "trace_id"})
    t.assertIn(app, t.shop("applications", actor="admin")["applications"])
    t.assertEqual(t.rows(f"SELECT id FROM business_offers WHERE title={quote(t.prefix)}"), [])


@variants("N-02b", {
    "application-list": ("c1", "GET", "applications"),
    "application-create": ("c1", "POST", "applications"),
    "application-approve": ("c1", "POST", "applications/{app}/approve"),
    "application-reject": ("c1", "POST", "applications/{app}/reject"),
    "offer-list": ("c1", "GET", "offers"),
    "offer-update": ("v2", "PATCH", "offers/{offer}"),
    "offer-publish": ("c1", "POST", "offers/{offer}/publish"),
    "foreign-publish": ("v2", "POST", "offers/{offer}/publish"),
    "cart-get": ("v1", "GET", "cart"),
    "cart-add": ("v1", "POST", "cart/items"),
    "cart-remove": ("v1", "DELETE", "cart/items/{offer}"),
    "checkout": ("v1", "POST", "orders"),
    "order-list": ("v1", "GET", "orders"),
    "order-read": ("v1", "GET", "orders/{order}"),
    "foreign-order-read": ("c2", "GET", "orders/{order}"),
    "foreign-pay": ("c2", "POST", "orders/{order}/pay"),
    "foreign-cancel": ("c2", "POST", "orders/{order}/cancel"),
    "ticket-role": ("c1", "POST", "tickets/{ticket}/redeem"),
    "foreign-ticket": ("v2", "POST", "tickets/{ticket}/redeem"),
    "sales": ("c1", "GET", "sales"),
})
def role_matrix(t, route):
    actor, method, path = route
    t.actors("c1", "c2", "v1", "v2", "admin")
    app = t.pending()
    a, b, order = t.control_order("PAID")
    draft = t.offer(suffix="draft", status="DRAFT")
    ticket = next(ticket for ticket in order["tickets"] if ticket["offer_id"] == a)
    snapshot = t.snapshot(order["id"], (a, b, draft))
    offers = t.shop("offers", actor="v1")["offers"]
    applications = t.shop("applications", actor="v1")["applications"]
    path = path.format(app=app["id"], order=order["id"], offer=draft, ticket=ticket["code"])
    data = t.shop(path, method, t.update_fields(offer_id=a, quantity=1, **t.application()), actor=actor,
                  headers={"Idempotency-Key": t.prefix + "-foreign"}, expected=403, code="FORBIDDEN")
    t.assertEqual(set(data), {"code", "demo", "message", "trace_id"})
    t.assertEqual(t.snapshot(order["id"], (a, b, draft)), snapshot)
    t.assertEqual(t.shop("offers", actor="v1")["offers"], offers)
    t.assertEqual(t.shop("applications", actor="v1")["applications"], applications)
    t.assertEqual(t.shop("cart", actor="c1")["cart"], {"items": [], "total_cents": 0})
    t.assertEqual(t.shop("orders", actor="c2")["orders"], [])


@variants("N-03", {key: key for key in ("missing", "fake", "expired", "revoked", "origin")})
def invalid_session(t, mode):
    t.actors("c1")
    offer = t.offer()
    token = t.tokens["c1"]
    filters = f" WHERE user_id={quote(t.actor_ids['c1'])}"
    if mode == "expired":
        t.sql("UPDATE identity.sessions SET expires_at=now()" + filters + ";")
    if mode == "revoked":
        t.http("/api/v1/auth/logout", "POST", {}, actor="c1", expected=204)
    headers = {"Cookie": "nd_session=" + ("f" * 64 if mode == "fake" else token)} if mode != "missing" else {}
    if mode == "origin":
        headers["Origin"] = "https://other.invalid"
    before = t.rows("SELECT token_hash,expires_at FROM identity.sessions" + filters)
    t.shop("cart/items", "POST", {"offer_id": offer, "quantity": 1}, headers=headers,
           expected=403 if mode == "origin" else 401, code="FORBIDDEN" if mode == "origin" else "UNAUTHENTICATED")
    t.assertEqual(t.rows("SELECT token_hash,expires_at FROM identity.sessions" + filters), before)
    t.assertEqual(t.rows(f"SELECT quantity FROM business_cart WHERE offer_id={quote(offer)}"), [])


@variants("N-05a", {field: field for field in ("organization", "title", "city", "category")})
def required_application_fields(t, field):
    body = t.application()
    del body[field]
    t.shop("applications", "POST", body, actor="v1", expected=400, code="REQUIRED_FIELDS")
    t.assertEqual(t.shop("applications", actor="v1")["applications"], [])
    t.assertEqual(t.shop("offers", actor="v1")["offers"], [])


@variants("N-05b", {"description": {"description": ""}, "price": {"price_cents": 0}, "capacity": {"capacity": 0, "available": 0}})
def incomplete_offer(t, overrides):
    oid = t.offer(status="DRAFT", **overrides)
    before = t.shop("offers", actor="v1")
    t.shop(f"offers/{oid}/publish", "POST", {}, actor="v1", expected=400, code="INCOMPLETE_OFFER")
    t.assertEqual(t.shop("offers", actor="v1")["offers"], before["offers"])
    t.assertEqual(t.catalog(city=t.prefix), [])


@variants("N-05c", {"no-reason": (None, "reject"), "approve-twice": ("approve", "approve"), "approve-reject": ("approve", "reject"),
                    "reject-approve": ("reject", "approve"), "reject-twice": ("reject", "reject")})
def invalid_moderation(t, actions):
    app = t.pending()
    first, second = actions
    if first:
        t.shop(f"applications/{app['id']}/{first}", "POST", {"reason": "Fixture"} if first == "reject" else {}, actor="admin")
    before = [t.shop(key, actor="v1")[key] for key in ("applications", "offers")]
    t.shop(f"applications/{app['id']}/{second}", "POST", {"reason": "Other"} if first else {}, actor="admin",
           expected=409 if first else 400, code="INVALID_STATE" if first else "REASON_REQUIRED")
    t.assertEqual([t.shop(key, actor="v1")[key] for key in ("applications", "offers")], before)


@variants("N-06a", {"zero": 0, "negative": -1, "fraction": 1.5, "over": 100000001, "min": 1, "max": 100000000})
def offer_price(t, amount):
    oid = t.offer()
    allowed = type(amount) is int and 1 <= amount <= 100000000
    t.shop(f"offers/{oid}", "PATCH", t.update_fields(price_cents=amount), actor="v1",
           expected=200 if allowed else 400, code=None if allowed else "INVALID_AMOUNT_OR_CAPACITY")
    t.assertEqual(t.catalog(city=t.prefix)[0]["price_cents"], amount if allowed else 150000)


@variants("N-06b", {"zero": 0, "negative": -1, "fraction": 1.5, "over": 21, "min": 1, "max": 20})
def cart_quantity(t, quantity):
    t.actors("c1")
    oid = t.offer(capacity=20, available=20)
    allowed = type(quantity) is int and 1 <= quantity <= 20
    t.shop("cart/items", "POST", {"offer_id": oid, "quantity": quantity}, actor="c1",
           expected=200 if allowed else 400, code=None if allowed else "INVALID_QUANTITY")
    cart = t.shop("cart", actor="c1")["cart"]
    t.assertEqual([row["quantity"] for row in cart["items"]], [quantity] if allowed else [])
    t.assertEqual(cart["total_cents"], 150000 * quantity if allowed else 0)
    t.assertEqual(t.stock(oid), (20,))


@variants("N-06c", {"below-reserved": (1, 2), "negative": (-1, 0), "fraction": (1.5, 0), "over": (100001, 0),
                    "zero": (0, 0), "max": (100000, 0)})
def offer_capacity(t, bounds):
    capacity, used = bounds
    oid = t.offer(status="DRAFT", available=2 - used)
    allowed = type(capacity) is int and used <= capacity <= 100000
    t.shop(f"offers/{oid}", "PATCH", t.update_fields(capacity=capacity), actor="v1",
           expected=200 if allowed else 400, code=None if allowed else "INVALID_AMOUNT_OR_CAPACITY")
    offer, = t.shop("offers", actor="v1")["offers"]
    t.assertEqual((offer["capacity"], offer["available"]), (capacity if allowed else 2, (capacity if allowed else 2) - used))


@variants("N-06d", {"overflow": b'{"quantity":9223372036854775808}', "null": b"null", "array": b"[]",
                    "malformed": b"{", "too-large": b'{"field":"' + b"x" * 65536 + b'"}'})
def malformed_shop_body(t, raw):
    t.actors("c1")
    oid = t.offer()
    t.shop("cart/items", "POST", raw, actor="c1", expected=400)
    t.assertEqual(t.shop("cart", actor="c1")["cart"], {"items": [], "total_cents": 0})
    t.assertEqual(t.shop("orders", actor="c1")["orders"], [])
    t.assertEqual(t.stock(oid), (2,))


@variants("N-07a", {"missing-order": True, "draft-in-cart": False})
def missing_entity(t, order):
    t.actors("c1")
    if order:
        t.shop("orders/" + t.prefix, actor="c1", expected=404, code="NOT_FOUND")
    else:
        offer = t.offer(status="DRAFT")
        t.shop("cart/items", "POST", {"offer_id": offer, "quantity": 1}, actor="c1", expected=404, code="NOT_FOUND")
    t.assertEqual(t.shop("orders", actor="c1")["orders"], [])
    t.assertEqual(t.shop("cart", actor="c1")["cart"]["items"], [])


@variants("N-07b", {"expired-pay": ("EXPIRED", "pay", "INVALID_STATE"), "expired-cancel": ("EXPIRED", "cancel", "INVALID_STATE"),
                    "cancelled-pay": ("CANCELLED", "pay", "INVALID_STATE"), "revoked-redeem": ("REVOKED", "redeem", "INVALID_TICKET_STATE"),
                    "redeemed-redeem": ("REDEEMED", "redeem", "INVALID_TICKET_STATE"),
                    "redeemed-cancel": ("REDEEMED", "cancel", "TICKET_ALREADY_REDEEMED")})
def forbidden_transition(t, variant):
    state, action, code = variant
    a, b, order = t.control_order("PAID" if state in ("REVOKED", "REDEEMED") else "RESERVED")
    ticket = next((entry for entry in order["tickets"] if entry["offer_id"] == a), None)
    if state == "EXPIRED":
        t.sql(f"UPDATE business_orders SET expires_at=now() WHERE id={quote(order['id'])};")
        until(lambda: t.events(order["id"], "order.expired"), 12)
    elif state in ("CANCELLED", "REVOKED"):
        t.order_change(order, "cancel")
    elif state == "REDEEMED":
        t.redeem(ticket)
    before = t.snapshot(order["id"], (a, b))
    if action == "redeem":
        t.redeem(ticket, expected=409, code=code)
    else:
        t.order_change(order, action, expected=409, code=code)
    t.assertEqual(t.snapshot(order["id"], (a, b)), before)


@case("N-08")
def atomic_reservation(t):
    a, b = t.control_cart()
    t.actors("c2")
    t.cart_item(b, 2, "c2")
    t.checkout("c2")
    before = t.shop("cart", actor="c1")["cart"]
    t.shop("orders", "POST", {}, actor="c1", headers={"Idempotency-Key": t.prefix}, expected=409, code="UNAVAILABLE_CAPACITY")
    t.assertEqual(t.shop("orders", actor="c1")["orders"], [])
    t.assertEqual(t.shop("cart", actor="c1")["cart"], before)
    t.assertEqual(t.stock(a, b), (2, 0))


@case("N-09")
def last_seat(t):
    a = t.offer(capacity=1, available=1)
    t.actors("c1", "c2")
    for actor in ("c1", "c2"):
        t.cart_item(a, 1, actor)
    responses = concurrent(*(lambda actor=actor: t.shop("orders", "POST", {}, actor=actor, expected=(200, 409),
                             headers={"Idempotency-Key": t.prefix}) for actor in ("c1", "c2")))
    t.assertEqual(Counter(response.get("code", "success") for response in responses), {"success": 1, "UNAVAILABLE_CAPACITY": 1})
    winner, = [response["order"] for response in responses if "order" in response]
    t.assertEqual((winner["status"], winner["items"][0]["quantity"]), ("RESERVED", 1))
    t.assertEqual(t.stock(a), (0,))
    t.assertEqual(len(t.events(winner["id"], "order.created")), 1)


@variants("N-10a", {action: action for action in ("checkout", "pay", "cancel")})
def concurrent_retries(t, action):
    if action == "checkout":
        a, b = t.control_cart()
        first, second = concurrent(t.checkout, t.checkout)
        expected = ("RESERVED", (), (0, 1), 0, "order.created")
    else:
        a, b, order = t.control_order("PAID" if action == "cancel" else "RESERVED")
        mutate = lambda: t.order_change(order, action)["order"]
        first, second = concurrent(mutate, mutate)
        expected = ("PAID", ("payment",), (0, 1), 3, "order.paid") if action == "pay" else ("CANCELLED", ("payment", "refund"), (2, 2), 3, "order.cancelled")
    t.assertEqual(first, second)
    state, effects, stock, tickets, event = expected
    t.assertEqual(first["status"], state)
    t.assertEqual(tuple(row["kind"] for row in t.effects(first["id"])), effects)
    t.assertEqual(t.stock(a, b), stock)
    t.assertEqual(len({ticket["code"] for ticket in first["tickets"]}), tickets)
    t.assertEqual(len(t.events(first["id"], event)), 1)
    t.assertEqual(len(t.shop("orders", actor="c1")["orders"]), 1)


@variants("N-10b", {"amount": "amount_cents", "order": "order_id"})
def payment_key_conflict(t, field):
    url = t.stack.urls["payment-mock"] + "/v1/payments"
    initial = {"order_id": t.prefix, "amount_cents": 1000, "currency": "RUB"}
    headers = {"Idempotency-Key": "payment:" + t.prefix}
    status, result, _ = request(url, "POST", initial, headers)
    t.assertEqual(status, 200)
    changed = 1001 if field == "amount_cents" else t.prefix + "-other"
    status, data, _ = request(url, "POST", {**initial, field: changed}, headers)
    t.assertEqual((status, data.get("code")), (409, "IDEMPOTENCY_CONFLICT"))
    t.assertEqual(request(url, "POST", initial, headers)[:2], (200, result))
    t.assertEqual(len(t.effects(t.prefix)), 1)
    t.assertEqual(t.effects(t.prefix + "-other"), [])


@variants("N-11a", {"decline": ("pay", "error", 502, "MOCK_FAILED")})
@variants("N-11b", {"refund-decline": ("cancel", "error", 502, "MOCK_DECLINED")})
@variants("N-11c", {"deadline": ("pay", "timeout", 504, "MOCK_TIMEOUT")})
def failed_provider_operation(t, variant):
    action, scenario, status, code = variant
    a, b, order = t.control_order("PAID" if action == "cancel" else "RESERVED")
    before = t.snapshot(order["id"], (a, b))
    started = time.monotonic()
    t.order_change(order, action, scenario, expected=status, code=code)
    t.assertLess(time.monotonic() - started, 5)
    t.assertEqual(t.snapshot(order["id"], (a, b)), before)
    if action == "cancel":
        ticket = next(entry for entry in order["tickets"] if entry["offer_id"] == a)
        t.assertEqual(t.redeem(ticket)["ticket"]["status"], "REDEEMED")


@variants("N-15a", {"redeem-twice": False})
@variants("N-15b", {"redeem-vs-refund": True})
def concurrent_ticket(t, refund):
    a, b, order = t.control_order("PAID")
    ticket = next(entry for entry in order["tickets"] if entry["offer_id"] == a)
    redeem = lambda: t.redeem(ticket, expected=(200, 409))
    cancel = lambda: t.order_change(order, "cancel", expected=(200, 409))
    first, second = concurrent(redeem, cancel if refund else redeem)
    t.assertEqual(sum("code" not in result for result in (first, second)), 1)
    after, stock, effects, _ = t.snapshot(order["id"], (a, b))
    if after["status"] == "PAID":
        t.assertEqual(next(entry["status"] for entry in after["tickets"] if entry["code"] == ticket["code"]), "REDEEMED")
        t.assertEqual(stock, (0, 1))
        t.assertEqual([effect["kind"] for effect in effects], ["payment"])
        t.assertEqual(len(t.events(order["id"], "ticket.redeemed")), 1)
        t.assertEqual(next(result["code"] for result in (first, second) if "code" in result),
                      "TICKET_ALREADY_REDEEMED" if refund else "INVALID_TICKET_STATE")
    else:
        t.assertEqual(after["status"], "CANCELLED")
        t.assertEqual({entry["status"] for entry in after["tickets"]}, {"REVOKED"})
        t.assertEqual(stock, (2, 2))
        t.assertEqual([effect["kind"] for effect in effects], ["payment", "refund"])
        t.assertEqual(t.events(order["id"], "ticket.redeemed"), [])
        t.assertIn(first["code"], ("INVALID_TICKET_STATE", "REFUND_PENDING"))


@case("N-16")
def frozen_price(t):
    a, b, order = t.control_order()
    t.shop("offers/" + a, "PATCH", t.update_fields(price_cents=160000), actor="v1")
    t.assertEqual({row["id"]: row["price_cents"] for row in t.catalog(city=t.prefix)}, {a: 160000, b: 90000})
    paid = t.order_change(order, "pay")["order"]
    t.assertEqual((paid["total_cents"], paid["commission_cents"]), (390000, 39000))
    t.assertEqual({row["offer_id"]: row["price_cents"] for row in paid["items"]}, {a: 150000, b: 90000})
    t.assertEqual([row["amount_cents"] for row in t.effects(paid["id"])], [390000])


@case("N-17")
def round_per_position(t):
    t.actors("c1")
    a = t.offer(price_cents=199)
    b = t.offer(suffix="b", price_cents=101)
    t.cart_item(a, 1)
    t.cart_item(b, 1)
    paid = t.order_change(t.checkout(), "pay")["order"]
    t.assertEqual((paid["total_cents"], paid["commission_cents"]), (300, 29))
    t.assertEqual({row["offer_id"]: row["commission_cents"] for row in paid["items"]}, {a: 19, b: 10})
    t.assertEqual(t.shop("sales", actor="v1")["net_cents"], 271)
