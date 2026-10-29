"""Business demo E2E; standard library, unique offers and preserved application history."""
import json, os, time, uuid, urllib.request, urllib.error
from pathlib import Path
BASE=os.getenv('GATEWAY_URL','http://gateway:8080')+'/api/v1/shop'
REPORT={'started_at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'checks':[]}
def call(method,path,body=None,actor='customer-demo',key=None,expected=(200,201)):
    headers={'X-Demo-Actor':actor,'Content-Type':'application/json'}
    if key: headers['Idempotency-Key']=key
    req=urllib.request.Request(BASE+path,data=None if body is None else json.dumps(body).encode(),headers=headers,method=method)
    try:
        with urllib.request.urlopen(req,timeout=12) as r: status=r.status; value=json.load(r)
    except urllib.error.HTTPError as e: status=e.code; value=json.load(e)
    assert status in expected,(method,path,status,value)
    assert value['demo'] is True and len(value['trace_id'])==32,value
    return value

def check(name): REPORT['checks'].append({'name':name,'passed':True})
def offer(vendor,capacity=2):
    app=call('POST','/applications',{'organization':'E2E '+uuid.uuid4().hex[:8],'title':'E2E offer','city':'Moscow','category':'test'},vendor)['application']
    call('POST',f"/applications/{app['id']}/approve",{},vendor,expected=(403,))
    value=call('POST',f"/applications/{app['id']}/approve",{},'admin-demo')['offer']
    oid=value['id']
    assert oid not in [o['id'] for o in call('GET','/catalog')['offers']]
    call('POST',f'/offers/{oid}/publish',{},vendor,expected=(400,))
    call('PATCH',f'/offers/{oid}',{'title':'E2E offer','description':'Demonstration','city':'Moscow','category':'test','price_cents':10000,'capacity':capacity},vendor)
    call('POST',f'/offers/{oid}/publish',{},vendor)
    return oid

def clear(actor):
    for item in call('GET','/cart',actor=actor)['cart']['items']:
        call('DELETE','/cart/items/'+item['offer_id'],actor=actor)

def run():
    a=offer('vendor-demo'); b=offer('vendor-two'); check('application_approval_publish_and_roles')
    clear('customer-demo');clear('customer-two')
    for oid in (a,b):call('POST','/cart/items',{'offer_id':oid,'quantity':1})
    call('POST','/orders',{},expected=(400,))
    call('POST','/cart/items',{'offer_id':a,'quantity':2},'customer-two')
    key=str(uuid.uuid4()); order=call('POST','/orders',{},key=key)['order'];oid=order['id']
    assert order['total_cents']==20000 and order['commission_cents']==2000 and len(order['items'])==2,order
    assert order['tickets']==[],order
    assert call('POST','/orders',{},key=key)['order']['id']==oid
    call('GET',f'/orders/{oid}',actor='customer-two',expected=(403,404))
    check('multi_vendor_checkout_idempotency_ownership_commission')
    # Reserve the final unit in another cart, then reject a third reservation.
    call('POST','/orders',{},'customer-two',str(uuid.uuid4()),expected=(409,))
    check('insufficient_stock_does_not_overreserve')
    call('POST',f'/orders/{oid}/pay',{'scenario':'error'},expected=(502,))
    assert call('GET',f'/orders/{oid}')['order']['status']=='RESERVED'
    paid=call('POST',f'/orders/{oid}/pay',{'scenario':'success'})['order']
    repeated=call('POST',f'/orders/{oid}/pay',{'scenario':'success'})['order']
    assert paid['status']=='PAID' and len(paid['tickets'])==2 and [t['code'] for t in repeated['tickets']]==[t['code'] for t in paid['tickets']]
    call('POST',f'/orders/{oid}/cancel',{'scenario':'error'},expected=(502,))
    assert call('GET',f'/orders/{oid}')['order']['status']=='PAID'
    check('payment_error_retry_idempotency_and_refund_failure_atomicity')
    ticket=next(t for t in paid['tickets'] if t['vendor_id']=='vendor-demo')
    call('POST','/tickets/'+ticket['code']+'/redeem',{},'vendor-two',expected=(403,))
    call('POST','/tickets/'+ticket['code']+'/redeem',{},'vendor-demo')
    call('POST','/tickets/'+ticket['code']+'/redeem',{},'vendor-demo',expected=(409,))
    call('POST',f'/orders/{oid}/cancel',{},expected=(409,))
    check('single_redemption_vendor_ownership_cancel_redeemed_rejected')
    clear('customer-two');call('POST','/cart/items',{'offer_id':b,'quantity':1},'customer-two')
    second=call('POST','/orders',{},'customer-two',str(uuid.uuid4()))['order']['id']
    call('POST',f'/orders/{second}/pay',{},'customer-two')
    cancelled=call('POST',f'/orders/{second}/cancel',{},'customer-two')['order']
    assert cancelled['status']=='CANCELLED' and all(t['status']=='REVOKED' for t in cancelled['tickets']),cancelled
    available=next(o['available'] for o in call('GET','/catalog')['offers'] if o['id']==b)
    call('POST',f'/orders/{second}/cancel',{},'customer-two')
    assert next(o['available'] for o in call('GET','/catalog')['offers'] if o['id']==b)==available
    check('paid_cancel_refund_revokes_ticket_and_releases_stock_once')
    deadline=time.monotonic()+90
    while time.monotonic()<deadline:
        with urllib.request.urlopen(os.getenv('MOCK_URL','http://integration-mock:8083')+'/mock/business-deliveries',timeout=8) as r: deliveries=json.load(r)['deliveries']
        matched=[d for d in deliveries if d['order_id'] in (oid,second)]
        if {d['order_id'] for d in matched}=={oid,second}:break
        time.sleep(1)
    assert {d['order_id'] for d in matched}=={oid,second},matched
    assert all(len(d['trace_id'])==32 for d in matched),matched
    check('business_outbox_to_mock_email_with_trace')
    REPORT['deliveries']=matched
    REPORT['order_ids']=[oid,second];REPORT['offer_ids']=[a,b]
try:
    run();REPORT['passed']=True
except Exception as e:
    REPORT['passed']=False;REPORT['failure']=str(e)
finally:
    Path('/results').mkdir(exist_ok=True);Path('/results/business.json').write_text(json.dumps(REPORT,indent=2))
print(json.dumps(REPORT));raise SystemExit(0 if REPORT['passed'] else 1)
