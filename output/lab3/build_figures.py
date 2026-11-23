"""Rebuild report figures from the repository's recorded evidence; no live requests."""
import json
import hashlib
from pathlib import Path
from datetime import datetime, timezone
from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[2]
OUT = Path(__file__).resolve().parent
ASSETS = OUT / 'assets'
ASSETS.mkdir(parents=True, exist_ok=True)
FONT = 'C:/Windows/Fonts/times.ttf'
BOLD = 'C:/Windows/Fonts/timesbd.ttf'
def font(size=28, bold=False):
    return ImageFont.truetype(BOLD if bold else FONT, size)
def canvas(w=2000,h=1250):
    im=Image.new('RGB',(w,h),'white'); return im,ImageDraw.Draw(im)
def text(d,xy,s,size=28,bold=False,anchor='mm'):
    d.multiline_text(xy,s,fill='black',font=font(size,bold),anchor=anchor,align='center',spacing=7)
def box(d,b,s,size=27):
    d.rectangle(b,fill='white',outline='black',width=2)
    text(d,((b[0]+b[2])/2,(b[1]+b[3])/2),s,size)
def arrow(d,pts,label=None,at=None):
    d.line(pts,fill='black',width=2)
    x,y=pts[-1]; px,py=pts[-2]
    import math
    a=math.atan2(y-py,x-px); r=12
    d.polygon([(x,y),(x-r*math.cos(a-.45),y-r*math.sin(a-.45)),(x-r*math.cos(a+.45),y-r*math.sin(a+.45))],fill='black')
    if label: text(d,at,label,24)

im,d=canvas(2000,1320)
text(d,(1000,48),'Развёрнутая архитектура NetDosuga',38,True)
text(d,(1000,96),'Docker Compose HA · шесть приложений Go · единый кластер MVP',28)
# Main request path, arranged in a single row to make routing unambiguous.
nodes=[(35,220,245,330),(320,220,565,330),(640,180,915,370),(990,220,1240,330),(1320,180,1650,370)]
labels=['Браузер','HTTP HAProxy\nhttp-router','API Gateway\ngateway-a / gateway-b','gRPC HAProxy\ngrpc-router','Orders Service\norder-a / order-b']
for b,s in zip(nodes,labels): box(d,b,s)
for a,b,l in zip(nodes[:-1],nodes[1:],['HTTP','HTTP','gRPC','gRPC']):
    arrow(d,[(a[2],275),(b[0],275)],l,((a[2]+b[0])/2,245))
box(d,(640,485,915,595),'Identity Service\nidentity-service')
arrow(d,[(775,370),(775,485)],'HTTP',(830,425))
arrow(d,[(1485,370),(1485,435),(915,435),(915,485)],'HTTP',(1180,413))
box(d,(1715,220,1965,330),'Payment Mock\npayment-mock')
arrow(d,[(1650,275),(1715,275)],'HTTP',(1682,245))
box(d,(1320,485,1650,595),'Transactional outbox\nв PostgreSQL')
arrow(d,[(1485,370),(1485,485)])
box(d,(1320,710,1650,820),'Kafka\nbusiness / demo events')
arrow(d,[(1485,595),(1485,710)],'публикация',(1580,652))
box(d,(780,710,1135,820),'Notification Worker\nnotification-service')
arrow(d,[(1320,765),(1135,765)],'события',(1228,738))
box(d,(335,710,625,820),'Mail Mock\nintegration-mock')
arrow(d,[(780,765),(625,765)],'HTTP',(700,738))
# Shared persistence is explicitly labelled instead of drawing crossing arrows.
d.line([(35,905),(1965,905)],fill='black',width=1)
text(d,(1000,945),'Общие данные: Orders, Identity, Payment и Notification -> postgres-router',29)
box(d,(220,1010,600,1120),'PostgreSQL HAProxy\npostgres-router')
box(d,(795,1010,1210,1120),'PostgreSQL primary\npg-a или pg-b')
box(d,(1450,1010,1865,1120),'PostgreSQL standby\nвторой узел')
arrow(d,[(600,1065),(795,1065)],'SQL',(698,1038))
arrow(d,[(1210,1065),(1450,1065)],'WAL · sync',(1330,1038))
text(d,(1000,1190),'Наблюдаемость: OTLP -> Jaeger; /metrics -> Prometheus -> Grafana',29)
text(d,(1000,1255),'Источник: platform/compose.ha.yaml. Переключение PostgreSQL выполняется вручную с fencing.',25)
im.save(ASSETS/'architecture.png',dpi=(300,300))

load_path=ROOT/'platform/test-results/load-20261106T123209Z-03d4d06b4a7b.json'
load=json.loads(load_path.read_text(encoding='utf-8-sig'))
im,d=canvas(2000,1120)
text(d,(1000,50),'Нагрузочная демонстрация: GET /api/v1/shop/catalog',36,True)
text(d,(1000,105),'06.11.2026, 12:31:59 UTC · длительность 10 с · 10 запросов/с · concurrency = 4',29)
text(d,(1000,170),'100 запросов · HTTP 200: 100 · ошибок: 0 · пропущенных тактов: 0',31,True)
text(d,(1000,240),'Задержка ответа, мс',31)
left,right,top,bottom=220,1850,310,790
for value in range(0,51,10):
    y=bottom-(bottom-top)*value/50
    d.line([(left,y),(right,y)],fill='#cccccc',width=1)
    text(d,(left-45,y),str(value),25)
keys=['avg','p50','p95','p99','max']; names=['Среднее','p50','p95','p99','Максимум']
for i,(key,name) in enumerate(zip(keys,names)):
    x=left+155+i*320; val=load['latency_ms'][key]; y=bottom-(bottom-top)*val/50
    d.rectangle((x-80,y,x+80,bottom),fill='#d9d9d9',outline='black',width=2)
    text(d,(x,y-27),f'{val:.3f}',29)
    text(d,(x,bottom+35),name,28)
d.line([(left,top),(left,bottom),(right,bottom)],fill='black',width=2)
text(d,(1000,915),'Балансировка: gateway-a / gateway-b = 51 / 49; order-a / order-b = 51 / 49',30)
text(d,(1000,980),'Локальный демонстрационный запуск; результаты не задают SLA.',28)
text(d,(1000,1045),load_path.name,24)
im.save(ASSETS/'load-results.png',dpi=(300,300))

sources=[]
for name in ['lab3-report/smoke.json','lab3-report/business.json','lab3-report/mvp.json',
             'smoke.json','business.json','mvp-main.json','mvp.json','ha-failures.json','bootstrap.json',load_path.name]:
    p=ROOT/'platform/test-results'/name
    if not p.exists(): continue
    obj=json.loads(p.read_text(encoding='utf-8-sig')); checks=obj.get('checks',[])
    items=list(checks.values()) if isinstance(checks,dict) else checks
    sources.append({'path':p.relative_to(ROOT).as_posix(),'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),
        'file_modified_at_utc':datetime.fromtimestamp(p.stat().st_mtime,timezone.utc).isoformat(),
        'started_at':obj.get('started_at'),'finished_at':obj.get('finished_at'),'passed':obj.get('passed'),
        'timestamp_source':'JSON started_at' if obj.get('started_at') else 'No execution timestamp in JSON; file_modified_at_utc is filesystem metadata only',
        'checks_total':len(items),'checks_passed':sum(c.get('passed') is True for c in items)})
evidence={'generated_at':datetime.now(timezone.utc).isoformat(),'architecture_source':'platform/compose.ha.yaml',
    'architecture_scope':'Six Go applications in the same HA MVP Compose cluster. Database primary is role-dependent; no live inspection performed by this figure build.',
    'load':{k:load[k] for k in ['started_at','scenario','configuration','requests','success','errors','http_statuses','skipped_ticks','elapsed_including_drain_seconds','achieved_rps','latency_ms','gateway_instances','order_instances']},
    'sources':sources,'note':'Figures represent source configuration and recorded test results, not a live availability claim.'}
(OUT/'evidence.json').write_text(json.dumps(evidence,ensure_ascii=False,indent=2),encoding='utf-8')
print('Created architecture.png, load-results.png, evidence.json')
