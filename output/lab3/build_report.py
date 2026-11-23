"""Build the compact laboratory report from captured UI and recorded results."""
from pathlib import Path
import json
from xml.sax.saxutils import escape
from reportlab.pdfgen import canvas
from reportlab.lib.pagesizes import A4
from reportlab.lib import colors
from reportlab.lib.styles import ParagraphStyle
from reportlab.platypus import Paragraph, Table, TableStyle
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from PIL import Image

ROOT = Path(__file__).resolve().parents[2]
BASE = ROOT / 'output/lab3'
ASSETS = BASE / 'assets'
OUT = ROOT / 'output/pdf/netdosuga-lab3.pdf'
W, H = A4
LEFT, RIGHT, TOP, BOTTOM = 48, 48, 45, 43
CW = W - LEFT - RIGHT
for name, filename in [('Times','times.ttf'), ('Times-Bold','timesbd.ttf'), ('Times-Italic','timesi.ttf')]:
    pdfmetrics.registerFont(TTFont(name, 'C:/Windows/Fonts/' + filename))
pdfmetrics.registerFont(TTFont('Mono', 'C:/Windows/Fonts/consola.ttf'))

styles = {
    'body': ParagraphStyle('body', fontName='Times', fontSize=11, leading=13.5, spaceAfter=7),
    'caption': ParagraphStyle('caption', fontName='Times', fontSize=10, leading=12, alignment=1),
    'small': ParagraphStyle('small', fontName='Times', fontSize=9.5, leading=11.5),
    'head': ParagraphStyle('head', fontName='Times-Bold', fontSize=14, leading=17),
    'cell': ParagraphStyle('cell', fontName='Times', fontSize=10, leading=12),
    'code': ParagraphStyle('code', fontName='Mono', fontSize=9.5, leading=12),
}

class Report:
    def __init__(self):
        OUT.parent.mkdir(parents=True, exist_ok=True)
        self.c = canvas.Canvas(str(OUT), pagesize=A4)
        self.c.setTitle('netdosuga. Лабораторная работа № 3')
        self.c.setAuthor('Команда Netdosuga, M3415')
        self.y = H - TOP
        self.page = 1
        self.figure = 0

    def p(self, text, style='body', gap=7):
        obj = Paragraph(text, styles[style])
        _, height = obj.wrap(CW, H)
        if self.y - height < BOTTOM:
            raise RuntimeError(f'Page {self.page} overflow: {text[:70]}')
        obj.drawOn(self.c, LEFT, self.y - height)
        self.y -= height + gap

    def heading(self, text):
        self.p(text, 'head', 12)

    def image(self, name, caption, max_height):
        path = ASSETS / name
        with Image.open(path) as img:
            iw, ih = img.size
        ratio = min(CW / iw, max_height / ih)
        width, height = iw * ratio, ih * ratio
        if self.y - height < BOTTOM + 30:
            raise RuntimeError(f'Image overflow on page {self.page}: {name}')
        self.c.drawImage(str(path), LEFT + (CW - width) / 2, self.y - height,
                         width=width, height=height)
        self.y -= height + 7
        self.figure += 1
        self.p(f'Рисунок {self.figure}. {caption}', 'caption', 12)

    def table(self, rows, widths):
        data = [[Paragraph(escape(str(v)), styles['cell']) for v in row] for row in rows]
        table = Table(data, colWidths=widths, hAlign='LEFT')
        table.setStyle(TableStyle([
            ('GRID',(0,0),(-1,-1),0.5,colors.black),
            ('VALIGN',(0,0),(-1,-1),'TOP'),
            ('LEFTPADDING',(0,0),(-1,-1),6), ('RIGHTPADDING',(0,0),(-1,-1),6),
            ('TOPPADDING',(0,0),(-1,-1),5), ('BOTTOMPADDING',(0,0),(-1,-1),5),
        ]))
        _, height = table.wrap(CW, H)
        if self.y - height < BOTTOM:
            raise RuntimeError(f'Table overflow on page {self.page}')
        table.drawOn(self.c, LEFT, self.y - height)
        self.y -= height + 12

    def footer(self):
        self.c.setFont('Times', 10)
        self.c.drawCentredString(W/2, 24, str(self.page))

    def next(self):
        self.footer()
        self.c.showPage()
        self.page += 1
        self.y = H - TOP

    def finish(self):
        self.footer()
        self.c.save()

def build():
    content = json.loads((BASE / 'report.json').read_text(encoding='utf-8'))
    r = Report()
    r.c.setFont('Times-Bold', 23)
    r.c.drawCentredString(W/2, r.y - 20, 'netdosuga')
    r.y -= 43
    for line in ['Лабораторная работа № 3', 'Реализация и проверка сервиса']:
        r.c.setFont('Times', 14)
        r.c.drawCentredString(W/2, r.y, line)
        r.y -= 18
    r.y -= 10
    r.p('Состав команды:', 'head', 5)
    for name in ['Ивашов Алексей', 'Трохан Александр', 'Айдашов Амир', 'Скуридин Дмитрий', 'Ким Александр', 'Матвеев Иван']:
        r.p(name + ' - M3415', 'body', 0)
    r.y -= 14
    r.heading('1. Цель и реализованная архитектура')
    r.p(content['architecture'])
    r.image('architecture.png', 'Состав локального HA-стенда по platform/compose.ha.yaml.', 330)

    r.next()
    r.heading('2. Пользовательский сценарий')
    r.p(content['scenario'])
    r.image('catalog.png', 'Афиша: опубликованные события доступны покупателю.', 430)
    r.table([
        ['Шаг', 'Наблюдаемый результат'],
        ['Заявка и модерация', 'После одобрения создаётся черновик. Организатор заполняет его и публикует событие.'],
        ['Корзина и заказ', 'Заказ резервирует места на 15 минут. Повтор ключа идемпотентности возвращает тот же заказ.'],
        ['Оплата', 'Успешный ответ тестового провайдера переводит заказ в PAID и создаёт билеты.'],
        ['Погашение / возврат', 'Билет погашается один раз. Возврат запрещён после использования.'],
    ], [105, CW-105])
    r.p('Заказ: RESERVED -&gt; PAID / CANCELLED / EXPIRED; PAID -&gt; CANCELLED.<br/>Билет: VALID -&gt; REDEEMED / REVOKED.', 'small')

    r.next()
    r.heading('3. HTTP API и проверка контрактов')
    r.p(content['api'])
    r.image('swagger.png', 'Swagger UI: запрос каталога с фильтром города вернул HTTP 200.', 400)
    r.table([
        ['Проверка', 'Подтверждаемый результат'],
        ['Login / session / logout', 'Cookie-сессия, завершение сессии и проверка роли на защищённых маршрутах.'],
        ['Mock: success / error / timeout', 'HTTP 200 / 502 / 504 в техническом контракте. Ошибка зависимости не считается успехом.'],
        ['Чужой заказ или роль', 'Ограничения владельца и роли проверяются в бизнес-сценариях.'],
    ], [150, CW-150])
    r.p('Сопоставление ответа API и афиши:', 'body', 5)
    r.table([
        ['Событие', 'Цена, коп.', 'Мест всего / доступно'],
        ['Планетарий', '150000', '100 / 98'],
        ['Мастерская', '90000', '50 / 50'],
    ], [180, 100, CW-280])
    r.p('В ответе обе записи имеют status=PUBLISHED и city=Москва. Те же события, цены в рублях и доступность показаны на рисунке 2.', 'small')

    r.next()
    r.heading('4. Сквозная трассировка')
    r.p(content['trace'])
    r.image('trace.png', 'Jaeger: операции оплаты; внутренние ветви отдельных служб свёрнуты.', 570)
    r.p('Трасса d82630de90066ae50f86611ebe615064: HTTP-вход занял 31,22 мс, operation.orders.pay - 29,68 мс, payment.authorize - 9,04 мс. Общий интервал трассы 1,14 с включает асинхронную публикацию outbox и обработку уведомления, поэтому он больше времени HTTP-ответа.', 'body')

    r.next()
    r.heading('5. Операции с данными')
    r.p(content['sql'])
    r.image('sql.png', 'Деталь SQL span в Jaeger: операция базы данных внутри бизнес-запроса.', 550)
    r.p('Запрос, показанный в span:', 'body', 4)
    r.p('<font name="Courier" size="10">SELECT customer_id,status,total_cents,expires_at<br/>FROM business_orders WHERE id=$1 FOR UPDATE</font>', 'body', 12)
    r.table([
        ['Атрибут', 'Значение и смысл'],
        ['db.system', 'postgresql - используемая СУБД'],
        ['db.operation', 'SELECT - чтение строки заказа'],
        ['db.query.parameter_count', '1 - идентификатор передаётся параметром $1'],
        ['db.response.returned_rows', '1 - запрос нашёл одну строку'],
    ], [175, CW-175])
    r.p('FOR UPDATE блокирует строку заказа. Конкурирующее изменение той же строки ждёт завершения транзакции.', 'body')
    r.p('Заказы, билеты, сессии, outbox и результаты mock-провайдера сохраняются в PostgreSQL. Транзакции и уникальные ключи обеспечивают атомарность и защиту от повторного эффекта. Между сервисами используется общая БД.', 'body')

    r.next()
    r.heading('6. События и метрики')
    r.p(content['telemetry'])
    r.image('kafka.png', 'Kafka UI: сообщения бизнес-топика в локальном брокере.', 260)
    r.image('grafana.png', 'Grafana: метрики операций из Prometheus на работающем стенде.', 260)
    r.p('Событие сохраняется вместе с изменением заказа в transactional outbox. Consumer уведомлений использует event_id для дедупликации; доставка имеет семантику at-least-once.', 'body')

    r.next()
    r.heading('7. Результаты проверок и ограничения')
    r.p(content['tests_intro'])
    r.table(content['tests_table'], [115, 80, CW-195])
    r.image('load-results.png', 'Короткий нагрузочный прогон каталога: задержки и распределение по репликам.', 190)
    r.p(content['load'], 'small')
    r.p(content['limits'])
    r.p('Запуск и воспроизведение:', 'head', 6)
    for line in [
        'docker compose -f platform/compose.ha.yaml up -d --build --wait',
        'docker compose -f platform/compose.tests.yaml up --abort-on-container-exit --exit-code-from tests',
        'powershell -File platform/scripts/verify-ha.ps1',
    ]:
        r.p(escape(line), 'code', 4)
    r.finish()
    print(OUT)
    print('pages:', r.page)

if __name__ == '__main__':
    build()
