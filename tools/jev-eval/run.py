#!/usr/bin/env python3
"""Fixed-scope evaluation runner. Default: offline validation only."""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import tempfile
import time
import urllib.request
from prepare_review import MODEL, PREFIX, prepare
from validate import FIXTURE, BOUNDARY, REGISTRY


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def validate_plan(plan):
    with tempfile.TemporaryDirectory() as folder:
        expected = prepare(Path(folder) / 'packet')
    if plan != expected:
        raise ValueError('plan differs from current frozen preparation')


def execute(plan, key, output, opener=None):
    validate_plan(plan)
    if not key or len(key) > 4096 or any(c in key for c in '\r\n'):
        raise ValueError('invalid evaluation key')
    registry = json.loads(REGISTRY.read_text())
    questions = {c['id']: {'type': 'noul', 'instructions': PREFIX + c['rule']} for c in registry}
    rows = [json.loads(line) for p in (FIXTURE, BOUNDARY) for line in p.read_text().splitlines() if line.strip()]
    if len(rows) != plan['proposed_requests'] or len(rows) > 108:
        raise ValueError('call limit mismatch')
    payloads = [json.dumps({'model': MODEL, 'state': r['text'], 'questions': questions},
                          ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode() for r in rows]
    if [hashlib.sha256(b).hexdigest() for b in payloads] != plan['request_sha256']:
        raise ValueError('request hash mismatch')
    # Create exclusively before any call; never overwrite an earlier run.
    with output.open('x') as f:
        result = dict(origin='provider' if opener is None else 'mock', model=MODEL,
                      registry_sha256=plan['suite']['datasets'][0]['registry_sha256'],
                      fixture_sha256=[d['fixture_sha256'] for d in plan['suite']['datasets']],
                      runner_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                      attempted_requests=0, usage_complete=False, results=[])
        def persist():
            f.seek(0)
            json.dump(result, f, indent=2, allow_nan=False)
            f.truncate()
            f.flush()
            os.fsync(f.fileno())
        persist()
        transport = opener or urllib.request.build_opener(NoRedirect())
        for row, body in zip(rows, payloads):
            observation = dict(sample_id=row['id'], status='failed', reason='request_failed', input_tokens=None)
            result['attempted_requests'] += 1
            # Persist the attempted slot first; a crash cannot authorize replay.
            result['results'].append(observation)
            persist()
            started = time.monotonic()
            try:
                request = urllib.request.Request(plan['endpoint'], data=body, method='POST',
                    headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
                with transport.open(request, timeout=15) as response:
                    if response.status != 200:
                        raise ValueError('status')
                    raw = response.read(65537)
                    if len(raw) > 65536:
                        raise ValueError('size')
                decoded = json.loads(raw)
                if decoded.get('model') != MODEL:
                    raise ValueError('model')
                scores = {}
                for category in questions:
                    answer = decoded['answers'][category]
                    score = answer['noul']
                    if (answer['type'] != 'noul' or isinstance(score, bool) or
                        not isinstance(score, (int, float)) or not math.isfinite(score) or not 0 <= score <= 1):
                        raise ValueError('score')
                    scores[category] = score
                observation.update(status='ok', reason='', scores=scores)
                usage = decoded.get('usage')
                token_count = usage.get('input_tokens') if isinstance(usage, dict) else None
                if type(token_count) is int and token_count >= 0:
                    observation['input_tokens'] = token_count
            except Exception:
                # No URL, credentials, prompt, provider body or exception text in artifacts.
                pass
            observation['elapsed_ms'] = round((time.monotonic() - started) * 1000, 3)
            persist()
        result['usage_complete'] = all(r['input_tokens'] is not None for r in result['results'])
        persist()
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plan', type=Path, required=True)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--execute', action='store_true', help='requires specific operator authorization outside this tool')
    args = parser.parse_args()
    try:
        plan = json.loads(args.plan.read_text())
        validate_plan(plan)
        if not args.execute:
            print('Offline plan validated. No key read and no requests sent.')
        else:
            if args.output is None:
                raise ValueError('output required')
            execute(plan, os.environ.get('JEV_EVAL_API_KEY', ''), args.output)
            print('Run finished; inspect result failures and usage completeness.')
    except (OSError, ValueError, TypeError, KeyError):
        parser.exit(2, 'Evaluation could not proceed; no sensitive error details emitted.\n')
