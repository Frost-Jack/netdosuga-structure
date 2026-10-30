"""Network smoke checks. Only Python standard library; all checks fail closed."""
from contracts import validate
import base64
import json
import os
from pathlib import Path
import re
import time
import urllib.error
import urllib.parse
import urllib.request

REPORT = {'started_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'checks': {}, 'scenarios': {}}
SERVICES = {'api-gateway', 'orders-service', 'notification-worker', 'mail-mock'}

def request(url, body=None, auth=False, raw=False):
    headers = {}
    if body is not None:
        headers['Content-Type'] = 'application/json'
    if auth:
        token = base64.b64encode(('demo:' + os.environ['GRAFANA_ADMIN_PASSWORD']).encode()).decode()
        headers['Authorization'] = 'Basic ' + token
    req = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(), headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=8) as response:
            return response.status, response.read().decode() if raw else json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)

def eventually(name, check, timeout=60):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                REPORT['checks'][name] = {'passed': True, 'evidence': result}
                return result
        except (AssertionError, OSError, ValueError, KeyError) as error:
            last = str(error)
        time.sleep(1)
    raise AssertionError(f'{name}: no valid result within {timeout}s ({last})')

def prom(expression):
    status, value = request('http://prometheus:9090/api/v1/query?' + urllib.parse.urlencode({'query': expression}))
    assert status == 200 and value['status'] == 'success', value
    return value['data']['result']

def trace(trace_id, all_services=False, error=False, delay=False):
    status, value = request('http://jaeger:16686/api/traces/' + trace_id)
    if status != 200 or not value.get('data'):
        return None
    data = value['data'][0]
    names = {p['serviceName'] for p in data['processes'].values()}
    if all_services and not SERVICES.issubset(names):
        return None
    spans = data['spans']
    errors = [s['operationName'] for s in spans if any(t['key'] in ('error', 'otel.status_code') and t['value'] in (True, 'ERROR') for t in s.get('tags', []))]
    if error and not errors:
        return None
    maximum = max(s['duration'] for s in spans)
    payment_spans = [s for s in spans if data['processes'][s['processID']]['serviceName'] == 'mail-mock' and s['operationName'] == 'POST /mock/payments']
    if delay and not any(s['duration'] >= 200000 for s in payment_spans):
        return None
    return {'trace_id': trace_id, 'services': sorted(names), 'error_spans': errors, 'max_duration_us': maximum, 'span_count': len(spans)}

def run():
    gateway_contract = json.loads(Path('/contracts/gateway/openapi.yaml').read_text())
    mock_contract = json.loads(Path('/contracts/mock-openapi.json').read_text())
    assert Path('/contracts/gateway/order.proto').read_bytes() == Path('/contracts/order-service/order.proto').read_bytes(), 'Proto contracts differ'
    REPORT['checks']['proto_contract_equal'] = {'passed':True}
    for host, port in [('gateway',8080),('order-service',8081),('notification-service',8082),('integration-mock',8083)]:
        eventually('ready_' + host, lambda host=host, port=port: request(f'http://{host}:{port}/readyz',raw=True)[0] == 200)
    for scenario, expected in [('success',200),('error',502),('delay',200),('timeout',504),('invalid',400)]:
        start = time.monotonic()
        status, body = request('http://gateway:8080/api/v1/demo/orders', {'scenario':scenario})
        elapsed = time.monotonic() - start
        assert status == expected, (scenario, status, body)
        schema = gateway_contract['paths']['/api/v1/demo/orders']['post']['responses'][str(status)]['content']['application/json']['schema']
        validate(body,schema,gateway_contract)
        assert body['mock'] is True and re.fullmatch('[0-9a-f]{32}', body['trace_id']), body
        if expected == 200:
            assert set(body) == {'mock','order_id','status','event_id','trace_id'}, body
            assert body['order_id'] == 'demo-order-001' and body['status'] == 'DEMO_ACCEPTED', body
            assert re.fullmatch('[0-9a-fA-F-]{36}',body['event_id']), body
        else:
            assert set(body) == {'mock','code','message','trace_id'} and isinstance(body['code'],str) and body['message'], body
        if scenario == 'delay':
            assert elapsed >= .2, elapsed
        if scenario == 'timeout':
            assert .8 <= elapsed < 4, elapsed
        REPORT['scenarios'][scenario] = {'status':status, 'elapsed_seconds':round(elapsed,3), 'body':body}
        REPORT['checks']['http_' + scenario] = {'passed':True}
    for scenario in ('success','delay'):
        body = REPORT['scenarios'][scenario]['body']
        def delivered(body=body):
            status, result = request('http://integration-mock:8083/mock/deliveries')
            assert status == 200 and result['mock'] is True
            validate(result, mock_contract['components']['schemas']['Deliveries'], mock_contract)
            matches = [d for d in result['deliveries'] if d['event_id'] == body['event_id']]
            assert matches and all(d['order_id'] == body['order_id'] and d['trace_id'] == body['trace_id'] for d in matches), matches
            return matches
        eventually('kafka_to_email_' + scenario, delivered)
        eventually('trace_' + scenario, lambda body=body, scenario=scenario: trace(body['trace_id'],all_services=True,delay=scenario=='delay'))
    for scenario in ('error','timeout'):
        eventually('trace_' + scenario, lambda scenario=scenario: trace(REPORT['scenarios'][scenario]['body']['trace_id'],error=True))
    def targets():
        status, result = request('http://prometheus:9090/api/v1/targets')
        active = result['data']['activeTargets']
        assert status == 200 and len(active) == int(os.environ.get('EXPECTED_PROM_TARGETS', '6')) and all(t['health']=='up' for t in active), active
        return [{'target':t['scrapeUrl'],'health':t['health']} for t in active]
    eventually('prometheus_targets', targets)
    expressions = ['netdosuga_http_requests_total','netdosuga_http_request_duration_seconds_count','netdosuga_grpc_requests_total','netdosuga_grpc_request_duration_seconds_count','netdosuga_events_total']
    for expression in expressions:
        eventually('metric_' + expression, lambda expression=expression: [r for r in prom(expression) if float(r['value'][1]) > 0])
    for status in ('502', '504'):
        expression = 'netdosuga_http_requests_total{service="api-gateway",status="' + status + '"}'
        eventually('metric_gateway_error_' + status, lambda expression=expression: [r for r in prom(expression) if float(r['value'][1]) > 0])
    for code in ('Unavailable', 'DeadlineExceeded'):
        expression = 'netdosuga_grpc_requests_total{service="orders-service",code="' + code + '"}'
        eventually('metric_grpc_error_' + code, lambda expression=expression: [r for r in prom(expression) if float(r['value'][1]) > 0])
    def grafana():
        status, source = request('http://grafana:3000/api/datasources/uid/prometheus',auth=True)
        assert status == 200 and source['url'] == 'http://prometheus:9090' and source['readOnly'] is True, source
        status, dashboard = request('http://grafana:3000/api/dashboards/uid/netdosuga-demo',auth=True)
        assert status == 200 and dashboard['meta']['provisioned'] is True, dashboard
        return {'datasource_uid': source['uid'], 'dashboard_uid':dashboard['dashboard']['uid'], 'panels':dashboard['dashboard']['panels']}
    result = eventually('grafana_provisioning',grafana)
    for panel in result['panels']:
        expression = panel['targets'][0]['expr']
        def query_panel(expression=expression):
            # Generate traffic between scrapes so rate/histogram queries have deltas.
            status, _ = request('http://gateway:8080/api/v1/demo/orders', {'scenario':'success'})
            assert status == 200
            if 'netdosuga_operation' in expression:
                import uuid
                oid='smoke-'+str(uuid.uuid4())
                for scenario in ('error','success'):
                    req=urllib.request.Request('http://payment-mock:8085/v1/payments',data=json.dumps({'order_id':oid,'amount_cents':100,'currency':'RUB','scenario':scenario}).encode(),headers={'Content-Type':'application/json','Idempotency-Key':'payment:'+oid})
                    try:
                        with urllib.request.urlopen(req,timeout=8) as response: response.read()
                    except urllib.error.HTTPError as error:
                        assert error.code==503
            status, result = request('http://grafana:3000/api/datasources/proxy/uid/prometheus/api/v1/query?' + urllib.parse.urlencode({'query': expression}), auth=True)
            assert status == 200 and result['status'] == 'success', result
            series = result['data']['result']
            assert series and any(r['value'][1] not in ('NaN','+Inf','-Inf') for r in series), series
            return {'expression':expression,'series':series}
        eventually('dashboard_panel_' + str(panel['id']),query_panel,90)
    result.pop('panels')

try:
    run()
    REPORT['passed'] = True
except Exception as error:
    REPORT['passed'] = False
    REPORT['failure'] = str(error)
finally:
    Path('/results').mkdir(exist_ok=True)
    Path('/results/smoke.json').write_text(json.dumps(REPORT,indent=2))
print(json.dumps({'passed':REPORT['passed'],'checks':len(REPORT['checks']),'report':'platform/test-results/smoke.json','failure':REPORT.get('failure')}))
raise SystemExit(0 if REPORT['passed'] else 1)
