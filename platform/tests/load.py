"""Bounded, read-only local catalog load. Python standard library only."""
import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor, wait, FIRST_COMPLETED
import json
import http.client
import math
import os
from pathlib import Path
import re
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def arguments(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, epilog='Rate is total scheduled RPS, shared by all workers. Saturated slots skip ticks; no catch-up bursts. Elapsed includes draining requests. This local demonstration does not establish an SLA.')
    parser.add_argument('--duration', type=float, default=15, help='Scheduling seconds (0 < value <= 120; default 15)')
    parser.add_argument('--concurrency', type=int, default=4, help='Maximum in-flight requests (1..32)')
    parser.add_argument('--rate', type=float, default=10, help='Total scheduling rate, requests/second (0 < value <= 100)')
    parser.add_argument('--timeout', type=float, default=3, help='Socket timeout seconds (0 < value <= 10); response body capped at 1 MiB')
    args = parser.parse_args(argv)
    for name, maximum in [('duration', 120), ('rate', 100), ('timeout', 10), ('concurrency', 32)]:
        value = getattr(args, name)
        if not math.isfinite(value) or not 0 < value <= maximum:
            parser.error(f'--{name} must be finite and > 0 and <= {maximum}')
    if math.ceil(args.duration * args.rate) > 10000:
        parser.error('duration * rate must not exceed 10000 scheduled requests')
    base = os.environ.get('GATEWAY_URL', 'http://gateway:8080')
    try:
        url = urllib.parse.urlsplit(base)
        valid = (url.scheme == 'http' and url.hostname in {'gateway', 'localhost', '127.0.0.1', '::1'}
                 and not url.username and not url.password and not url.query and not url.fragment
                 and url.path in {'', '/'} and url.port is not None)
    except ValueError:
        valid = False
    if not valid:
        parser.error('GATEWAY_URL must be an http origin with explicit port on gateway or loopback, without credentials/path/query')
    args.url = base.rstrip('/') + '/api/v1/shop/catalog'
    return args


def latency_stats(samples):
    values = sorted(samples)
    if not values:
        return dict.fromkeys(['avg', 'p50', 'p95', 'p99', 'max'], None)
    result = {'avg': sum(values) / len(values), 'max': values[-1]}
    for percentile in (50, 95, 99):
        result[f'p{percentile}'] = values[math.ceil(len(values) * percentile / 100) - 1]
    return {key: round(value * 1000, 3) for key, value in result.items()}


def request(url, timeout):
    start = time.monotonic()
    status, headers, body, error = None, {}, b'', None
    # No environment proxy or redirects: the validated local target is the only destination.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        try:
            response = opener.open(url, timeout=timeout)
        except urllib.error.HTTPError as failure:
            response = failure
        with response:
            status = response.code
            headers = response.headers
            body = response.read(1048577)
            if len(body) > 1048576:
                error = 'response_too_large'
            elif status != 200:
                error = 'unexpected_http_status'
    except (OSError, ValueError, http.client.HTTPException) as failure:
        error = type(failure).__name__
    trace = headers.get('X-Trace-Id', '')
    if not re.fullmatch('[0-9a-fA-F]{32}', trace):
        try:
            trace = json.loads(body).get('trace_id', '')
        except (ValueError, AttributeError):
            trace = ''
    return {'seconds': time.monotonic() - start, 'status': status, 'error': error,
            'trace': trace if isinstance(trace, str) and re.fullmatch('[0-9a-fA-F]{32}', trace) else '',
            'gateway': headers.get('X-Gateway-Instance'), 'order': headers.get('X-Order-Instance')}


def run(args):
    report = {'started_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
              'scenario': 'GET /api/v1/shop/catalog', 'configuration': vars(args),
              'notes': ['Read-only local demonstration, not an SLA.', 'Rate is globally scheduled RPS; saturated ticks are skipped, without catch-up.',
                        'Elapsed includes draining in-flight requests; preflight is excluded from load metrics.',
                        'Timeout is a socket timeout, not a strict total deadline; response reads are capped at 1 MiB.'],
              'interrupted': False, 'trace_samples': []}
    statuses, errors, gateways, orders = Counter(), Counter(), Counter(), Counter()
    samples, pending = [], set()
    skipped = 0
    started = None
    next_progress = None
    def progress():
        nonlocal next_progress
        now = time.monotonic()
        if now >= next_progress:
            print(f'elapsed={now-started:.1f}s completed={len(samples)} in_flight={len(pending)} errors={sum(errors.values())} skipped={skipped}', flush=True)
            next_progress = now + 5
    def pause_until(deadline):
        while time.monotonic() < deadline:
            time.sleep(max(0, min(deadline - time.monotonic(), next_progress - time.monotonic())))
            progress()
    def record(result):
        samples.append(result['seconds'])
        if result['status'] is not None:
            statuses[str(result['status'])] += 1
        if result['error']:
            errors[result['error']] += 1
        for counter, key in [(gateways, 'gateway'), (orders, 'order')]:
            if result[key]:
                counter[result[key][:128]] += 1
        trace = result['trace']
        if trace and len(report['trace_samples']) < 8 and not any(s['trace_id'] == trace for s in report['trace_samples']):
            report['trace_samples'].append({'trace_id': trace, 'jaeger_url': 'http://localhost:16686/trace/' + trace})
    try:
        preflight = request(args.url, args.timeout)
        report['preflight'] = {key: preflight[key] for key in ('status', 'error', 'seconds')}
        if preflight['error']:
            report['failure'] = 'Catalog preflight failed; no load scheduled'
            print(f'Catalog preflight failed: status={preflight["status"]} error={preflight["error"]}; no load scheduled', flush=True)
            return 1
        started = time.monotonic()
        next_progress = started + 5
        with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            try:
                for tick in range(math.ceil(args.duration * args.rate)):
                    due = started + tick / args.rate
                    pause_until(due)
                    now = time.monotonic()
                    done = {future for future in pending if future.done()}
                    for future in done:
                        record(future.result())
                    pending -= done
                    if now >= started + args.duration:
                        break
                    if now - due >= 1 / args.rate:
                        skipped += 1
                        continue
                    if len(pending) < args.concurrency:
                        pending.add(pool.submit(request, args.url, args.timeout))
                    else:
                        skipped += 1
                    progress()
                pause_until(started + args.duration)
            except KeyboardInterrupt:
                report['interrupted'] = True
            while pending:
                done, pending = wait(pending, timeout=1, return_when=FIRST_COMPLETED)
                for future in done:
                    record(future.result())
                progress()
        return 130 if report['interrupted'] else (1 if errors else 0)
    except KeyboardInterrupt:
        report['interrupted'] = True
        return 130
    except Exception as failure:
        report['failure'] = type(failure).__name__
        print(f'Load failed: {report["failure"]}', flush=True)
        return 1
    finally:
        elapsed = time.monotonic() - started if started is not None else 0
        report.update(requests=len(samples), success=len(samples)-sum(errors.values()), errors=dict(errors),
                      http_statuses=dict(statuses), skipped_ticks=skipped, elapsed_including_drain_seconds=round(elapsed, 3),
                      achieved_rps=round(len(samples)/elapsed, 3) if elapsed else 0,
                      latency_ms=latency_stats(samples), gateway_instances=dict(gateways), order_instances=dict(orders))
        destination = Path('/results')
        destination.mkdir(parents=True, exist_ok=True)
        path = destination / ('load-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '-' + uuid.uuid4().hex[:12] + '.json')
        path.write_text(json.dumps(report, indent=2, allow_nan=False) + '\n', encoding='utf-8')
        print(f'Report: {path}\nrequests={report["requests"]} success={report["success"]} errors={sum(errors.values())} achieved_rps={report["achieved_rps"]} latency_ms={report["latency_ms"]}', flush=True)
        print('Observe: http://localhost:3000 (Grafana), http://localhost:16686 (Jaeger). Trace links, when returned, are in the report.', flush=True)


if __name__ == '__main__':
    raise SystemExit(run(arguments()))
