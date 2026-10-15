const assert = require('node:assert/strict');
const { boot } = require('./dashboard.cjs');
process.exitCode = 1;
async function guided(app, kind, existing = false) {
  await app.$('new-rule').fire('click');
  app.$('rf-kind').value = kind; await app.$('rf-kind').fire('change');
  if (existing) { app.$('rf-policy').value = 'engineering'; await app.$('rf-policy').fire('change'); }
  else { app.$('rf-policy-name').value = 'new-guardrail'; app.$('rf-team').value = 'platform'; }
  app.$('rf-name').value = 'guided-' + kind;
}
(async () => {
  let app = boot(); await app.connect();
  assert.equal(app.$('new-rule').disabled, true, 'read session gained rule writes');
  await app.$('open-command').fire('click');
  app.$('command-input').value = 'engineering'; await app.$('command-input').fire('input');
  assert.match(app.$('command-results').textContent, /Team|Policy/);
  await app.$('command-input').fire('keydown', { key: 'Enter' });
  assert.equal(app.$('usage-view').hidden, false, 'search result did not open team detail');
  assert.equal(app.$('models-section').hidden, false);
  await app.$('open-command').fire('click');
  app.$('command-input').value = 'node-b'; await app.$('command-input').fire('input');
  await app.$('command-input').fire('keydown', { key: 'Enter' });
  assert.equal(app.$('fleet-view').hidden, false);
  assert.equal(app.$('node-details').hidden, false);
  assert.match(app.$('node-rejections').textContent, /not executable/);
  const mismatch = app.$('attention-list').querySelectorAll('button')[0];
  await mismatch.fire('click');
  assert.equal(app.$('fleet-state-filter').value, 'differs');
  assert.match(app.$('fleet-table').textContent, /node-b/);
  assert.doesNotMatch(app.$('fleet-table').textContent, /node-a/);
  await app.$('range-today').fire('click');
  const today = new Date().toISOString().slice(0, 10);
  assert.equal(app.$('since').value, today); assert.equal(app.$('until').value, today);

  await app.credential();
  await guided(app, 'modelAccess');
  app.$('rf-models').value = 'private-coder, economy-coder';
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes.length, 0, 'guided rule published without review');
  assert.match(app.$('change-summary').textContent, /New policy|Added/);
  await app.$('dialog-back').fire('click');
  assert.equal(app.$('rule-form-fields').hidden, false);
  assert.equal(app.$('rf-models').value, 'private-coder, economy-coder', 'review navigation lost the draft');
  await app.$('dialog-confirm').fire('click');
  await app.$('dialog-confirm').fire('click');
  assert.deepEqual(app.writes.at(-1).body.spec.rules[0], { name: 'guided-modelAccess', failurePolicy: 'FailClosed', modelAccess: { allow: ['private-coder', 'economy-coder'] } });
  await guided(app, 'privacy', true);
  app.$('rf-detected').value = 'InternalOnly'; app.$('rf-internal').value = 'private-coder';
  await app.$('dialog-confirm').fire('click');
  assert.match(app.$('change-summary').textContent, /Added rule|guided-privacy/);
  await app.$('dialog-confirm').fire('click');
  const privacy = app.writes.at(-1).body;
  assert.deepEqual(privacy.spec.rules.slice(0, 4), app.policy.policies[0].policy.spec.rules, 'wizard changed unrelated rules');
  assert.deepEqual(privacy.spec.rules[4].sensitiveData, { onDetected: 'InternalOnly', onUninspectable: 'Block', internalModels: ['private-coder'] });
  await guided(app, 'tier', true);
  app.$('rf-budget-ref').value = 'daily'; app.$('rf-threshold').value = '80';
  app.$('rf-source').value = 'premium'; app.$('rf-target').value = 'economy'; app.$('rf-strict').checked = true;
  await app.$('dialog-confirm').fire('click');
  assert.match(app.$('change-summary').textContent, /every attempt|refuse/);
  await app.$('dialog-confirm').fire('click');
  assert.deepEqual(app.writes.at(-1).body.spec.rules[4].routing.budgetTiers, { budgetRef: 'daily', enforceTargets: true, tiers: [{ thresholdPercent: 80, substitute: { premium: 'economy' } }] });
  await guided(app, 'rate');
  app.$('rf-rpm').value = '120'; app.$('rf-tpm').value = '500000';
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  assert.deepEqual(app.writes.at(-1).body.spec.rules[0].rate, { rpm: 120, tpm: 500000 });

  // No required subject, malformed lists, prototype names, or unsafe targets
  // may slip through the simple workflow; all remain explicit drafts.
  const invalids = [
    ['modelAccess', () => { app.$('rf-team').value = ''; }],
    ['modelAccess', () => { app.$('rf-models').value = ''; }],
    ['privacy', () => { app.$('rf-detected').value = 'InternalOnly'; app.$('rf-internal').value = '*'; }],
    ['rate', () => { app.$('rf-rpm').value = '0'; app.$('rf-tpm').value = '0'; }],
    ['tier', () => { app.$('rf-budget-ref').value = 'missing'; }],
  ];
  for (const [kind, setup] of invalids) {
    await guided(app, kind); setup();
    const before = app.writes.length;
    await app.$('dialog-confirm').fire('click');
    assert.equal(app.writes.length, before);
    assert.equal(app.$('dialog-error').hidden, false);
    await app.$('dialog-cancel').fire('click');
  }
  await guided(app, 'tier', true);
  app.$('rf-budget-ref').value = 'daily'; app.$('rf-source').value = '__proto__'; app.$('rf-target').value = 'economy'; app.$('rf-threshold').value = '50';
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  assert.deepEqual(Object.keys(app.writes.at(-1).body.spec.rules[4].routing.budgetTiers.tiers[0].substitute), ['__proto__'], 'model name lost through prototype mutation');

  // Exact differences must distinguish integers beyond the safe Number range.
  app.policy.policies[0].policy.spec.rules[0].budget.limitMilliUSD = 9007199254740;
  await app.$('refresh').fire('click');
  await app.$('budgets-list').querySelector('.budget-edit').fire('click');
  app.$('bf-limit').value = '9007199254.741';
  await app.$('dialog-confirm').fire('click');
  assert.match(app.$('change-summary').textContent, /9007199254740.*9007199254741/);
  await app.$('dialog-cancel').fire('click');

  await app.$('open-command').fire('click');
  app.$('command-input').value = 'engineering'; await app.$('command-input').fire('input');
  await app.$('disconnect').fire('click');
  assert.equal(app.$('command-dialog').open, false);
  assert.equal(app.$('command-results').textContent, '');
  assert.equal(app.$('command-input').value, '');
  assert.equal(app.$('rule-form-fields').hidden, true);
  assert.equal(app.$('rf-policy-name').value, '');
  assert.equal(app.$('change-summary').textContent, '');
  await app.document.fire('keydown', { key: 'k', ctrlKey: true });
  assert.equal(app.$('command-dialog').open, false, 'locked session can search old data');
  if (process.argv.includes('--payloads')) process.stdout.write(JSON.stringify(app.writes.map(w => w.body)));
  else console.log('Workspace checks passed: search, attention actions, presets, guided rules, exact change review, draft validation and disconnect.');
  process.exitCode = 0;
})().catch(e => { console.error(e); process.exitCode = 1; });
