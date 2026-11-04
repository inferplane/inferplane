// Execute the shipped UI and localization, observing rendered DOM at the API boundary.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
class Element {
  constructor(tag = "div") { this.tag = tag; this.children = []; this.listeners = {}; this.dataset = {}; this.value = ""; this.className = ""; this.classList = { toggle() {}, add() {}, remove() {} }; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  append(...nodes) { nodes.forEach(n => this.appendChild(n)); }
  appendChild(n) { this.children.push(n); }
  replaceChildren(...nodes) { this.children = []; this.text = ""; this.append(...nodes); }
  set textContent(v) { this.children = []; this.text = String(v); }
  get textContent() { return (this.text || "") + this.children.map(n => n.textContent).join(" "); }
  setAttribute(k, v) { (this.attributes ||= {})[k] = v; }
  querySelector(s) { if (s === "tbody") { let b = this.children.find(c => c.tag === "tbody"); if (!b) { b = new Element("tbody"); this.appendChild(b); } return b; } return null; }
  querySelectorAll() { return []; }
}
const elements = new Map(), get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
const operations = { completed: 8, succeeded: 4, failed: 2, partial: 1, unknown: 1,
  estimated_requests: 3, uncertain_requests: 1, unpriced_requests: 1, unavailable_requests: 2,
  legacy_unknown_requests: 1, estimated_cost_micros: 1300, uncertain_recorded_cost_micros: 2050, legacy_projection_records: 1 };
let summary = { totals: { requests: 6, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cost_micros: 3350, operations },
  by_team: [{ team: "fixture-team", requests: 6, cost_micros: 3350, operations }],
  by_model: [{ model: "fixture-model", requests: 6, cost_micros: 3350, operations }] };
const day = new Date().toISOString().slice(0, 10);
let series = [{ day, requests: 6, cost_micros: 3350, operations }];
let events = [
  ["A", 200, "succeeded", "estimated", 1000, true], ["B", 502, "failed", "unavailable", 0, false],
  ["C", 200, "partial", "uncertain", 2000, true], ["D", 200, "succeeded", "unpriced", 0, true],
  ["E", 200, "succeeded", "legacy_unknown", 50, true], ["F", 200, "succeeded", "estimated", 0, true],
  ["G", 0, "unknown", "unavailable", 0, false], ["H", 400, "failed", "estimated", 300, true],
].map(([id, status, outcome_state, cost_state, cost_micros, has_cost]) => ({ id, status, outcome_state, cost_state, cost_micros, has_cost,
  partial: id === "C", error_code: id === "C" ? "stream_incomplete" : "", ts: day, team: "fixture-team", model: "fixture-model", provider: "fixture" }));
const reads = [];
let logsFailure = false, logsReply = null, afterSummary = null, testClock = null;
class ClockDate extends Date { constructor(...args) { super(...(args.length ? args : testClock ? [testClock] : [])); } }
const context = vm.createContext({
  document: { getElementById: get, querySelectorAll: () => [], createElement: t => new Element(t), createElementNS: (_, t) => new Element(t), addEventListener() {}, documentElement: new Element(), body: new Element() },
  Date: ClockDate, navigator: { language: "en" }, location: { search: "" }, window: { location: { origin: "http://localhost" } }, URLSearchParams, structuredClone, console,
  fetch: async url => { reads.push(url); if (logsReply && url.startsWith("/admin/logs")) return logsReply(url); if (logsFailure && url.startsWith("/admin/logs")) throw Error("fixture network failure"); let body = {};
    if (url.startsWith("/admin/analytics/summary")) { body = summary; if (afterSummary) afterSummary(); }
    else if (url.startsWith("/admin/analytics/timeseries")) body = series;
    else if (url.startsWith("/admin/logs")) body = { events };
    else if (url === "/metrics") return { text: async () => 'inferplane_requests_total{model="fixture",provider="fixture",status="2xx",team="fixture"} 99' };
    return { ok: true, status: 200, text: async () => JSON.stringify(body) };
  },
});
for (const name of ["i18n.js", "app.js", "evaluation.js", "audit.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "../static/", name), "utf8"), context);
const run = code => vm.runInContext(code, context);
const text = id => get(id).textContent;
async function usagePeriodRegression() {
  run('applyLang("en"); caps = { analytics_index: "A" }');
  testClock = "2026-10-21T23:59:59Z";
  series = ["2026-09-21", "2026-09-22", "2026-10-21", "2026-10-22"].map(day => ({ day, operations }));
  afterSummary = () => { testClock = "2026-10-22T00:00:01Z"; };
  await run('refreshUsageView()');
  const requested = reads.findLast(url => url.startsWith("/admin/analytics/summary"));
  assert.equal(requested, "/admin/analytics/summary?since=2026-09-22&until=2026-10-21");
  const rows = get("usage-by-day").querySelector("tbody").children;
  assert.deepEqual(rows.map(row => row.children[0].textContent), ["2026-09-22", "2026-10-21"], "daily rows must share the captured inclusive summary period, even across midnight");
  afterSummary = null; testClock = null;
}
async function paginationRecoveryRegression() {
  run('applyLang("en"); caps = { analytics_index: "A" }');
  logsFailure = false; events = [{ id: "cursor-one", status: 200 }];
  await run('refreshLogsView()');
  const click = () => get("logs-load-more").listeners.click[0]();
  logsFailure = true;
  await click();
  assert.equal(get("workspace-error").hidden, false);
  assert.match(text("workspace-error"), /fixture network failure/);
  logsFailure = false; events = [{ id: "cursor-two", status: 200 }];
  await click();
  assert.equal(run('logsCursor'), "cursor-two");
  assert.equal(get("logs-table").querySelector("tbody").children.length, 2);
  assert.equal(get("workspace-error").hidden, true, "successful pagination retry must dismiss its prior error");
  assert.equal(text("workspace-error"), "");
  for (const unrelated of [false, true]) {
    logsFailure = true; await click(); logsFailure = false;
    let finish;
    logsReply = () => new Promise(resolve => { finish = resolve; });
    const pending = click();
    logsReply = null; logsFailure = true;
    if (unrelated) { run('showView("logs")'); await new Promise(resolve => setImmediate(resolve)); }
    else await click();
    assert.equal(get("workspace-error").hidden, false);
    finish({ ok: true, status: 200, text: async () => JSON.stringify({ events: [{ id: "cursor-three" }] }) });
    await pending;
    assert.equal(get("workspace-error").hidden, false, "an older pagination success must preserve newer " + (unrelated ? "view" : "pagination") + " errors");
    assert.match(text("workspace-error"), /fixture network failure/);
  }
  logsFailure = false;
}
async function localeEvidenceRegression() {
  summary.totals.operations = operations;
  series = [{ day, operations }, { day: new Date(Date.now() - 86400000).toISOString().slice(0, 10), operations }];
  for (const lang of ["zh", "ja"]) {
    run('applyLang(' + JSON.stringify(lang) + '); caps = { analytics_index: "A" }');
    await run('refreshOverview()');
    assert.match(text("stat-spend-sub"), /Last 30 days/, lang + " period must use the corrected fallback");
    assert.doesNotMatch(text("stat-spend-sub"), /结算|確定支出/);
    const chart = get("stat-spend-spark").children[0];
    assert.equal(chart.attributes["aria-label"], "Daily estimable or legacy recorded amounts over the last 30 days");
    run('caps = { analytics_index: "off" }'); await run('refreshOverview()');
    assert.equal(text("stat-spend-sub"), "Cost evidence unavailable · analytics required");
  }
}
const regressions = { usage: usagePeriodRegression, pagination: paginationRecoveryRegression, locales: localeEvidenceRegression };
(async () => {
  if (process.argv[2]) { await regressions[process.argv[2]](); console.log(process.argv[2] + " regression passed"); return; }
  run('caps = { analytics_index: "A" }');
  await run('refreshUsageView()');
  assert.match(text("usage-totals"), /Observed completed records\s+8/, "Usage must count all eight completions, including costless failures");
  assert.match(text("usage-totals"), /3\/8 \(37\.5%\)/);
  assert.match(text("usage-totals"), /Unknown outcome[^0-9]*1/);
  for (const id of ["usage-by-team", "usage-by-model", "usage-by-day"]) {
    assert.match(text(id), /3\/8 \(37\.5%\)/, id + " lacks outcome breakdown");
    assert.match(text(id), /\$0\.001300/); assert.match(text(id), /\$0\.002050/);
  }
  assert.match(text("usage-totals"), /Legacy projections awaiting replay[^0-9]*1/);
  await run('refreshOverview()');
  assert.equal(text("stat-requests"), "8", "metrics total must not overwrite observed completed records");
  assert.match(text("stat-requests-sub"), /3\/8 \(37\.5%\)/);
  assert.match(text("overview-evidence"), /\$0\.002050/);
  await run('refreshLogsView()');
  const rows = get("logs-table").querySelector("tbody").children;
  assert.equal(rows.length, 8);
  assert.match(rows[2].textContent, /200.*Partial response/);
  assert.match(rows[2].children[4].className, /warn|err/, "partial HTTP 200 must be visibly warned");
  assert.match(rows[2].textContent, /stream_incomplete/);
  assert.match(rows[5].textContent, /\$0\.000000.*Estimate/);
  assert.match(rows[3].textContent, /Unpriced/); assert.doesNotMatch(rows[3].textContent, /\$/);
  assert.match(rows[1].textContent, /Unavailable/); assert.doesNotMatch(rows[1].textContent, /\$/);
  assert.match(rows[4].textContent, /Recorded amount.*Evidence missing/);
  assert.match(rows[6].textContent, /Unknown outcome/);
  assert.equal(run('operationRateText(null)'), "—");
  assert.equal(run('operationRateText({completed:0, failed:0, partial:0})'), "—");
  assert.equal(run('operationRateText({completed:"9007199254740993",failed:"9007199254740992",partial:"0"})'), "9007199254740992/9007199254740993 (100.0%)");
  assert.equal(run(`usd(parseExactJSON('{"cost_micros":9007199254740993}').cost_micros)`), "$9,007,199,254.740993");
  context.largeEvent = { cost_state: "estimated", cost_micros: "9007199254740993" };
  assert.match(run('costEvidenceText(largeEvent)'), /\$9,007,199,254\.740993/);
  events = [{ id: "legacy", status: 200, cost_micros: 50, error_code: '<img src=x onerror=alert(1)>' }];
  await run('refreshLogsView()');
  const legacy = get("logs-table").querySelector("tbody").children[0];
  assert.match(legacy.textContent, /Legacy aggregate \/ evidence unavailable/);
  assert.doesNotMatch(legacy.textContent, /Succeeded|\$0\.000000|<img/);
  assert.match(legacy.textContent, /upstream_error/);
  delete summary.totals.operations; delete summary.by_team[0].operations;
  await run('refreshUsageView()');
  assert.match(text("usage-totals"), /Legacy aggregate \/ evidence unavailable/);
  assert.match(text("usage-totals"), /Recorded amount.*\$0\.003350/);
  await run('refreshOverview()');
  assert.equal(text("stat-requests"), "—");
  assert.match(text("stat-requests-sub"), /Legacy aggregate/);
  assert.match(text("stat-spend-sub"), /Recorded amount/);
  assert.match(text("stat-spend-label"), /Recorded amount/, "legacy cost card heading must not claim an estimable amount");
  run('applyLang("ko")'); events = [{ id: "partial", status: 200, outcome_state: "partial", cost_state: "unavailable", error_code: "upstream_timeout" }];
  await run('refreshLogsView()');
  assert.match(text("logs-table"), /부분 응답/); assert.match(text("logs-table"), /미확정/);
  for (const [key, value] of Object.entries(run('MSG'))) if (key.startsWith("evidence.")) assert(value.en && value.ko, key + " requires English and Korean");
  events = [{ id: "large", status: 200, outcome_state: "succeeded", cost_state: "estimated", cost_micros: "9007199254740993" }];
  await run('refreshLogsView()');
  assert.match(text("logs-table"), /\$9,007,199,254\.740993/);
  logsFailure = true;
  await assert.doesNotReject(() => get("logs-load-more").listeners.click[0](), "pagination failure must surface in the workspace without an unhandled rejection");
  assert.equal(get("workspace-error").hidden, false);
  assert.match(text("workspace-error"), /fixture network failure/);
  await assert.rejects(run('refreshLogsView()'), /fixture network failure/);
  assert.equal(get("logs-table").querySelector("tbody").children.length, 0, "failed refresh must remove stale log rows");
  for (const regression of Object.values(regressions)) await regression();
  console.log("Analytics evidence behavior passed: A–H views, cost states, partial warnings, legacy fallback, bounded errors, exact int64, en/ko.");
})().catch(err => { console.error(err); process.exitCode = 1; });
