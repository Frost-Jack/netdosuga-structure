import json,html,re
from pathlib import Path
ROOT=Path(__file__).resolve().parent
p='docker compose -f platform/compose.ha.yaml '
commands=[]
def add(title,command,purpose,expected,effect='Читает состояние; данные не изменяет.',tags=''):
 commands.append(dict(title=title,command=command,purpose=purpose,workdir='Корень репозитория',expected=expected,side_effects=effect,tags=tags))
add('Сборка и запуск',p+'up -d --build --wait','Подготовить HA-стенд перед защитой.','Контейнеры запущены и прошли healthcheck.','Собирает образы, запускает контейнеры и инициализирует стенд.','подготовка docker запуск')
add('Состояние',p+'ps','Проверить доступность контейнеров.','Сервисы Running / healthy.')
add('Проверки сценариев','docker compose -f platform/compose.tests.yaml up --abort-on-container-exit --exit-code-from tests','Запустить контейнер проверок.','tests завершился с кодом 0.','Выполняет бизнес-сценарии и меняет демонстрационные данные.','тесты smoke mvp')
add('Остановка',p+'stop','Остановить демонстрационный стенд без удаления томов.','Контейнеры остановлены.','Прерывает доступность стенда.')
add('Повторный запуск',p+'up -d --no-build --wait','Вернуть ранее собранный стенд.','Сервисы healthy.','Запускает контейнеры.')
add('Журналы',p+'logs --tail 100 gateway-a order-a identity-service payment-mock notification-service','Посмотреть последние сообщения сервисов.','JSON-журналы запросов и обработки событий.','Читает журналы.','логи logs')
for name,title in [('verify-ha','Проверка HA'),('verify-bootstrap','Проверка bootstrap')]:
 add(title,f'powershell -NoProfile -ExecutionPolicy Bypass -File platform/scripts/{name}.ps1','Выполнить воспроизводимую проверку стенда.','Скрипт завершился без ошибки.','Меняет состояние демонстрационного стенда; выполнять до выступления.','тесты отказоустойчивость')
for action,description,effect in [
 ('sync','Включить или восстановить синхронную репликацию remote_apply','Меняет конфигурацию primary: ALTER SYSTEM SET synchronous_standby_names=\'*\', затем reload. При потере standby подтверждение записи может блокироваться.'),
 ('failover','Остановить прежний primary, доказать fencing и повысить standby','Останавливает прежний primary, повышает standby, отключает ожидание синхронной реплики и меняет маршрут PostgreSQL. Стенд остаётся с одной долговечной копией; затем требуется rejoin.'),
 ('rejoin','Вернуть прежний primary как реплику через base backup','Останавливает возвращаемый узел, архивирует прежний PGDATA на его HA-томе и заново клонирует данные с текущего primary. Затем запускает реплику и восстанавливает синхронный режим.')]:
 add('PostgreSQL: '+action,f'powershell -NoProfile -ExecutionPolicy Bypass -File platform/scripts/ha-postgres.ps1 {action}',description+'.','Скрипт завершился без ошибки; проверить ps и доступность записи.',effect,'postgres реплика репликация отказ отказы')
for service in ['gateway-a','kafka']:
 add('Остановить '+service,p+'stop '+service,'Показать отказ компонента.','Компонент остановлен; проверить ожидаемое поведение.','Нарушает доступность компонента; затем выполнить восстановление.','отказ отказы demo')
 add('Восстановить '+service,p+'up -d --no-build --no-deps --wait '+service,'Восстановить компонент после демонстрации отказа.','Компонент healthy.','Запускает компонент.','восстановление')
for title,command,purpose in [('Вход','POST /api/v1/auth/login','В Swagger передать email и пароль demo12345; сохранить cookie сессии.'),('Сессия','GET /api/v1/auth/session','Проверить текущего пользователя и роль.'),('Корзина','GET /api/v1/shop/cart','Проверить корзину покупателя.'),('Выход','POST /api/v1/auth/logout','Завершить сессию перед сменой роли.')]:
 add('HTTP / '+title,command,purpose,'Swagger показывает ответ сервиса.','HTTP-операция в Swagger; не команда PowerShell.','HTTP Swagger авторизация')
 commands[-1]['type']='HTTP'
for c in commands: c.setdefault('type','PowerShell')
plan=(ROOT/'plan.txt').read_text(encoding='utf-8-sig')
data=dict(title='НетДосуга — руководство к защите',plan_markdown=plan,commands=commands)
(ROOT/'guide.json').write_text(json.dumps(data,ensure_ascii=False,indent=2),encoding='utf-8')
def inline(text):
 text=html.escape(text)
 text=re.sub(r'\[([^\]]+)\]\(([^)]+)\)',lambda m: '<a href="'+html.escape(m[2],quote=True)+'">'+m[1]+'</a>',text)
 text=re.sub(r'\*\*(.+?)\*\*',r'<strong>\1</strong>',text)
 text=re.sub(r'`([^`]+)`',r'<code>\1</code>',text)
 return text
def render(md):
 out=[]; fence=False; table=False
 for line in md.splitlines():
  if line.startswith('|'):
   if re.match(r'^\|[\s:|\-]+$',line): continue
   if not table: out.append('<table>');table=True
   out.append('<tr>'+''.join('<td>'+inline(cell.strip())+'</td>' for cell in line.strip('|').split('|'))+'</tr>');continue
  if table:out.append('</table>');table=False
  if line.startswith('```'):
   out.append('</code></pre>' if fence else '<pre><code>');fence=not fence;continue
  if fence:out.append(html.escape(line)+'\n');continue
  m=re.match(r'^(#{1,6})\s+(.*)',line)
  if m:
   n=len(m[1]);out.append(f'<h{n} id="s{len(out)}">{inline(m[2])}</h{n}>')
  elif line.strip():out.append('<p>'+inline(line)+'</p>')
 if table:out.append('</table>')
 return '\n'.join(out)
body=render(plan)
cards=''.join('<article class="command" data-search="'+html.escape(' '.join(str(v) for v in c.values()).lower(),quote=True)+'"><h3>'+html.escape(c['title'])+'</h3><p>'+html.escape(c['purpose'])+'</p><pre><code>'+html.escape(c['command'])+'</code></pre><button class="copy">Копировать</button><dl>'+''.join('<dt>'+k+'</dt><dd>'+html.escape(c[v])+'</dd>' for k,v in [('Каталог','workdir'),('Ожидаемый результат','expected'),('Побочные эффекты','side_effects')])+'</dl></article>' for c in commands)
page='''<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Защита НетДосуга</title><style>
table{table-layout:fixed;overflow-wrap:anywhere;border-collapse:collapse;width:100%;font-size:14px}td{border:1px solid #ccc;padding:9px;vertical-align:top}tr:first-child{font-weight:600;background:#f3f3f3}*{box-sizing:border-box}body{margin:0;color:#242424;background:#fff;font:16px/1.6 "Segoe UI",Arial,sans-serif}aside{position:fixed;inset:0 auto 0 0;width:245px;padding:28px 22px;background:#f3f3f3;border-right:1px solid #ccc;overflow:auto}aside a{display:block;margin:10px 0;color:#333}main{max-width:1160px;margin-left:245px;padding:35px 48px}h1,h2,h3{line-height:1.25}h1{font-size:32px}h2{margin-top:42px;border-top:3px solid #D94B32;padding-top:18px}a{color:#a72f1c}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f3f3f3;padding:16px;font:14px/1.6 Consolas,monospace}p{white-space:pre-wrap}button,input{font:inherit;padding:8px 12px}button{cursor:pointer;background:white;border:1px solid #777}input{max-width:100%;width:75%;border:1px solid #777}.search{position:sticky;top:0;background:#fff;padding:16px 0;border-bottom:1px solid #aaa;z-index:1}.command{padding:16px 0;border-bottom:1px solid #aaa}dt{font-weight:600}dd{margin-left:0;margin-bottom:10px}[hidden]{display:none!important}#status{color:#a72f1c}@media(max-width:800px){aside{position:static;width:auto}main{margin:0;padding:20px}.search{position:static}}@media print{aside,.search,button,.download{display:none!important}main{margin:0;padding:0;max-width:none}.command[hidden]{display:block!important}h2{break-after:avoid}pre{border:1px solid #bbb}.command{break-inside:avoid}}
</style><aside><strong>НЕТДОСУГА / ЗАЩИТА</strong><a href="#guide">План и объяснения</a><a href="#commands">Поиск команд</a><a class="download" href="../pdf/netdosuga-defense.pdf">Скачать PDF</a><p>20 минут · инженерный справочник</p><p>Команды только отображаются и копируются. Страница ничего не запускает.</p></aside><main><section id="guide">'''+body+'''</section><section id="commands"><h2>Команды для подготовки и демонстрации</h2><div class="search"><label for="q">Найти команду, назначение или тег</label><br><input id="q" type="search" placeholder="Например: postgres, отказ, журналы"><button id="clear">Очистить</button><div id="count" aria-live="polite"></div><div id="status" role="status"></div></div><p id="empty" hidden>Ничего не найдено. Измените запрос.</p>'''+cards+'''</section></main><script>
const q=document.querySelector('#q'),cards=[...document.querySelectorAll('.command')];function filter(){let query=q.value.trim().toLocaleLowerCase('ru');let n=0;cards.forEach(c=>{c.hidden=!c.dataset.search.includes(query);if(!c.hidden)n++});document.querySelector('#count').textContent=`Показано: ${n} из ${cards.length}`;document.querySelector('#empty').hidden=n!==0}q.addEventListener('input',filter);document.querySelector('#clear').onclick=()=>{q.value='';filter();q.focus()};document.addEventListener('keydown',e=>{if(e.key==='Escape'){q.value='';filter()}if(e.key==='/'&&!/INPUT|TEXTAREA|SELECT/.test(e.target.tagName)&&!e.target.isContentEditable){e.preventDefault();q.focus()}});document.querySelectorAll('.copy').forEach(b=>b.onclick=async()=>{const code=b.parentElement.querySelector('code');let ok=false;try{await navigator.clipboard.writeText(code.textContent);ok=true}catch(e){const t=document.createElement('textarea');t.value=code.textContent;document.body.append(t);t.select();try{ok=document.execCommand('copy')}catch(e){}t.remove()}document.querySelector('#status').textContent=ok?'Команда скопирована.':'Копирование недоступно: команда выделена, нажмите Ctrl+C.';if(!ok){const r=document.createRange();r.selectNodeContents(code);const s=getSelection();s.removeAllRanges();s.addRange(r)}});filter();
</script></html>'''
(ROOT/'index.html').write_text(page,encoding='utf-8')
print(f'Built {len(commands)} commands; {len(plan)} source characters')

