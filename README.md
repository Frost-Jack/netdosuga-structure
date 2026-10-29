# НетДосуга: бизнес-demo и HA-каркас

Go-сервисы демонстрируют полный базовый путь: заявка вендора → одобрение → черновик → публикация → корзина нескольких вендоров → заказ с резервом → mock-оплата → билеты → погашение или отмена с mock-возвратом. Данные сохраняются в PostgreSQL; события проходят через transactional outbox и Kafka к mock-уведомлениям.

HA-вариант содержит два gateway, два order-service, балансировщики HAProxy и синхронную реплику PostgreSQL с управляемым переключением primary. Это локальная демонстрация: роли переключаются без login, платёжный провайдер и почта имитируются. Полное покрытие и пробелы показаны в [матрице архитектуры](docs/architecture-status.md), результаты фактических прогонов — в [отчёте](docs/verification.md).

Проверено 29.10.2026: 35 smoke-проверок, 7 бизнес-групп, 11 отказных проверок, чистый bootstrap и полный stop/up с повтором тестов. Все 15 постоянных контейнеров оставлены healthy.

## Запуск в PowerShell

Нужен Docker Desktop с Linux containers. Go, Python и protoc на хосте не требуются. Выполняйте команды из platform:

```powershell
Set-Location 'C:\Users\trokh\Pictures\netdosuga-structure\platform'
# Только если .env ещё нет:
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\setup.ps1
# Базовый и HA-проекты занимают одинаковые порты:
docker compose -f compose.yaml stop
docker compose -f compose.ha.yaml config --quiet
docker compose -f compose.ha.yaml up -d --no-build --wait pg-a pg-b
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\ha-postgres.ps1 sync
docker compose -f compose.ha.yaml up -d --build --wait
```

Существующий .env сохраняйте: изменение пароля в нём само по себе не меняет пароль существующей БД или Grafana. Файл исключён из Git. HA-проект использует собственные тома; старый базовый проект и его данные сохраняются отдельно.

## Как показать бизнес-сценарий

Откройте [демонстратор](http://localhost:8080). Выбор роли расположен сверху. Это demo-переключатель, а не аутентификация.

1. **Вендор 1 → Заявки:** заполните организацию, название, город, категорию и отправьте заявку.
2. **Администратор → Заявки:** одобрите её. Предложение станет черновиком и ещё не попадёт в каталог.
3. **Вендор 1 → Предложения:** заполните описание, цену в копейках и ёмкость, сохраните и опубликуйте.
4. **Покупатель 1 → Каталог:** добавьте предложения в корзину. В ней можно объединить разных вендоров. Создайте заказ.
5. **Заказы и билеты:** попробуйте оплату со сценарием «Ошибка», затем «Успех». Успешная оплата выдаёт уникальные билеты. «Подробнее» показывает состав и коды.
6. **Вендор предложения → Продажи:** вставьте код и погасите билет. Повторное погашение и отмена использованного билета отклоняются.
7. Для возврата создайте отдельный заказ, оплатите и нажмите «Отменить / вернуть» до погашения. Билеты будут отозваны, доступность восстановлена.

В интерфейсе видны HTTP-результат, обслужившие запрос экземпляры и ссылка на трассу Jaeger. Начальные предложения уже есть в каталоге. Комиссия 10% — демонстрационная политика; суммы хранятся целыми копейками. Автоматического истечения резервов пока нет, поэтому ненужные RESERVED-заказы отменяйте вручную.

## Адреса и доступ

| Компонент | Адрес |
|---|---|
| Демонстратор / gateway | http://localhost:8080 |
| Grafana | http://localhost:3000 |
| Prometheus | http://localhost:9090 |
| Jaeger | http://localhost:16686 |

**Grafana в текущем окружении: логин demo, пароль demo12345.** Datasource и dashboard загружаются автоматически. При новом setup генерируются случайные пароли; актуальный пароль указан в platform/.env. БД, Kafka, gRPC и mock endpoints доступны только внутри Compose; внешние порты привязаны к 127.0.0.1.

## Проверки и отказы

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify.ps1 -HA
docker compose -f compose.ha.yaml run --rm smoke python business.py
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-ha.ps1
```

Бизнес-проверка создаёт собственные тестовые предложения и заказы, сохраняет результат в test-results/business.json. Отказная проверка временно останавливает экземпляры приложений, Kafka и primary БД, затем восстанавливает их. Запускайте её отдельно от ручной демонстрации. Она меняет текущий primary; это нормальное ожидаемое поведение. Отчёт — test-results/ha-failures.json.

Управляемое переключение вручную:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\ha-postgres.ps1 failover
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\ha-postgres.ps1 rejoin
```

Между failover и rejoin работает одна копия БД. Старый primary скрипт сначала останавливает и проверяет, затем продвигает реплику. Не запускайте старый primary самостоятельно до rejoin. Автоматического referee/quorum нет.

## Остановка и повторный запуск

```powershell
docker compose -f compose.ha.yaml stop
docker compose -f compose.ha.yaml up -d --no-build --wait
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\ha-postgres.ps1 sync
docker compose -f compose.ha.yaml ps
```

Именованные тома сохраняются. Не используйте down -v, если хотите сохранить данные. Jaeger хранит трассы в памяти; mock-доставки также ограничены памятью процесса.

Базовый вариант без балансировки можно запустить отдельно: сначала остановите HA, затем docker compose -f compose.yaml up -d --build --wait. Для него остаётся scripts/verify.ps1. Старый endpoint /api/v1/demo/orders со сценариями success/error/delay/timeout сохранён для технического smoke.

## Документы и дальнейшая работа

- [Требования](docs/requirements.md), [архитектура](docs/architecture.md), [ADR](docs/adr.md).
- [Матрица реализации схемы и FR](docs/architecture-status.md).
- [Backlog](docs/backlog.md), [спринт 1](docs/sprint-1.md), [спринт 2](docs/sprint-2.md).
- [Результаты проверки](docs/verification.md), [эксплуатация](docs/operations.md), [инструкция платформы](platform/README.md).

Каталоги gateway/, order-service/, notification-service/, integration-mock/ и platform/ предназначены для отдельных Git-проектов. При разделении клонируйте их рядом. Общего родительского Go-модуля нет. Исходники задания сохранены в Technical Task/. Коммиты не создавались.

Следующие продуктовые задачи: ND-100 (утвердить политики), ND-101/102 (настоящий вход и доступ), ND-205 (истечение резервов), ND-206/207 (провайдер), ND-210 (SMTP). Антифрод, ClickHouse, автоматическое переключение БД, rate limiting, production TLS, backup/restore и нагрузочные гарантии не выдаются за выполненные.



