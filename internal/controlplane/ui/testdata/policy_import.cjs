// Policy export/import (backup/restore) through the real UI handlers.
const assert = require('node:assert/strict');
const { boot } = require('./dashboard.cjs');
process.exitCode = 1;

const bundle = 'apiVersion: inferplane.dev/v1alpha1\nkind: GovernancePolicy\nmetadata: { name: restored }\nspec:\n  subject: { team: t }\n  rules:\n  - name: models\n    failurePolicy: FailOpen\n    modelAccess: { allow: ["*"] }\n';
const preview = { dry_run: true, current_generation: 'g2', result_generation: 'g3',
  changes: [{ name: 'engineering', action: 'removed' }, { name: 'restored', action: 'added' }],
  summary: { added: 1, changed: 0, removed: 1, unchanged: 0 } };
const file = text => ({ size: Buffer.byteLength(text), text: async () => text });

function importRoutes(state) {
  return async (url, init, resp) => {
    if (url === '/v1alpha1/config/export') { state.exported = (state.exported || 0) + 1; return { status: 200, ok: true, text: async () => bundle, json: async () => { throw Error('not json'); } }; }
    if (!url.startsWith('/v1alpha1/config/import')) return null;
    state.imports.push({ url, init });
    const q = new URLSearchParams(url.split('?')[1]);
    if (q.get('dry_run') === 'true') return resp(200, state.preview || preview);
    if (state.conflict) return resp(409, { error: 'policies changed since preview', current_generation: 'g9' });
    return resp(200, { ...(state.preview || preview), dry_run: false });
  };
}

(async () => {
  // Read-only session: export available, import hidden/disabled.
  let state = { imports: [] };
  let app = boot({ custom: importRoutes(state) }); await app.connect(); await app.$('nav-policies').fire('click');
  assert.equal(app.$('export-policies').disabled, false, 'export needs only read access');
  assert.equal(app.$('import-policies').disabled, true, 'read token enabled import');
  await app.$('import-policies').fire('click');
  app.$('import-file').files = [file(bundle)]; await app.$('import-file').fire('change');
  assert.equal(state.imports.length, 0, 'import handler bypassed read-only controls');

  // File-authoritative deployment: import button hidden entirely.
  app = boot({ writable: false, custom: importRoutes({ imports: [] }) }); await app.connect(); await app.credential();
  assert.equal(app.$('import-policies').hidden, true, 'import shown without a policy store');

  // Export downloads the server YAML verbatim.
  state = { imports: [] };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  await app.$('export-policies').fire('click');
  assert.equal(state.exported, 1);
  const exportCall = app.calls.find(c => c.url === '/v1alpha1/config/export');
  assert.equal(exportCall.init.headers.Authorization, 'Bearer read-secret', 'export must use the read credential');
  const link = app.document.body.querySelectorAll('a').find(a => /^inferplane-policies-g2.*\.yaml$/.test(a.download || ''));
  assert(link, 'no download link was created for the export');

  // Import: dry-run preview first, nothing applied yet.
  assert.equal(app.$('import-policies').disabled, false);
  app.$('import-file').files = [file(bundle)];
  await app.$('import-file').fire('change');
  assert.equal(state.imports.length, 1, 'preview was not requested');
  assert.match(state.imports[0].url, /dry_run=true/);
  assert.equal(state.imports[0].init.method, 'POST');
  assert.equal(state.imports[0].init.body, bundle, 'bundle was not sent verbatim');
  assert.equal(state.imports[0].init.headers['Content-Type'], 'application/yaml');
  assert.equal(state.imports[0].init.headers.Authorization, 'Bearer write-secret');
  assert.equal(app.$('policy-dialog').open, true);
  assert.match(app.$('policy-review').textContent, /1 added · 0 changed · 1 removed · 0 unchanged/);
  assert.match(app.$('policy-review').textContent, /removed\s+engineering/);
  assert.match(app.$('dialog-confirm').textContent, /delete 1 policy/i, 'removal count not surfaced on confirm');

  // Confirm applies with the previewed generation.
  await app.$('dialog-confirm').fire('click');
  assert.equal(state.imports.length, 2);
  const apply = new URLSearchParams(state.imports[1].url.split('?')[1]);
  assert.equal(apply.get('expected_generation'), 'g2');
  assert.equal(apply.get('dry_run'), null);
  assert.equal(apply.get('allow_empty'), null);
  assert.equal(app.$('policy-dialog').open, false, 'dialog stayed open after restore');
  assert.match(app.$('notice').textContent, /restored/i);

  // 409: dialog stays open with a re-preview instruction.
  state = { imports: [], conflict: true };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [file(bundle)]; await app.$('import-file').fire('change');
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('policy-dialog').open, true);
  assert.match(app.$('dialog-error').textContent, /changed since preview; re-run preview/i);

  // Empty bundle: explicit warning and allow_empty on apply.
  state = { imports: [], preview: { dry_run: true, current_generation: 'g2', result_generation: 'g0', changes: [{ name: 'engineering', action: 'removed' }], summary: { added: 0, changed: 0, removed: 1, unchanged: 0 } } };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [file('')]; await app.$('import-file').fire('change');
  assert.match(app.$('dialog-error').textContent, /deletes every policy/i);
  await app.$('dialog-confirm').fire('click');
  assert.equal(new URLSearchParams(state.imports[1].url.split('?')[1]).get('allow_empty'), 'true');

  // Oversized file never leaves the browser.
  state = { imports: [] };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [{ size: (64 << 20) + 1, text: async () => 'x' }]; await app.$('import-file').fire('change');
  assert.equal(state.imports.length, 0, 'oversized bundle was uploaded');
  assert.match(app.$('error').textContent, /64 MiB/);

  // A refused WRITE credential keeps the read session (dashboard.cjs write-403 contract).
  state = { imports: [] };
  app = boot({ custom: async (url, init, resp) => url.startsWith('/v1alpha1/config/import') ? resp(403, { error: 'forbidden' }) : importRoutes(state)(url, init, resp) });
  await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [file(bundle)]; await app.$('import-file').fire('change');
  assert.equal(app.$('policy-dialog').open, false, 'preview dialog opened after 403');
  assert.equal(app.$('workspace-content').hidden, false, 'a write refusal discarded the read session');
  assert.match(app.$('error').textContent, /write refused/i);

  // A file read that resolves after disconnect never uploads with the next session.
  state = { imports: [] };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  let finishRead; app.$('import-file').files = [{ size: 10, text: () => new Promise(r => { finishRead = () => r(bundle); }) }];
  const pending = app.$('import-file').fire('change');
  await app.$('disconnect').fire('click');
  assert.equal(app.$('import-policies').disabled, true, 'import stayed enabled after disconnect');
  assert.equal(app.$('export-policies').disabled, true, 'export stayed enabled after disconnect');
  await app.connect(); await app.credential();
  finishRead(); await pending;
  assert.equal(state.imports.length, 0, 'a stale-session bundle was uploaded with the new credential');

  process.exitCode = 0;
})().catch(e => { console.error(e); process.exitCode = 1; });
