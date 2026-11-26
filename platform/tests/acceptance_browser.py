"""Playwright checks against the same fresh HA stack used by the HTTP tests."""

from contextlib import contextmanager
import json
import os
import re
from uuid import uuid4

from acceptance_cases import case, variants


def launch(playwright, browser):
    executable = os.getenv("YANDEX_BROWSER_PATH") if browser == "yandex" else None
    if browser == "yandex" and not executable:
        raise RuntimeError("Set YANDEX_BROWSER_PATH to the installed Yandex Browser executable")
    channel = {"chrome": "chrome", "edge": "msedge"}.get(browser)
    return playwright.chromium.launch(channel=channel, executable_path=executable)


def record_coverage(t, page):
    if not getattr(t.stack, "instrumented_js", None):
        return
    coverage = page.evaluate("() => window.__coverage__ || {}")
    if coverage:
        directory = t.stack.directory / "js-raw"
        directory.mkdir(exist_ok=True)
        (directory / (uuid4().hex + ".json")).write_text(json.dumps(coverage), encoding="utf-8")


@contextmanager
def page_for(t, actor=None, width=1440):
    from playwright.sync_api import expect, sync_playwright

    with sync_playwright() as playwright:
        browser = launch(playwright, t.browser)
        (t.stack.directory / "browser-version.txt").write_text(f"{t.browser} {browser.version}\n", encoding="utf-8")
        context = browser.new_context(base_url=t.base, viewport={"width": width, "height": 950})
        if actor:
            context.add_cookies([{"name": "nd_session", "value": t.tokens[actor], "url": t.base}])
        page = context.new_page()
        page.set_default_timeout(5000)
        if getattr(t.stack, "instrumented_js", None):
            def instrument(route):
                response = route.fetch(timeout=5000)
                if response.body() != t.stack.original_js:
                    raise AssertionError("app.js served by gateway differs from coverage source")
                headers = {key: value for key, value in response.headers.items()
                           if key not in {"content-length", "content-encoding", "etag", "last-modified"}}
                route.fulfill(status=response.status, headers=headers, body=t.stack.instrumented_js)
            page.route("**/app.js", instrument)
        try:
            page.goto("/", timeout=5000)
            expect(page.locator("#title")).to_have_text("Каталог")
            yield page
        finally:
            record_coverage(t, page)
            context.close()
            browser.close()


def card(page, title):
    return page.locator("#content article").filter(has=page.get_by_role("heading", name=title, exact=True))


def select_view(page, view):
    page.locator(f'nav [data-view="{view}"]').click()
    page.locator("#content." + view).wait_for()


def filter_city(page, city):
    from playwright.sync_api import expect
    page.locator("#city").fill(city)
    with page.expect_response(lambda response: "/api/v1/shop/catalog?" in response.url) as response:
        page.locator("#filter").click()
    assert response.value.ok, "catalog request failed"
    expect(page.locator("#content")).not_to_contain_text("Загружаем…")


@case("S-02", "sanity")
def public_catalog(t):
    from playwright.sync_api import expect
    visible = t.offer(price_cents=1000)
    draft = t.offer(suffix="draft", status="DRAFT")
    with page_for(t) as page:
        filter_city(page, t.prefix)
        expect(card(page, visible)).to_contain_text(re.compile(r"10,00\s*₽"))
        expect(card(page, draft)).to_have_count(0)
        expect(page.locator("#result")).not_to_have_class("error")
    t.assertEqual([(row["id"], row["price_cents"]) for row in t.catalog(city=t.prefix)], [(visible, 1000)])


@case("E-01", "browser")
def browser_publication(t):
    from playwright.sync_api import expect
    values = t.application()
    with page_for(t, "v1") as page:
        select_view(page, "applications")
        for name, value in values.items():
            page.locator(f'#application [name="{name}"]').fill(value)
        page.get_by_role("button", name="Отправить на проверку").click()
        expect(card(page, t.prefix)).to_contain_text("На проверке")
    with page_for(t, "admin") as page:
        select_view(page, "applications")
        card(page, t.prefix).get_by_role("button", name="Одобрить").click()
        expect(card(page, t.prefix)).to_contain_text("Одобрено")
    with page_for(t) as page:
        filter_city(page, t.prefix)
        expect(card(page, t.prefix)).to_have_count(0)
    with page_for(t, "v1") as page:
        select_view(page, "offers")
        offer = card(page, t.prefix)
        expect(offer).to_contain_text("Черновик")
        for field, value in {"description": "Проверка через браузер", "capacity": "2", "price_cents": "10"}.items():
            offer.locator(f'[name="{field}"]').fill(value)
        offer.get_by_role("button", name="Сохранить").click()
        expect(offer).to_contain_text(re.compile(r"10,00\s*₽"))
        offer.get_by_role("button", name="Опубликовать").click()
        expect(offer).to_contain_text("В продаже")
    with page_for(t) as page:
        filter_city(page, t.prefix)
        expect(card(page, t.prefix)).to_contain_text(re.compile(r"10,00\s*₽"))


def login_browser(page, email, password):
    from playwright.sync_api import expect
    page.locator("#account").get_by_role("button", name="Войти").click()
    page.locator('#auth-form [name="email"]').fill(email)
    page.locator('#auth-form [name="password"]').fill(password)
    page.locator("#auth-submit").click()
    expect(page.locator("#account")).to_contain_text("Покупатель")


def buy(t, assert_stages=False):
    from playwright.sync_api import expect
    a = t.offer()
    b = t.offer("v2", suffix="b", price_cents=90000)
    email = t.prefix + "@example.test"
    password = "browser-fixture-password"
    t.http("/api/v1/auth/register", "POST", {"email": email, "name": "Покупатель", "password": password, "role": "customer"}, expected=201)
    stages = []
    with page_for(t) as page:
        filter_city(page, t.prefix)
        expect(card(page, a)).to_be_visible()
        stages.append("choice")
        login_browser(page, email, password)
        stages.append("sign-in")
        for offer, quantity in ((a, "2"), (b, "1")):
            current = card(page, offer)
            current.get_by_role("spinbutton").fill(quantity)
            with page.expect_response(lambda res: res.url.endswith("/shop/cart/items") and res.request.method == "POST"):
                current.get_by_role("button", name="В корзину").click()
        select_view(page, "cart")
        expect(page.locator("#content.cart")).to_contain_text(re.compile(r"3\s900,00\s*₽"))
        with (
            page.expect_response(lambda res: res.url.endswith("/shop/orders") and res.request.method == "POST") as reservation,
            page.expect_response(lambda res: res.url.endswith("/pay") and res.request.method == "POST") as payment,
        ):
            page.get_by_role("button", name="Оплатить тестово").click()
        order_id = reservation.value.json()["order"]["id"]
        stages.append("pay")
        expect(page.locator("#content.orders")).to_contain_text("Оплачен")
        codes = page.locator(".order-details .ticket code")
        expect(codes).to_have_count(3)
        stages.append("ticket")
        actual = payment.value.json()
        order = actual["order"]
        t.assertEqual(order["id"], order_id)
        t.assertEqual(set(codes.all_text_contents()), {ticket["code"] for ticket in order["tickets"]})
    from acceptance_cases import assert_paid
    assert_paid(t, order, a, b)
    if assert_stages:
        t.stack.observations[t.name] = {"observed": stages, "maximum": 4}
        t.assertLessEqual(len(stages), 4, "observed required stages: " + ", ".join(stages))
    else:
        t.mail(order_id, "order.paid", actual["trace_id"], seconds=15)


@case("E-02", "browser")
def browser_purchase(t):
    buy(t)


@case("E-02-NFR-8", "browser")
def purchase_stages(t):
    # Use the same visible flow in a fresh account to avoid history masking UI stages.
    buy(t, assert_stages=True)


@case("E-03", "browser")
def browser_refund(t):
    from acceptance_cases import assert_cancelled
    from playwright.sync_api import expect
    a, b, order = t.control_order("PAID")
    with page_for(t, "c1") as page:
        select_view(page, "orders")
        page.get_by_role("button", name="Отменить / вернуть").click()
        expect(page.locator("#content.orders")).to_contain_text("Отменён")
        record_coverage(t, page)
        page.reload(timeout=5000)
        select_view(page, "orders")
        expect(page.locator("#content.orders")).to_contain_text("Отменён")
        page.get_by_role("button", name="Подробнее").click()
        expect(page.get_by_text("Билет · Аннулирован", exact=True)).to_have_count(3)
        expect(page.get_by_role("button", name="Оплатить тестово")).to_have_count(0)
    after = t.shop("orders/" + order["id"], actor="c1")["order"]
    assert_cancelled(t, after, (a, b), True)
    t.order_change(after, "pay", expected=409, code="INVALID_STATE")


@case("E-04", "browser")
def browser_redeem(t):
    from playwright.sync_api import expect
    a, _, order = t.control_order("PAID")
    ticket = next(row for row in order["tickets"] if row["offer_id"] == a)
    with page_for(t, "v1") as page:
        select_view(page, "sales")
        totals = page.locator("#content .price")
        expect(totals).to_be_visible()
        previous = totals.inner_text()
        for expected in ("success", "error"):
            page.locator('#redeem input[name="code"]').fill(ticket["code"])
            page.get_by_role("button", name="Погасить билет").click()
            expect(page.locator("#result")).to_have_class(expected)
        expect(totals).to_have_text(previous)
    t.assertEqual(len(t.events(order["id"], "ticket.redeemed")), 1)
    current = t.shop("orders/" + order["id"], actor="c1")["order"]
    t.assertEqual(next(row["status"] for row in current["tickets"] if row["code"] == ticket["code"]), "REDEEMED")


@case("E-05", "browser")
def browser_retry_payment(t):
    from acceptance_cases import assert_paid
    from playwright.sync_api import expect
    a, b, order = t.control_order()
    with page_for(t, "c1") as page:
        select_view(page, "orders")
        scenario = page.get_by_label("Сценарий оплаты")
        pay = page.get_by_role("button", name="Оплатить тестово")
        scenario.select_option("error")
        pay.click()
        expect(page.locator("#result")).to_have_class("error")
        expect(pay).to_be_enabled()
        t.assertEqual(t.effects(order["id"]), [])
        scenario.select_option("success")
        pay.click()
        expect(page.locator("#content.orders")).to_contain_text("Оплачен")
        page.get_by_role("button", name="Подробнее").click()
        expect(page.get_by_text("Билет · Действителен", exact=True)).to_have_count(3)
    after = t.shop("orders/" + order["id"], actor="c1")["order"]
    assert_paid(t, after, a, b)


@case("E-05-cart", "browser")
def browser_retry_cart_payment(t):
    from acceptance_cases import assert_paid
    from playwright.sync_api import expect

    a, b = t.control_cart()
    with page_for(t, "c1") as page:
        select_view(page, "cart")
        page.get_by_label("Сценарий оплаты").select_option("error")
        page.get_by_role("button", name="Оплатить тестово").click()
        expect(page.locator("#result")).to_have_class("error")
        expect(page.locator("#content.orders")).to_contain_text("Ожидает оплаты")
        reserved, = t.shop("orders", actor="c1")["orders"]
        t.assertEqual(reserved["status"], "RESERVED")
        t.assertEqual(t.effects(reserved["id"]), [])

        page.get_by_label("Сценарий оплаты").select_option("success")
        page.get_by_role("button", name="Оплатить тестово").click()
        expect(page.locator(".order-details .ticket code")).to_have_count(3)
    orders = t.shop("orders", actor="c1")["orders"]
    t.assertEqual([order["id"] for order in orders], [reserved["id"]])
    assert_paid(t, t.shop("orders/" + reserved["id"], actor="c1")["order"], a, b)


@variants("E-06", {str(width): width for width in (360, 390, 1440)}, "browser")
def responsive_login(t, width):
    from playwright.sync_api import expect
    city = "Город " + t.prefix[-6:]
    t.offer(title="Выставка", city=city, category="Культура")
    with page_for(t, width=width) as page:
        filter_city(page, city)
        t.assertTrue(page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"))
        page.locator("#account").get_by_role("button", name="Войти").press("Enter")
        email = page.locator('#auth-form [name="email"]')
        password = page.locator('#auth-form [name="password"]')
        submit = page.locator("#auth-submit")
        expect(email).to_be_focused()
        page.keyboard.press("Tab")
        expect(password).to_be_focused()
        page.keyboard.press("Tab")
        expect(submit).to_be_focused()
        t.assertTrue(submit.evaluate("(element) => element.matches(':focus-visible')"))
        for first, second in ((email, password), (password, submit)):
            x, y = first.bounding_box(), second.bounding_box()
            t.assertTrue(x["x"] + x["width"] <= y["x"] or y["x"] + y["width"] <= x["x"]
                         or x["y"] + x["height"] <= y["y"] or y["y"] + y["height"] <= x["y"])
        page.keyboard.press("Enter")
        expect(email).to_be_focused()
        t.assertTrue(email.evaluate("(element) => element.validity.valueMissing && element.validationMessage.length > 0"))
        t.assertTrue(page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"))


@variants("E-06", {"long-title-360": 360, "long-title-390": 390}, "browser")
def responsive_long_title(t, width):
    from playwright.sync_api import expect
    city = "Город " + t.prefix[-6:]
    title = "Сверхдлинноеназваниесобытиябезпробелов" * 2
    t.offer(title=title, city=city, category="Культура")
    with page_for(t, width=width) as page:
        filter_city(page, city)
        expect(card(page, title)).to_be_visible()
        size = page.evaluate("() => ({viewport: window.innerWidth, document: document.documentElement.scrollWidth})")
        t.stack.observations[t.name] = size
        t.assertLessEqual(size["document"], size["viewport"])


@case("N-22", "browser")
def browser_text_escaping(t):
    from playwright.sync_api import expect
    markup = '<img src="/acceptance-inert-image" data-probe="literal">'
    t.offer(title=markup)
    with page_for(t) as page:
        fetched = []
        page.on("request", lambda req: fetched.append(req.url) if req.url.endswith("/acceptance-inert-image") else None)
        filter_city(page, t.prefix)
        expect(card(page, markup)).to_be_visible()
        expect(page.locator("#content [data-probe=literal]")).to_have_count(0)
        t.assertEqual(fetched, [])
        page.locator("#account").get_by_role("button", name="Войти").click()
        expect(page.locator("#auth-form")).to_be_visible()


@case("UI-register-logout", "browser")
def browser_account(t):
    from playwright.sync_api import expect
    with page_for(t) as page:
        page.locator("#account").get_by_role("button", name="Войти").click()
        page.locator("#auth-toggle").click()
        for name, value in {"email": t.prefix + "@example.test", "password": "Valid-password", "name": "Покупатель"}.items():
            page.locator(f'#auth-form [name="{name}"]').fill(value)
        page.locator("#auth-submit").click()
        expect(page.locator("#auth")).to_be_hidden()
        page.get_by_role("button", name="Выйти").click()
        expect(page.locator("#account").get_by_role("button", name="Войти")).to_be_visible()
        expect(page.locator('nav [data-view="orders"]')).to_be_hidden()


@case("UI-admin-reject", "browser")
def browser_reject(t):
    from playwright.sync_api import expect
    t.pending()
    with page_for(t, "admin") as page:
        select_view(page, "applications")
        application = card(page, t.prefix)
        application.get_by_role("button", name="Отклонить").click()
        reason = application.get_by_label("Причина отклонения")
        expect(reason).to_be_focused()
        reason.fill("Нет документов")
        application.get_by_role("button", name="Подтвердить отказ").click()
        expect(application).to_contain_text("Отклонено")


@case("UI-remove-last-item", "browser")
def browser_empty_cart(t):
    from playwright.sync_api import expect
    t.actors("c1")
    offer = t.offer()
    t.cart_item(offer, 1)
    with page_for(t, "c1") as page:
        select_view(page, "cart")
        card(page, offer).get_by_role("button", name="Убрать").click()
        expect(page.locator("#content")).to_contain_text("В корзине пока нет билетов")
        expect(page.get_by_role("button", name="Оплатить тестово")).to_have_count(0)
