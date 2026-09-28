#!/usr/bin/env python3
"""Sequential, bounded evaluation of the gateway's /v1/chat contract."""
import argparse
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None  # Never forward the caller token to another endpoint.


def load_cases(path):
    cases = json.loads(Path(path).read_text())
    if not isinstance(cases, list) or not cases:
        raise ValueError('cases must be a nonempty JSON array')
    seen = set()
    for case in cases:
        if not isinstance(case, dict) or set(case) != {'id', 'prompt', 'check'}:
            raise ValueError('each case requires only id, prompt, check')
        if not isinstance(case['id'], str) or not case['id'] or case['id'] in seen:
            raise ValueError('case IDs must be nonempty and unique')
        seen.add(case['id'])
        if not isinstance(case['prompt'], str) or not case['prompt'].strip():
            raise ValueError('prompt must be a nonempty string')
        check = case['check']
        if not isinstance(check, dict) or len(check) != 1:
            raise ValueError('check must contain one criterion')
        kind, expected = next(iter(check.items()))
        if kind not in {'exact', 'contains', 'json_equal'}:
            raise ValueError('unsupported criterion: ' + kind)
        if kind in {'exact', 'contains'} and (not isinstance(expected, str) or not expected):
            raise ValueError('text criterion must be a nonempty string')
    return cases


def request(base, path, token, payload, timeout):
    headers = {'Accept': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    data = None
    if payload is not None:
        headers['Content-Type'] = 'application/json'
        data = json.dumps(payload).encode()
    req = urllib.request.Request(base + path, data=data, headers=headers)
    opener = urllib.request.build_opener(NoRedirect)
    try:
        response = opener.open(req, timeout=timeout)
    except urllib.error.HTTPError as exc:
        response = exc
    with response:
        body = response.read(1048577)
        if len(body) > 1048576:
            raise ValueError('response exceeds 1 MiB')
        return response.status, body


def score(body, case, model):
    if not isinstance(body, dict) or body.get('model') != model:
        raise ValueError('response model does not match requested model')
    choices = body.get('choices')
    if not isinstance(choices, list) or len(choices) != 1:
        raise ValueError('expected exactly one choice')
    choice = choices[0]
    if not isinstance(choice, dict) or choice.get('finish_reason') != 'stop':
        raise ValueError('completion did not finish normally')
    message = choice.get('message')
    if not isinstance(message, dict) or message.get('role') != 'assistant':
        raise ValueError('expected an assistant message')
    content = message.get('content')
    if not isinstance(content, str) or not content.strip():
        raise ValueError('missing assistant text')
    usage = body.get('usage')
    if not isinstance(usage, dict):
        raise ValueError('missing token usage')
    values = [usage.get(key) for key in ('prompt_tokens', 'completion_tokens', 'total_tokens')]
    if any(type(value) is not int or value < 0 for value in values) or sum(values[:2]) != values[2]:
        raise ValueError('invalid token usage')
    kind, expected = next(iter(case['check'].items()))
    if kind == 'exact':
        passed = content.strip() == expected
    elif kind == 'contains':
        passed = expected.casefold() in content.casefold()
    else:
        try:
            passed = json.dumps(json.loads(content), sort_keys=True, allow_nan=False) == json.dumps(expected, sort_keys=True, allow_nan=False)
        except ValueError:
            passed = False
    return passed, usage


def evaluate(base, token, model, cases, timeout, max_tokens, max_latency_ms):
    results = []
    # Readiness and missing-token refusal do not consume provider tokens.
    for name, path, auth, payload, expected in [
        ('ready', '/readyz', None, None, 200),
        ('reject_missing_token', '/v1/chat', None,
         {'model': model, 'max_tokens': 1, 'messages': [{'role': 'user', 'content': 'Hi'}]}, 401),
    ]:
        status, _ = request(base, path, auth, payload, timeout)
        if status != expected:
            raise ValueError(f'preflight {name}: HTTP {status}, expected {expected}')
    for case in cases:
        started = time.monotonic()
        row = {'id': case['id'], 'passed': False}
        try:
            status, raw = request(base, '/v1/chat', token, {
                'model': model, 'max_tokens': max_tokens, 'temperature': 0,
                'messages': [{'role': 'user', 'content': case['prompt']}],
            }, timeout)
            row['status'] = status
            if status != 200:
                row['failure'] = f'HTTP {status}'
            else:
                passed, usage = score(json.loads(raw), case, model)
                row.update(passed=passed, usage=usage)
                if not passed:
                    row['failure'] = 'answer did not satisfy criterion'
        except (ValueError, OSError, urllib.error.URLError) as exc:
            # Do not persist provider response bodies or credentials.
            row['failure'] = 'request or response error: ' + type(exc).__name__
        row['latency_ms'] = round((time.monotonic() - started) * 1000, 2)
        if row['latency_ms'] > max_latency_ms:
            row.update(passed=False, failure='latency threshold exceeded')
        results.append(row)
        print(f"{case['id']}: {'PASS' if row['passed'] else 'FAIL'} ({row['latency_ms']} ms)")
    latencies = sorted(row['latency_ms'] for row in results)
    return {'model': model, 'cases': results, 'summary': {
        'total': len(results), 'passed': sum(row['passed'] for row in results),
        'latency_p50_ms': statistics.median(latencies),
        'latency_p95_ms': latencies[math.ceil(.95 * len(latencies)) - 1],
        'total_tokens': sum(row.get('usage', {}).get('total_tokens', 0) for row in results),
    }}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', required=True, help='Gateway origin, e.g. http://127.0.0.1:8080')
    parser.add_argument('--model', required=True, help='Exact model in the configured single-provider route')
    parser.add_argument('--cases', default=str(Path(__file__).with_name('cases.json')))
    parser.add_argument('--output', default='evals/results/latest.json')
    parser.add_argument('--timeout', type=float, default=40)
    parser.add_argument('--max-tokens', type=int, default=128)
    parser.add_argument('--max-latency-ms', type=float, default=30000)
    args = parser.parse_args(argv)
    report = {'model': args.model, 'cases': [], 'summary': {'total': 0, 'passed': 0}}
    try:
        base = args.base_url.rstrip('/')
        url = urllib.parse.urlsplit(base)
        if url.scheme not in {'http', 'https'} or not url.hostname or url.path or url.query or url.fragment or url.username or url.password:
            raise ValueError('base URL must be an HTTP(S) origin without credentials')
        if url.scheme == 'http' and url.hostname not in {'127.0.0.1', 'localhost', '::1'}:
            raise ValueError('use HTTPS for remote gateways; HTTP is allowed only on loopback')
        if not args.model.strip() or not 1 <= args.max_tokens <= 4096:
            raise ValueError('model is required and max-tokens must be 1..4096')
        if any(not math.isfinite(value) or value <= 0 for value in (args.timeout, args.max_latency_ms)):
            raise ValueError('timeouts and latency thresholds must be finite and positive')
        cases = load_cases(args.cases)
        token = os.environ.get('GATEWAY_TOKEN', '').strip()
        if not token:
            raise ValueError('set GATEWAY_TOKEN to a caller ServiceAccount token')
        report = evaluate(base, token, args.model, cases, args.timeout, args.max_tokens, args.max_latency_ms)
        report['run'] = {
            'finished_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'target': base, 'cases_sha256': hashlib.sha256(Path(args.cases).read_bytes()).hexdigest(),
            'timeout_seconds': args.timeout, 'max_tokens': args.max_tokens,
            'max_latency_ms': args.max_latency_ms,
        }
        code = 0 if report['summary']['passed'] == len(cases) else 1
    except (ValueError, OSError, urllib.error.URLError) as exc:
        report['error'] = str(exc) if isinstance(exc, ValueError) else type(exc).__name__
        print('Evaluation could not complete: ' + report['error'], file=sys.stderr)
        code = 2
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + '\n')
    return code


if __name__ == '__main__':
    sys.exit(main())
