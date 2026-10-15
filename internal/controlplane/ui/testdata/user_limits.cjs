const assert = require('node:assert/strict');
const { boot, flush } = require('./dashboard.cjs');
const entry = (key, spent, tokens = 10) => ({ key, spent_micro_usd: spent, input_tokens: tokens, output_tokens: 5, cache_read_tokens: 30, cache_write_5m_tokens: 4, cache_write_1h_tokens: 1 });
const reported = 'identity-v1:' + 'a'.repeat(64);
process.exitCode = 1;
(async () => {
  let app = boot({ users: { total_micro_usd: 1007, rows: [entry(reported, 5), entry('developer-b', 2), entry('', 1000)] } });
  app.policy.identity_bindings = [{ reported_ref: reported, account_ref: ' legacy-account ', kind: 'human' }];
  await app.connect();
  assert.equal(app.$('stat-users').textContent, '2');
  assert.equal(app.$('stat-user-average').title, '$0.000004', 'average was rounded or included unattributed cost');
  assert.match(app.$('user-summary-status').textContent, /unattributed/i);
  await app.$('nav-users').fire('click');
  app.$('users-search').value = 'legacy-account'; await app.$('users-search').fire('input');
  assert.equal(app.$('users-table').querySelectorAll('tr').length, 1);
  assert.match(app.$('users-table').textContent, /legacy-account/);
  assert.equal(app.$('users-table').querySelector('.user-budget').disabled, true);
  await app.credential();
  const row = app.$('users-table').querySelector('tr');
  await row.querySelector('.user-budget').fire('click');
  assert.equal(app.$('bf-user').value, ' legacy-account ', 'telemetry digest replaced the financial account');
  assert(app.$('bf-policy-name').value, 'quick budget required an internal policy name');
  assert(app.$('bf-rule').value, 'quick budget required an internal rule name');
  app.$('bf-limit').value = '20.125';
  await app.$('dialog-confirm').fire('click');
  const budget = JSON.parse(app.$('policy-review').textContent);
  assert.equal(budget.spec.subject.user, ' legacy-account ', 'budget form trimmed an exact account reference');
  assert.equal(budget.spec.rules[0].budget.limitMilliUSD, 20125);
  assert.equal(app.writes.length, 0);
  await app.$('dialog-confirm').fire('click');
  await app.$('users-table').querySelector('.user-button').fire('click');
  assert(app.calls.some(c => c.url.includes('user=' + encodeURIComponent(reported)) && c.url.includes('group_by=model')), 'model drill-down used the wrong user filter');
  const emitted = [...app.writes];
  // Known reporting aliases are a read-side correlation, never two users or
  // two financial accounts. Model queries must remain disjoint exact filters.
  app.setUsers({ total_micro_usd: 10, rows: [entry(reported, 5), entry(' legacy-account ', 3), entry('developer-b', 2)] });
  await app.$('refresh').fire('click');
  assert.equal(app.$('stat-users').textContent, '2', 'a legacy binding was counted twice');
  assert.equal(app.$('stat-user-average').title, '$0.000005');
  assert.equal(app.$('users-table').querySelectorAll('tr').length, 1); // retained search
  const correlated = app.$('users-table').querySelector('tr');
  assert.match(correlated.textContent, /\$0\.000008/);
  await correlated.querySelector('.user-button').fire('click');
  assert(app.calls.some(c => c.url.includes('user=+legacy-account+') || c.url.includes('user=%20legacy-account%20')), 'the legacy reporting alias was not queried');
  await correlated.querySelector('.user-token-limit').fire('click');
  assert.equal(app.$('rf-user').value, ' legacy-account ');
  await app.$('dialog-cancel').fire('click');

  // Unbound managed IDs are evidence, not guessed financial account selectors.
  app = boot({ users: { total_micro_usd: 3, rows: [entry(reported, 2), entry('', 1)] } });
  await app.connect(); await app.credential();
  const actions = app.$('users-table').querySelectorAll('.user-budget');
  await actions[0].fire('click');
  assert.equal(app.$('bf-user').value, '');
  app.$('bf-team').value = 'engineering'; app.$('bf-limit').value = '100';
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('dialog-error').hidden, false, 'missing user silently created a team budget');
  await app.$('dialog-cancel').fire('click');
  assert.equal(actions[1].disabled, true);
  await actions[1].fire('click');
  assert.equal(app.$('policy-dialog').open, false, 'empty-user row opened an all-user action');

  app = boot(); await app.connect(); await app.credential();
  await app.$('new-token-policy').fire('click');
  assert.equal(app.$('rf-kind').value, 'tokenQuota');
  app.$('rf-policy-name').value = 'user-token-policy'; app.$('rf-user').value = 'person';
  app.$('rf-name').value = 'monthly-tokens'; app.$('rf-token-period').value = 'CalendarMonth';
  app.$('rf-token-limit').value = '10000000';
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('dialog-error').hidden, false, 'shared-only quota silently claimed default-profile support');
  app.$('rf-shared').checked = true;
  await app.$('dialog-confirm').fire('click');
  let doc = JSON.parse(app.$('policy-review').textContent);
  assert.deepEqual(doc.spec.rules[0], { name: 'monthly-tokens', failurePolicy: 'FailClosed', tokenQuota: { limitTokens: 10000000, period: 'CalendarMonth' } });
  await app.$('dialog-confirm').fire('click');
  emitted.push(...app.writes);
  // Editing a quota preserves every other rule and unrendered metadata.
  app.policy.policies[0].policy.spec.rules.push({ name: 'tokens', failurePolicy: 'FailClosed', tokenQuota: { limitTokens: 12345, period: 'CalendarDay' } });
  await app.$('refresh').fire('click');
  const prior = structuredClone(app.policy.policies[0].policy);
  const edit = app.$('token-policy-list').querySelectorAll('.token-edit').find(b => b.dataset.rule === 'tokens');
  await edit.fire('click');
  assert.equal(app.$('rf-policy').disabled, true);
  app.$('rf-token-limit').value = '23456'; app.$('rf-shared').checked = true;
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  prior.spec.rules.at(-1).tokenQuota.limitTokens = 23456;
  assert.deepEqual(app.writes.at(-1).body, prior);
  emitted.push(app.writes.at(-1));
  // User throughput is also shared-only, unlike a default/local team rate.
  await app.$('new-rule').fire('click');
  app.$('rf-kind').value = 'rate'; await app.$('rf-kind').fire('change');
  app.$('rf-policy-name').value = 'user-throughput'; app.$('rf-user').value = 'person';
  app.$('rf-rpm').value = '30'; app.$('rf-tpm').value = '200000';
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('dialog-error').hidden, false);
  app.$('rf-shared').checked = true;
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  emitted.push(app.writes.at(-1));

  // Token limits never use floating-point or parseInt truncation.
  for (const amount of ['1.5', '1e6', '-1', '0', '9223372036854775808']) {
    await app.$('new-token-policy').fire('click');
    app.$('rf-policy-name').value = 'invalid-tokens'; app.$('rf-team').value = 'engineering'; app.$('rf-name').value = 'tokens';
    app.$('rf-token-limit').value = amount; app.$('rf-shared').checked = true;
    await app.$('dialog-confirm').fire('click');
    assert.equal(app.$('dialog-error').hidden, false, 'invalid token limit accepted: ' + amount);
    await app.$('dialog-cancel').fire('click');
  }
  await app.$('new-token-policy').fire('click');
  app.$('rf-policy-name').value = 'large-tokens'; app.$('rf-team').value = 'engineering'; app.$('rf-name').value = 'tokens';
  app.$('rf-token-limit').value = '9223372036854775807'; app.$('rf-shared').checked = true;
  await app.$('dialog-confirm').fire('click'); await app.$('dialog-confirm').fire('click');
  assert.match(app.writes.at(-1).init.body, /"limitTokens":9223372036854775807[,}]/);
  emitted.push(app.writes.at(-1));

  await app.$('new-budget-routing').fire('click');
  app.$('rf-policy').value = 'engineering'; await app.$('rf-policy').fire('change');
  app.$('rf-name').value = 'two-thresholds'; app.$('rf-source').value = 'premium'; app.$('rf-target').value = 'economy';
  app.$('rf-threshold').value = '80'; app.$('rf-next-threshold').value = '95'; app.$('rf-next-target').value = 'local';
  await app.$('dialog-confirm').fire('click');
  doc = JSON.parse(app.$('policy-review').textContent);
  assert.deepEqual(doc.spec.rules.at(-1).routing.budgetTiers.tiers, [
    { thresholdPercent: 80, substitute: { premium: 'economy' } }, { thresholdPercent: 95, substitute: { premium: 'local' } },
  ]);
  await app.$('dialog-confirm').fire('click'); emitted.push(app.writes.at(-1));

  // A scoped read cannot repopulate the current view after its scope changed.
  let release;
  app.setCustom((url, init, resp) => url.includes('group_by=user') && url.includes('team=engineering')
    ? new Promise(resolve => { release = () => resolve(resp(200, { total_micro_usd: 99, rows: [entry('stale-user', 99)] })); }) : null);
  app.$('users-team').value = 'engineering';
  const pending = app.$('users-team').fire('change'); await flush();
  app.$('users-team').value = ''; await app.$('users-team').fire('change');
  release(); await pending;
  assert.doesNotMatch(app.$('users-table').textContent, /stale-user/);
  await app.$('disconnect').fire('click');
  assert.equal(app.$('users-table').textContent, '');
  assert.equal(app.$('stat-user-average').textContent, '—');
  assert.equal(app.$('budget-user-options').textContent, '');
  assert.equal(app.$('rf-shared').checked, false);
  assert.equal(app.$('token-policy-list').textContent, '');

  if (process.argv.includes('--payloads')) process.stdout.write('[' + emitted.map(w => w.init.body).join(',') + ']');
  else console.log('User costs and limits passed: exact average, account binding, guided budget/quota/two tiers, preserved edits, profile acknowledgment, scope fences and cleanup.');
  process.exitCode = 0;
})().catch(err => { console.error(err); process.exitCode = 1; });
