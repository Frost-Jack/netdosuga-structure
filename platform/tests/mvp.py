"""Authentication, provider durability and real trace structure checks. No credentials in reports."""
import http.cookiejar,json,os,time,urllib.request,urllib.error,uuid
from pathlib import Path
BASE=os.getenv('GATEWAY_URL','http://gateway:8080')
REPORT={'checks':[],'passed':False}
def check(name):REPORT['checks'].append({'name':name,'passed':True})
def request(opener,url,body=None,headers=None,method=None,timeout=8):
    h={'Content-Type':'application/json'};h.update(headers or {})
    req=urllib.request.Request(url,data=None if body is None else json.dumps(body).encode(),headers=h,method=method)
    try:
        with opener.open(req,timeout=timeout) as r:return r.status,json.loads(r.read() or '{}'),r.headers
    except urllib.error.HTTPError as e:return e.code,json.loads(e.read() or '{}'),e.headers
try:
    anonymous=urllib.request.build_opener()
    status,_,_=request(anonymous,BASE+'/api/v1/shop/cart',headers={'X-Demo-Actor':'admin-demo'})
    assert status==401,status;check('spoofed_demo_actor_cannot_authenticate')
    jar=http.cookiejar.CookieJar();session=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    status,data,_=request(session,BASE+'/api/v1/auth/login',{'email':'buyer@netdosuga.local','password':os.getenv('MVP_SEED_PASSWORD','demo12345')})
    assert status==200 and 'session_token' not in data,status
    cookies=list(jar);assert len(cookies)==1 and cookies[0].name=='nd_session' and cookies[0].has_nonstandard_attr('HttpOnly')
    gateways=set()
    for _ in range(24):
        status,data,headers=request(session,BASE+'/api/v1/auth/session')
        assert status==200,status
        if headers.get('X-Gateway-Instance'):gateways.add(headers['X-Gateway-Instance'])
        status,_,headers=request(session,BASE+'/api/v1/shop/cart');assert status==200,status
        if headers.get('X-Gateway-Instance'):gateways.add(headers['X-Gateway-Instance'])
    if int(os.getenv('EXPECTED_PROM_TARGETS','8'))==8:assert len(gateways)==2,gateways
    check('opaque_http_only_cookie_session_across_gateway_instances')
    status,_,_=request(session,BASE+'/api/v1/shop/cart/items',{'offer_id':'unknown','quantity':1},headers={'Origin':'https://foreign.invalid'})
    assert status==403,status;check('foreign_origin_mutation_rejected')
    business_report=json.loads(Path('/results/business.json').read_text())
    assert business_report.get('passed') and business_report.get('payment_trace_id'),'business payment trace evidence missing'
    status,owned_order,_=request(session,BASE+'/api/v1/shop/orders/'+business_report['order_ids'][0]);assert status==200,status
    sensitive_values=[os.getenv('MVP_SEED_PASSWORD','demo12345'),'buyer@netdosuga.local']+[cookie.value for cookie in jar]+[ticket['code'] for ticket in owned_order['order']['tickets']]
    status,_,_=request(session,BASE+'/api/v1/auth/logout',{},method='POST');assert status==204,status
    status,_,_=request(session,BASE+'/api/v1/shop/cart');assert status==401,status;check('logout_revokes_session')
    payment=os.getenv('PAYMENT_URL','http://payment-mock:8085');oid='mvp-'+str(uuid.uuid4());key={'Idempotency-Key':'payment:'+oid}
    body={'order_id':oid,'amount_cents':1234,'currency':'RUB','scenario':'error'}
    status,_,_=request(anonymous,payment+'/v1/payments',body,key);assert status==503,status
    body['scenario']='timeout'
    try:request(anonymous,payment+'/v1/payments',body,key,timeout=.2);raise AssertionError('timeout scenario completed unexpectedly')
    except TimeoutError:pass
    body['scenario']='success';status,paid,_=request(anonymous,payment+'/v1/payments',body,key);assert status==200,paid
    status,repeated,_=request(anonymous,payment+'/v1/payments',body,key);assert status==200 and repeated==paid
    body['amount_cents']=1235;status,_,_=request(anonymous,payment+'/v1/payments',body,key);assert status==409,status
    body['amount_cents']=1234;body['order_id']=oid+'-other';status,_,_=request(anonymous,payment+'/v1/payments',body,key);assert status==409,status
    refund={'order_id':oid,'amount_cents':1234,'scenario':'success'};rkey={'Idempotency-Key':'refund:'+oid}
    status,refunded,_=request(anonymous,payment+'/v1/refunds',refund,rkey);assert status==200,refunded
    status,repeated,_=request(anonymous,payment+'/v1/refunds',refund,rkey);assert status==200 and repeated==refunded
    check('payment_error_timeout_retry_and_durable_keys_refund_once')
    deadline=time.monotonic()+45;traces=[]
    while time.monotonic()<deadline:
        status,data,_=request(anonymous,'http://jaeger:16686/api/traces?service=payment-mock&limit=100')
        traces=data.get('data',[])
        if any({'validate','idempotency.lookup','provider.simulate','persist'}.issubset({s['operationName'] for s in tr['spans']}) for tr in traces):break
        time.sleep(1)
    matched=[tr for tr in traces if {'validate','idempotency.lookup','provider.simulate','persist'}.issubset({s['operationName'] for s in tr['spans']})]
    assert matched,'payment internal spans absent'
    trace=matched[0];operations=[s for s in trace['spans'] if s['operationName']=='operation.payment'];assert operations
    parent=operations[0]['spanID'];children=[s for s in trace['spans'] if any(r['spanID']==parent for r in s.get('references',[]))]
    assert {'validate','idempotency.lookup','provider.simulate','persist'}.issubset({s['operationName'] for s in children})
    assert any(t['key']=='app.request' for s in operations for t in s.get('tags',[]))
    check('real_jaeger_trace_has_sanitized_payload_and_nested_steps')
    # Follow the exact successful purchase from the preceding sequential business suite.
    # Prove real descendants inside orders-service, rather than merely counting spans.
    purchase_trace_id=business_report['payment_trace_id']
    required={'authentication.resolve','db.SELECT','payment.authorize','transaction.commit','outbox.enqueue'}
    def missing_purchase_evidence(candidate):
        service=lambda span:candidate['processes'][span['processID']]['serviceName']
        roots=[span for span in candidate['spans'] if span['operationName']=='operation.orders.pay' and service(span)=='orders-service']
        if len(roots)!=1:return ['unique orders-service operation.orders.pay']
        operation=roots[0];descendant_ids={operation['spanID']}
        while True:
            added={span['spanID'] for span in candidate['spans'] if any(ref['spanID'] in descendant_ids for ref in span.get('references',[]))}-descendant_ids
            if not added:break
            descendant_ids.update(added)
        descendants=[span for span in candidate['spans'] if span['spanID'] in descendant_ids and span['spanID']!=operation['spanID']]
        missing=sorted(required-{span['operationName'] for span in descendants if service(span)=='orders-service'})
        if not any(span['operationName']=='session.lookup' and service(span)=='identity-service' for span in descendants):missing.append('identity-service session.lookup descendant')
        tags={tag['key'] for tag in operation.get('tags',[])}
        missing.extend(sorted({'app.request','app.response'}-tags))
        return missing
    deadline=time.monotonic()+45;purchase=None;missing=['trace not visible']
    while time.monotonic()<deadline:
        status,data,_=request(anonymous,'http://jaeger:16686/api/traces/'+purchase_trace_id)
        candidates=data.get('data',[])
        if status==200 and candidates:
            candidate=candidates[0];missing=missing_purchase_evidence(candidate)
            if not missing:purchase=candidate;break
        time.sleep(1)
    assert purchase,'purchase trace incomplete after 45s; missing: '+', '.join(missing)
    roots=[span for span in purchase['spans'] if span['operationName']=='operation.orders.pay' and purchase['processes'][span['processID']]['serviceName']=='orders-service']
    assert len(roots)==1,'ambiguous purchase operation span'
    operation=roots[0];descendant_ids={operation['spanID']}
    while True:
        added={span['spanID'] for span in purchase['spans'] if any(ref['spanID'] in descendant_ids for ref in span.get('references',[]))}-descendant_ids
        if not added:break
        descendant_ids.update(added)
    descendants=[span for span in purchase['spans'] if span['spanID'] in descendant_ids and span['spanID']!=operation['spanID']]
    internal=[span for span in descendants if purchase['processes'][span['processID']]['serviceName']=='orders-service']
    assert required.issubset({span['operationName'] for span in internal}),'orders steps are not nested under purchase operation'
    assert any(span['operationName']=='session.lookup' and purchase['processes'][span['processID']]['serviceName']=='identity-service' for span in descendants),'identity session resolution is not in the purchase trace'
    attributes={tag['key']:tag['value'] for tag in operation.get('tags',[])}
    assert 'app.request' in attributes and 'app.response' in attributes,'orders payload attributes missing'
    request_payload=json.loads(attributes['app.request']);response_payload=json.loads(attributes['app.response'])
    assert request_payload.get('operation')=='orders.pay' and response_payload.get('order',{}).get('status')=='PAID','orders diagnostic payload does not describe the successful payment'
    trace_json=json.dumps(purchase)
    assert all(value not in trace_json for value in sensitive_values if value),'sensitive value leaked into actual purchase trace'
    forbidden={'password','authorization','cookie','session_token','token','email','name','code'}
    def assert_clean(value):
        if isinstance(value,dict):
            assert forbidden.isdisjoint(value),'sensitive diagnostic field leaked'
            for child in value.values():assert_clean(child)
        elif isinstance(value,list):
            for child in value:assert_clean(child)
    assert_clean(request_payload);assert_clean(response_payload)
    check('orders_purchase_trace_contains_nested_auth_sql_payment_commit_outbox_and_no_secrets')
    REPORT['orders_payment_trace_id']=purchase_trace_id
    REPORT['payment_order_id']=oid;REPORT['trace_id']=trace['traceID'];REPORT['passed']=True
except Exception as e:REPORT['failure']=str(e)
finally:
    Path('/results').mkdir(exist_ok=True);Path('/results/mvp.json').write_text(json.dumps(REPORT,indent=2))
print(json.dumps(REPORT));raise SystemExit(0 if REPORT['passed'] else 1)
