"""Exercise the Audit view with synthetic /admin/audit/* responses.
Run: python3 internal/server/adminui/testdata/audit_qa.py --screenshots <local-scratch>/audit-qa
Requires existing Playwright + Chromium. No service, upstream or credentials.
"""
import argparse
import json
import re
import sys
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote, urlparse

sys.path.insert(0, str(Path(__file__).resolve().parent))
import browser_qa  # noqa: E402
from playwright.sync_api import sync_playwright  # noqa: E402

I18N = (browser_qa.STATIC / 'i18n.js').read_text(encoding='utf-8')


def en(key):
    """English string for an i18n key, read from i18n.js so copy lives in one place."""
    m = re.search(r'"' + re.escape(key) + r'": \{ en: "((?:[^"\\]|\\.)*)"', I18N)
    assert m, f'missing i18n key {key}'
    return json.loads('"' + m.group(1) + '"')


TRACE = '4bf92f3577b34da6a3ce929d0e0e4736'
# Realistic widths: a Bedrock inference-profile model id and gateway key ids.
LONG_MODEL = 'global.anthropic.claude-haiku-4-5-20251001-v1:0'
KEY_A, KEY_B, KEY_C = 'ik_a49124618069', 'ik_b7310c2d5e88', 'ik_c0ffee000001'
# KEY_A has no name (shown once); KEY_B and KEY_C share the name "ci" (ambiguous).
KEYS = [dict(browser_qa.KEYS[0], key_id=KEY_A),
        dict(browser_qa.KEYS[1], key_id=KEY_B, metadata={'name': 'ci'}),
        dict(browser_qa.KEYS[1], key_id=KEY_C, metadata={'name': 'ci'})]
SESSION = 's1:' + 'a' * 32
SESSION2 = 's1:' + 'b' * 32
ID_MAIN, ID_DENIED, ID_LEGACY = ('01JC0000000000000000000003', '01JC0000000000000000000002', '01JC0000000000000000000001')
HASH = 'sha256-hash-fixture'
EVENTS = [
    {'id': ID_MAIN, 'ts': '2026-10-29T10:44:12Z', 'event': 'request_completed', 'kind': 'ok', 'team': 'engineering',
     'key_id': KEY_A, 'ingress': 'bedrock', 'stream': False, 'status': 200, 'request_class': 'main', 'session_ref': SESSION,
     'trace_id': TRACE, 'model_requested': LONG_MODEL, 'model_actual': LONG_MODEL, 'provider': 'bedrock-global', 'fallback': True,
     'attempt_count': 2, 'input_tokens': 1200, 'output_tokens': 80, 'cost_micros': 31210, 'total_ms': 6810, 'ttft_ms': 1420},
    {'id': ID_DENIED, 'ts': '2026-10-29T10:36:22Z', 'event': 'request_started', 'kind': 'policy_denied', 'team': 'engineering',
     'key_id': KEY_A, 'ingress': 'anthropic', 'stream': False, 'status': 429, 'deny_code': 'key_rate_limited',
     'request_class': 'subagent', 'session_ref': SESSION, 'model_requested': 'private-coder', 'attempt_count': 0,
     'input_tokens': 0, 'output_tokens': 0, 'cost_micros': 0, 'total_ms': 6},
    {'id': ID_LEGACY, 'ts': '2026-10-29T10:31:40Z', 'event': 'request_completed', 'kind': 'privacy_masked', 'team': 'platform',
     'key_id': KEY_B, 'ingress': 'anthropic', 'stream': False, 'status': 200, 'masked': True, 'pii': True, 'categories': ['email'],
     'policy_ref': 'org-pii-mask#4·0', 'planned_boundary': 'external', 'actual_boundary': 'external', 'model_requested': 'economy-coder',
     'model_actual': 'economy-coder', 'attempt_count': 1, 'input_tokens': 600, 'output_tokens': 50, 'cost_micros': 2650, 'total_ms': 0},
]
NOCLASS = [{k: v for k, v in e.items() if k != 'request_class'} for e in EVENTS]
AUTO_EVENTS = [
    {'id': '01JC0000000000000000000010', 'ts': '2026-10-29T10:40:00Z', 'event': 'request_completed', 'kind': 'auto_mode_blocked', 'team': 'engineering',
     'key_id': KEY_A, 'session_ref': SESSION, 'auto_stage': 'classifier', 'auto_verdict': 'block', 'auto_signature': 'sig-1',
     'model_requested': 'private-coder', 'model_actual': 'private-coder', 'input_tokens': 10, 'output_tokens': 2, 'cost_micros': 0},
    {'id': '01JC0000000000000000000011', 'ts': '2026-10-29T10:41:00Z', 'event': 'request_completed', 'kind': 'ok', 'team': 'engineering',
     'key_id': KEY_A, 'session_ref': SESSION, 'auto_stage': 'classifier', 'auto_verdict': 'unknown', 'auto_signature': 'sig-2',
     'model_requested': 'private-coder', 'input_tokens': 10, 'output_tokens': 2, 'cost_micros': 0},
]
DETAIL = {'event': EVENTS[0], 'hash': HASH, 'record': {'prev_hash': 'sha256-prev-fixture', 'principal': {'key_id': KEY_A, 'team': 'engineering'}},
          'waterfall': {'total_ms': 6810, 'ttft_ms': 1420, 'rows': [
              {'kind': 'gateway', 'start_ms': 0, 'dur_ms': 15}, {'kind': 'attempt', 'provider': 'bedrock', 'status': 'upstream_error', 'http': 503, 'start_ms': 15, 'dur_ms': 1225},
              {'kind': 'attempt', 'provider': 'private', 'status': 'ok', 'start_ms': 1240, 'dur_ms': 5560}, {'kind': 'settle', 'start_ms': 6800, 'dur_ms': 10}]}}
# Latency but no provider attempts: neutral bar plus the noAttempt hint, not "no timing".
DENIED = {'event': EVENTS[1], 'hash': 'sha256-denied-fixture', 'record': {}, 'waterfall': {'total_ms': 6, 'rows': []}}
# Legacy record: no latency at all, so the waterfall says no timing was recorded.
LEGACY = {'event': EVENTS[2], 'record': {}, 'waterfall': {'total_ms': 0, 'rows': []}}
SUMMARY = {'since_ms': 0, 'until_ms': 0, 'by_kind': {'ok': 1, 'policy_denied': 1, 'privacy_masked': 1},
           'categories': [{'name': 'email', 'count': 1}], 'pii': {'masked': 1, 'internal': 0, 'blocked': 0, 'shadow': 0},
           'denials': [{'code': 'key_rate_limited', 'team': 'engineering', 'key_id': KEY_A, 'count': 1, 'last_ts': '2026-10-29T10:36:22Z'}],
           'auto_mode': {'block': 1, 'allow': 5, 'unknown': 1, 'signatures': []},
           'buckets': [{'start_ms': i, 'requests': i % 3, 'denials': i % 2, 'pii': 0, 'auto_blocked': 0} for i in range(12)],
           'coverage_start_ms': 1793235600000}
SESSIONS = [
    {'session_ref': SESSION, 'team': 'engineering', 'key_id': KEY_A, 'first_ts': '2026-10-29T10:02:11Z', 'last_ts': '2026-10-29T10:44:12Z',
     'requests': 2, 'cost_micros': 31210, 'denials': 1, 'pii': 0, 'auto_blocked': 0, 'subagents': 1},
    {'session_ref': SESSION2, 'team': 'platform', 'key_id': KEY_B, 'first_ts': '2026-10-29T09:00:00Z', 'last_ts': '2026-10-29T09:05:00Z',
     'requests': 1, 'cost_micros': 0, 'denials': 0, 'pii': 0, 'auto_blocked': 0, 'subagents': 0},
]
# Console errors are failures except the 404 the shared harness serves for /admin/auth/config on purpose.
EXPECTED_ERROR_URL_SUFFIX = '/admin/auth/config'


class Handler(browser_qa.Handler):
    def do_GET(self):
        # The browser sends encodeURIComponent("s1:...") as s1%3A...; the real server unescapes it.
        path = unquote(urlparse(self.path).path)
        q = self.path
        if path.startswith('/admin/') and self.headers.get('Authorization') == 'Bearer fixture-admin':
            if path == '/admin/keys':
                return self.respond(200, {'data': KEYS})
            if path == '/admin/capabilities':
                return self.respond(200, {'analytics_index': 'A', 'teams_records': True, 'audit_console': True, 'logs_bodies': False,
                                          'trace_link_template': 'https://grafana.example/explore?traceId={trace_id}'})
            if path == '/admin/audit/summary':
                return self.respond(200, SUMMARY)
            if path == '/admin/audit/events':
                if 'auto=true' in q:
                    return self.respond(200, {'events': [] if 'team=empty-team' in q else AUTO_EVENTS, 'next': 'more'})
                if 'pii=true' in q:
                    return self.respond(200, {'events': [e for e in EVENTS if e.get('pii')], 'next': 'more'})
                return self.respond(200, {'events': NOCLASS if 'team=noclass' in q else EVENTS, 'next': ''})
            if path == '/admin/audit/events/' + ID_MAIN:
                return self.respond(200, DETAIL)
            if path == '/admin/audit/events/' + ID_DENIED:
                return self.respond(200, DENIED)
            if path == '/admin/audit/events/' + ID_LEGACY:
                return self.respond(200, LEGACY)
            if path == '/admin/audit/sessions':
                return self.respond(200, {'sessions': SESSIONS, 'next': 'more'})
            if path == '/admin/audit/sessions/' + SESSION:
                return self.respond(200, {'events': list(reversed(EVENTS[:2])), 'next': ''})
            if path == '/admin/audit/sessions/' + SESSION2:
                return self.respond(200, {'events': NOCLASS[2:], 'next': ''})
            if path == '/admin/analytics/health':
                return self.respond(200, {'mode': 'A', 'last_ingest_ts': '2026-10-29T10:44:13Z'})
        return super().do_GET()


TRACE_LAYOUT_JS = """() => {
  const row = document.querySelector('#audit-trace-table tbody tr');
  const lines = node => { const r = document.createRange(); r.selectNodeContents(node);
    return new Set([...r.getClientRects()].filter(x => x.width > 0).map(x => Math.round(x.top))).size; };
  // app.js wraps every table.data in a .table-scroll region: the horizontal-scroll container.
  const wrap = document.querySelector('#audit-trace-table').closest('.table-scroll');
  const lat = document.querySelector('#audit-trace-table thead th:last-child');
  const w = wrap.getBoundingClientRect(), l = lat.getBoundingClientRect();
  return { timeLines: lines(row.cells[0]), modelLines: lines(row.cells[4].firstChild.firstChild),
           latencyLines: lines(row.cells[7]), latInside: l.left >= w.left - 1 && l.right <= w.right + 1,
           latClipped: lat.scrollWidth > lat.clientWidth, header: lat.textContent };
}"""


def run_scenario(page, base, width, scheme, screenshots):
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))

    def on_console(m):
        if m.type != 'error':
            return
        if m.location.get('url', '').endswith(EXPECTED_ERROR_URL_SUFFIX):
            return
        errors.append(m.text + ' @ ' + m.location.get('url', ''))
    page.on('console', on_console)
    wait = lambda js: page.wait_for_function(js)  # noqa: E731
    text = lambda sel: page.locator(sel).inner_text()  # noqa: E731

    page.goto(base + '/admin/ui/')
    page.locator('#token').fill('fixture-admin')
    page.locator('#token-form button').click()
    page.locator('#shell').wait_for(state='visible')
    page.locator('button.nav[data-view="audit"]').click()
    wait("() => document.querySelectorAll('#audit-trace-table tbody tr').length === 3")

    # Header cards: four stats; auto card carries allow/block/unparsed counts from the summary.
    assert page.locator('#audit-cards .audit-stat').count() == 4
    auto_card = page.locator('#audit-cards .audit-stat').nth(3).inner_text()
    for part in (f"{en('audit.verdictAllow')} 5", f"{en('audit.verdictBlock')} 1", f"{en('audit.verdictUnknown')} 1"):
        assert part in auto_card, (part, auto_card)

    # Every fixture row carries a class here, so the "class not reported" hint stays hidden.
    assert page.locator('#audit-class-hint').is_hidden()

    # Traces table: time on one line, the long model id wraps at word-ish
    # boundaries only, latency is whole and inside the scroll container.
    layout = page.evaluate(TRACE_LAYOUT_JS)
    if not layout['latInside']:  # narrow screens scroll the table inside its wrapper
        page.evaluate("() => { const w = document.querySelector('#audit-trace-table').closest('.table-scroll'); w.scrollLeft = w.scrollWidth; }")
        layout = page.evaluate(TRACE_LAYOUT_JS)
        assert width < 1280, f'latency must be visible without scrolling at {width}px: {layout}'
    assert layout['timeLines'] == 1, layout
    assert layout['latencyLines'] == 1 and layout['latInside'] and not layout['latClipped'], layout
    assert 1 <= layout['modelLines'] <= 3, layout
    assert layout['header'] == en('audit.latency'), layout
    page.evaluate("() => { document.querySelector('#audit-trace-table').closest('.table-scroll').scrollLeft = 0; }")
    assert text('#audit-trace-table thead th:first-child') == en('audit.time')

    # Main record: 4 waterfall rows, trace link, chain position shows hash next to prev_hash.
    page.locator('#audit-trace-table tbody tr').first.click()
    wait("() => document.querySelectorAll('#audit-detail .audit-wf-row').length === 4")
    if screenshots:
        page.screenshot(path=str(screenshots / f'audit-{width}-{scheme}.png'), full_page=True)
    assert page.locator('#audit-detail a[href*="traceId=' + TRACE + '"]').count() == 1
    detail = text('#audit-detail')
    assert HASH in detail and 'sha256-prev-fixture' in detail, detail
    assert en('audit.recordHash') in detail and en('audit.prevHash') in detail

    # "Open in Virtual keys" switches view and pre-fills the key filter.
    page.locator('#audit-detail button.linklike', has_text=en('audit.openKey')).click()
    page.locator('#view-keys').wait_for(state='visible')
    assert page.locator('#key-search').input_value() == KEY_A
    page.locator('button.nav[data-view="audit"]').click()
    wait("() => document.querySelectorAll('#audit-trace-table tbody tr').length === 3")

    # Latency without attempts: neutral gateway bar and the noAttempt hint, not "no timing".
    page.locator('#audit-trace-table tbody tr').nth(1).click()
    wait(f"() => document.getElementById('audit-detail').textContent.includes({json.dumps(en('audit.noAttempt'))})")
    detail = text('#audit-detail')
    assert en('audit.noTiming') not in detail
    assert page.locator('#audit-detail .audit-wf-row').count() == 1

    # Legacy record (total_ms 0): "no timing was recorded", no NaN anywhere.
    page.locator('#audit-trace-table tbody tr').nth(2).click()
    wait(f"() => document.getElementById('audit-detail').textContent.includes({json.dumps(en('audit.noTiming'))})")
    detail = text('#audit-detail')
    assert 'NaN' not in detail and en('audit.noAttempt') not in detail

    # No row reports a class: the traces hint appears.
    page.locator('#audit-team').fill('noclass')
    page.locator('#audit-team').dispatch_event('change')
    page.locator('#audit-class-hint').wait_for(state='visible')
    page.locator('#audit-team').fill('')
    page.locator('#audit-team').dispatch_event('change')
    page.locator('#audit-class-hint').wait_for(state='hidden')
    wait("() => document.querySelectorAll('#audit-trace-table tbody tr').length === 3")

    # Sessions: path with encoded ':' resolves, subagent indented, truncation note shown.
    page.locator('#audit-tab-sessions').click()
    wait("() => document.querySelectorAll('#audit-session-table tbody tr').length === 2")
    assert en('audit.showingFirst').replace('{n}', '2') == text('#audit-sessions-note').strip()
    page.locator('#audit-session-table tbody tr').first.click()
    wait("() => document.querySelectorAll('#audit-session-detail .audit-tl-item').length === 2")
    assert page.locator('#audit-session-detail .audit-tl-item.indent').count() == 1
    assert en('audit.classNotReported') not in text('#audit-session-detail')
    # A session where no event has a class shows the classNotReported hint.
    page.locator('#audit-session-table tbody tr').nth(1).click()
    wait("() => document.querySelectorAll('#audit-session-detail .audit-tl-item').length === 1")
    assert en('audit.classNotReported') in text('#audit-session-detail')

    # PII tab: one detection row, truncation note shown.
    page.locator('#audit-tab-pii').click()
    wait("() => document.querySelectorAll('#audit-pii-table tbody tr').length === 1")
    assert en('audit.showingFirst').replace('{n}', '1') == text('#audit-pii-note').strip()

    # Denials tab: denial row, auto-table lists block + unknown verdicts, unparsed warning visible.
    page.locator('#audit-tab-denials').click()
    wait("() => document.querySelectorAll('#audit-auto-table tbody tr').length === 2")
    assert 'key_rate_limited' in text('#audit-deny-table')
    auto_rows = text('#audit-auto-table')
    assert 'block' in auto_rows and 'unknown' in auto_rows
    assert page.locator('#audit-auto-warning').is_visible()
    assert en('audit.showingFirst').replace('{n}', '2') == text('#audit-auto-note').strip()

    overflow = page.evaluate('document.documentElement.scrollWidth - window.innerWidth')
    assert overflow <= 0, f'horizontal overflow {overflow}px at {width}px'
    if screenshots:
        page.screenshot(path=str(screenshots / f'audit-{width}-{scheme}-denials.png'), full_page=True)

    # Empty auto result shows the current empty-state copy.
    page.locator('#audit-team').fill('empty-team')
    page.locator('#audit-team').dispatch_event('change')
    wait(f"() => document.getElementById('audit-auto-table').textContent.includes({json.dumps(en('audit.autoNotObserved'))})")

    assert not errors, errors


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--screenshots', type=Path)
    args = parser.parse_args()
    if args.screenshots:
        args.screenshots.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f'http://127.0.0.1:{server.server_port}'
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        for width, scheme in [(1280, 'light'), (390, 'dark')]:
            page = browser.new_page(viewport={'width': width, 'height': 1000}, color_scheme=scheme, locale='en-US')
            try:
                run_scenario(page, base, width, scheme, args.screenshots)
            finally:
                page.close()
        browser.close()
    server.shutdown()
    print('audit_qa.py: ok')


if __name__ == '__main__':
    main()
