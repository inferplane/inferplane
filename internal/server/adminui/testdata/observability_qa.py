"""Exercise the Observability view with synthetic /admin/observability responses.
Run: python3 internal/server/adminui/testdata/observability_qa.py --screenshots <local-scratch>/obs-qa
Requires existing Playwright + Chromium. No service, collector or credentials.
"""
import argparse
import copy
import json
import re
import sys
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse

sys.path.insert(0, str(Path(__file__).resolve().parent))
import browser_qa  # noqa: E402
from playwright.sync_api import sync_playwright  # noqa: E402

I18N = (browser_qa.STATIC / 'i18n.js').read_text(encoding='utf-8')


def en(key):
    """English string for an i18n key, read from i18n.js so copy lives in one place."""
    m = re.search(r'"' + re.escape(key) + r'": \{ en: "((?:[^"\\]|\\.)*)"', I18N)
    assert m, f'missing i18n key {key}'
    return json.loads('"' + m.group(1) + '"')


REV_L0, REV_L1 = 'sha256:' + '1' * 64, 'sha256:' + '2' * 64
REV_R0, REV_R1 = 'sha256:' + '3' * 64, 'sha256:' + '4' * 64
LONG_ENDPOINT = 'otel-collector.observability.svc.cluster.local:4318'
# A pod-style hostname plus the per-run ULID, as instanceID() builds it.
INSTANCE = 'inferplane-7d9f8c6b5-x2k4p-01JC8X3V4N5Q6R7S8T9V0WXYZA'
OBS = {}


def reset(cp=False, admin=True, legacy_stats=True):
    OBS.clear()
    OBS.update(legacy_stats=legacy_stats, cp=cp, paused=False, limits_rev=REV_L0, rules_rev=REV_R0, rules=[], conflict=False, puts=[], gets=0)
    browser_qa.STATE['admin'] = admin


def stat(signal, sent, dropped, sampled, error=''):
    row = {'signal': signal, 'outcomes': {'sent': sent, 'dropped_queue_full': dropped, 'dropped_oversize': 0, 'dropped_export_error': 0,
                                          'withheld_limit': 0, 'sampled_out': sampled}, 'last_success_at': '2026-10-29T10:05:00Z'}
    if error:
        row.update(last_error_at='2026-10-29T10:06:00Z', last_error=error)
    return row


def status():
    paused = OBS['paused']
    rules = ({'editable': False, 'reason': 'control_plane', 'content_rules': False, 'console': []} if OBS['cp'] else
             {'editable': True, 'revision': OBS['rules_rev'], 'content_rules': False, 'console': copy.deepcopy(OBS['rules'])})
    return {
        'configured': True, 'instance': INSTANCE, 'policy_source': 'control_plane' if OBS['cp'] else 'files', 'service_name': 'inferplane',
        'counters_since': '2026-10-29T10:00:00Z', 'limits': {'editable': True, 'revision': OBS['limits_rev']}, 'rules': rules,
        'exporters': [
            {'name': 'corp', 'endpoint': LONG_ENDPOINT, 'protocol': 'http', 'tls': True, 'header_names': ['authorization'],
             'signals': ['audit', 'traces'], 'effective_signals': [] if paused else ['audit', 'traces'], 'queue_limit_bytes': 67108864,
             'queued_bytes': 4096, 'limit': {'paused': True} if paused else {}, 'stats': [stat('audit', 12, 1, 0), stat('traces', 30, 0, 9)]},
            {'name': 'vault', 'endpoint': 'secure-collector.internal:4317', 'protocol': 'grpc', 'tls': True, 'header_names': [],
             'data_boundary': 'internal', 'signals': ['content'], 'effective_signals': ['content'], 'max_content_bytes': 2097152,
             'effective_max_content_bytes': 2097152, 'queue_limit_bytes': 67108864, 'queued_bytes': 0, 'limit': {},
             'stats': [stat('content', 3, 2, 0, error='collector code Unavailable')]}],
        'legacy_otel': dict({'endpoint': 'localhost:4318', 'protocol': 'http', 'tls': False, 'sample_ratio': 0.25},
                            **({'stats': stat('traces', 5, 0, 15)} if OBS['legacy_stats'] else {})),
        'decisions': [{'signal': s, 'decisions': {'selected': 4, 'no_rule': 20, 'content_conflict': 1 if s == 'content' else 0, 'withheld_limit': 0}}
                      for s in ('traces', 'audit', 'content')],
        'selections': [{'policy': 'engineering-observability', 'rule': 'export', 'source': 'files', 'subject': {'team': 'engineering'},
                        'targets': [{'signal': 'traces', 'exporter': 'corp', 'sample_percent': 20, 'effective_percent': 20, 'exported': not paused},
                                    {'signal': 'audit', 'exporter': 'corp', 'exported': not paused}]}],
    }


class Handler(browser_qa.Handler):
    # The base handler serves observability.js with the other static files (Task 11).
    def do_GET(self):
        path = urlparse(self.path).path
        if path.startswith('/admin/') and self.headers.get('Authorization') == 'Bearer fixture-admin':
            if path == '/admin/capabilities':
                return self.respond(200, {'analytics_index': 'A', 'teams_records': True, 'observability_export': True})
            if path == '/admin/observability':
                OBS['gets'] += 1
                return self.respond(200, status())
        return super().do_GET()

    def do_PUT(self):
        path = urlparse(self.path).path
        if path not in ('/admin/observability/limits', '/admin/observability/rules'):
            return super().do_PUT()
        if self.headers.get('Authorization') != 'Bearer fixture-admin':
            return self.respond(401, {})
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
        OBS['puts'].append((path, body))
        if OBS['conflict']:
            return self.respond(409, {'error': 'the state changed since it was read; reload and preview again',
                                      'code': 'revision_conflict', 'current_revision': REV_L1})
        if path.endswith('/limits'):
            assert body['expected_revision'] == OBS['limits_rev'], body
            res = {'dry_run': body['dry_run'], 'current_revision': OBS['limits_rev'], 'result_revision': REV_L1,
                   'changes': [{'exporter': 'corp', 'field': 'paused', 'before': False, 'after': True}],
                   'effects': [{'policy': 'engineering-observability', 'rule': 'export', 'signal': 'audit', 'exporter': 'corp',
                                'before': {'exported': True}, 'after': {'exported': False}}]}
            if not body['dry_run']:
                OBS.update(paused=True, limits_rev=REV_L1)
            return self.respond(200, res)
        assert body['expected_revision'] == OBS['rules_rev'], body
        res = {'dry_run': body['dry_run'], 'current_revision': OBS['rules_rev'], 'result_revision': REV_R1,
               'changes': [{'name': r['name'], 'action': 'added'} for r in body['rules']],
               'summary': {'added': len(body['rules']), 'changed': 0, 'removed': 0, 'unchanged': 0},
               'decisions': [{'subject': {'team': 'finance'}, 'before': {'traces': [], 'audit': []}, 'after': {'traces': [], 'audit': ['corp']}}]}
        if not body['dry_run']:
            OBS.update(rules=copy.deepcopy(body['rules']), rules_rev=REV_R1)
        return self.respond(200, res)


def watch_errors(page):
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))

    def on_console(m):
        url = m.location.get('url', '')
        # The shared harness serves /admin/auth/config as 404 on purpose, and the
        # conflict step provokes the 409 it asserts on; nothing else may fail.
        if m.type != 'error' or url.endswith('/admin/auth/config'):
            return
        if OBS.get('conflict') and urlparse(url).path in ('/admin/observability/limits', '/admin/observability/rules'):
            return
        errors.append(m.text + ' @ ' + url)
    page.on('console', on_console)
    return errors


def login(page, base):
    page.goto(base + '/admin/ui/')
    page.locator('#token').fill('fixture-admin')
    page.locator('#token-form button').click()
    page.locator('#shell').wait_for(state='visible')
    page.locator('button.nav[data-view="observability"]').click()


def starts(sel, text):
    return f"() => document.querySelector({json.dumps(sel)}).textContent.startsWith({json.dumps(text)})"


def run_scenario(page, base, width, scheme, shots):
    reset()
    errors = watch_errors(page)
    text = lambda sel: page.locator(sel).inner_text()  # noqa: E731
    login(page, base)
    page.wait_for_function("() => document.querySelectorAll('#obs-exporters tbody tr').length === 3")
    assert page.locator('#obs-signals tbody tr').count() == 4
    assert 'authorization' in text('#obs-exporters') and LONG_ENDPOINT in text('#obs-exporters')
    assert 'collector code Unavailable' in text('#obs-signals')
    assert page.locator('#obs-decisions tbody tr').count() == 3
    assert page.locator('#obs-selections tbody tr').count() == 2
    assert text('#obs-source').endswith(en('obs.source.files'))
    assert text('#obs-instance') == en('obs.instance').replace('{i}', INSTANCE), text('#obs-instance')
    # Each exporter's endpoint cell carries its data boundary; only vault declares one.
    boundary = en('obs.boundary').replace('{b}', 'internal')
    assert text('#obs-exporters tbody tr:nth-child(2)').count(boundary) == 1 and text('#obs-exporters').count(boundary) == 1
    assert text('#obs-exporters tbody tr:nth-child(1)').count(en('obs.boundary').split('{b}')[0]) == 0
    assert en('obs.notCounted') not in text('#obs-signals'), 'a legacy exporter with counters must not render the not-counted row'

    # Limits: preview; a change after the preview disables Apply; preview again; apply.
    corp = '#obs-limits-rows fieldset[data-exporter="corp"]'
    vault_cap = '#obs-limits-rows fieldset[data-exporter="vault"] input[data-field="max_content_bytes"]'
    page.locator(corp + ' input[data-field="paused"]').check()
    page.locator('#obs-limits-preview').click()
    page.wait_for_function("() => document.querySelectorAll('#obs-limits-diff li').length === 2")
    assert OBS['puts'][-1][1] == {'dry_run': True, 'expected_revision': REV_L0, 'exporters': {'corp': {'paused': True}}}, OBS['puts'][-1]
    assert page.locator('#obs-limits-apply').is_enabled()
    page.locator(vault_cap).fill('1024')
    assert page.locator('#obs-limits-apply').is_disabled(), 'an edit after the preview must disable Apply'
    page.locator(vault_cap).fill('')
    page.locator('#obs-limits-preview').click()
    page.wait_for_function("() => !document.getElementById('obs-limits-apply').disabled")
    if shots:
        page.screenshot(path=str(shots / f'observability-{width}-{scheme}.png'), full_page=True)
    puts = len(OBS['puts'])
    page.locator('#obs-limits-apply').click()
    page.wait_for_function(starts('#obs-limits-status', en('obs.applied').split('{r}')[0]))
    assert OBS['puts'][puts][1]['dry_run'] is False and OBS['puts'][puts][1]['expected_revision'] == REV_L0, OBS['puts'][puts]
    assert en('obs.paused') in text('#obs-exporters')
    assert text('#obs-selections').count(en('obs.withheld')) == 2

    # Rules: client-side validation sends nothing; a valid rule previews and applies.
    page.locator('#obs-rules-add').click()
    row = page.locator('#obs-rules-table tbody tr.obs-rule').last
    row.locator('[data-field="name"]').fill('finance')
    puts = len(OBS['puts'])
    page.locator('#obs-rules-preview').click()
    page.wait_for_function(f"() => document.getElementById('obs-rules-status').textContent.includes({json.dumps(en('obs.err.ruleName'))})")
    assert len(OBS['puts']) == puts, 'an invalid row reached the server'
    row.locator('[data-field="name"]').fill('console-finance')
    row.locator('[data-field="team"]').fill('finance')
    row.locator('[data-field="audit"]').select_option('corp')
    assert row.locator('[data-field="content"]').is_disabled(), 'content selection needs content_rules'
    # Cells keep words whole and the table scrolls instead: Remove never wraps per letter.
    remove_box, add_box = row.locator('button').bounding_box(), page.locator('#obs-rules-add').bounding_box()
    assert remove_box['height'] <= add_box['height'] + 1, f'Remove wraps: {remove_box} vs {add_box} at {width}px'
    page.locator('#obs-rules-preview').click()
    page.wait_for_function("() => document.querySelectorAll('#obs-rules-diff li').length === 2")
    assert OBS['puts'][-1][1]['rules'] == [{'name': 'console-finance', 'subject': {'team': 'finance'}, 'audit': {'exporter': 'corp'}}], OBS['puts'][-1]
    page.locator('#obs-rules-apply').click()
    page.wait_for_function(starts('#obs-rules-status', en('obs.applied').split('{r}')[0]))
    assert page.locator('#obs-rules-table tbody tr.obs-rule').count() == 1

    # A revision conflict shows the translated message and keeps Apply off.
    OBS['conflict'] = True
    page.locator(corp + ' input[data-field="paused"]').uncheck()
    page.locator('#obs-limits-preview').click()
    page.wait_for_function(starts('#obs-limits-status', en('obs.err.revision_conflict')))
    assert page.locator('#obs-limits-apply').is_disabled()

    overflow = page.evaluate('document.documentElement.scrollWidth - window.innerWidth')
    assert overflow <= 0, f'horizontal overflow {overflow}px at {width}px'
    if shots:
        page.screenshot(path=str(shots / f'observability-{width}-{scheme}-applied.png'), full_page=True)
    assert not errors, errors


def run_readonly(page, base):
    reset(cp=True)
    errors = watch_errors(page)
    login(page, base)
    page.locator('#obs-rules-readonly').wait_for(state='visible')
    assert page.locator('#obs-rules-readonly').inner_text() == en('obs.reason.control_plane')
    assert page.locator('#obs-rules-add').is_disabled() and page.locator('#obs-rules-preview').is_disabled()
    assert page.locator('#obs-source').inner_text().endswith(en('obs.source.control_plane'))
    assert page.locator('#obs-limits-preview').is_enabled(), 'limits narrow the local ceiling, so they stay editable'
    assert not errors, errors


def run_legacy(page, base):
    reset(legacy_stats=False)
    errors = watch_errors(page)
    login(page, base)
    page.wait_for_function("() => document.querySelectorAll('#obs-signals tbody tr').length === 4")
    row = page.locator('#obs-signals tbody tr').last
    cells = [c.strip() for c in row.locator('td').all_inner_texts()]
    assert cells[:2] == ['otel', 'traces'], cells
    assert cells[2:6] == [en('obs.notCounted')] * 4 and cells[6:] == ['—', '—'], cells
    assert page.evaluate('document.documentElement.scrollWidth - window.innerWidth') <= 0
    assert not errors, errors


def run_member(page, base):
    reset(admin=False)
    errors = watch_errors(page)
    login(page, base)
    page.locator('#obs-admin-only').wait_for(state='visible')
    assert OBS['gets'] == 0, 'a non-admin view fetched the status'
    assert page.locator('#obs-content').is_hidden()
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
        for check in (run_readonly, run_legacy, run_member):
            page = browser.new_page(viewport={'width': 1280, 'height': 1000}, color_scheme='light', locale='en-US')
            try:
                check(page, base)
            finally:
                page.close()
        browser.close()
    server.shutdown()
    print('observability_qa.py: ok')


if __name__ == '__main__':
    main()
