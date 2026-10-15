"""Local browser QA against synthetic APIs only. No installed service is touched.
Run: python3 internal/controlplane/ui/testdata/browser_qa.py [--screenshots DIR]
Requires an already installed Playwright + Chromium; not a Go test dependency.
"""
import argparse
import json
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlparse
from playwright.sync_api import sync_playwright

STATIC = Path(__file__).resolve().parent.parent / 'static'
NOW = datetime.now(timezone.utc).isoformat()
POLICY = {
    'apiVersion': 'inferplane.dev/v1alpha1', 'kind': 'GovernancePolicy',
    'metadata': {'name': 'engineering-guardrails', 'generation': 12},
    'spec': {'subject': {'team': 'engineering'}, 'rules': [
        {'name': 'daily-budget', 'failurePolicy': 'FailClosed',
         'budget': {'period': 'CalendarDay', 'limitMilliUSD': 100000, 'hardCap': True,
                    'lease': {'grantMilliUSD': 100, 'renewInterval': '10s'}}},
        {'name': 'throughput', 'failurePolicy': 'FailClosed', 'rate': {'rpm': 120, 'tpm': 500000}},
        {'name': 'model-access', 'failurePolicy': 'FailClosed', 'modelAccess': {'allow': ['*']}},
        {'name': 'privacy', 'failurePolicy': 'FailClosed', 'sensitiveData': {'onDetected': 'Block', 'onUninspectable': 'Block'}},
    ]},
}

def row(key, spent, tokens):
    return dict(key=key, spent_micro_usd=spent, input_tokens=tokens, output_tokens=tokens // 10,
                cache_read_tokens=tokens * 2, cache_write_5m_tokens=tokens // 5, cache_write_1h_tokens=0)

ROWS = [row('engineering', 72458053, 1304200), row('platform', 42981742, 981420),
        row('research', 28261950, 724610), row('design', 13984200, 261400)]
USER_REF = 'identity-v1:' + 'a' * 64
USER_ROWS = [row(USER_REF, 42981742, 980000), row('developer-b', 29476311, 324200)]
STATE = {'degraded': False, 'usage_status': 200, 'chart_status': 200, 'writable': True, 'writes': [], 'model_delay': 0}

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, status, body):
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(json.dumps(body).encode())

    def do_GET(self):
        url = urlparse(self.path)
        if url.path.startswith('/ui/'):
            name = url.path[4:] or 'index.html'
            if name not in ('index.html', 'app.js', 'style.css'):
                self.respond(404, {})
                return
            self.send_response(200)
            self.send_header('Content-Type', {'index.html': 'text/html', 'app.js': 'text/javascript', 'style.css': 'text/css'}[name])
            self.send_header('Content-Security-Policy', "default-src 'self'; frame-ancestors 'none'")
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.send_header('Referrer-Policy', 'no-referrer')
            self.end_headers()
            self.wfile.write((STATIC / name).read_bytes())
            return
        if self.headers.get('Authorization') != 'Bearer qa-read-token':
            self.respond(401, {})
            return
        if url.path == '/v1alpha1/usage/charts':
            if STATE['chart_status'] != 200:
                self.respond(STATE['chart_status'], {})
                return
            qs = parse_qs(url.query)
            start = qs['since'][0][:10]
            today = datetime.now(timezone.utc).date().isoformat()
            self.respond(STATE['usage_status'], {
                'total_micro_usd': 157685945, 'other_micro_usd': 0, 'model_count': 3,
                'days': [{'day': start, 'spent_micro_usd': 38982000}, {'day': today, 'spent_micro_usd': 118703945}] if start != today else [{'day': today, 'spent_micro_usd': 157685945}],
                'models': [{'key': 'gpt-6.1-sol', 'spent_micro_usd': 72458053}, {'key': 'gpt-6-astra', 'spent_micro_usd': 56946092}, {'key': 'private-coder', 'spent_micro_usd': 28281800}],
            })
        elif url.path == '/v1alpha1/usage':
            qs = parse_qs(url.query)
            group = qs.get('group_by')
            if group == ['team']:
                rows = ROWS
            elif group == ['user']:
                rows = USER_ROWS + ([] if qs.get('team') else [row('', 85227892, 1930000)])
            elif qs.get('user') == [USER_REF]:
                rows = [row('gpt-6.1-sol', 42981742, 980000)]
            elif qs.get('user') == ['developer-b']:
                rows = [row('gpt-6-astra', 29476311, 324200)]
            else:
                rows = [row('gpt-6.1-sol', 42981742, 980000), row('gpt-6-astra', 29476311, 324200)]
            self.respond(STATE['usage_status'], {'total_micro_usd': sum(r['spent_micro_usd'] for r in rows), 'rows': rows, 'degraded': STATE['degraded']})
        elif url.path == '/v1alpha1/dataplanes':
            self.respond(200, {'generation': 'sha256:7fa94c2a8e1d', 'apiVersions': ['inferplane.dev/v1alpha1'], 'dataplanes': {
                'mayu-us-east-01': {'generation': 'sha256:7fa94c2a8e1d', 'lastSeen': NOW, 'apiVersions': ['inferplane.dev/v1alpha1']},
                'mayu-us-east-02': {'generation': 'sha256:7fa94c2a8e1d', 'lastSeen': NOW, 'apiVersions': ['inferplane.dev/v1alpha1']},
                'mayu-eu-west-01': {'generation': 'sha256:283aa9b0c16f', 'lastSeen': NOW, 'apiVersions': ['inferplane.dev/v1alpha1'], 'rejections': [{'policy': 'engineering-guardrails', 'rule': 'privacy', 'reason': '<img src=x onerror=alert(1)> unsupported sensitiveData rule'}]},
                'mayu-ap-northeast-01': {'generation': 'sha256:7fa94c2a8e1d', 'lastSeen': NOW, 'apiVersions': ['inferplane.dev/v1alpha1']},
            }})
        elif url.path == '/v1alpha1/policies':
            self.respond(200, {'generation': 'sha256:7fa94c2a8e1d', 'writable': STATE['writable'], 'policies': [{'name': POLICY['metadata']['name'], 'policy': POLICY}],
                'identity_bindings': [{'reported_ref': USER_REF, 'account_ref': ' developer-a ', 'kind': 'human'}]})
        elif url.path == '/v1alpha1/usage/export':
            self.respond(200, [{**r, 'dataplane': 'synthetic-node', 'window_start': NOW, 'window_end': NOW,
                               'team': r['key'], 'user': 'synthetic-user', 'model': 'synthetic-model'} for r in ROWS])
        else:
            self.respond(404, {})

    def do_PUT(self):
        if self.headers.get('Authorization') != 'Bearer qa-write-token':
            self.respond(403, {'error': 'write refused'})
            return
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        STATE['writes'].append((unquote(self.path), body))
        self.respond(204, {})

    def do_DELETE(self):
        if self.headers.get('Authorization') != 'Bearer qa-write-token':
            self.respond(403, {})
            return
        STATE['writes'].append((unquote(self.path), None))
        self.respond(204, {})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--screenshots', type=Path)
    args = parser.parse_args()
    if args.screenshots:
        args.screenshots.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f'http://127.0.0.1:{server.server_port}'
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            page = browser.new_page(viewport={'width': 1440, 'height': 1080}, color_scheme='light')
            errors, requests = [], []
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.on('console', lambda m: errors.append(m.text) if 'Content Security Policy' in m.text else None)
            page.on('request', lambda r: requests.append(r.url))
            page.goto(base + '/ui/')
            assert page.locator('#auth-section').is_visible()
            assert not any('/v1alpha1/' in r for r in requests), 'protected data requested before login'
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'login.png'), full_page=True)
            page.locator('#token').fill('qa-read-token')
            page.locator('#connect').click()
            page.locator('#workspace-content').wait_for(state='visible')
            assert page.locator('#stat-spend').get_attribute('title') == '$157.685945'
            assert page.locator('#spend-trend svg').count() == 1
            assert page.locator('#model-share svg').count() == 1
            assert page.locator('#token').input_value() == ''
            assert page.locator('#fleet-coverage').inner_text() == '3 / 4 generations match'
            assert page.locator('#stat-users').inner_text() == '2'
            assert page.locator('#stat-user-average').get_attribute('title') == '$36.229026'
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'overview.png'), full_page=True)
            page.locator('#nav-usage').click()
            page.locator('#teams .team-button').first.click()
            page.wait_for_function("() => document.getElementById('model-status').textContent.startsWith('Total')")
            assert page.locator('#models tbody tr').count() == 2
            page.locator('#usage-sort').select_option('name')
            assert page.locator('#teams tbody tr').first.inner_text().startswith('design')
            page.locator('#team-search').fill('platform')
            assert page.locator('#teams tbody tr').count() == 1
            with page.expect_download() as dl:
                page.locator('#export-usage').click()
            download = dl.value
            assert len(json.loads(Path(download.path()).read_text())) == 4
            page.locator('#nav-fleet').click()
            page.locator('#fleet-table .text-button').click()
            assert '<img src=x' in page.locator('#node-rejections').inner_text()
            assert page.locator('#node-rejections img').count() == 0
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'fleet.png'), full_page=True)
            page.locator('#nav-policies').click()
            assert page.locator('#new-policy').is_disabled()
            page.locator('#write-token').fill('qa-write-token')
            page.locator('#write-token-form button[type=submit]').click()
            assert page.locator('#write-token').input_value() == ''
            page.locator('#nav-users').click()
            page.locator('#users-search').fill('developer-a')
            assert page.locator('#users-table tbody tr').count() == 1
            page.locator('#users-table .user-button').click()
            page.wait_for_function("() => document.getElementById('user-model-status').textContent.startsWith('Total')")
            assert page.locator('#user-models tbody tr').count() == 1
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'user-costs.png'), full_page=True)
            page.locator('#users-table .user-budget').click()
            assert page.locator('#bf-user').input_value() == ' developer-a '
            assert page.locator('#bf-policy-name').input_value(), 'personal budget required technical naming'
            page.locator('#bf-limit').fill('75.125')
            before = len(STATE['writes'])
            page.locator('#dialog-confirm').click()
            assert len(STATE['writes']) == before
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            assert STATE['writes'][-1][1]['spec']['subject']['user'] == ' developer-a '
            assert STATE['writes'][-1][1]['spec']['rules'][0]['budget']['limitMilliUSD'] == 75125
            page.locator('#users-table .user-token-limit').click()
            assert page.locator('#rf-user').input_value() == ' developer-a '
            page.locator('#rf-token-limit').fill('20000000')
            page.locator('#dialog-confirm').click()
            assert page.locator('#dialog-error').is_visible()
            page.locator('#rf-shared').check()
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'user-token-policy.png'), full_page=True)
            page.locator('#dialog-confirm').click()
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            assert STATE['writes'][-1][1]['spec']['rules'][0]['tokenQuota'] == {'limitTokens': 20000000, 'period': 'CalendarMonth'}
            page.locator('#nav-policies').click()
            page.locator('.policy-editor-details summary').click()
            page.locator('[data-field="daily-budget-limit"]').fill('125000')
            page.locator('.policy-save').click()
            writes_before_policy = len(STATE['writes'])
            assert writes_before_policy == 2, 'unexpected policy mutation before confirmation'
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            assert STATE['writes'][-1][1]['spec']['rules'][0]['budget']['period'] == 'CalendarDay'
            assert STATE['writes'][-1][1]['spec']['rules'][3] == POLICY['spec']['rules'][3]
            page.locator('#new-policy').click()
            text = json.loads(page.locator('#policy-editor').input_value())
            text['metadata']['name'] = 'qa-created-policy'
            page.locator('#policy-editor').fill(json.dumps(text))
            page.locator('#dialog-confirm').click()
            assert page.locator('#dialog-title').inner_text() == 'Review · qa-created-policy'
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            assert STATE['writes'][-1][0].endswith('/qa-created-policy')
            page.locator('.policy-editor-details summary').click()
            page.locator('.policy-delete').click()
            page.locator('#dialog-confirm').click()
            assert page.locator('#dialog-error').is_visible()
            page.locator('#policy-editor').fill('engineering-guardrails')
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            assert STATE['writes'][-1][1] is None
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'policies.png'), full_page=True)
            page.locator('#nav-budgets').click()
            assert page.locator('#budgets-list .budget-card').count() == 1
            assert '$100.000' in page.locator('#budget-table').inner_text()
            page.locator('#budget-period-filter').select_option('CalendarMonth')
            assert page.locator('#budgets-list .budget-card').count() == 0
            page.locator('#budget-period-filter').select_option('')
            page.locator('#new-budget').click()
            page.locator('#budget-names summary').click()
            page.locator('#bf-policy-name').fill('qa-budget')
            page.locator('#bf-team').fill('platform')
            page.locator('#bf-rule').fill('monthly-cap')
            page.locator('#bf-limit').fill('12.345')
            assert page.locator('#bf-hard').is_checked()
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'budget-create.png'), full_page=True)
            writes_before = len(STATE['writes'])
            page.locator('#dialog-confirm').click()
            assert len(STATE['writes']) == writes_before
            assert '12345' in page.locator('#policy-review').inner_text()
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            saved_budget = STATE['writes'][-1][1]['spec']['rules'][0]
            assert saved_budget['failurePolicy'] == 'FailClosed'
            assert saved_budget['budget'] == {'limitMilliUSD': 12345, 'period': 'CalendarMonth', 'hardCap': True}
            page.locator('#budget-table .budget-table-edit').click()
            assert page.locator('#bf-policy').is_disabled()
            assert page.locator('#bf-team').is_disabled()
            page.locator('#bf-limit').fill('19.999')
            page.locator('#dialog-confirm').click()
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            edited = STATE['writes'][-1][1]
            assert edited['spec']['rules'][0]['budget']['limitMilliUSD'] == 19999
            assert edited['spec']['rules'][0]['budget']['lease'] == POLICY['spec']['rules'][0]['budget']['lease']
            assert edited['spec']['rules'][3] == POLICY['spec']['rules'][3]
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'budgets.png'), full_page=True)
            page.locator('#nav-overview').click()
            page.locator('#attention-list button').first.click()
            assert page.locator('#fleet-state-filter').input_value() == 'differs'
            assert page.locator('#fleet-table tbody tr').count() == 1
            page.locator('#fleet-table .text-button').click()
            page.locator('#node-rejections .text-button').click()
            assert page.locator('#policies-view').is_visible()
            assert page.locator('#policy-search').input_value() == 'engineering-guardrails'
            page.keyboard.press('Control+k')
            page.locator('#command-input').fill('engineering')
            assert page.locator('#command-results button').count() == 2
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'workspace-search.png'), full_page=True)
            page.locator('#command-input').press('Enter')
            assert page.locator('#usage-view').is_visible()
            assert page.locator('#models-section').is_visible()
            page.locator('#range-today').click()
            assert page.locator('#since').input_value() == page.locator('#until').input_value()
            page.locator('#nav-policies').click()
            page.locator('#new-rule').click()
            page.locator('#rf-kind').select_option('tier')
            page.locator('#rf-policy').select_option('engineering-guardrails')
            page.locator('#rule-names summary').click()
            page.locator('#rf-name').fill('economy-at-80')
            page.locator('#rf-source').fill('premium-coder')
            page.locator('#rf-target').fill('economy-coder')
            page.locator('#rf-next-threshold').fill('95')
            page.locator('#rf-next-target').fill('local-coder')
            page.locator('#rf-strict').check()
            before = len(STATE['writes'])
            page.locator('#dialog-confirm').click()
            assert len(STATE['writes']) == before
            assert 'every attempt' in page.locator('#change-summary').inner_text()
            page.locator('#dialog-back').click()
            assert page.locator('#rf-target').input_value() == 'economy-coder'
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'guided-substitution.png'), full_page=True)
            page.locator('#dialog-confirm').click()
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'change-review.png'), full_page=True)
            page.locator('#dialog-confirm').click()
            page.locator('#policy-dialog').wait_for(state='hidden')
            tier = STATE['writes'][-1][1]['spec']['rules'][-1]['routing']['budgetTiers']
            assert tier == {'budgetRef': 'daily-budget', 'enforceTargets': True, 'tiers': [
                {'thresholdPercent': 80, 'substitute': {'premium-coder': 'economy-coder'}},
                {'thresholdPercent': 95, 'substitute': {'premium-coder': 'local-coder'}}]}
            page.locator('#notice button').click()
            assert page.locator('#fleet-view').is_visible()
            STATE['degraded'] = True
            page.locator('#nav-overview').click()
            page.locator('#refresh').click()
            page.locator('#degraded-banner').wait_for(state='visible')
            page.locator('#theme-toggle').click()
            assert page.locator('html').get_attribute('data-theme') == 'dark'
            for width in (390, 768, 1440):
                page.set_viewport_size({'width': width, 'height': 1000})
                for view in ('overview', 'usage', 'users', 'budgets', 'fleet', 'policies'):
                    page.locator('#nav-' + view).click()
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), f'{view} overflow at {width}px'
                    if args.screenshots and (view == 'overview' or width == 390):
                        page.screenshot(path=str(args.screenshots / f'{view}-dark-{width}.png'), full_page=True)
                page.locator('#nav-budgets').click()
                page.locator('#new-budget').click()
                assert page.evaluate('document.getElementById("policy-dialog").scrollWidth <= document.getElementById("policy-dialog").clientWidth'), f'budget form overflow at {width}px'
                page.locator('#dialog-cancel').click()
                page.locator('#nav-policies').click()
                page.locator('#new-rule').click()
                for kind in ('modelAccess', 'rate', 'tokenQuota', 'privacy', 'tier'):
                    page.locator('#rf-kind').select_option(kind)
                    assert page.evaluate('document.getElementById("policy-dialog").scrollWidth <= document.getElementById("policy-dialog").clientWidth'), f'{kind} guide overflow at {width}px'
                page.locator('#dialog-cancel').click()
                page.keyboard.press('Control+k')
                page.locator('#command-input').fill('mayu')
                assert page.evaluate('document.getElementById("command-dialog").scrollWidth <= document.getElementById("command-dialog").clientWidth'), f'search overflow at {width}px'
                page.locator('#command-input').press('ArrowDown')
                assert page.locator('#command-input').get_attribute('aria-activedescendant') == 'command-result-1'
                page.locator('#command-input').press('Escape')
                assert page.locator('#command-dialog').is_hidden()
            STATE['chart_status'] = 404
            page.locator('#nav-overview').click()
            page.locator('#refresh').click()
            page.wait_for_function("() => document.querySelector('#model-share')?.textContent.includes('synthetic-model')")
            assert page.locator('#spend-trend svg').count() == 1
            assert page.locator('#model-share svg').count() == 1
            page.locator('#disconnect').click()
            assert page.locator('#workspace-content').is_hidden()
            assert page.locator('#policies-list').inner_text() == ''
            STATE['usage_status'] = 401
            page.locator('#token').fill('qa-read-token')
            page.locator('#connect').click()
            page.locator('#error').wait_for(state='visible')
            assert page.locator('#workspace-content').is_hidden()
            assert not errors, errors
            browser.close()
            print('Browser QA passed: auth, precise spend, search/keyboard, attention actions, guided substitution/review/back, export, CRUD, XSS/CSP, themes, responsive guides, disconnect.')
    finally:
        server.shutdown()
        server.server_close()

if __name__ == '__main__':
    main()
