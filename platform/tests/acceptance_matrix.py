"""Map acceptance IDs to assertions, source and evidence without treating skipped work as covered."""

import argparse
from collections import Counter
import json
from pathlib import Path

import acceptance_browser
import acceptance_faults
import acceptance_load
import acceptance_negative
import acceptance_observe
from acceptance_cases import cases
from acceptance_run import source_hash


# Assertion descriptions apply to each parameterized variant. Each variant is
# counted independently, including variants with missing evidence.
RULES = {
    "S-01": ("P0", "platform/compose.ha.yaml", "Все readiness, проверка primary и чтение контрольной записи"),
    "S-02": ("P1", "gateway/web/app.js", "В UI и API одна и та же цена, DRAFT скрыт"),
    "S-03": ("P0", "identity-service/main.go", "Cookie отозвана, повторный запрос возвращает 401"),
    "S-04": ("P0", "order-service/business.go", "Сумма, комиссия, единственный билет и эффект у провайдера"),
    "S-05": ("P1", "notification-service/business.go", "Уведомление по event_id, trace_id совпадает"),
    "S-06": ("P1", "gateway/swagger.go", "Выполнение запроса через Swagger, 8 targets UP, рост счётчика и данные панели"),
    "F-01": ("P0", "identity-service/main.go", "Роль и нормализованная почта, bcrypt и подтверждение входом"),
    "F-02": ("P0", "gateway/auth.go", "Общая сессия реплик, cookie HttpOnly/Lax, хэш и срок 24 часа"),
    "F-03": ("P1", "order-service/business.go", "PENDING с полями и владельцем, заявка видна модератору"),
    "F-04": ("P1", "order-service/business.go", "APPROVED и один DRAFT того же владельца, черновик скрыт"),
    "F-05": ("P1", "order-service/business.go", "REJECTED и точная причина, черновик отсутствует"),
    "F-06": ("P1", "order-service/business.go", "PUBLISHED, точные поля и остаток в каталоге"),
    "F-07": ("P1", "order-service/business.go", "Точные множества фикстур по фильтрам, лимит и порядок по ID"),
    "F-08": ("P0", "order-service/business.go", "Одна строка, точная сумма, замена или удаление количества"),
    "F-09": ("P0", "order-service/business.go", "390000 и 39000, A×2/B×1, остаток 0/1, пустая корзина и +15 мин"),
    "F-10": ("P0", "order-service/business.go", "Повтор возвращает тот же заказ без резерва и события"),
    "F-10-changed-cart": ("P0", "order-service/business.go", "Повтор со старым ключом возвращает исходный заказ, сохраняет новую корзину и остатки"),
    "F-11": ("P0", "order-service/business.go", "RESERVED→PAID, A/A/B VALID, один платёж и одно событие"),
    "F-12": ("P0", "order-service/business.go", "Повторная оплата возвращает прежние платёж и билеты"),
    "F-13": ("P0", "order-service/business.go", "Список и детали ограничены заказами покупателя"),
    "F-14": ("P0", "order-service/business.go", "RESERVED→CANCELLED, места освобождены без списания"),
    "F-15": ("P0", "order-service/business.go", "PAID→CANCELLED, VALID→REVOKED, один возврат на 390000"),
    "F-16": ("P0", "order-service/business.go", "Повтор отмены сохраняет эффект, остатки и события"),
    "F-17": ("P0", "order-service/payment_expiry.go", "Просроченный и будущий резерв, идемпотентное истечение"),
    "F-18": ("P0", "order-service/business.go", "VALID→REDEEMED ровно для одного билета"),
    "F-19": ("P0", "order-service/business.go", "Продажи по владельцу и карточке, количество билетов, gross/commission/net; отмена исключена"),
    "F-20": ("P1", "notification-service/business.go", "Тип, order_id, event_id, одна доставка и сквозной trace"),
    "N-01a": ("P0", "identity-service/main.go", "EMAIL_EXISTS и один пользователь для каждого варианта почты"),
    "N-01b": ("P0", "identity-service/main.go", "Одинаковый 401, без создания сессии"),
    "N-02a": ("P0", "gateway/auth.go", "Создание admin и подмена роли запрещены, сущности сохранены"),
    "N-02b": ("P0", "order-service/business.go", "403 и сохранность данных по всем маршрутам матрицы"),
    "N-03": ("P0", "gateway/auth.go", "401/403 для cookie и Origin, корзина и записи сессии сохранены"),
    "N-05a": ("P1", "order-service/business.go", "Не создана заявка без обязательного поля"),
    "N-05b": ("P1", "order-service/business.go", "Неполный черновик остаётся скрытым"),
    "N-05c": ("P1", "order-service/business.go", "Состояние заявки и количество черновиков сохранены"),
    "N-06a": ("P0", "order-service/business.go", "Целые границы цены приняты, неверные цены не записаны"),
    "N-06b": ("P0", "order-service/business.go", "Количество и сумма корзины, места не зарезервированы"),
    "N-06c": ("P0", "order-service/business.go", "Ниже занятых нельзя, остаток согласован с вместимостью"),
    "N-06d": ("P0", "gateway/shop.go", "4xx, корзина, заказы и остаток сохранены"),
    "N-07a": ("P0", "order-service/business.go", "404 для несуществующей записи либо черновика"),
    "N-07b": ("P0", "order-service/business.go", "Запрет перехода, данные заказа и деньги сохранены"),
    "N-08": ("P0", "order-service/business.go", "Откат всего резерва при нехватке мест во второй позиции"),
    "N-09": ("P0", "order-service/business.go", "В конкуренции за последнее место ровно один победитель"),
    "N-10a": ("P0", "order-service/business.go", "Один ID, набор билетов и провайдерский эффект в гонке"),
    "N-10b": ("P0", "payment-mock/main.go", "409 при изменении суммы или заказа, прежний результат сохраняется"),
    "N-11a": ("P0", "order-service/payment_expiry.go", "Отказ оплаты не создаёт платёж или билет"),
    "N-11b": ("P0", "order-service/payment_expiry.go", "Первый отказ возврата не оставляет блокировку погашения"),
    "N-11c": ("P0", "order-service/payment_expiry.go", "Deadline оплаты, состояние заказа и деньги сохранены"),
    "N-12b": ("P0", "order-service/payment_expiry.go", "Откат PAID компенсирован одним возвратом"),
    "N-13b": ("P0", "order-service/business.go", "Откат CANCELLED сохраняет блокировку, возврат не дублируется"),
    "N-15a": ("P0", "order-service/business.go", "Одно погашение и одно событие при конкуренции"),
    "N-15b": ("P0", "order-service/business.go", "Возврат со статусом REVOKED либо погашение без возврата"),
    "N-16": ("P0", "order-service/business.go", "Новая цена в каталоге, прежняя цена и сумма платежа"),
    "N-17": ("P0", "order-service/business.go", "Комиссии 19 и 10, суммарно 29, net 271"),
    "N-18a": ("P0", "notification-service/business.go", "Повторное событие создаёт один внешний эффект"),
    "N-18b": ("P1", "notification-service/business.go", "Poison привязан к позиции, следующее событие доставлено"),
    "N-19": ("P0", "order-service/business.go", "Резерв в БД при недоступном Kafka, последующее опустошение outbox"),
    "N-20a": ("P0", "order-service/business.go", "Прерывание после publish и повтор с одним уведомлением"),
    "N-20b": ("P0", "notification-service/business.go", "Прерывание после письма, один внешний эффект и сдвиг offset"),
    "N-22": ("P0", "gateway/web/app.js", "HTML остаётся текстом, браузер не создаёт элемент и не запрашивает src"),
    "E-01": ("P1", "gateway/web/app.js", "Заявка и модерация в кабинетах, публичная карточка после публикации"),
    "E-02": ("P0", "gateway/web/app.js", "Покупка через UI, 390000, A/A/B и одна доставка"),
    "E-02-NFR-8": ("P1", "gateway/web/app.js", "Число обязательных этапов покупки ≤4"),
    "E-03": ("P0", "gateway/web/app.js", "Возврат сохраняется после refresh, места и платёж сверены"),
    "E-04": ("P0", "gateway/web/app.js", "Повтор погашения запрещён, выручка сохраняется"),
    "E-05": ("P1", "gateway/web/app.js", "После отказа доступен повтор, успешный платёж один"),
    "E-06": ("P1", "gateway/web/style.css", "Ширины 360/390/1440, клавиатура, фокус, обязательное поле"),
    "UI": ("P2", "gateway/web/app.js", "Дополнительные ветки регистрации, отклонения заявки и пустой корзины"),
    "H-01": ("P0", "platform/ha/gateway.cfg", "Реплика отключена, повтор checkout возвращает один резерв"),
    "H-02": ("P0", "platform/scripts/ha-postgres.ps1", "Fencing и сохранённый заказ, запись после promotion"),
    "H-03a": ("P0", "platform/scripts/ha-postgres.ps1", "Rejoin, правильные роли, маркер дошёл на реплику"),
    "H-03b": ("P0", "platform/compose.ha.yaml", "Сессия, платежи и возврат переживают полный stop/up"),
    "H-04": ("P0", "platform/ha/postgres.sh", "Запуск на новых томах и повтор без дублей миграций"),
    "O-01": ("P1", "order-service/business.go", "HTTP/gRPC, SQL, Kafka, связи и параметры точной трассы"),
    "O-02": ("P1", "order-service/internal/observe/diagnostic.go", "Span ошибки, связанный лог, рост счётчика"),
    "O-03": ("P0", "gateway/telemetry.go", "Секреты отсутствуют в scoped traces/logs/metrics"),
    "L-01": ("P1", "platform/tests/acceptance_load.py", "99 API + 1 браузер, средние ≤2 с, проверка фильтров и реплик"),
    "L-02": ("P0", "platform/tests/acceptance_load.py", "20 пользователей, точная сверка заказов, билетов, эффектов, остатков и outbox"),
}

GO_RULES = {
    "N-04-register": ("P0", "identity-service/main.go", "Окно регистрации на IP", "identity-service", "TestN04RegistrationRateLimitPerIP"),
    "N-04-login": ("P0", "identity-service/main.go", "Окно входа на нормализованный email", "identity-service", "TestIdentityDatabase/N04_normalized_login_key_and_window"),
    "N-04-success": ("P0", "identity-service/main.go", "Успешный вход сбрасывает счётчик", "identity-service", "TestIdentityDatabase/N04_successful_login_resets_the_counter"),
    "N-12a": ("P0", "order-service/payment_expiry.go", "Неопределённая оплата компенсируется при истечении", "order-service", "TestBusinessDatabase"),
    "N-13a": ("P0", "order-service/business.go", "Потеря ответа и поздний отказ сохраняют блокировку погашения", "order-service", "TestBusinessDatabase"),
    "N-14a": ("P0", "order-service/payment_expiry.go", "Оплата удерживает строку во время прохода истечения", "order-service", "TestBusinessDatabase/N14a_payment_owns_the_order_row_when_expiry_runs"),
    "N-14b": ("P0", "order-service/payment_expiry.go", "Намерение отмены блокирует оплату до завершения отмены", "order-service", "TestBusinessDatabase"),
    "N-21a-offline": ("P1", "gateway/shop.go", "Unavailable преобразуется в 502", "gateway", "TestShopErrors/offline"),
    "N-21a-deadline": ("P1", "gateway/shop.go", "Истечение deadline преобразуется в 504", "gateway", "TestN21ShopTransportDeadline"),
    "N-21b": ("P1", "gateway/shop.go", "Некорректный ответ каталога преобразуется в 502", "gateway", "TestShopErrors"),
    "O-03-gateway-limits": ("P0", "gateway/telemetry.go", "Лимиты количества, глубины и размера диагностики", "gateway", "TestO03DiagnosticLimits"),
    "O-03-order-limits": ("P0", "order-service/internal/observe/diagnostic.go", "Обрезка длинной диагностики до статуса payload_omitted", "order-service", "TestO03OversizedDiagnosticUsesOmissionStatus"),
}

for boundary in ("empty name", "name 129", "name 128", "bad email", "email over 254",
                 "password 7", "password 8", "password 72 bytes", "password 73 bytes"):
    GO_RULES["N-01c-" + boundary.replace(" ", "-")] = (
        "P0", "identity-service/main.go", "Граница валидации регистрации: " + boundary,
        "identity-service", "TestN01RegistrationBoundaries/" + boundary.replace(" ", "_"),
    )

REQUIREMENTS = {
    "FR-1": ("PARTIAL", "F-03, E-01", "Заявка MVP, полный набор юридических, контактных и банковских реквизитов отложен"),
    "FR-2": ("PARTIAL", "N-05a", "Валидация обязательных полей формы MVP до модерации"),
    "FR-3": ("PARTIAL", "F-04, F-06, E-01", "Одобрение создаёт черновик аттракциона, организатор отдельно публикует карточку"),
    "FR-4": ("PARTIAL", "F-06, N-05b, N-06a, N-06c", "Редактирование описания, цены и мест; фото и расписание отложены"),
    "FR-5": ("TESTED_BY", "F-19, E-04", "Количество проданных билетов и выручка по карточкам своего вендора"),
    "FR-6": ("TESTED_BY", "F-01, F-02, S-03, N-01a, N-01b", "Регистрация и вход по email и паролю, завершение сессии"),
    "FR-7": ("TESTED_BY", "F-07, S-02", "Каталог опубликованных карточек и фильтры по городу и категории"),
    "FR-8": ("PARTIAL", "F-06, S-02, E-02", "Описание, цена и доступность в карточке MVP; фотографии отложены"),
    "FR-9": ("PARTIAL", "F-08, F-09, F-10, E-02", "Один заказ с билетами разных вендоров; абонементы отложены"),
    "FR-10": ("PARTIAL", "F-11, F-12, N-11a, E-02, E-05", "Покупка билета через платёжный mock; реальная оплата и абонементы отложены"),
    "FR-11": ("PARTIAL", "F-11, F-20, E-02", "Уникальный код билета в кабинете и доставка события в mail-mock; письмо с билетом отложено"),
    "FR-12": ("TESTED_BY", "F-13, E-03", "История и детали собственных покупок в кабинете"),
    "FR-13": ("PARTIAL", "F-14, F-15, F-16, N-15b, E-03", "Отмена и возврат за билет через mock; политика срока до посещения требует решения"),
    "FR-14": ("DEFERRED", "", "Право оставить отзыв и оценку после покупки"),
    "FR-15": ("TESTED_BY", "F-09, F-11, F-19, N-17", "Комиссия с продажи, округление по позициям и выручка после комиссии"),
    "FR-16": ("TESTED_BY", "F-11, F-12, E-02", "Автоматическое создание уникальных билетов при оплате"),
    "FR-17": ("TESTED_BY", "F-04, F-05, N-05c, E-01", "Администратор одобряет или отклоняет заявку, причина отказа сохраняется"),
    "FR-18": ("PARTIAL", "F-03, F-04, F-05, E-01", "Управление заявками; полное управление вендорами и аттракционами отложено"),
    "FR-19": ("PARTIAL", "F-20, O-01", "События покупки доставляются в mail-mock; email и все статусы заявки отложены"),
    "FR-20": ("PARTIAL", "F-15, F-16, N-13a, N-13b, E-03", "Обработка возврата через mock, повтор и восстановление после сбоя; внешний возврат отложен"),
    "FR-21": ("DEFERRED", "", "Отображение среднего рейтинга и отзывов на карточке"),
    "FR-22": ("DEFERRED", "", "Бонусы, правила начисления и списания требуют решения"),
    "FR-23": ("DEFERRED", "", "Промокоды, правила применения требуют решения"),
    "FR-24": ("PARTIAL", "F-06, N-16", "Ручное изменение базовой цены и фиксация цены заказа; динамические скидки и наценки отложены"),
    "NFR-1": ("TESTED_BY", "L-01", "Среднее время API и готовности страницы при 100 пользователях"),
    "NFR-2": ("PARTIAL", "H-01, H-02, H-03a", "HA-проверки стенда, доступность за период не подтверждена"),
    "NFR-3": ("PARTIAL", "F-01, F-02, N-03, O-03", "Хэширование паролей, защита сессии и фильтрация секретов; HTTPS и оценка обработки персональных данных отложены"),
    "NFR-4": ("PARTIAL", "F-11, N-11a, O-03", "Платёжный mock без приёма реквизитов карты; сертифицированный провайдер отложен"),
    "NFR-5": ("PARTIAL", "H-01, L-01", "Две реплики gateway и orders; масштабирование остальных служб отдельно не подтверждено"),
    "NFR-6": ("TESTED_BY", "E-01, E-02, E-03, E-04, E-05, E-06", "Браузерный набор Chrome, Яндекс Браузера и Edge; адаптивность на 360/390/1440 px"),
    "NFR-7": ("PARTIAL", "O-01, O-02, O-03", "Трассы и логи контейнеров, централизованное хранение отложено"),
    "NFR-8": ("TESTED_BY", "E-02-NFR-8", "Не более четырёх обязательных этапов покупки"),
    "NFR-9": ("DEFERRED", "", "Ежедневное резервное копирование; репликация и failover проверяются отдельно"),
}


def evidence(paths, revision):
    observations = {}
    for path in paths:
        report = json.loads(path.read_text(encoding="utf-8"))
        if report["source_sha256"] != revision:
            raise ValueError(f"Report belongs to a different source revision: {path}")
        for result in report.get("cases", []):
            observations.setdefault(result["id"], []).append({"run_id": report["run_id"], "status": result["status"]})
        for module, result in report.get("modules", {}).items():
            for test in result.get("cases", []):
                name = module + ":" + test["test"]
                observations.setdefault(name, []).append({"run_id": report["run_id"], "status": test["status"]})
    return observations


def outcome(name, suite, priority, code, assertion, records):
    status = next((failure for failure in ("FAIL", "SKIP", "BLOCKED") if any(row["status"] == failure for row in records)),
                  "PASS" if records else "BLOCKED")
    return {"id": name, "suite": suite, "priority": priority, "code": code, "assertion": assertion,
            "status": status, "evidence": records}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path, nargs="*")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--gate", action="store_true", help="Exit nonzero when any mandatory outcome is unresolved.")
    args = parser.parse_args()
    revision = source_hash()
    records = evidence(args.report, revision)
    registry = []
    for suite, name, _ in cases:
        key = max((prefix for prefix in RULES if name == prefix or name.startswith(prefix + "-")), key=len)
        registry.append(outcome(name, suite, *RULES[key], records.get(name, [])))
    for name, (priority, code, assertion, module, test) in GO_RULES.items():
        registry.append(outcome(name, "go", priority, code, assertion, records.get(module + ":" + test, [])))
    summary = Counter(row["status"] for row in registry)
    critical = Counter(row["status"] for row in registry if row["priority"] == "P0")
    requirement_ids = [f"FR-{index}" for index in range(1, 25)] + [f"NFR-{index}" for index in range(1, 10)]
    requirements = {}
    for name in requirement_ids:
        status, linked, detail = REQUIREMENTS[name]
        requirements[name] = {"status": status, "cases": linked, "detail": detail}
    report = {"source_sha256": revision, "outcomes": registry, "counts": dict(summary), "critical": dict(critical),
              "coverage_percent": round(100 * summary["PASS"] / len(registry), 2), "requirements": requirements,
              "uncovered_p2": [row["id"] for row in registry if row["priority"] == "P2" and row["status"] != "PASS"]}
    report["logic_status"] = "PASS" if summary["PASS"] * 100 >= 85 * len(registry) and all(
        row["status"] == "PASS" or row["priority"] == "P2" for row in registry) else "FAIL"
    document = json.dumps(report, ensure_ascii=False, indent=2)
    if args.output:
        args.output.write_text(document, encoding="utf-8")
    else:
        print(document)
    return int(args.gate and report["logic_status"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
