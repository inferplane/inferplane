"""Publish allow-listed repeat/CLI measurements. This command never invokes models."""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
from statistics import median

ROOT = Path(__file__).resolve().parents[1]
DATA = ROOT / 'docs/assets/data/client-compatibility.json'
API_FIELDS = ('id', 'model_id', 'phase', 'group', 'region', 'transport', 'input_style',
              'context_window', 'max_output_tokens', 'success', 'status', 'stop_reason',
              'terminal', 'final_usage', 'retrieval_correct', 'elapsed_ms', 'ttft_ms',
              'observed_input_tokens', 'context_occupancy', 'billed_output_tokens_per_second',
              'estimated_cost_micro_usd', 'state', 'campaign_workers', 'concurrency')
CLI_FIELDS = ('run_id', 'client', 'model_id', 'variant', 'task', 'iteration', 'success',
              'exit_code', 'timeout', 'elapsed_ms', 'client_context_windows', 'tool_names',
              'tests_pass', 'marker_found', 'request_ids', 'invoked_requests',
              'known_cost_micro_usd', 'unknown_cost_requests', 'http_statuses',
              'input_units', 'declared_window', 'input_tokens', 'occupancy', 'gateway_build')
REQUEST_FIELDS = ('id', 'model_id', 'path', 'body_bytes', 'output_limit', 'invoked',
                  'http_status', 'elapsed_ms', 'ttft_ms', 'terminal', 'final_usage',
                  'estimated_cost_micro_usd', 'rejected_before_inference', 'started_at')
FAILURES = {
    'context_limit': ('Context limit', '컨텍스트 한도'),
    'content_filtered': ('Filtered', '필터 거절'),
    'conversation_blocked': ('Conversation blocked', '대화 거절'),
    'output_limit': ('Output cap', '출력 한도'),
    'unavailable': ('Access unavailable', '접근 불가'),
    'throttled': ('Throttled', '요청 제한'),
    'timeout': ('Timeout', '시간 초과'),
    'retrieval_mismatch': ('Retrieval mismatch', '회수 오답'),
    'empty_output': ('Empty output', '빈 응답'),
    'incomplete_stream': ('Incomplete stream', '스트림 미완료'),
    'upstream_error': ('Upstream error', '업스트림 오류'),
    'gateway_incompatible': ('Gateway incompatibility', '게이트웨이 호환성'),
    'guard_refused': ('Test guard', '시험 가드'),
    'tool_task_failed': ('Coding task failed', '코딩 과제 실패'),
    'not_invoked': ('Not invoked', '호출하지 않음'),
    'client_input_limit': ('CLI text length limit', 'CLI 글자 수 제한'),
}

def pick(row, fields):
    return {key: row[key] for key in fields if key in row}


def usage(row):
    return pick(row.get('usage') or {}, ('input', 'output', 'read', 'write'))


def failure(row):
    if row.get('state') in ('budget_skipped', 'unsupported_transport'):
        return 'not_invoked'
    error = str(row.get('error') or '').lower()
    if 'conversation_blocked' in error:
        return 'conversation_blocked'
    if any(s in error for s in ('context window', 'context length', 'max_model_len', 'input is too long')):
        return 'context_limit'
    if row.get('status') in (401, 403, 404):
        return 'unavailable'
    if row.get('status') == 429 or 'throttl' in error:
        return 'throttled'
    if 'timeout' in error or 'timed out' in error or 'deadline' in error:
        return 'timeout'
    if error:
        return 'upstream_error'
    if row.get('stop_reason') == 'content_filtered':
        return 'content_filtered'
    if row.get('success'):
        return None
    if row.get('stop_reason') in ('max_tokens', 'length'):
        return 'output_limit'
    if row.get('output_characters') and row.get('retrieval_correct') is False:
        return 'retrieval_mismatch'
    if row.get('terminal') and not row.get('output_characters'):
        return 'empty_output'
    return 'incomplete_stream'


def project_api(row):
    result = pick(row, API_FIELDS)
    result['failure_class'] = failure(row)
    result['success'] = bool(row.get('success')) and result['failure_class'] is None
    result['invoked'] = result['failure_class'] != 'not_invoked'
    result['response_error'] = result['invoked'] and (row.get('status') != 200 or bool(row.get('error')) or not row.get('terminal'))
    result['usage'] = usage(row)
    return result


def project_probe(row):
    result = pick(row, ('client', 'model_id', 'variant', 'declared_window', 'success',
                       'reported_context_window', 'configured_context_window', 'configured_compact_limit'))
    if row['client'] == 'claude':
        result['reported_context_windows'] = sorted(set(
            item['contextWindow'] for item in (row.get('model_usage') or {}).values()
            if type(item.get('contextWindow')) is int))
    result['requests'] = [pick(r, ('path', 'model', 'beta', 'body_bytes', 'max_tokens', 'stream', 'tool_count'))
                          for r in row.get('requests', [])]
    return result


def project_request(row, directory):
    result = pick(row, REQUEST_FIELDS)
    result['usage'] = usage(row)
    if row['model_id'].startswith('global.'):
        result['requested_model_id'] = row['model_id']
        result['model_id'] = row['model_id'].removeprefix('global.')
    if not row['invoked']:
        result['failure_class'] = 'guard_refused'
    elif row.get('stop_reason') == 'content_filtered':
        result['failure_class'] = 'content_filtered'
    elif row.get('terminal') and row.get('final_usage') and row.get('http_status') == 200:
        result['failure_class'] = None
    else:
        response = directory / (row['id'] + '.response')
        body = response.read_text(errors='replace') if response.exists() else ''
        errors = []
        for line in body.splitlines():
            if not line.startswith('data:'):
                continue
            try:
                event = json.loads(line[5:])
            except ValueError:
                continue
            if event.get('type') in ('error', 'response.failed'):
                errors.append(event)
        text = json.dumps(errors).lower() if errors else (body.lower() if row.get('http_status') != 200 else '')
        if 'no policy-permitted compatible responses target' in text:
            result['failure_class'] = 'gateway_incompatible'
        else:
            result['failure_class'] = failure(dict(status=row.get('http_status'), error=text,
                                                   terminal=row.get('terminal'), stop_reason=row.get('stop_reason'), output_characters=row.get('text_characters', 0)))
    return result


def cli_failure(run, requests, directory):
    if not run['invoked_requests']:
        stderr = directory / 'stderr.txt'
        text = stderr.read_text(errors='replace') if stderr.exists() else ''
        if 'input_too_large' in text and 'max_chars' in text:
            for field in ('max_chars', 'actual_chars'):
                match = re.search(r'"' + field + r'"\s*:\s*(\d+)', text)
                if match:
                    run[field] = int(match[1])
            return 'client_input_limit'
        return 'guard_refused' if requests else 'not_invoked'
    if run['success']:
        return None
    if run.get('timeout'):
        return 'timeout'
    failures = [project_request(r, directory)['failure_class'] for r in requests]
    if any(f for f in failures if f != 'guard_refused'):
        return next(f for f in failures if f and f != 'guard_refused')
    if 'guard_refused' in failures:
        return 'guard_refused'
    return 'tool_task_failed' if run['task'] == 'tools' else 'retrieval_mismatch'


def project_compaction(row, requests, name):
    invoked = [r for r in requests if r['invoked']]
    inputs = [sum((r.get('usage') or {}).get(k, 0) for k in ('input', 'read', 'write'))
              if r.get('usage') else None for r in invoked]
    completed = any(e.get('method') == 'item/completed' and
                    e.get('params', {}).get('item', {}).get('type') == 'contextCompaction'
                    for e in row.get('compaction_events', []))
    return dict(run_id=name, model_id=row['model_id'].removeprefix('global.'), requested_model_id=row['model_id'], compact_limit=row['compact_limit'],
                gateway_build='local-compaction-fix' if 'local' in name else
                              ('compaction-fix' if 'fixed' in name else 'baseline-a24b6dd'),
                turn_statuses=[t['status'] for t in row['turns']], compaction_completed=completed,
                initial_input_tokens=inputs[0] if inputs else None,
                compaction_input_tokens=inputs[1] if len(inputs) > 1 else None,
                following_input_tokens=inputs[2] if len(inputs) > 2 else None,
                elapsed_ms=row['elapsed_ms'], request_ids=[r['id'] for r in requests],
                invoked_requests=len(invoked),
                known_cost_micro_usd=sum(r.get('estimated_cost_micro_usd') or 0 for r in invoked),
                unknown_cost_requests=sum(r.get('estimated_cost_micro_usd') is None for r in invoked))


def read_rows(path):
    rows = []
    for line in path.read_text().splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if isinstance(row, dict) and 'model_id' in row:
            rows.append(row)
    return rows


def import_data(directory):
    previous = json.loads((ROOT / 'docs/assets/data/performance.json').read_text())
    data = {'schema': 1, 'as_of': '2026-10-21', 'measurement_dates': ['2026-10-20', '2026-10-21'],
            'versions': {'claude_code': '2.1.304', 'codex': '0.163.0',
                         'gateway_commit': 'a24b6dd59a7971e7d29eecf433b142c4615afec5'},
            'record_types': {'api_records': 'direct_bedrock', 'offline_probes': 'local_fixture', 'cli_runs': 'live_cli_via_inferplane', 'compaction_runs': 'live_codex_two_turn_compaction'},
            'models': [pick(m, ('id', 'name', 'provider', 'region', 'context_window', 'rates', 'rate_source'))
                       for m in previous['models']],
            'api_records': [], 'offline_probes': [], 'cli_runs': [], 'cli_requests': [], 'compaction_runs': [], 'sources': []}
    def source(path):
        data['sources'].append({'path': str(path.relative_to(directory)),
                               'sha256': hashlib.sha256(path.read_bytes()).hexdigest()})
    for phase in ('repeat-short', 'repeat-near-full'):
        for path in sorted((directory / phase / 'records').glob('*.json')):
            row = json.loads(path.read_text())
            if row.get('state') == 'in_flight':
                raise ValueError('finish or explicitly reconcile in-flight API records before import')
            data['api_records'].append(project_api(row)); source(path)
    for name in ('claude-offline-all.jsonl', 'codex-offline-all.jsonl'):
        path = directory / name
        data['offline_probes'].extend(project_probe(row) for row in read_rows(path)); source(path)
    # Explicit manifest selects valid runs, including failed model tasks. Environment
    # failures stay in private evidence and are counted separately in the manifest.
    manifest_path = directory / 'publication-manifest.json'
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text()); source(manifest_path)
        data['environment_exclusions'] = manifest['environment_exclusions']
        data['gateway_builds'] = {
            'baseline-a24b6dd': {'commit': data['versions']['gateway_commit']},
            'compaction-fix': json.loads((directory / 'gateway/fixed-build.json').read_text()),
            'local-compaction-fix': pick(json.loads((directory / 'runtime-install.json').read_text()), ('binary_sha256',)),
        }
        for relative in manifest['runs']:
            path = directory / relative; row = json.loads(path.read_text()); source(path)
            run = pick(row, CLI_FIELDS)
            data['cli_runs'].append(run)
            requests_path = path.parent / 'requests.json'
            if not requests_path.exists():
                raise ValueError('missing request evidence for ' + relative)
            raw_requests = json.loads(requests_path.read_text())
            for req in raw_requests:
                data['cli_requests'].append(project_request(req, path.parent))
            run['guard_refusals'] = sum(not req['invoked'] for req in raw_requests)
            run['failure_class'] = cli_failure(run, raw_requests, path.parent)
            run.setdefault('gateway_build', 'baseline-a24b6dd')
            source(requests_path)
        for relative in manifest.get('compaction_runs', []):
            path = directory / relative
            raw = json.loads(path.read_text())
            requests_path = path.parent / 'requests.json'
            raw_requests = json.loads(requests_path.read_text())
            data['compaction_runs'].append(project_compaction(raw, raw_requests, path.parent.name))
            data['cli_requests'].extend(project_request(req, path.parent) for req in raw_requests)
            source(path); source(requests_path)
    return data


def validate(data):
    allowed = {
        'api_records': set(API_FIELDS) | {'failure_class', 'invoked', 'usage', 'response_error'},
        'cli_runs': set(CLI_FIELDS) | {'failure_class', 'guard_refusals', 'max_chars', 'actual_chars'},
        'cli_requests': set(REQUEST_FIELDS) | {'failure_class', 'usage', 'requested_model_id'},
        'offline_probes': {'client', 'model_id', 'variant', 'declared_window', 'success',
                           'reported_context_window', 'configured_context_window', 'configured_compact_limit',
                           'reported_context_windows', 'requests'},
        'compaction_runs': {'run_id', 'model_id', 'requested_model_id', 'compact_limit', 'gateway_build',
                            'turn_statuses', 'compaction_completed', 'initial_input_tokens',
                            'compaction_input_tokens', 'following_input_tokens', 'elapsed_ms',
                            'request_ids', 'invoked_requests', 'known_cost_micro_usd', 'unknown_cost_requests'},
    }
    for group, fields in allowed.items():
        for row in data.get(group, []):
            if set(row) - fields:
                raise ValueError('unexpected public fields in ' + group)
            for key, value in (row.get('usage') or {}).items():
                if key not in {'input', 'output', 'read', 'write'} or type(value) is not int or value < 0:
                    raise ValueError('invalid public usage')
            if row.get('failure_class') is not None and row['failure_class'] not in FAILURES:
                raise ValueError('unrecognized failure class')
            if group == 'offline_probes':
                for request in row.get('requests', []):
                    if set(request) - {'path', 'model', 'beta', 'body_bytes', 'max_tokens', 'stream', 'tool_count'}:
                        raise ValueError('unexpected fixture request fields')
    ids = {m['id'] for m in data['models']}
    if len(ids) != len(data['models']):
        raise ValueError('duplicate models')
    for group, key in (('api_records', 'id'), ('cli_runs', 'run_id'), ('cli_requests', 'id'), ('compaction_runs', 'run_id')):
        seen = set()
        for row in data.get(group, []):
            if row[key] in seen or row['model_id'] not in ids:
                raise ValueError('duplicate or unknown record')
            seen.add(row[key])
            for field in ('estimated_cost_micro_usd', 'known_cost_micro_usd'):
                value = row.get(field)
                if value is not None and (type(value) is not int or value < 0):
                    raise ValueError('cost must be nonnegative integer microUSD or null')
    seen_probes = set()
    for probe in data['offline_probes']:
        key = (probe['model_id'], probe['client'], probe['variant'])
        if key in seen_probes or probe['model_id'] not in ids:
            raise ValueError('duplicate or unknown offline probe')
        seen_probes.add(key)
    requests = {r['id']: r for r in data['cli_requests']}
    assigned_requests = set()
    for run in data['cli_runs'] + data.get('compaction_runs', []):
        if len(run['request_ids']) != len(set(run['request_ids'])):
            raise ValueError('duplicate request within run')
        if any(rid not in requests or rid in assigned_requests for rid in run['request_ids']):
            raise ValueError('missing or multiply assigned request')
        if any(requests[rid]['model_id'] != run['model_id'] for rid in run['request_ids']):
            raise ValueError('request model mismatch')
        assigned_requests.update(run['request_ids'])
        invoked = [requests[rid] for rid in run['request_ids'] if requests[rid]['invoked']]
        if len(invoked) != run['invoked_requests']:
            raise ValueError('request denominator mismatch')
        if sum(r.get('estimated_cost_micro_usd') or 0 for r in invoked) != run['known_cost_micro_usd']:
            raise ValueError('run cost mismatch')
        if sum(r.get('estimated_cost_micro_usd') is None for r in invoked) != run['unknown_cost_requests']:
            raise ValueError('unknown cost mismatch')
    if assigned_requests != set(requests):
        raise ValueError('unassigned request')


def stats(rows):
    rows = [r for r in rows if r.get('invoked', True)]
    good = [r for r in rows if r['success']]
    def med(field):
        values = [r[field] for r in good if r.get(field) is not None]
        return median(values) if values else None
    costs = [r.get('estimated_cost_micro_usd') for r in rows]
    return {'calls': len(rows), 'successes': len(good),
            'response_errors': sum(bool(r.get('response_error')) for r in rows),
            'failure_counts': dict(Counter(r['failure_class'] for r in rows if not r['success'])),
            'ttft': med('ttft_ms'), 'elapsed': med('elapsed_ms'),
            'tps': med('billed_output_tokens_per_second'),
            'known_cost': sum(c for c in costs if c is not None),
            'unknown': sum(c is None for c in costs),
            'occupancies': [r['context_occupancy'] for r in rows if r.get('context_occupancy') is not None]}


def money(value):
    return f'${value // 1000000:,}.{value % 1000000:06d}'


def number(value, scale=1):
    return '—' if value is None else f'{value / scale:,.3f}'


def table(headers, rows):
    return '\n'.join(['| ' + ' | '.join(headers) + ' |', '| ' + ' | '.join('---' for _ in headers) + ' |',
                      *['| ' + ' | '.join(map(str, row)) + ' |' for row in rows]])


def tables(data, ko=False):
    out = {}; language = int(ko)
    overview = []
    for phase in ('repeat-short', 'repeat-near-full'):
        measured = stats([r for r in data['api_records'] if r['phase'] == phase])
        label = {'repeat-short': ('Short input', '짧은 입력'), 'repeat-near-full': ('Near-full input', '창 근접 입력')}[phase][language]
        overview.append([label, str(measured['calls']), str(measured['successes']),
                         str(measured['response_errors']), money(measured['known_cost']), str(measured['unknown'])])
    request_map = {r['id']: r for r in data.get('cli_requests', [])}
    for task in ('smoke', 'tools', 'long_retrieval'):
        runs = [r for r in data['cli_runs'] if r['task'] == task]
        requests = [request_map[rid] for run in runs for rid in run['request_ids'] if request_map[rid]['invoked']]
        failures = sum(r.get('failure_class') is not None for r in requests)
        label = {'smoke': ('CLI smoke', 'CLI 기본 응답'), 'tools': ('CLI coding', 'CLI 코딩 과제'), 'long_retrieval': ('CLI long input', 'CLI 긴 입력')}[task][language]
        overview.append([label, str(len(runs)), str(sum(r['success'] for r in runs)),
                         f'{failures}/{len(requests)} ' + ('요청' if ko else 'requests'), money(sum(r['known_cost_micro_usd'] for r in runs)),
                         str(sum(r['unknown_cost_requests'] for r in runs))])
    out['overview'] = table(['시험' if ko else 'Campaign', '호출 / CLI 시도' if ko else 'Calls / CLI attempts',
                             '과제 성공' if ko else 'Task successes', '오류 요청' if ko else 'Error requests',
                             '확인된 추정 비용' if ko else 'Known estimated cost',
                             '비용 미확인 요청' if ko else 'Unknown-cost requests'], overview)
    for phase in ('repeat-short', 'repeat-near-full'):
        rows = []
        for model in data['models']:
            values = [r for r in data['api_records'] if r['model_id'] == model['id'] and r['phase'] == phase]
            s = stats(values); n = s['calls']
            breakdown = ', '.join(FAILURES.get(k, (k, k))[language] + f' ×{v}' for k,v in sorted(s['failure_counts'].items())) or '—'
            cost = money(s['known_cost']) + (f' + ? ({s["unknown"]})' if s['unknown'] else '') if n else '—'
            occ = s['occupancies']; occupancy = f'{min(occ):.2%}–{max(occ):.2%}' if occ else '—'
            row = [model['name'], f'{s["successes"]}/{n}' if n else ('호출하지 않음' if ko else 'Not invoked'),
                   f'{s["response_errors"]/n:.1%}' if n else '—',
                   f'{(n-s["successes"])/n:.1%}' if n else '—']
            if phase == 'repeat-near-full': row.append(occupancy)
            row += [number(s['ttft'],1000),number(s['elapsed'],1000),number(s['tps']),cost,breakdown]
            rows.append(row)
        heads = ['모델' if ko else 'Model','성공/호출' if ko else 'Success/calls','HTTP·스트림 오류율' if ko else 'HTTP/stream error rate','과제 실패율' if ko else 'Task failure rate']
        if phase == 'repeat-near-full': heads.append('관측 입력 점유율' if ko else 'Observed input occupancy')
        heads += ['TTFT p50 (s)','E2E p50 (s)','p50 billed tok/s','전체 호출 비용' if ko else 'All-call cost','실패 분류' if ko else 'Failures']
        out[phase] = table(heads,rows)
    probes = {(p['model_id'],p['client'],p['variant']):p for p in data['offline_probes']}
    rows = []
    for m in data['models']:
        cells = [m['name'],f'{m["context_window"]:,}']
        for client, variants in (('claude',('default','suffix_1m','explicit_window')),('codex',('default','explicit_window','launcher'))):
            for variant in variants:
                p = probes.get((m['id'],client,variant),{})
                v = p.get('reported_context_windows',[]) if client=='claude' else [p.get('reported_context_window')]
                cells.append(', '.join(f'{x:,}' for x in v if type(x)is int) or '—')
        rows.append(cells)
    out['windows'] = table(['모델' if ko else 'Model','서버 선언' if ko else 'Declared','Claude default','Claude [1m]','Claude MAX env','Codex default','Codex explicit','icodex'], rows)
    rows=[]
    for m in data['models']:
        cells=[m['name']]
        for client,task,variant in (('claude','smoke','default'),('claude','tools','default'),('codex','smoke','default'),('codex','tools','default')):
            runs=[r for r in data['cli_runs'] if (r['model_id'],r['client'],r['task'],r['variant'])==(m['id'],client,task,variant) and r['invoked_requests']]
            cells.append(f'{sum(r["success"] for r in runs)}/{len(runs)}' if runs else '—')
        rows.append(cells)
    out['cli'] = table(['모델' if ko else 'Model','Claude smoke','Claude tools','Codex smoke','Codex tools'],rows)
    rows=[]
    for r in data['cli_runs']:
        if r['task']!='long_retrieval':continue
        rows.append([next(m['name'] for m in data['models'] if m['id']==r['model_id']),r['client'],r['variant'],r['iteration'],', '.join(f'{n:,}' for n in r.get('input_tokens',[])) or '—',r['invoked_requests'],r.get('guard_refusals',0),number(r.get('elapsed_ms'),1000),'✓' if r['success'] else ('—' if not r['invoked_requests'] and r.get('failure_class') == 'not_invoked' else '✗') + ' (' + FAILURES.get(r.get('failure_class'), ('Unknown', '미분류'))[language] + ')',money(r['known_cost_micro_usd'])+(f' + ? ({r["unknown_cost_requests"]})' if r['unknown_cost_requests'] else '')])
    out['long-cli'] = table(['모델','CLI','설정','회차','입력 토큰','요청 수','가드 거절','CLI 경과 (s)','결과','추정 비용'] if ko else ['Model','CLI','Variant','Run','Input tokens','Requests','Guard refusals','CLI wall (s)','Success','Cost'],rows)
    rows = []
    for r in data.get('compaction_runs', []):
        rows.append([r['run_id'], r['gateway_build'], f"{r['compact_limit']:,}",
                     '—' if r['initial_input_tokens'] is None else f"{r['initial_input_tokens']:,}",
                     '—' if r['compaction_input_tokens'] is None else f"{r['compaction_input_tokens']:,}",
                     '—' if r['following_input_tokens'] is None else f"{r['following_input_tokens']:,}", '✓' if r['compaction_completed'] else '✗',
                     ' → '.join(r['turn_statuses']), money(r['known_cost_micro_usd']) +
                     (f' + ? ({r["unknown_cost_requests"]})' if r['unknown_cost_requests'] else '')])
    out['compaction'] = table(['시험','빌드','압축 기준','첫 입력','압축 입력','다음 입력','압축 완료','턴 상태','추정 비용'] if ko else ['Run', 'Build', 'Trigger', 'First input', 'Compaction input',
                              'Next input', 'Compacted', 'Turns', 'Cost'], rows)
    rows = []
    for m in data['models']:
        for client in ('claude', 'codex'):
            runs = [r for r in data['cli_runs'] if r['model_id'] == m['id'] and r['client'] == client
                    and r['task'] in ('smoke', 'tools') and r['invoked_requests']]
            if not runs:
                continue
            times = []
            for task in ('smoke', 'tools'):
                values = [r['elapsed_ms'] for r in runs if r['task'] == task and r['success']]
                times.append(number(median(values) if values else None, 1000))
            unknown = sum(r['unknown_cost_requests'] for r in runs)
            cost = money(sum(r['known_cost_micro_usd'] for r in runs))
            if unknown:
                cost += f' + ? ({unknown})'
            failures = Counter(r['failure_class'] for r in runs if not r['success'])
            breakdown = ', '.join(FAILURES.get(k, (k, k))[language]+f' ×{v}' for k,v in sorted(failures.items())) or '—'
            rows.append([m['name'], client, sum(r['invoked_requests'] for r in runs), *times, cost,
                         sum(r.get('guard_refusals', 0) for r in runs), breakdown])
    out['cli-metrics'] = table(['모델','CLI','요청 수','Smoke p50 (s)','코딩 과제 (s)','전체 호출 비용','가드 거절','과제 실패'] if ko else ['Model', 'CLI', 'Requests', 'Smoke p50 (s)', 'Tools (s)',
                               'All-call cost', 'Guard refusals', 'Task failures'], rows)
    return out


def render(data):
    changed=[]
    for ko in (False, True):
        path=ROOT/'docs/performance'/('clients.ko.md' if ko else 'clients.md')
        if not path.exists():
            raise ValueError('missing compatibility page: ' + str(path))
        content=path.read_text()
        for name,value in tables(data,ko).items():
            pattern=r'(<!-- compatibility:'+re.escape(name)+r' -->)\n.*?(<!-- /compatibility:'+re.escape(name)+r' -->)'
            content, count = re.subn(pattern,lambda match:match[1]+'\n'+value+'\n'+match[2],content,flags=re.S)
            if count != 1:
                raise ValueError('missing or duplicate table marker: ' + name)
        if content!=path.read_text():changed.append((path,content))
    return changed


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--import-recordings',type=Path);p.add_argument('--check',action='store_true');args=p.parse_args()
    data=import_data(args.import_recordings) if args.import_recordings else json.loads(DATA.read_text())
    validate(data)
    if args.import_recordings:DATA.write_text(json.dumps(data,ensure_ascii=False,indent=2,allow_nan=False)+'\n')
    changed=render(data)
    if args.check and changed:raise SystemExit('compatibility tables are stale')
    for path,text in changed:path.write_text(text)
    print(f'{len(data["models"])} models; {len(data["api_records"])} API records; {len(data["cli_runs"])} CLI runs')
if __name__=='__main__':main()
