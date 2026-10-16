const assert = require('node:assert/strict');
const { boot, flush } = require('./dashboard.cjs');
const epoch = Date.now();
const claims = { nonce: 'fixture-nonce', token_use: 'id', iss: 'https://fixture-idp.invalid', aud: 'fixture-client', sub: 'fixture-operator' };
const jwt = (exp, extra = {}) => 'header.' + Buffer.from(JSON.stringify({ ...claims, exp, ...extra })).toString('base64url') + '.signature';
const wait = async () => { await new Promise(resolve => setTimeout(resolve, 110)); await flush(); };
(async () => {
  const initial = jwt(Math.floor(epoch / 1000) + 90);
  const renewed = jwt(Math.floor(epoch / 1000) + 3600);
  const app = boot({ sso: true, now: epoch, idToken: initial, refreshedIDToken: renewed, refreshToken: 'fixture-refresh', acceptTokens: [initial, renewed] });
  await wait();
  assert.equal(app.$('workspace-content').hidden, false);
  const marker = JSON.parse(app.pkce.get('ip_sso_session'));
  assert.deepEqual(Object.keys(marker).sort(), ['expiresAt', 'pending']);
  assert.equal(marker.pending, false);
  assert(marker.expiresAt > epoch && marker.expiresAt <= epoch + 8 * 3600 * 1000);
  for (const key of ['ip_sso_verifier', 'ip_sso_state', 'ip_sso_nonce']) assert.equal(app.pkce.has(key), false);
  assert(!JSON.stringify([...app.pkce]).includes(initial));
  assert(!JSON.stringify([...app.pkce]).includes('fixture-refresh'));
  assert(!JSON.stringify([...app.pkce]).includes('fixture-operator'));

  // A reload restores through the trusted IdP, without a saved bearer.
  const storage = new Map(app.pkce);
  const reload = boot({ ssoEnabled: true, storage, now: epoch });
  await wait();
  assert.equal(reload.redirects.length, 1);
  const redirect = new URL(reload.redirects[0]);
  assert.equal(redirect.origin, 'https://fixture-idp.invalid');
  assert.equal(redirect.searchParams.get('response_type'), 'code');
  assert.equal(redirect.searchParams.get('code_challenge_method'), 'S256');
  assert.equal(reload.calls.some(c => c.url.startsWith('/v1alpha1/')), false);
  assert.equal(JSON.parse(storage.get('ip_sso_session')).pending, true);
  const interrupted = boot({ ssoEnabled: true, storage, now: epoch });
  await wait();
  assert.equal(interrupted.redirects.length, 0, 'unfinished restoration caused a redirect loop');
  assert.equal(storage.has('ip_sso_session'), false);

  // Expiry renews once and future requests use the new ID token.
  app.advance(31000);
  await app.runTimers();
  await app.$('refresh').fire('click');
  const renewals = app.calls.filter(c => c.url.endsWith('/token') && new URLSearchParams(c.init.body).get('grant_type') === 'refresh_token');
  assert.equal(renewals.length, 1);
  assert.equal(new URLSearchParams(renewals[0].init.body).get('refresh_token'), 'fixture-refresh');
  assert(app.calls.filter(c => c.url.startsWith('/v1alpha1/')).slice(-5).every(c => c.init.headers.Authorization === 'Bearer ' + renewed));
  await app.$('disconnect').fire('click');
  assert.equal(app.pkce.has('ip_sso_session'), false);
  assert.equal(app.timers.size, 0);

  // Storage corruption and expiry cannot supply credentials or URLs.
  for (const value of ['bad-json', JSON.stringify({ expiresAt: epoch - 1, pending: false }), JSON.stringify({ expiresAt: epoch + 1000, pending: false, id_token: initial })]) {
    const saved = new Map([['ip_sso_session', value]]);
    const rejected = boot({ ssoEnabled: true, storage: saved, now: epoch });
    await wait();
    assert.equal(rejected.redirects.length, 0);
    assert.equal(saved.has('ip_sso_session'), false);
  }
  // Audience is a set: string/array forms and array ordering are equivalent.
  for (const [before, after] of [
    ['fixture-client', ['fixture-client']],
    [['fixture-client'], 'fixture-client'],
    [['fixture-client', 'second-client'], ['second-client', 'fixture-client']],
  ]) {
    const original = jwt(Math.floor(epoch / 1000) + 90, { aud: before });
    const replacement = jwt(Math.floor(epoch / 1000) + 3600, { aud: after });
    const equivalent = boot({ sso: true, now: epoch, idToken: original, refreshedIDToken: replacement,
      refreshToken: 'fixture-refresh', acceptTokens: [original, replacement] });
    await wait(); equivalent.advance(31000); await equivalent.runTimers();
    assert.equal(equivalent.$('workspace-content').hidden, false, 'equivalent audiences locked renewal');
    await equivalent.$('refresh').fire('click');
    assert(equivalent.calls.filter(c => c.url.startsWith('/v1alpha1/')).slice(-5)
      .every(c => c.init.headers.Authorization === 'Bearer ' + replacement));
    await equivalent.$('disconnect').fire('click');
  }
  for (const extra of [{ sub: 'other-user' }, { iss: 'https://other-idp.invalid' }, { aud: 'other-client' },
    { aud: ['fixture-client', 'other-client'] }, { aud: [] }, { aud: null },
    { aud: ['fixture-client', 7] }, { aud: { 0: 'fixture-client' } }, { aud: '' }]) {
    const bad = jwt(Math.floor(epoch / 1000) + 3600, extra);
    const rejected = boot({ sso: true, now: epoch, idToken: initial, refreshedIDToken: bad, refreshToken: 'fixture-refresh', acceptTokens: [initial] });
    await wait(); rejected.advance(31000); await rejected.runTimers();
    assert.equal(rejected.$('workspace-content').hidden, true);
    assert.equal(rejected.pkce.has('ip_sso_session'), false);
  }
  let release;
  const late = boot({ sso: true, now: epoch, idToken: initial, refreshToken: 'fixture-refresh', tokenEndpoint: async (body, resp) =>
    body.get('grant_type') === 'refresh_token' ? new Promise(resolve => { release = () => resolve(resp(200, { id_token: renewed })); }) : resp(200, { id_token: initial, refresh_token: 'fixture-refresh' }) });
  await wait(); late.advance(31000); const pending = late.runTimers(); await flush();
  assert(release); await late.$('disconnect').fire('click'); release(); await pending;
  assert.equal(late.$('workspace-content').hidden, true);
  assert.equal(late.pkce.has('ip_sso_session'), false, 'late renewal resurrected a disconnected session');
  assert.equal(late.timers.size, 0);
  let renewCount = 0;
  const rotation = boot({ sso: true, now: epoch, idToken: initial, acceptTokens: [initial, renewed], tokenEndpoint: async (body, resp) => {
    if (body.get('grant_type') !== 'refresh_token') return resp(200, { id_token: initial, refresh_token: 'first-refresh' });
    renewCount++;
    assert.equal(body.get('refresh_token'), renewCount === 1 ? 'first-refresh' : 'second-refresh');
    return resp(200, { id_token: renewed, refresh_token: 'second-refresh' });
  } });
  await wait(); rotation.advance(31000); await rotation.runTimers();
  rotation.advance(3510000); await rotation.runTimers();
  assert.equal(renewCount, 2, 'rotation did not use the latest refresh token');
  const rejectedGrant = boot({ sso: true, now: epoch, idToken: initial, tokenEndpoint: async (body, resp) =>
    body.get('grant_type') === 'refresh_token' ? resp(400, { error: 'invalid_grant' }) : resp(200, { id_token: initial, refresh_token: 'revoked-refresh' }) });
  await wait(); rejectedGrant.advance(31000); await rejectedGrant.runTimers();
  assert.equal(rejectedGrant.$('workspace-content').hidden, true);
  assert.equal(rejectedGrant.pkce.has('ip_sso_session'), false);
  assert.equal(rejectedGrant.calls.filter(c => c.url.endsWith('/token') && new URLSearchParams(c.init.body).get('grant_type') === 'refresh_token').length, 1);
  const absolute = boot({ sso: true, now: epoch, idToken: jwt(Math.floor(epoch / 1000) + 86400), refreshToken: 'fixture-refresh' });
  await wait(); absolute.advance(8 * 3600 * 1000 + 1); await absolute.$('refresh').fire('click');
  assert.equal(absolute.$('workspace-content').hidden, true, 'absolute session limit was ignored');
  assert.equal(absolute.pkce.has('ip_sso_session'), false);
  const manual = boot(); await manual.connect();
  assert.equal(manual.pkce.has('ip_sso_session'), false, 'a manually entered token became resumable');
  let releaseCode;
  const staleCode = boot({ sso: true, idToken: initial, acceptTokens: ['read-secret', initial], tokenEndpoint: async (_, resp) =>
    new Promise(resolve => { releaseCode = () => resolve(resp(200, { id_token: initial, refresh_token: 'fixture-refresh' })); }) });
  await flush(); assert(releaseCode);
  await staleCode.connect(); const countBeforeCode = staleCode.calls.length;
  releaseCode(); await wait();
  assert.equal(staleCode.$('workspace-content').hidden, false);
  assert.equal(staleCode.pkce.has('ip_sso_session'), false, 'late code exchange replaced a newer manual session');
  assert.equal(staleCode.calls.length, countBeforeCode, 'late code exchange initiated stale protected reads');
  console.log('SSO session resume/expiry, token-free storage, claim binding and disconnect fencing passed.');
})().catch(error => { console.error(error); process.exitCode = 1; });
