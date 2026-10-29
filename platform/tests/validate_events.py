import json
from pathlib import Path
from contracts import validate
report=json.loads(Path('/results/smoke.json').read_text())
ids={report['scenarios'][s]['body']['event_id'] for s in ('success','delay')}
schemas=[json.loads(Path(p).read_text()) for p in ('/contracts/order-service/demo-order.schema.json','/contracts/notification-event.schema.json')]
events=[]
for line in Path('/results/kafka-events.jsonl').read_text(encoding='utf-8-sig').splitlines():
 try:
  event=json.loads(line)
 except ValueError:
  continue
 for schema in schemas:
  validate(event,schema)
 if event['event_id'] in ids:
  events.append(event)
assert {event['event_id'] for event in events} == ids, 'Kafka did not expose both current smoke events'
Path('/results/kafka-contracts.json').write_text(json.dumps({'passed':True,'events':events},indent=2))
print('Kafka event contracts passed for both current smoke events')
