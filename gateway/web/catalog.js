"use strict";

function renderCatalog(data) {
  for (const offer of data.offers || []) {
    const r = row(
      offer.title,
      offer.city + " · " + offer.category + " · доступно " + offer.available,
    );
    r.info.append(
      node("p", offer.description),
      node("p", "Расписание: " + (offer.schedule || "—")),
      node("div", money(offer.price_cents), "price"),
    );
    r.actions.append(
      button("Подробнее", async () => {
        const d = await api("offers/" + idpath(offer.id));
        const card = d.offer || {};
        let box = r.info.querySelector(".offer-details");
        if (!box) {
          box = node("div", undefined, "offer-details");
          r.info.append(box);
        }
        const details = [
          node("p", "Актуальная цена: " + money(card.current_price_cents)),
          node(
            "p",
            "Рейтинг: " +
              (card.rating?.average ?? 0).toFixed(1) +
              " · отзывов: " +
              (card.rating?.reviews_count ?? 0),
          ),
          node("p", "Расписание: " + (card.schedule || "—")),
        ];
        if (card.photo_path) {
          const img = node("img");
          img.src = card.photo_path;
          img.alt = card.title || "Фото предложения";
          img.loading = "lazy";
          details.unshift(img);
        }
        box.replaceChildren(...details);
      }),
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
