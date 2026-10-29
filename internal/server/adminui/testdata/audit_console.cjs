// Pure helper checks for audit.js (no DOM). Run: node testdata/audit_console.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
// Minimal element double so the waterfall renderer can be inspected without a browser.
class El {
  constructor(tag) { this.tag = tag; this.className = ""; this.textContent = ""; this.children = []; this.style = {}; this.attrs = {}; }
  appendChild(c) { this.children.push(c); return c; }
  append(...cs) { cs.forEach(c => this.children.push(c)); }
  setAttribute(k, v) { this.attrs[k] = v; }
  all() { return [this, ...this.children.flatMap(c => c.all())]; }
}
const ctx = vm.createContext({ navigator: { language: "en" }, document: { getElementById: () => null, createElement: t => new El(t), documentElement: {} }, Node: El, URLSearchParams, console });
for (const f of ["i18n.js", "audit.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), ctx, { filename: f });
const run = (src) => vm.runInContext(src, ctx);
assert.equal(run('auditKindClass("policy_denied")'), "err");
assert.equal(run('auditKindClass("auto_mode_blocked")'), "est");
assert.equal(run('auditKindClass("upstream_error")'), "warn");
assert.equal(run('msg("audit.kind.upstream_error")'), "Error");
const w = JSON.parse(run('JSON.stringify(auditWindow("7d", Date.UTC(2026, 9, 29, 12)))'));
assert.equal(w.since, "2026-10-22T12:00:00.000Z");
assert.equal(run('auditQuery({ a: "", b: "x", c: null, d: 2 })'), "?b=x&d=2");
run('auditState.keys = new Map([["ik_1", { key_id: "ik_1", metadata: { name: "laptop" } }], ["ik_2", { key_id: "ik_2", metadata: { name: "ci" } }], ["ik_3", { key_id: "ik_3", metadata: { name: "ci" } }]])');
assert.equal(run('JSON.stringify(auditResolveKey("laptop"))'), '{"keyId":"ik_1","ambiguous":false}');
assert.equal(run('JSON.stringify(auditResolveKey("ci"))'), '{"keyId":"","ambiguous":true}');   // ambiguous names never pick one
assert.equal(run('JSON.stringify(auditResolveKey("ik_9"))'), '{"keyId":"ik_9","ambiguous":false}');
assert.equal(run('auditKeyLabel("ik_1")'), "laptop");
assert.equal(run('auditKeyLabel("ik_9")'), "ik_9");
const trace = "4bf92f3577b34da6a3ce929d0e0e4736";
assert.equal(run(`auditTraceLink("https://g.example/x?t={trace_id}", "${trace}")`), `https://g.example/x?t=${trace}`);
assert.equal(run(`auditTraceLink("https://g.example/x?t={trace_id}", "not-hex")`), "");
assert.equal(run(`auditTraceLink("javascript:alert({trace_id})", "${trace}")`), "");
assert.equal(run('auditDuration(0)'), "—");
assert.equal(run('auditDuration(412)'), "412ms");
assert.equal(run('auditDuration(6810)'), "6.81s");
assert.equal(run('JSON.stringify(auditBar({ start_ms: 0, dur_ms: 0 }, 0))'), '{"left":0,"width":100}'); // zero total: full-width neutral bar, no NaN
assert.equal(run('JSON.stringify(auditBar({ start_ms: 250, dur_ms: 500 }, 1000))'), '{"left":25,"width":50}');
assert.equal(run('auditScope("team_budget_exceeded")'), "team");
assert.equal(run('auditScope("key_rate_limited")'), "key");
assert.equal(run('auditScope("sensitive_blocked")'), "route");
assert.equal(run('auditSparkPoints([0, 0, 0], 120, 32).length'), 3);

// A legacy record (no latency, no attempts) shows "no timing recorded", never a zero-width bar or NaN%.
const legacy = vm.runInContext('renderAuditWaterfall({ total_ms: 0, rows: [] })', ctx);
const legacyNodes = legacy.all();
assert.equal(legacyNodes.filter(n => /audit-wf-bar/.test(n.className)).length, 0);
assert.ok(legacyNodes.some(n => n.textContent === run('msg("audit.noTiming")')));
assert.ok(!legacyNodes.some(n => /NaN/.test(n.textContent) || /NaN/.test(n.style.width || "")));
// A gateway denial after an earlier attempt keeps both rows; the failed attempt shows its status.
const after = vm.runInContext('renderAuditWaterfall({ total_ms: 900, rows: [{ kind: "attempt", provider: "bedrock", status: "timeout", start_ms: 10, dur_ms: 700 }, { kind: "denied", start_ms: 710, dur_ms: 5 }] })', ctx);
const labels = after.all().filter(n => n.className === "lab").map(n => n.textContent);
assert.deepEqual(labels, ["Attempt · bedrock · timeout", "Gateway denial"]);
assert.ok(after.all().filter(n => /audit-wf-bar/.test(n.className)).every(n => /fail/.test(n.className) && !/NaN/.test(n.style.width)));
assert.equal(run('msg("audit.wf.denied")'), "Gateway denial");
// A current record with latency but no attempts (e.g. count_tokens) is not "legacy": one neutral full-width bar plus a short hint.
const noAttempts = vm.runInContext('renderAuditWaterfall({ total_ms: 35, rows: [] }, { request: { ingress: "anthropic", routing: { requested_model: "m" } } })', ctx).all();
assert.ok(!noAttempts.some(n => n.textContent === run('msg("audit.noTiming")')));
assert.ok(noAttempts.some(n => n.textContent === run('msg("audit.noAttempt")')));
const bars = noAttempts.filter(n => /audit-wf-bar/.test(n.className));
assert.equal(bars.length, 1);
assert.deepEqual([bars[0].style.left, bars[0].style.width], ["0%", "100%"]);
assert.ok(!/fail|ok/.test(bars[0].className.replace("audit-wf-bar", "")));
assert.ok(!noAttempts.some(n => n.textContent === run('msg("audit.attemptTimingMissing")')));
// A pre-upgrade Responses record has latency, provider and usage but no attempts:
// attempt timing was not recorded, the gateway did not answer it itself.
const preUpgrade = vm.runInContext('renderAuditWaterfall({ total_ms: 900, rows: [] }, { request: { ingress: "responses", provider: "openai" }, usage: { input_tokens: 10, output_tokens: 2 } })', ctx).all();
assert.ok(preUpgrade.some(n => n.textContent === run('msg("audit.attemptTimingMissing")')));
assert.ok(!preUpgrade.some(n => n.textContent === run('msg("audit.noAttempt")') || n.textContent === run('msg("audit.noTiming")')));
assert.equal(preUpgrade.filter(n => /audit-wf-bar/.test(n.className)).length, 0); // no "gateway work" bar for provider time
assert.equal(run('msg("audit.attemptTimingMissing")'), "Attempt timing was not recorded (written before attempt timing existed).");
// Usage alone (provider name missing) is still not a gateway-only answer.
assert.ok(vm.runInContext('renderAuditWaterfall({ total_ms: 5, rows: [] }, { request: {}, usage: { input_tokens: 1 } })', ctx).all().some(n => n.textContent === run('msg("audit.attemptTimingMissing")')));
assert.equal(run('auditShownNote(100, 100)'), run('msg("audit.showingFirst")').replace("{n}", "100"));
assert.equal(run('auditShownNote(5, "")'), "");
// A key without a name is shown once, never "ik_… · ik_…".
assert.equal(run('auditKeyText("ik_1")'), "laptop · ik_1");
assert.equal(run('auditKeyText("ik_9")'), "ik_9");
assert.equal(run('auditKeyText("")'), "—");
// An ambiguous key name yields no query parameters at all, so nothing is fetched for every key.
const now = Date.UTC(2026, 9, 29, 12);
assert.equal(run(`JSON.stringify(auditFilterParams("24h", "", "ci", ${now}))`), '{"params":null,"ambiguous":true}');
assert.equal(run(`auditFilterParams("24h", "platform", "laptop", ${now}).params.key_id`), "ik_1");
assert.equal(run(`auditFilterParams("24h", "platform", "laptop", ${now}).params.team`), "platform");
assert.equal(run(`auditFilterParams("24h", "", "", ${now}).params.key_id`), "");
console.log("audit_console.cjs: ok");
