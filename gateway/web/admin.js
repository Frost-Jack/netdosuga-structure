"use strict";

async function renderAdmin() {
  const data = await api("admin/vendors");
  $("content").replaceChildren();
  for (const vendor of data.vendors || []) {
    const r = row(
      vendor.organization || vendor.id,
        "Заявок: " + vendor.applications + " · офферов: " + vendor.offers,
    );
    r.actions.append(
      button("Открыть события", async () => {
        view = "offers";
        await load();
      }),
    );
  }
  if (!$("content").children.length)
    $("content").append(node("p", "Зарегистрированных вендоров нет.", "muted"));
}

async function uploadOfferPhoto(id, input) {
  const file = input.files && input.files[0];
  if (!file) throw Object.assign(new Error("Выберите файл JPEG или PNG."), { userFacing: true });
  const form = new FormData();
  form.append("photo", file);
  const response = await fetch(
    "/api/v1/shop/offers/" + encodeURIComponent(id) + "/photo",
    { method: "POST", body: form },
  );
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(userError(data, response.status, "offers/photo"));
    error.userFacing = true;
    throw error;
  }
  $("result").textContent = "Фото загружено: " + data.photo_path;
  $("result").className = "success";
  $("json").textContent = JSON.stringify(data, null, 2);
  await load();
}
