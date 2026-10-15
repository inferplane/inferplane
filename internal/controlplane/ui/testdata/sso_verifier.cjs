// Go supplies a synthetic signed ID token and loopback fake CP. Nothing is
// persisted and no external IdP/network credentials are ever involved.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { boot, flush } = require('./dashboard.cjs');
const fixture = JSON.parse(fs.readFileSync(0, 'utf8'));
(async () => {
  const statuses = [];
  const app = boot({ sso: true, idToken: fixture.idToken, custom: async (path, options) => {
    if (!path.startsWith('/v1alpha1/')) return null;
    const response = await fetch(fixture.baseURL + path, options);
    statuses.push(response.status);
    return response;
  } });
  const deadline = Date.now() + 15000;
  while (app.pkce.size && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 10));
  await flush();
  assert.equal(app.$('workspace-content').hidden, false, 'cold OIDC verifier rejected SSO bootstrap');
  assert.equal(app.$('error').hidden, true, 'SSO bootstrap reported an API error');
  assert.deepEqual(statuses, [200, 200, 200, 200, 200]);
  assert.equal(app.pkce.size, 0);
  await app.$('refresh').fire('click');
  assert.equal(app.$('workspace-content').hidden, false, 'OIDC refresh logged out a valid session');
  assert.deepEqual(statuses, [200, 200, 200, 200, 200, 200, 200, 200, 200, 200]);
  await app.$('disconnect').fire('click');
  assert.equal(app.$('policies-list').textContent, '');
  console.log('Real signature/JWKS verifier + shipped SSO callback/bootstrap/refresh: ten protected reads returned 200.');
})().catch(error => { console.error(error.message); process.exitCode = 1; });
