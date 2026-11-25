# Приёмочные тесты Netdosuga

Команды выполняются из корня репозитория. Нужны Python 3.11+, Docker Compose с Linux-контейнерами и Git. Браузерные проверки используют Playwright. Для HA-проверок нужен PowerShell.

## Команды

| Набор | Команда |
|---|---|
| Go и PostgreSQL | `python platform/tests/coverage_run.py` |
| Быстрая проверка работоспособности | `python platform/tests/acceptance_run.py sanity` |
| Функциональные и негативные сценарии, проверка телеметрии | `python platform/tests/acceptance_run.py functional` |
| Браузер | `python platform/tests/acceptance_run.py browser` |
| Восстановление и Kafka | `python platform/tests/acceptance_run.py faults` |
| L-01, каталог | `python platform/tests/acceptance_run.py load-catalog` |
| L-02, оформление и оплата | `python platform/tests/acceptance_run.py load-orders` |

`--case F-09 --case N-10a` запускает указанные сценарии и их варианты. Для двух независимых функциональных прогонов выполните одну и ту же команду дважды на одной версии файлов. Нагрузочные профили запускать отдельно при свободном стенде.

Каждый запуск создаёт новый Compose-проект, собственную сеть и тома. Порты приложений назначаются динамически на loopback. Тесты используют уникальные данные и сохраняют отчёты в `platform/test-results/<run_id>/`. HA-скрипт получает копию конфига маршрутизации из каталога прогона.

## Браузеры и покрытие

```powershell
python -m pip install playwright
python -m playwright install chromium
python platform/tests/acceptance_run.py browser --browser chrome
python platform/tests/acceptance_run.py browser --browser edge
$env:YANDEX_BROWSER_PATH = 'C:\path\to\browser.exe'
python platform/tests/acceptance_run.py browser --browser yandex
```

Проверки по умолчанию используют Chromium из Playwright. Для Chrome, Edge и Яндекс Браузера нужны их установленные версии. Имя и версия браузера сохраняются в `browser-version.txt`. Для L-01 можно передать тот же `--browser`.

Сбор покрытия по операторам собственного `gateway/web/app.js`:

```powershell
npm --prefix platform/test-results/javascript install --no-audit --no-fund istanbul-lib-instrument@6.0.3 istanbul-lib-coverage@3.2.2
python platform/tests/acceptance_run.py browser --js-coverage
```

Профиль покрытия Go формируется для каждого модуля. Общий процент рассчитывается по суммарному числу операторов. При отсутствии обязательного теста с базой данных, статусе `SKIP` или покрытии ниже 85% команда возвращает ошибку. Проверки используют три отдельные базы. Для прогона с локальным комплектом Go предусмотрен флаг `--local-go`. Каталог с исполняемым файлом `go` также должен быть в `PATH`.

Для объединения Go-тестов с покрытием сервисов от запросов к инструментированному стенду:

```powershell
python platform/tests/acceptance_run.py functional --go-coverage
python platform/tests/acceptance_run.py browser --go-coverage --js-coverage
python platform/tests/coverage_run.py --app-run platform/test-results/<functional_run_id> --app-run platform/test-results/<browser_run_id>
```

Профили должны соответствовать одной версии исходников. Реплики пишут счётчики при штатной остановке, два тестовых источника объединяются по блокам исходного кода. Сгенерированные Go-файлы исключаются по метке генератора. Покрытие JavaScript считается отдельно.

## Отчёты

`acceptance.json` и `go-coverage.json` содержат статусы, время сценариев, `run_id` и контрольную сумму исходников. Отчёт нагрузки включает задержки, ошибки, распределение по репликам, ресурсы и outbox. Отчёт HA сохраняет время восстановления чтения, записи и репликации.

```powershell
python platform/tests/acceptance_matrix.py --output platform/test-results/matrix.json
python platform/tests/acceptance_matrix.py platform/test-results/<run_id>/acceptance.json platform/test-results/<go_run_id>/go-coverage.json --output platform/test-results/matrix.json
```

В матрице перечислены проверки и исходы, участки кода, приоритеты и все идентификаторы FR/NFR. Без результатов исход отмечен BLOCKED. Повтор оформления с прежним ключом возвращает исходный заказ и сохраняет изменённую корзину. Это проверяет `F-10-changed-cart`.

Номера требований сверены с «Тех платформы ЛР 1.pdf», стр. 1–3. Последовательности операций сверены с `netdosuga_architecture.pdf`, стр. 3–11, а границы реализованного стенда с `netdosuga-lab3.pdf`, стр. 1–7.

Штатные smoke/business/MVP-команды из `compose.tests.yaml` доступны отдельно для уже работающего стенда.
