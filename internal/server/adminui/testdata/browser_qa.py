"""Exercise the embedded gateway console with synthetic local APIs.
Run: python3 internal/server/adminui/testdata/browser_qa.py --screenshots <local-scratch>/gateway-qa
Requires existing Playwright + Chromium. No service, upstream, credentials or IdP.
"""
import argparse
import json
import threading
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse
from playwright.sync_api import sync_playwright

STATIC = Path(__file__).resolve().parent.parent / 'static'
KEYS = [
    {'key_id': 'key-engineering-01', 'team': 'engineering', 'allowed_models': ['private-coder'], 'budget_usd_micros': 10000000},
    {'key_id': 'key-platform-02', 'team': 'platform', 'allowed_models': ['economy-coder']},
]
PROVIDER = {'name': 'private', 'type': 'anthropic', 'base_url': 'https://private.invalid', 'auth': 'env:PRIVATE_KEY', 'data_boundary': 'internal'}
MODEL = {'name': 'private-coder', 'context_window': 32768, 'capabilities': ['tools'], 'aliases': ['private-alias'],
         'targets': [{'provider': 'private', 'model': 'upstream', 'api': 'invoke_model'}]}
STATE = {'admin': True, 'analytics': True, 'summary_status': 200, 'logs_status': 200, 'large_money': True, 'evidence': False, 'writes': [], 'reads': []}

OPERATIONS = {'completed': 8, 'succeeded': 4, 'failed': 2, 'partial': 1, 'unknown': 1,
              'estimated_requests': 3, 'uncertain_requests': 1, 'unpriced_requests': 1, 'unavailable_requests': 2,
              'legacy_unknown_requests': 1, 'estimated_cost_micros': 1300, 'uncertain_recorded_cost_micros': 2050,
              'legacy_projection_records': 1}
EVENTS = [dict(id=id, status=status, outcome_state=outcome, cost_state=cost, cost_micros=amount,
               has_cost=has_cost, partial=id == 'C', input_tokens=0, output_tokens=0,
               error_code='stream_incomplete' if id == 'C' else '', team='engineering', model='private-coder',
               provider='private', ts='2026-10-21T10:00:00Z', projection_version=1)
          for id, status, outcome, cost, amount, has_cost in [
              ('A', 200, 'succeeded', 'estimated', 1000, True), ('B', 502, 'failed', 'unavailable', 0, False),
              ('C', 200, 'partial', 'uncertain', 2000, True), ('D', 200, 'succeeded', 'unpriced', 0, True),
              ('E', 200, 'succeeded', 'legacy_unknown', 50, True), ('F', 200, 'succeeded', 'estimated', 0, True),
              ('G', 0, 'unknown', 'unavailable', 0, False), ('H', 400, 'failed', 'estimated', 300, True)]]


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, status, body, content_type='application/json'):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write((json.dumps(body) if content_type == 'application/json' else body).encode())

    def do_GET(self):
        path = urlparse(self.path).path
        if path.startswith('/admin/ui/'):
            name = path[len('/admin/ui/'):] or 'index.html'
            if name not in ('index.html', 'style.css', 'app.js', 'i18n.js', 'favicon.svg'):
                self.respond(404, {})
                return
            self.send_response(200)
            self.send_header('Content-Type', {'index.html': 'text/html', 'style.css': 'text/css', 'app.js': 'text/javascript', 'i18n.js': 'text/javascript', 'favicon.svg': 'image/svg+xml'}[name])
            self.send_header('Content-Security-Policy', "default-src 'self'; frame-ancestors 'none'")
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.end_headers()
            self.wfile.write((STATIC / name).read_bytes())
            return
        if path == '/healthz':
            self.respond(200, {})
            return
        if path == '/metrics':
            self.respond(200, 'inferplane_requests_total{model="private-coder",provider="private",status="2xx",team="engineering"} 72\n', 'text/plain')
            return
        if path == '/admin/auth/config':
            self.respond(404, {})
            return
        STATE['reads'].append(self.path)
        if self.headers.get('Authorization') != 'Bearer fixture-admin':
            self.respond(401, {})
            return
        if path == '/admin/keys':
            self.respond(200, {'data': KEYS})
        elif path == '/admin/whoami':
            self.respond(200, {'is_admin': STATE['admin'], 'subject': 'fixture-subject', 'auth_method': 'static', 'teams': ['engineering']})
        elif path == '/admin/capabilities':
            self.respond(200, {'analytics_index': 'A' if STATE['analytics'] else 'off', 'teams_records': True})
        elif path == '/admin/config':
            self.respond(200, {'writable': True, 'providers': [PROVIDER], 'models': [MODEL]})
        elif path == '/admin/config/export':
            self.respond(200, {'providers': {'private': {'type': 'anthropic', 'base_url': 'https://private.invalid',
                              'api_key_ref': {'env': 'PRIVATE_KEY'}, 'data_boundary': 'internal', 'auth': {'profile': 'private-profile'}}}})
        elif path == '/admin/providers/catalog':
            self.respond(200, {'models': ['upstream']})
        elif path == '/admin/analytics/summary':
            if STATE['evidence']:
                totals = {'requests': 6, 'input_tokens': 0, 'output_tokens': 0, 'cache_read_tokens': 0, 'cost_micros': 3350, 'operations': OPERATIONS}
                self.respond(STATE['summary_status'], {'totals': totals,
                    'by_team': [dict(totals, team='engineering')], 'by_model': [dict(totals, model='private-coder')]})
            else:
                self.respond(STATE['summary_status'], {'totals': {'requests': 72, 'input_tokens': 200, 'output_tokens': 100, 'cache_read_tokens': 400, 'cost_micros': 9007199254740993 if STATE['large_money'] else 72458053},
                                                     'by_team': [{'team': 'engineering', 'requests': 72, 'cost_micros': 5}], 'by_model': []})
        elif path == '/admin/analytics/timeseries':
            now = datetime.now(timezone.utc)
            if STATE['evidence']:
                self.respond(200, [{'day': now.date().isoformat(), 'requests': 6, 'cost_micros': 3350, 'operations': OPERATIONS}, {'day': (now - timedelta(days=30)).date().isoformat(), 'requests': 6, 'cost_micros': 3350, 'operations': OPERATIONS}])
            else:
                self.respond(200, [{'day': now.date().isoformat(), 'cost_micros': 9007199254740993}, {'day': (now - timedelta(days=1)).date().isoformat(), 'cost_micros': 5}])
        elif path == '/admin/teams':
            self.respond(200, {'data': [{'name': 'engineering', 'source': 'record', 'budget_usd_micros': 20000000,
                                        'budget_usd_micros_per_day': 1500000, 'tokens_per_day': 2500000}]})
        elif path == '/admin/users':
            self.respond(200, {'data': [{'owner': 'fixture-person', 'teams': ['engineering'], 'key_count': 1}]})
        elif path == '/admin/logs':
            self.respond(STATE['logs_status'], {'events': EVENTS if STATE['evidence'] else []})
        else:
            self.respond(404, {})

    def do_POST(self):
        self.mutate()

    def do_PUT(self):
        self.mutate()

    def mutate(self):
        if self.headers.get('Authorization') != 'Bearer fixture-admin':
            self.respond(403, {})
            return
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        STATE['writes'].append((self.path, body))
        if self.path == '/admin/keys':
            self.respond(200, {'plaintext': 'once-only-fixture'})
        else:
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
            page = browser.new_page(viewport={'width': 1440, 'height': 1080}, color_scheme='light', locale='en-US')
            errors = []
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.on('console', lambda m: errors.append(m.text) if 'Content Security Policy' in m.text else None)
            page.goto(base + '/admin/ui/')
            assert not STATE['reads'], 'protected data fetched before login'
            page.locator('#token').fill('fixture-admin')
            page.locator('#token-form button').click()
            page.locator('#shell').wait_for(state='visible')
            page.wait_for_function("() => document.getElementById('stat-spend').textContent.startsWith('$')")
            assert page.locator('#stat-spend').inner_text() == '$9,007,199,254.740993'
            assert page.locator('#stat-spend-label').inner_text() == 'Recorded amount'
            assert page.locator('#token').input_value() == ''
            assert page.locator('#stat-spend-spark svg').count() == 1
            STATE['large_money'] = False
            page.locator('button.nav[data-view="overview"]').click()
            page.wait_for_function("() => document.getElementById('stat-spend').textContent === '$72.458053'")
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'gateway-overview.png'), full_page=True)
            page.locator('button.nav[data-view="keys"]').click()
            page.locator('#key-search').fill('platform')
            assert page.locator('#keys tbody tr:visible').count() == 1
            page.locator('#key-search').fill('')
            page.locator('#team').fill('engineering')
            page.locator('#models').fill('private-coder')
            page.locator('#kf-budget').fill('12.345678')
            page.locator('#kf-budget-day').fill('1.000001')
            page.locator('#create-form button').click()
            page.locator('#plaintext-box').wait_for(state='visible')
            assert STATE['writes'][-1][1]['budget_usd_micros'] == 12345678
            assert STATE['writes'][-1][1]['budget_usd_micros_per_day'] == 1000001
            page.locator('#kf-attribution').select_option('account_ref')
            page.locator('#kf-account-ref').fill(' legacy-account ')
            page.locator('#create-form button').click()
            page.wait_for_function("() => document.getElementById('kf-attribution').value === 'owner'")
            assert STATE['writes'][-1][1]['account_ref'] == ' legacy-account '
            assert 'owner' not in STATE['writes'][-1][1]
            page.keyboard.press('Control+k')
            page.locator('#workspace-query').fill('once-only-fixture')
            assert page.locator('#workspace-results button').count() == 0, 'shown-once plaintext entered search'
            page.locator('#workspace-query').fill('key-platform')
            assert page.locator('#workspace-results button').count() == 1
            page.locator('#workspace-query').press('Enter')
            assert page.locator('#key-search').input_value() == 'key-platform-02'
            assert page.locator('#keys tbody tr:visible').count() == 1
            page.locator('button.nav[data-view="teams"]').click()
            page.locator('#teams-table button.ghost').first.click()
            assert page.locator('#tf-budget-day').input_value() == '1.500000'
            assert page.locator('#tf-tpd').input_value() == '2500000'
            page.locator('#tf-budget-day').fill('2.345678')
            page.locator('#tf-tpd').fill('1000000')
            page.locator('#team-form button[type=submit]').click()
            page.wait_for_function("() => document.getElementById('tf-name').value === ''")
            assert STATE['writes'][-1][1]['budget_usd_micros_per_day'] == 2345678
            assert STATE['writes'][-1][1]['budget_usd_micros'] == 20000000
            assert STATE['writes'][-1][1]['tokens_per_day'] == 1000000
            if args.screenshots:
                page.locator('#teams-table button.ghost').first.click()
                page.screenshot(path=str(args.screenshots / 'gateway-team-budgets-tokens.png'), full_page=True)
            page.locator('button.nav[data-view="providers"]').click()
            page.locator('#provider-write').wait_for(state='visible')
            page.locator('#providers-table button.ghost').first.click()
            page.wait_for_function("() => document.getElementById('pf-name').value === 'private'")
            assert page.locator('#pf-data-boundary').input_value() == 'internal'
            page.locator('#provider-form button[type=submit]').click()
            page.wait_for_function("() => document.getElementById('pf-name').value === ''")
            assert STATE['writes'][-1][1]['data_boundary'] == 'internal'
            assert STATE['writes'][-1][1]['auth']['profile'] == 'private-profile'
            page.locator('#routes-table button.ghost').first.click()
            assert page.locator('#mf-context-window').input_value() == '32768'
            page.locator('#model-form button[type=submit]').click()
            page.wait_for_function("() => document.getElementById('mf-name').value === ''")
            assert STATE['writes'][-1][1]['aliases'] == ['private-alias']
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'gateway-models.png'), full_page=True)
            # Exercise the actual evidence views in both languages/themes at every supported width.
            STATE['evidence'] = True
            page.locator('button.nav[data-view="usage"]').click()
            page.wait_for_function("() => document.getElementById('usage-totals').textContent.includes('3/8 (37.5%)')")
            assert 'Observed completed records' in page.locator('#usage-totals').inner_text()
            assert '$0.001300' in page.locator('#usage-by-model').inner_text()
            assert '$0.002050' in page.locator('#usage-by-day').inner_text()
            assert page.locator('#usage-by-day tbody tr').count() == 1, 'sparse cutoff-day row escaped summary period'
            page.locator('button.nav[data-view="logs"]').click()
            page.wait_for_function("() => document.querySelectorAll('#logs-table tbody tr').length === 8")
            rows = page.locator('#logs-table tbody tr')
            assert 'HTTP 200' in rows.nth(2).inner_text() and 'Partial response' in rows.nth(2).inner_text()
            assert rows.nth(2).locator('td.outcome.warn').count() == 1
            assert '$' not in rows.nth(1).inner_text() and '$' not in rows.nth(3).inner_text()
            assert '$0.000000' in rows.nth(5).inner_text()
            assert 'Recorded amount' in rows.nth(4).inner_text() and 'Evidence missing' in rows.nth(4).inner_text()
            for language in ('zh', 'ja'):
                page.locator('#lang-select').select_option(language)
                page.locator('button.nav[data-view="overview"]').click()
                page.wait_for_function("() => document.getElementById('stat-spend-sub').textContent.includes('Last 30 days')")
                assert '结算' not in page.locator('#stat-spend-sub').inner_text()
                assert '確定支出' not in page.locator('#stat-spend-sub').inner_text()
                if args.screenshots:
                    page.screenshot(path=str(args.screenshots / f'evidence-overview-{language}.png'), full_page=True)
                STATE['summary_status'] = 503
                page.locator('button.nav[data-view="overview"]').click()
                page.wait_for_function("() => document.getElementById('stat-spend-sub').textContent === 'Cost evidence unavailable · analytics required'")
                STATE['summary_status'] = 200
                if args.screenshots:
                    page.screenshot(path=str(args.screenshots / f'evidence-overview-unavailable-{language}.png'), full_page=True)
            for language in ('en', 'ko'):
                page.locator('#lang-select').select_option(language)
                for theme in ('light', 'dark'):
                    page.evaluate("theme => document.documentElement.dataset.theme = theme", theme)
                    for width in (390, 768, 1440):
                        page.set_viewport_size({'width': width, 'height': 1000})
                        for view in ('overview', 'usage', 'logs'):
                            page.locator(f'button.nav[data-view="{view}"]').click()
                            target = '#stat-requests-sub' if view == 'overview' else '#usage-totals' if view == 'usage' else '#logs-table'
                            expected = '3/8 (37.5%)' if view != 'logs' else '부분 응답' if language == 'ko' else 'Partial response'
                            page.wait_for_function("([id, expected]) => document.querySelector(id).textContent.includes(expected)", arg=[target, expected])
                            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), f'{view} overflow: {language}/{theme}/{width}'
                            if view == 'overview':
                                assert page.locator('#stat-spend-label').inner_text() == ('산정 가능한 비용' if language == 'ko' else 'Estimable cost')
                            if args.screenshots:
                                page.screenshot(path=str(args.screenshots / f'evidence-{view}-{theme}-{language}-{width}.png'), full_page=True)
                            if view == 'logs' and width == 390:
                                wrapper = page.locator('#logs-table').locator('..')
                                assert wrapper.evaluate('(el) => el.scrollWidth > el.clientWidth')
                                wrapper.evaluate('(el) => el.scrollLeft = el.scrollWidth')
                                assert rows.nth(2).locator('td').nth(6).evaluate('(el) => { const r = el.getBoundingClientRect(), w = el.closest(".table-scroll").getBoundingClientRect(); return r.left < w.right && r.right > w.left; }'), 'mobile cost column unreachable'
                                wrapper.evaluate('(el) => { const cell = document.querySelector("#logs-table tbody tr:nth-child(3) td.outcome"); el.scrollLeft += cell.getBoundingClientRect().left - el.getBoundingClientRect().left; }')
                                assert rows.nth(2).locator('td.outcome').evaluate('(el) => { const r = el.getBoundingClientRect(), w = el.closest(".table-scroll").getBoundingClientRect(); return r.left >= w.left - 1 && r.right <= w.right + 1; }'), 'mobile outcome column unreachable'
                                if args.screenshots:
                                    page.screenshot(path=str(args.screenshots / f'evidence-logs-scrolled-{theme}-{language}-{width}.png'), full_page=True)

            page.evaluate("document.documentElement.dataset.theme = 'light'")
            page.locator('#lang-select').select_option('ko')
            assert page.locator('#workspace-search').inner_text() == '통합 검색'
            page.locator('#theme-toggle').click()
            for width in (390, 768, 1440):
                page.set_viewport_size({'width': width, 'height': 1000})
                for view in ('overview', 'usage', 'logs', 'keys', 'teams', 'providers', 'governance', 'settings'):
                    page.locator(f'button.nav[data-view="{view}"]').click()
                    page.wait_for_timeout(50)
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), f'{view} overflow at {width}px'
                    if args.screenshots and (width == 390 or view == 'overview'):
                        page.screenshot(path=str(args.screenshots / f'gateway-{view}-dark-ko-{width}.png'), full_page=True)
                page.keyboard.press('Control+k')
                page.locator('#workspace-query').press('Escape')
                assert page.locator('#workspace-dialog').is_hidden()
            # Analytics failures and read-only profiles must remain visible.
            page.on('dialog', lambda d: d.accept())
            page.locator('button.nav[data-view="logs"]').click()
            page.wait_for_function("() => document.querySelectorAll('#logs-table tbody tr').length === 8")
            STATE['logs_status'] = 503
            page.locator('#logs-load-more').click()
            page.locator('#workspace-error').wait_for(state='visible')
            assert page.locator('#logs-table tbody tr').count() == 8, 'failed pagination lost loaded rows'
            STATE['logs_status'] = 200
            page.locator('#logs-load-more').click()
            page.locator('#workspace-error').wait_for(state='hidden')
            assert page.locator('#logs-table tbody tr').count() == 16, 'pagination recovery did not append rows'
            assert page.locator('#workspace-error').inner_text() == ''
            STATE['logs_status'] = 503
            page.locator('button.nav[data-view="logs"]').click()
            page.locator('#workspace-error').wait_for(state='visible')
            assert page.locator('#logs-table tbody tr').count() == 0, 'failed refresh retained stale log rows'
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / 'evidence-logs-network-failure.png'), full_page=True)
            STATE['logs_status'] = 200
            STATE['summary_status'] = 503
            page.locator('button.nav[data-view="overview"]').click()
            page.wait_for_function("() => document.getElementById('stat-spend').textContent === '—'")
            page.locator('button.nav[data-view="usage"]').click()
            page.locator('#workspace-error').wait_for(state='visible')
            assert page.locator('#usage-content').is_hidden(), 'unavailable analytics retained fresh-looking totals'
            STATE['admin'] = False
            page.locator('#disconnect').click()
            page.locator('#token').wait_for(state='visible')
            assert page.locator('#workspace-results').inner_text() == ''
            page.locator('#token').fill('fixture-admin')
            page.locator('#token-form button').click()
            page.locator('#shell').wait_for(state='visible')
            page.locator('button.nav[data-view="providers"]').click()
            page.locator('#providers-session-ro').wait_for(state='visible')
            assert page.locator('#provider-write').is_hidden()
            assert page.locator('#model-write').is_hidden()
            page.locator('button.nav[data-view="keys"]').click()
            assert page.locator('#team-select').is_visible()
            assert page.locator('#team').is_hidden()
            assert not errors, errors
            browser.close()
            print('Gateway browser QA passed: A–H evidence views, partial HTTP200 warning, zero/unavailable/unpriced distinctions, English/Korean × light/dark × 390/768/1440 screenshots, auth, exact spend/chart/key amounts, metadata preservation, filtering/search/keyboard, plaintext exclusion, Korean, themes, all views/mobile, read-only profile, disconnect.')
    finally:
        server.shutdown()
        server.server_close()


if __name__ == '__main__':
    main()
