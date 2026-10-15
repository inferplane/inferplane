// Runs the embedded application and real event handlers, using only Node stdlib.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
class Element {
  constructor(tag = 'div') { this.tagName = tag; this.children = []; this.listeners = {}; this.attributes = {}; this.dataset = {}; this.value = ''; this.hidden = false; this.disabled = false; this.checked = false; this.className = ''; this.open = false;
    this.classList = { toggle: (name, on) => { const c = new Set(this.className.split(' ')); on ? c.add(name) : c.delete(name); this.className = [...c].join(' '); }, add: name => { this.className += ' ' + name; } }; }
  set textContent(v) { this.text = String(v); this.children = []; }
  get textContent() { return (this.text || '') + this.children.map(c => c.textContent).join(''); }
  append(...nodes) { for (const node of nodes) this.appendChild(node); }
  appendChild(node) { this.children.push(node); node.parentElement = this; return node; }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  removeAttribute(k) { delete this.attributes[k]; }
  addEventListener(k, fn) { (this.listeners[k] ||= []).push(fn); }
  async fire(k, event = {}) { for (const fn of this.listeners[k] || []) await fn({ preventDefault() {}, target: this, ...event }); await flush(); }
  querySelectorAll(selector) { return this.children.flatMap(c => [ ...(matches(c, selector) ? [c] : []), ...c.querySelectorAll(selector) ]); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  showModal() { this.open = true; }
  close() { this.open = false; }
  focus() {}
  remove() {}
  click() { return this.fire('click'); }
}
function matches(el, selector) {
  if (selector.startsWith('.')) return el.className.split(' ').includes(selector.slice(1));
  const field = selector.match(/^\[data-field="([^"]+)"\]$/);
  if (field) return el.dataset.field === field[1];
  return el.tagName === selector;
}
const flush = async () => { for (let n = 0; n < 25; n++) await Promise.resolve(); };
const source = fs.readFileSync(path.join(__dirname, '../static/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../static/index.html'), 'utf8');
const doc = { apiVersion: 'inferplane.dev/v1alpha1', kind: 'GovernancePolicy', metadata: { name: 'engineering', generation: 12 }, spec: { subject: { team: 'engineering' }, rules: [
  { name: 'daily', failurePolicy: 'FailClosed', budget: { period: 'CalendarDay', limitMilliUSD: 20000, hardCap: true, lease: { grantMilliUSD: 20, renewInterval: '10s' }, adminContact: '#ops' } },
  { name: 'monthly', failurePolicy: 'FailClosed', budget: { period: 'CalendarMonth', limitMilliUSD: 300000, hardCap: false } },
  { name: 'rate', failurePolicy: 'FailClosed', rate: { rpm: 100, tpm: 100000 } },
  { name: 'privacy', failurePolicy: 'FailClosed', sensitiveData: { onDetected: 'Block', onUninspectable: 'Block' } },
] } };
const rows = [{ key: 'engineering', spent_micro_usd: 1496085, input_tokens: 2, output_tokens: 5, cache_read_tokens: 0, cache_write_5m_tokens: 271999, cache_write_1h_tokens: 0 }];
function boot(options = {}) {
  const elements = new Map();
  for (const m of html.matchAll(/<([\w-]+)[^>]*\bid="([^"]+)"[^>]*>/g)) {
    const e = new Element(m[1]); e.hidden = /\bhidden\b/.test(m[0]); e.disabled = /\bdisabled\b/.test(m[0]); elements.set(m[2], e);
  }
  for (const id of ['teams', 'models', 'fleet-table', 'users-table', 'user-models', 'budget-table']) elements.get(id).append(new Element('tbody'));
  const $ = id => { assert(elements.has(id), 'unknown DOM id ' + id); return elements.get(id); };
  const calls = [], writes = [], redirects = [], timers = new Map();
  let now = options.now || Date.now(), timerID = 0;
  class ClockDate extends Date {
    constructor(...args) { super(...(args.length ? args : [now])); }
    static now() { return now; }
  }
  const pkce = options.storage || new Map([['ip_sso_verifier', 'fixture-verifier'], ['ip_sso_state', 'fixture-state'], ['ip_sso_nonce', 'fixture-nonce']]);
  const idToken = options.idToken || ['fixture-header', Buffer.from(JSON.stringify({ nonce: 'fixture-nonce', token_use: 'id', iss: 'https://fixture-idp.invalid', aud: 'fixture-client', sub: 'fixture-operator', exp: Math.floor(now / 1000) + 3600 })).toString('base64url'), 'fixture-signature'].join('.');
  let jwtInFlight = 0, maxJWTInFlight = 0, jwtDenied = 0;
  let usage = options.usage || { total_micro_usd: 1496085, rows, degraded: false };
  let userUsage = options.users || { total_micro_usd: 1496085, rows: [{ ...rows[0], key: 'developer-a' }], degraded: false };
  let policy = { generation: 'g2', writable: options.writable !== false, policies: [{ name: 'engineering', policy: structuredClone(doc) }] };
  let fail = options.fail || {};
  let custom = options.custom;
  const resp = (status, body) => ({ status, ok: status >= 200 && status < 300, text: async () => typeof body === 'string' ? body : JSON.stringify(body), json: async () => body });
  const context = vm.createContext({
    document: Object.assign(new Element('document'), { getElementById: $, createElement: tag => new Element(tag), createElementNS: (_, tag) => new Element(tag), documentElement: new Element('html'), body: new Element('body') }),
    URLSearchParams, URL, Date: ClockDate, BigInt, Intl, AbortController, structuredClone, Blob, atob, btoa, TextEncoder, TextDecoder,
    crypto: require('node:crypto').webcrypto,
    setTimeout: (fn, delay) => { const id = ++timerID; timers.set(id, { fn, due: now + delay }); return id; },
    clearTimeout: id => timers.delete(id),
    matchMedia: () => ({ matches: false }), confirm: () => true,
    location: { search: options.sso ? '?code=fixture-code&state=' + (options.callbackState || 'fixture-state') : '', origin: 'http://localhost', pathname: '/ui/', assign: url => redirects.push(url) },
    history: { replaceState() {} }, sessionStorage: { getItem: k => pkce.get(k), setItem: (k, v) => pkce.set(k, String(v)), removeItem: k => pkce.delete(k) },
    fetch: async (url, init = {}) => {
      calls.push({ url, init });
      if (url === '/ui/auth/config') return options.sso || options.ssoEnabled ? resp(200, { sso: true, issuer: 'https://fixture-idp.invalid', client_id: 'fixture-client' }) : resp(404, {});
      if (url === 'https://fixture-idp.invalid/.well-known/openid-configuration') return resp(200, { authorization_endpoint: 'https://fixture-idp.invalid/auth', token_endpoint: 'https://fixture-idp.invalid/token' });
      if (url === 'https://fixture-idp.invalid/token') {
        const body = new URLSearchParams(init.body);
        if (options.tokenEndpoint) return options.tokenEndpoint(body, resp);
        if (body.get('grant_type') === 'refresh_token') return resp(200, { id_token: options.refreshedIDToken || idToken, refresh_token: 'fixture-rotated-refresh' });
        assert.equal(body.get('code_verifier'), 'fixture-verifier');
        assert.equal(body.get('redirect_uri'), 'http://localhost/ui/');
        return resp(200, { id_token: idToken, access_token: 'fixture-access-not-the-id-token', ...(options.refreshToken ? { refresh_token: options.refreshToken } : {}) });
      }
      if (options.sso && url.startsWith('/v1alpha1/')) {
        assert((options.acceptTokens || [idToken]).some(token => init.headers.Authorization === 'Bearer ' + token), 'SSO used the wrong token kind');
        if (options.rejectConcurrentJWT && jwtInFlight) { jwtDenied++; return resp(401, {}); }
        jwtInFlight++; maxJWTInFlight = Math.max(maxJWTInFlight, jwtInFlight);
        await new Promise(resolve => setTimeout(resolve, 10));
        jwtInFlight--;
      }
      if (custom) { const r = await custom(url, init, resp); if (r) return r; }
      if (init.method === 'PUT' || init.method === 'DELETE') {
        writes.push({ url, init, body: init.body ? JSON.parse(init.body) : null });
        if (options.persistWrites && !options.writeFailure) {
          const name = decodeURIComponent(url.split('/').at(-1));
          policy.policies = policy.policies.filter(v => v.name !== name);
          if (init.method === 'PUT') policy.policies.push({ name, policy: JSON.parse(init.body) });
        }
        return resp(options.writeFailure || 204, { error: 'write refused' });
      }
      const pathname = url.split('?')[0];
      if (fail[pathname]) return resp(fail[pathname], { error: 'unavailable' });
      if (pathname === '/v1alpha1/usage') return resp(200, new URLSearchParams(url.split('?')[1]).get('group_by') === 'user' ? userUsage : usage);
      if (pathname === '/v1alpha1/usage/charts') return resp(200, options.charts || { total_micro_usd: 1496085, days: [{ day: new Date().toISOString().slice(0, 10), spent_micro_usd: 1496085 }], models: [{ key: 'model-a', spent_micro_usd: 1496085 }], model_count: 1, other_micro_usd: 0 });
      if (pathname === '/v1alpha1/usage/export') return resp(200, options.exportRows || []);
      if (pathname === '/v1alpha1/policies') return resp(200, policy);
      if (pathname === '/v1alpha1/dataplanes') return resp(200, { generation: 'g2', apiVersions: ['inferplane.dev/v1alpha1'], dataplanes: {
        'node-a': { generation: 'g2', apiVersions: ['inferplane.dev/v1alpha1'], lastSeen: new Date().toISOString() },
        'node-b': { generation: 'g1', apiVersions: ['older'], lastSeen: new Date().toISOString(), rejections: [{ policy: 'privacy', rule: 'pii', reason: '<script>not executable</script>' }] },
      } });
      throw Error('unexpected request ' + url);
    }, console,
  });
  vm.runInContext(source, context);
  return { $, document: context.document, calls, writes, pkce, redirects, timers,
    advance: ms => { now += ms; },
    runTimers: async () => { for (const [id, timer] of [...timers]) if (timer.due <= now) { timers.delete(id); await timer.fn(); } await flush(); },
    jwtState: () => ({ maxJWTInFlight, jwtDenied }), setUsage: v => usage = v, setUsers: v => userUsage = v, setFailure: v => fail = v, setCustom: v => custom = v, policy,
    connect: async () => { $('token').value = 'read-secret'; await $('connect-form').fire('submit'); },
    credential: async () => { $('write-token').value = 'write-secret'; await $('write-token-form').fire('submit'); } };
}
module.exports = { boot, flush };
if (require.main === module) {
process.exitCode = 1; // unresolved promises must not exit as a successful suite
(async () => {
  // The deployed CP's JWKS miss limiter refuses concurrent signature checks.
  // Exercise the actual callback/code exchange, not just a pasted read token.
  let sso = boot({ sso: true, rejectConcurrentJWT: true });
  await new Promise(resolve => setTimeout(resolve, 100)); await flush();
  assert.equal(sso.$('workspace-content').hidden, false, 'SSO bootstrap was rejected by concurrent JWT verification');
  assert.deepEqual(sso.jwtState(), { maxJWTInFlight: 1, jwtDenied: 0 });
  for (const key of ['ip_sso_verifier', 'ip_sso_state', 'ip_sso_nonce']) assert.equal(sso.pkce.has(key), false, 'PKCE state survived the callback');
  assert.equal(sso.pkce.size, 1, 'storage must contain only the credential-free resume marker');
  assert.equal(sso.$('new-policy').disabled, false, 'SSO identity did not retain its policy write path');
  await sso.$('refresh').fire('click');
  assert.deepEqual(sso.jwtState(), { maxJWTInFlight: 1, jwtDenied: 0 }, 'refresh reintroduced concurrent signature verification');
  assert.equal(sso.$('workspace-content').hidden, false);

  // Genuine authorization failures remain fail-closed; serialization is not
  // an auth retry, and a valid nonce is not a substitute for server admission.
  const deniedSSO = boot({ sso: true, fail: { '/v1alpha1/usage': 401 } });
  await new Promise(resolve => setTimeout(resolve, 100)); await flush();
  assert.equal(deniedSSO.$('workspace-content').hidden, true);
  assert.equal(deniedSSO.$('auth-section').hidden, false);
  for (const group of ['team', 'user']) assert.equal(deniedSSO.calls.filter(c => c.url.startsWith('/v1alpha1/usage?') && c.url.includes('group_by=' + group)).length, 1, 'SSO silently retried the unauthorized ' + group + ' query');
  for (const options of [{ callbackState: 'wrong-state' }, { idToken: 'fixture-header.' + Buffer.from('{"nonce":"wrong-nonce"}').toString('base64url') + '.fixture-signature' }]) {
    const rejectedCallback = boot({ sso: true, ...options });
    await flush();
    assert.equal(rejectedCallback.$('workspace-content').hidden, true);
    assert.equal(rejectedCallback.calls.filter(c => c.url.startsWith('/v1alpha1/')).length, 0);
    assert.equal(rejectedCallback.pkce.size, 0);
  }

  // Disconnect must fence queued OIDC requests as well as the in-flight reply.
  const queued = boot({ sso: true });
  await new Promise(resolve => setTimeout(resolve, 100)); await flush();
  let releaseQueued;
  queued.setCustom((url, init, resp) => url.startsWith('/v1alpha1/usage?') ? new Promise(r => { releaseQueued = () => r(resp(200, { total_micro_usd: 0, rows: [] })); }) : null);
  const callsBeforeQueue = queued.calls.length;
  const queuedRefresh = queued.$('refresh').fire('click');
  while (!releaseQueued) await new Promise(resolve => setTimeout(resolve, 1));
  await queued.$('disconnect').fire('click'); releaseQueued(); await queuedRefresh;
  assert.equal(queued.calls.length, callsBeforeQueue + 1, 'queued old-session requests still sent after disconnect');
  assert.equal(queued.$('workspace-content').hidden, true);
  assert.equal(queued.$('stat-spend').textContent, '—');

  let app = boot(); await app.connect();
  assert.equal(app.$('workspace-content').hidden, false, 'valid connection did not open workspace');
  assert.equal(app.$('token').value, '', 'read token remained in input');
  assert.equal(app.$('stat-spend').title, '$1.496085', 'microUSD display lost precision');
  assert.equal(app.$('stat-tokens').title, '272,006', 'cache or output was double counted');
  assert.equal(app.$('stat-nodes').textContent, '2');
  for (const c of app.calls.filter(c => c.url.startsWith('/v1alpha1/'))) {
    assert.equal(c.init.redirect, 'error', 'a bearer may follow redirects');
    assert.equal(c.init.credentials, 'omit', 'ambient cookies were sent');
    assert.equal(c.init.cache, 'no-store');
  }
  assert.match(app.$('fleet-coverage').textContent, /1.*2/);
  assert.match(app.$('attention-list').textContent, /generation|rejection/i);
  assert.equal(app.$('new-policy').disabled, true, 'heartbeat token became a write credential');
  await app.credential();
  assert.equal(app.$('write-token').value, '');
  assert.equal(app.$('new-policy').disabled, false);
  const card = app.$('policies-list').querySelector('.policy-card');
  const before = structuredClone(app.policy.policies[0].policy);
  const limit = card.querySelector('[data-field="daily-limit"]');
  limit.value = '25000';
  await card.querySelector('.policy-save').fire('click');
  assert.equal(app.writes.length, 0, 'write occurred without review confirmation');
  assert.equal(app.$('policy-dialog').open, true);
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes[0].init.headers.Authorization, 'Bearer write-secret');
  const saved = app.writes[0].body;
  assert.equal(saved.spec.rules[0].budget.limitMilliUSD, 25000);
  assert.equal(saved.spec.rules[0].budget.period, 'CalendarDay');
  assert.deepEqual(saved.spec.rules.slice(1), before.spec.rules.slice(1), 'round trip changed unedited rules');
  assert.equal(saved.spec.rules[2].failurePolicy, 'FailClosed', 'save weakened failure policy');
  assert.deepEqual(app.policy.policies[0].policy, before, 'save mutated fetched snapshot');
  await app.$('disconnect').fire('click');
  assert.equal(app.$('workspace-content').hidden, true);
  assert.equal(app.$('stat-spend').textContent, '—', 'disconnect retained spend');
  assert.equal(app.$('policies-list').children.length, 0, 'disconnect retained policy data');
  assert.equal(app.$('dialog-description').textContent, '', 'disconnect retained policy review context');

  app = boot({ fail: { '/v1alpha1/usage': 401 } }); await app.connect();
  assert.equal(app.$('workspace-content').hidden, true, 'invalid auth unlocked console');
  assert.equal(app.$('auth-section').hidden, false);

  app = boot({ usage: '{"total_micro_usd":9007199254740993,"rows":[],"degraded":true}' }); await app.connect();
  assert.equal(app.$('stat-spend').title, '$9,007,199,254.740993', 'int64 JSON was rounded through JS Number');
  assert.equal(app.$('degraded-banner').hidden, false);
  app.$('since').value = '2026-10-07'; app.$('until').value = '2026-10-07';
  await app.$('apply-range').fire('click');
  assert(app.calls.some(c => c.url.includes('until=2026-10-08T00%3A00%3A00.000Z')), 'end date was not made inclusive');
  app.$('since').value = '2026-10-09'; const callsBefore = app.calls.length;
  await app.$('apply-range').fire('click');
  assert.equal(app.calls.length, callsBefore, 'invalid date range sent requests');

  app = boot({ writable: false }); await app.connect(); await app.credential();
  assert.equal(app.$('new-policy').disabled, true, 'credential bypassed file-authoritative store');
  assert.equal(app.$('policies-readonly').hidden, false);

  app = boot({ fail: { '/v1alpha1/policies': 404, '/v1alpha1/dataplanes': 404 } }); await app.connect();
  assert.equal(app.$('workspace-content').hidden, false, 'telemetry-only deployment cannot open console');
  assert.equal(app.$('stat-nodes').textContent, '—', 'unmounted fleet API was presented as zero nodes');

  // A deferred reply from an old session must not repopulate a disconnected page.
  app = boot(); await app.connect();
  let release;
  app.setCustom((url, init, resp) => url.startsWith('/v1alpha1/usage?') && url.includes('group_by=team') ? new Promise(r => { release = () => r(resp(200, { total_micro_usd: 999999999, rows })); }) : null);
  const pending = app.$('refresh').fire('click'); await flush();
  await app.$('disconnect').fire('click'); release(); await pending;
  assert.equal(app.$('stat-spend').textContent, '—', 'late response repopulated disconnected page');

  // Unsafe/fractional integer form values cannot be submitted as financial policy.
  app = boot(); await app.connect(); await app.credential();
  for (const invalid of ['-1', '1.5', '9007199254740992']) {
    app.$('policies-list').querySelector('[data-field="daily-limit"]').value = invalid;
    await app.$('policies-list').querySelector('.policy-save').fire('click');
    assert.equal(app.$('policy-dialog').open, false, 'invalid financial value reached confirmation');
  }

  // Missing/fractional counts do not leave the old snapshot looking current.
  app = boot(); await app.connect();
  app.setUsage({ total_micro_usd: 1.5, rows });
  await app.$('refresh').fire('click');
  assert.equal(app.$('stat-spend').textContent, '—');
  assert.match(app.$('teams').textContent, /integer/i);

  // Late model replies cannot be displayed under another team's heading.
  app = boot(); await app.connect();
  let releaseFirst;
  app.setUsage({ total_micro_usd: 2000000, rows: [...rows, { ...rows[0], key: 'platform', spent_micro_usd: 503915 }] });
  await app.$('refresh').fire('click');
  app.setCustom((url, init, resp) => {
    if (!url.includes('group_by=model')) return null;
    if (url.includes('team=engineering')) return new Promise(r => { releaseFirst = () => r(resp(200, { total_micro_usd: 1, rows: [{ ...rows[0], key: 'old-model' }] })); });
    return resp(200, { total_micro_usd: 2, rows: [{ ...rows[0], key: 'new-model' }] });
  });
  const buttons = app.$('teams').querySelectorAll('.team-button');
  const pendingModel = buttons[0].fire('click'); await flush();
  await buttons[1].fire('click'); releaseFirst(); await pendingModel;
  assert.equal(app.$('model-team-label').textContent, 'Models · platform');
  assert.match(app.$('models').textContent, /new-model/);
  assert(!app.$('models').textContent.includes('old-model'));

  // A refused mutation retains the review payload and the selected read session.
  app = boot({ writeFailure: 403 }); await app.connect(); await app.credential();
  await app.$('policies-list').querySelector('.policy-save').fire('click');
  const review = app.$('policy-review').textContent;
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('policy-dialog').open, true);
  assert.equal(app.$('policy-review').textContent, review);
  assert.equal(app.$('workspace-content').hidden, false);
  assert.equal(app.$('dialog-error').hidden, false);

  // Typed delete confirmation does not send a destructive request on a mismatch.
  app = boot(); await app.connect(); await app.credential();
  await app.$('policies-list').querySelector('.policy-delete').fire('click');
  app.$('policy-editor').value = 'wrong'; await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes.length, 0);
  app.$('policy-editor').value = 'engineering'; await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes[0].init.method, 'DELETE');
  assert.equal(app.writes[0].init.headers.Authorization, 'Bearer write-secret');

  // A large fetched int64 stays numeric and exact on a whole-document round trip.
  const largeDoc = JSON.stringify({ generation: 'g2', writable: true, policies: [{ name: 'engineering', policy: doc }] })
    .replace('"generation":12', '"generation":9007199254740993');
  app = boot({ custom: (url, init, resp) => url === '/v1alpha1/policies' ? resp(200, largeDoc) : null });
  await app.connect(); await app.credential();
  await app.$('policies-list').querySelector('.policy-save').fire('click');
  assert(app.$('policy-review').textContent.includes('9007199254740993'));
  await app.$('dialog-confirm').fire('click');
  assert(app.writes[0].init.body.includes('"generation":9007199254740993'), 'lossless integer was quoted or rounded on write');

  // New policy mode must not silently overwrite an existing named document.
  app = boot(); await app.connect(); await app.credential(); await app.$('new-policy').fire('click');
  app.$('policy-editor').value = JSON.stringify(doc); await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes.length, 0);
  assert.match(app.$('dialog-error').textContent, /already exists/);

  // Fractional/exponent monetary literals cannot be rounded into an integer cap.
  app.$('policy-editor').value = JSON.stringify(doc).replace('"limitMilliUSD":20000', '"limitMilliUSD":2.00000000000000001e4');
  await app.$('dialog-confirm').fire('click');
  assert.match(app.$('dialog-error').textContent, /integer JSON numbers/);
  console.log('control-plane dashboard behavioral checks passed');
  process.exitCode = 0;
})().catch(e => { console.error(e); process.exitCode = 1; });
}
