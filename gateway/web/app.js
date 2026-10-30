"use strict";
const $ = (id) => document.getElementById(id);
let view = "catalog",
  busy = false,
  user = null,
  registering = false,
  mutationTrace = false;
const checkoutKeys = {};
const money = (n) =>
  new Intl.NumberFormat("ru-RU", { style: "currency", currency: "RUB" }).format(
    Number(n || 0) / 100,
  );
function node(tag, text, cls) {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  if (cls) el.className = cls;
  return el;
}
function button(text, fn) {
  const b = node("button", text);
  b.type = "button";
  b.onclick = () => run(fn);
  return b;
}
async function run(fn) {
  if (busy) return;
  busy = true;
  document.querySelectorAll("button").forEach((b) => (b.disabled = true));
  try {
    await fn();
  } catch (e) {
    $("result").textContent = e.userFacing
      ? e.message
      : "Не удалось связаться с сервисом. Проверьте соединение и повторите действие.";
    $("result").className = "error";
  } finally {
    busy = false;
    document.querySelectorAll("button").forEach((b) => (b.disabled = false));
  }
}
// Backend diagnostics stay in the collapsed panel; customer copy is fixed here.
function userError(data, status, path) {
  const messages = {
    UNAUTHENTICATED: "Войдите в аккаунт, чтобы продолжить.",
    FORBIDDEN:
      "В этом аккаунте действие недоступно. Проверьте, под какой ролью вы вошли.",
    IDENTITY_UNAVAILABLE: "Сервис входа временно недоступен. Попробуйте позже.",
    UNAVAILABLE: "Сервис временно недоступен. Повторите действие через минуту.",
    DEPENDENCY_ERROR:
      "Сервис временно недоступен. Повторите действие через минуту.",
    DEADLINE_EXCEEDED:
      "Сервис не успел ответить. Обновите заказ перед повторной попыткой.",
    NOT_FOUND: "Запись больше не доступна. Обновите раздел.",
    UNKNOWN_OPERATION: "Действие недоступно. Обновите страницу.",
    INVALID_STATE:
      "Состояние заказа изменилось. Обновите раздел перед следующим действием.",
    UNAVAILABLE_CAPACITY:
      "Билетов в таком количестве уже нет. Уменьшите количество или выберите другое событие.",
    INSUFFICIENT_STOCK:
      "Билетов в таком количестве уже нет. Уменьшите количество.",
    RESERVATION_EXPIRED:
      "Время резерва истекло. Добавьте билеты в корзину и оформите новый заказ.",
    CANCELLATION_PENDING:
      "Результат отмены не подтверждён. Повторите «Отменить / вернуть», чтобы завершить проверку; билет временно недоступен.",
    REFUND_PENDING:
      "Результат возврата не подтверждён. Повторите «Отменить / вернуть», чтобы завершить проверку; билет временно недоступен.",
    PAYMENT_PENDING:
      "Результат оплаты ещё уточняется. Обновите заказ через минуту.",
    RECONCILIATION_TIMEOUT:
      "Не удалось уточнить результат отмены или возврата. Повторите «Отменить / вернуть», чтобы завершить проверку; билет временно недоступен.",
    RECONCILIATION_FAILED:
      "Результат отмены или возврата не подтверждён. Повторите «Отменить / вернуть», чтобы завершить проверку; билет временно недоступен.",
    MOCK_TIMEOUT:
      "Тестовая оплата не ответила вовремя. Обновите заказ перед повторной попыткой; деньги не списываются.",
    MOCK_FAILED:
      "Тестовая платёжная служба недоступна. Попробуйте позже; деньги не списываются.",
    MOCK_DECLINED:
      "Тестовый платёж отклонён. Выберите сценарий «Успех» и повторите оплату; деньги не списываются.",
    MOCK_PROVIDER_ERROR:
      "Тестовый платёж отклонён. Выберите сценарий «Успех» и повторите оплату.",
    PAYMENT_FAILED:
      "Тестовая оплата не прошла. Повторите попытку; деньги не списываются.",
    TICKET_ALREADY_REDEEMED:
      "Билет уже использован. Повторное погашение и возврат недоступны.",
    INVALID_TICKET_STATE:
      "Этот билет нельзя погасить. Проверьте статус заказа и билета.",
    INVALID_JSON:
      "Не удалось отправить форму. Обновите страницу и заполните её снова.",
    INVALID_ARGUMENT: "Проверьте заполненные поля и повторите действие.",
    INVALID_ID: "Запись недоступна. Обновите раздел и выберите её снова.",
    INVALID_SCENARIO: "Выберите доступный сценарий тестовой оплаты.",
    REQUIRED_FIELDS: "Заполните все обязательные поля.",
    REASON_REQUIRED: "Укажите причину отклонения заявки.",
    INCOMPLETE_OFFER:
      "Заполните название, описание, город, категорию, цену и количество билетов перед публикацией.",
    INVALID_AMOUNT_OR_CAPACITY:
      "Укажите положительную цену и целое количество билетов.",
    INVALID_QUANTITY: "Укажите целое количество билетов от 1 до 20.",
    IDEMPOTENCY_KEY_REQUIRED:
      "Не удалось оформить заказ. Обновите корзину и повторите попытку.",
    EMPTY_CART: "Корзина пуста. Сначала выберите событие в афише.",
    EMAIL_EXISTS:
      "Аккаунт с этой почтой уже существует. Войдите или используйте другой адрес.",
    RATE_LIMITED:
      "Слишком много попыток входа. Подождите минуту и попробуйте снова.",
    INVALID_CREDENTIALS:
      path === "auth/register"
        ? "Проверьте почту, имя и пароль. Пароль должен содержать не менее 8 символов."
        : "Не удалось войти. Проверьте электронную почту и пароль.",
  };
  if (Object.hasOwn(messages, data.code)) return messages[data.code];
  if (status === 401) return messages.UNAUTHENTICATED;
  if (status === 403) return messages.FORBIDDEN;
  if (status === 429) return messages.RATE_LIMITED;
  return "Не удалось выполнить действие. Обновите раздел и попробуйте ещё раз.";
}
async function api(path, method = "GET", body, key) {
  const headers = {};
  if (body) headers["Content-Type"] = "application/json";
  if (key) headers["Idempotency-Key"] = key;
  const response = await fetch(
    (path.startsWith("auth/") ? "/api/v1/" : "/api/v1/shop/") + path,
    { method, headers, body: body ? JSON.stringify(body) : undefined },
  );
  const data = response.status === 204 ? {} : await response.json();
  if (!path.startsWith("auth/") && (method !== "GET" || !mutationTrace)) {
    if (method !== "GET") mutationTrace = true;
    $("json").textContent = JSON.stringify(
      { method, path, response: data },
      null,
      2,
    );
    $("result").textContent = response.ok
      ? method === "GET"
        ? ""
        : "Готово."
      : userError(data, response.status, path);
    $("result").className = response.ok ? "success" : "error";
    $("instances").textContent =
      "Gateway: " +
      (response.headers.get("X-Gateway-Instance") || "—") +
      " · Order: " +
      (response.headers.get("X-Order-Instance") || "—");
    $("trace").hidden = !/^[a-f0-9]{32}$/.test(data.trace_id || "");
    $("trace").href =
      "http://localhost:16686/trace/" + encodeURIComponent(data.trace_id || "");
  }
  if (!response.ok) {
    const error = new Error(userError(data, response.status, path));
    error.userFacing = true;
    throw error;
  }
  if (method !== "GET") {
    $("result").textContent = "Готово.";
    $("result").className = "success";
  }
  return data;
}
const idpath = (id) => encodeURIComponent(id);
function row(title, detail) {
  const row = node("article", undefined, "row"),
    info = node("div"),
    actions = node("div", undefined, "actions");
  info.append(node("h3", title), node("div", detail, "muted"));
  row.append(info, actions);
  $("content").append(row);
  return { row, info, actions };
}
async function mutate(path, method = "POST", body) {
  await api(path, method, body);
  await load();
}
function orderDetails(parent, order) {
  parent.replaceChildren();
  for (const item of order.items || [])
    parent.append(
      node(
        "p",
        (item.title || item.offer_id) +
          " × " +
          item.quantity +
          " · " +
          money(item.total_cents),
      ),
    );
  for (const ticket of order.tickets || []) {
    const box = node("div", undefined, "ticket");
    box.append(
      node("div", "Билет · " + statusText(ticket.status)),
      node("code", ticket.code),
    );
    box.append(
      button("Скопировать код", async () => {
        await navigator.clipboard.writeText(ticket.code);
        $("result").textContent = "Код билета скопирован";
      }),
    );
    parent.append(box);
  }
  if (!(order.tickets || []).length)
    parent.append(node("p", "Билеты появятся после успешной оплаты.", "muted"));
}
function permittedViews() {
  return !user
    ? ["catalog"]
    : user.role === "customer"
      ? ["catalog", "cart", "orders"]
      : user.role === "vendor"
        ? ["catalog", "applications", "offers", "sales"]
        : ["catalog", "applications", "offers", "orders", "sales"];
}
async function load() {
  const permitted = permittedViews();
  if (!permitted.includes(view)) view = permitted[0];
  document
    .querySelectorAll("[data-view]")
    .forEach((b) => (b.hidden = !permitted.includes(b.dataset.view)));
  $("title").textContent = {
    catalog: "Каталог",
    cart: "Корзина",
    orders: "Заказы и билеты",
    applications: "Заявки вендоров",
    offers: "Предложения",
    sales: "Продажи",
  }[view];
  document
    .querySelectorAll("[data-view]")
    .forEach((b) => b.classList.toggle("active", b.dataset.view === view));
  $("filters").hidden = view !== "catalog";
  $("application").hidden = view !== "applications" || user?.role !== "vendor";
  $("redemption").hidden = view !== "sales" || user?.role !== "vendor";
  const query =
    view === "catalog"
      ? "?" +
        new URLSearchParams({
          city: $("city").value,
          category: $("category").value,
        })
      : "";
  $("content").className = view;
  $("content").replaceChildren(node("p", "Загружаем…", "muted"));
  const data = await api(view + query);
  $("content").replaceChildren();
  if (view === "catalog") {
    for (const offer of data.offers || []) {
      const r = row(
        offer.title,
        offer.city + " · " + offer.category + " · доступно " + offer.available,
      );
      r.info.append(
        node("p", offer.description),
        node("div", money(offer.price_cents), "price"),
      );
      const qty = node("input");
      qty.type = "number";
      qty.min = 1;
      qty.max = Math.min(20, Number(offer.available || 0));
      qty.value = 1;
      qty.setAttribute("aria-label", "Количество для " + offer.title);
      if (user && user.role !== "customer") {
        r.actions.append(
          node("p", "Для покупки войдите как покупатель.", "muted"),
        );
        continue;
      }
      if (Number(offer.available) <= 0) {
        r.actions.append(node("p", "Все билеты разобрали", "muted"));
        continue;
      }
      r.actions.append(
        qty,
        button(user ? "В корзину" : "Войти и купить", () =>
          user
            ? mutate("cart/items", "POST", {
                offer_id: offer.id,
                quantity: Number(qty.value),
              })
            : showAuth(),
        ),
      );
    }
  }
  if (view === "cart") {
    const cart = data.cart || {};
    for (const item of cart.items || []) {
      const r = row(
        item.title || item.offer_id,
        "Количество: " + item.quantity + " · " + money(item.total_cents),
      );
      r.actions.append(
        button("Убрать", () =>
          mutate("cart/items/" + idpath(item.offer_id), "DELETE"),
        ),
      );
    }
    $("content").append(
      node("p", "Итого: " + money(cart.total_cents), "price"),
    );
    if ((cart.items || []).length)
      $("content").append(
        button("Забронировать на 15 минут", async () => {
          const actor = user.id;
          checkoutKeys[actor] ??= crypto.randomUUID();
          await api("orders", "POST", {}, checkoutKeys[actor]);
          delete checkoutKeys[actor];
          view = "orders";
          await load();
        }),
      );
  }
  if (view === "orders") {
    for (const order of data.orders || []) {
      const r = row(
        "Заказ от " + new Date(order.created_at).toLocaleDateString("ru-RU"),
        statusText(order.status) +
          " · " +
          money(order.total_cents) +
          (order.expires_at && order.status === "RESERVED"
            ? " · резерв до " +
              new Date(order.expires_at).toLocaleTimeString("ru-RU", {
                hour: "2-digit",
                minute: "2-digit",
              })
            : ""),
      );
      for (const item of order.items || [])
        r.info.append(
          node(
            "div",
            (item.title || item.offer_id) +
              " × " +
              item.quantity +
              " · " +
              money(item.total_cents),
          ),
        );
      for (const ticket of order.tickets || []) {
        r.info.append(
          node("div", "Билет · " + statusText(ticket.status), "ticket"),
        );
      }
      r.actions.append(
        button("Подробнее", async () => {
          const d = await api("orders/" + idpath(order.id));
          let box = r.info.querySelector(".order-details");
          if (!box) {
            box = node("div", undefined, "order-details");
            r.info.append(box);
          }
          orderDetails(box, d.order);
        }),
      );
      if (user?.role === "customer" && order.status === "RESERVED") {
        r.info.append(
          node(
            "p",
            "Тестовая оплата: деньги не списываются, данные карты не нужны.",
            "muted",
          ),
        );
        const scenario = node("select");
        scenario.setAttribute("aria-label", "Сценарий оплаты");
        for (const [value, label] of [
          ["success", "Успех"],
          ["error", "Ошибка"],
          ["delay", "Задержка"],
          ["timeout", "Таймаут"],
        ]) {
          const opt = node("option", label);
          opt.value = value;
          scenario.append(opt);
        }
        r.actions.append(
          scenario,
          button("Оплатить тестово", () =>
            mutate("orders/" + idpath(order.id) + "/pay", "POST", {
              scenario: scenario.value,
            }),
          ),
        );
      }
      if (user?.role === "customer" && !["CANCELLED", "REFUNDED", "EXPIRED"].includes(order.status))
        r.actions.append(
          button("Отменить / вернуть", () =>
            mutate("orders/" + idpath(order.id) + "/cancel"),
          ),
        );
    }
  }
  if (view === "applications") {
    for (const app of data.applications || []) {
      const r = row(
        app.title,
        app.organization +
          " · " +
          app.city +
          " · " +
          app.category +
          " · " +
          statusText(app.status),
      );
      if (user?.role === "admin" && app.status === "PENDING") {
        r.actions.append(
          button("Одобрить", () =>
            mutate("applications/" + idpath(app.id) + "/approve"),
          ),
          button("Отклонить", async () => {
            const form = node("form"),
              input = node("input"),
              send = node("button", "Подтвердить отказ");
            input.required = true;
            input.placeholder = "Причина отклонения";
            input.setAttribute("aria-label", "Причина отклонения");
            form.append(input, send);
            form.onsubmit = (e) => {
              e.preventDefault();
              run(() =>
                mutate("applications/" + idpath(app.id) + "/reject", "POST", {
                  reason: input.value,
                }),
              );
            };
            r.info.append(form);
            input.focus();
          }),
        );
      }
    }
  }
  if (view === "offers") {
    for (const offer of data.offers || []) {
      const r = row(
        offer.title,
        statusText(offer.status) +
          " · " +
          money(offer.price_cents) +
          " · доступно " +
          offer.available +
          " / " +
          offer.capacity,
      );
      if (user?.role !== "vendor") continue;
      const form = node("form", undefined, "edit");
      for (const [name, label, type] of [
        ["title", "Название", "text"],
        ["description", "Описание", "text"],
        ["city", "Город", "text"],
        ["category", "Категория", "text"],
        ["price_cents", "Цена, ₽", "number"],
        ["capacity", "Ёмкость", "number"],
      ]) {
        const l = node("label", label),
          input = node("input");
        input.name = name;
        input.type = type;
        input.value =
          name === "price_cents"
            ? Number(offer[name] || 0) / 100
            : (offer[name] ?? "");
        if (name === "price_cents") input.step = "0.01";
        input.required = true;
        if (type === "number") input.min = 1;
        l.append(input);
        form.append(l);
      }
      const save = node("button", "Сохранить");
      form.append(save);
      form.onsubmit = (e) => {
        e.preventDefault();
        run(async () => {
          const values = Object.fromEntries(new FormData(form));
          values.price_cents = Math.round(Number(values.price_cents) * 100);
          values.capacity = Number(values.capacity);
          await mutate("offers/" + idpath(offer.id), "PATCH", values);
        });
      };
      r.info.append(form);
      r.actions.append(
        button("Опубликовать", () =>
          mutate("offers/" + idpath(offer.id) + "/publish"),
        ),
      );
    }
  }
  if (view === "sales") {
    for (const sale of data.sales || []) {
      row(
        sale.title || sale.order_id || sale.offer_id,
        "Выручка: " +
          money(sale.gross_cents ?? sale.total_cents) +
          " · комиссия: " +
          money(sale.commission_cents),
      );
    }
    $("content").append(
      node(
        "p",
        "Выручка " +
          money(data.gross_cents) +
          " · комиссия " +
          money(data.commission_cents) +
          " · вендору " +
          money(data.net_cents),
        "price",
      ),
    );
  }
  if (view === "cart" && !(data.cart?.items || []).length)
    $("content").prepend(
      node(
        "p",
        "В корзине пока нет билетов. Выберите событие в афише.",
        "empty",
      ),
    );
  if (!$("content").children.length)
    $("content").append(node("p", "Пока нет записей.", "muted"));
}
document.querySelectorAll("[data-view]").forEach(
  (b) =>
    (b.onclick = () =>
      run(async () => {
        if (view !== b.dataset.view) {
          $("result").textContent = "";
          $("result").className = "";
        }
        view = b.dataset.view;
        await load();
      })),
);
$("refresh").onclick = () => run(load);
$("filter").onclick = () => run(load);
$("application").onsubmit = (e) => {
  e.preventDefault();
  run(() =>
    mutate("applications", "POST", Object.fromEntries(new FormData(e.target))),
  );
};
$("redeem").onsubmit = (e) => {
  e.preventDefault();
  run(() =>
    mutate("tickets/" + idpath(new FormData(e.target).get("code")) + "/redeem"),
  );
};
run(async () => {
  try {
    const r = await fetch("/api/v1/auth/session");
    if (r.ok) user = (await r.json()).user;
  } catch {}
  renderAccount();
  await load();
});

function statusText(status) {
  return (
    {
      RESERVED: "Ожидает оплаты",
      PAID: "Оплачен",
      CANCELLED: "Отменён",
      REFUNDED: "Возвращён",
      EXPIRED: "Резерв истёк",
      PENDING: "На проверке",
      APPROVED: "Одобрено",
      REJECTED: "Отклонено",
      DRAFT: "Черновик",
      PUBLISHED: "В продаже",
      ACTIVE: "Действует",
      ISSUED: "Действует",
      VALID: "Действителен",
      REDEEMED: "Использован",
      REVOKED: "Аннулирован",
    }[status] || status
  );
}
function showAuth() {
  $("auth").hidden = false;
  $("auth-form").elements.email.focus();
}
function renderAccount() {
  $("account").replaceChildren();
  if (user) {
    $("account").append(
      node(
        "span",
        user.name +
          " · " +
          { customer: "Покупатель", vendor: "Организатор", admin: "Модератор" }[
            user.role
          ],
      ),
      button("Выйти", async () => {
        await api("auth/logout", "POST", {});
        user = null;
        view = "catalog";
        renderAccount();
        await load();
      }),
    );
  } else $("account").append(button("Войти", showAuth));
}
$("auth-close").onclick = () => {
  $("auth").hidden = true;
};
$("auth-toggle").onclick = () => {
  registering = !registering;
  $("name-field").hidden = !registering;
  $("role-field").hidden = !registering;
  $("auth-title").textContent = registering ? "Новый аккаунт" : "Вход в кассу";
  $("auth-submit").textContent = registering ? "Зарегистрироваться" : "Войти";
  $("auth-toggle").textContent = registering
    ? "У меня есть аккаунт"
    : "Создать аккаунт";
  $("auth-form").elements.name.required = registering;
};
$("auth-form").onsubmit = (e) => {
  e.preventDefault();
  run(async () => {
    const values = Object.fromEntries(new FormData(e.target));
    if (!registering) {
      delete values.name;
      delete values.role;
    }
    const data = await api(
      "auth/" + (registering ? "register" : "login"),
      "POST",
      values,
    );
    user = data.user;
    e.target.reset();
    $("auth").hidden = true;
    renderAccount();
    view =
      user.role === "vendor"
        ? "offers"
        : user.role === "admin"
          ? "applications"
          : "catalog";
    await load();
  });
};
