// Budget workflows exercise real UI handlers with synthetic API boundaries.
const assert = require('node:assert/strict');
const { boot } = require('./dashboard.cjs');

async function ready(options) {
  const app = boot(options); await app.connect(); await app.credential();
  await app.$('nav-budgets').fire('click'); return app;
}
async function create(app) {
  await app.$('new-budget').fire('click');
  app.$('bf-policy-name').value = 'platform-budget';
  app.$('bf-team').value = 'platform';
  app.$('bf-rule').value = 'daily-cap';
  app.$('bf-period').value = 'CalendarDay';
  app.$('bf-limit').value = '12.345';
}

(async () => {
  let app = boot(); await app.connect(); await app.$('nav-budgets').fire('click');
  assert.equal(app.$('budgets-view').hidden, false);
  assert.equal(app.$('controls').hidden, true, 'reporting dates imply budget-window filtering');
  assert.equal(app.$('budgets-list').querySelectorAll('.budget-card').length, 2);
  assert.equal(app.$('new-budget').disabled, true, 'read token authorized a write');
  assert(app.$('budgets-list').querySelectorAll('.budget-edit').every(b => b.disabled));
  await app.$('new-budget').fire('click');
  assert.equal(app.$('policy-dialog').open, false, 'handler bypassed read-only controls');
  app.$('budget-period-filter').value = 'CalendarDay';
  await app.$('budget-period-filter').fire('change');
  assert.equal(app.$('budgets-list').querySelectorAll('.budget-card').length, 1);
  app.$('budget-search').value = 'missing'; await app.$('budget-search').fire('input');
  assert.equal(app.$('budgets-list').querySelectorAll('.budget-card').length, 0);

  app = await ready({ persistWrites: true }); await create(app);
  app.$('bf-user').value = 'declared-account-ref';
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes.length, 0, 'form submitted without review');
  const review = JSON.parse(app.$('policy-review').textContent);
  assert.deepEqual(review.spec.subject, { team: 'platform', user: 'declared-account-ref' });
  assert.deepEqual(review.spec.rules, [{ name: 'daily-cap', failurePolicy: 'FailClosed',
    budget: { limitMilliUSD: 12345, period: 'CalendarDay', hardCap: true } }]);
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.writes[0].url, '/v1alpha1/policies/platform-budget');
  assert.equal(app.writes[0].init.headers.Authorization, 'Bearer write-secret');
  assert.equal(app.writes[0].body.spec.rules[0].budget.limitMilliUSD, 12345);
  assert.equal(app.$('budgets-list').querySelectorAll('.budget-card').length, 3, 'saved budget missing after API reload');
  assert.match(app.$('budgets-list').textContent, /platform-budget/);
  assert.match(app.$('budgets-list').textContent, /\$12\.345/);

  // Add a monthly cap to the same policy without losing the daily cap/privacy.
  app = await ready(); const original = structuredClone(app.policy.policies[0].policy);
  await app.$('new-budget').fire('click'); app.$('bf-policy').value = 'engineering';
  await app.$('bf-policy').fire('change');
  assert.equal(app.$('bf-team').value, 'engineering');
  assert.equal(app.$('bf-team').disabled, true, 'adding a rule could retarget all existing rules');
  app.$('bf-rule').value = 'extra-month'; app.$('bf-limit').value = '0.029';
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  assert.deepEqual(app.writes[0].body.spec.rules.slice(0, -1), original.spec.rules);
  assert.equal(app.writes[0].body.spec.rules.at(-1).budget.limitMilliUSD, 29);
  assert.equal(app.writes[0].body.spec.rules.at(-1).budget.period, 'CalendarMonth');

  // Editing preserves omitted defaults, failure semantics and unknown fields.
  app = boot();
  const rule = app.policy.policies[0].policy.spec.rules[1];
  delete rule.budget.period; rule.budget.extension = { retained: true };
  await app.connect(); await app.credential();
  const before = structuredClone(app.policy.policies[0].policy);
  await app.$('budgets-list').querySelectorAll('.budget-edit')[1].fire('click');
  assert.equal(app.$('bf-limit').value, '300.000');
  app.$('bf-limit').value = '321.123';
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  before.spec.rules[1].budget.limitMilliUSD = 321123;
  assert.deepEqual(app.writes[0].body, before);
  assert.equal(app.policy.policies[0].policy.spec.rules[1].budget.limitMilliUSD, 300000);

  // Hard caps must force FailClosed even when the prior soft rule was FailOpen.
  app = boot(); app.policy.policies[0].policy.spec.rules[1].failurePolicy = 'FailOpen';
  await app.connect(); await app.credential();
  await app.$('budgets-list').querySelectorAll('.budget-edit')[1].fire('click');
  app.$('bf-hard').checked = true;
  await app.$('dialog-confirm').fire('click');
  assert.equal(JSON.parse(app.$('policy-review').textContent).spec.rules[1].failurePolicy, 'FailClosed');

  for (const value of ['0', '-1', '1.0001', '1e3', 'NaN', '', '9223372036854.776']) {
    app = await ready(); await create(app); app.$('bf-limit').value = value;
    await app.$('dialog-confirm').fire('click');
    assert.equal(app.$('dialog-error').hidden, false, 'accepted invalid USD ' + value);
    assert.equal(app.writes.length, 0);
    assert.equal(app.$('budget-form-fields').hidden, false);
  }
  app = await ready(); await create(app); app.$('bf-limit').value = '9223372036854.775';
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  assert.match(app.writes[0].init.body, /"limitMilliUSD":9223372036854775[,}]/);

  for (const duplicate of ['policy', 'rule']) {
    app = await ready(); await create(app);
    if (duplicate === 'policy') app.$('bf-policy-name').value = 'engineering';
    else { app.$('bf-policy').value = 'engineering'; await app.$('bf-policy').fire('change'); app.$('bf-rule').value = 'privacy'; }
    await app.$('dialog-confirm').fire('click');
    assert.equal(app.$('dialog-error').hidden, false, 'silently replaced existing ' + duplicate);
    assert.equal(app.writes.length, 0);
  }
  app = await ready(); await create(app); app.$('bf-team').value = '';
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('dialog-error').hidden, false, 'empty subject accepted');

  for (const options of [{ writable: false }, { fail: { '/v1alpha1/policies': 404 } }]) {
    app = await ready(options);
    assert.equal(app.$('new-budget').disabled, true);
    assert.match(app.$('budget-access-status').textContent, /read.only|unavailable/i);
  }
  app = await ready({ writeFailure: 403 }); await create(app);
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('policy-dialog').open, true);
  assert.equal(app.$('dialog-error').hidden, false);
  assert.match(app.$('policy-review').textContent, /platform-budget/);
  await app.$('disconnect').fire('click');
  assert.equal(app.$('budgets-list').textContent, '');
  assert.equal(app.$('bf-team').value, '');
  assert.equal(app.$('bf-policy').children.length, 0, 'disconnect retained policy choices');
  assert.equal(app.$('policy-review').textContent, '');
  console.log('Budget workflows passed: exact USD, create/add/edit/review, preservation, authorization, validation, cleanup.');
})().catch(e => { console.error(e); process.exitCode = 1; });
