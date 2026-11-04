// Pure helper checks for observability.js (no DOM). Run: node testdata/observability_console.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const ctx = vm.createContext({ navigator: { language: "en" }, document: { getElementById: () => null, documentElement: {} }, console });
for (const f of ["i18n.js", "observability.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), ctx, { filename: f });
const run = src => vm.runInContext(src, ctx);
const json = src => JSON.parse(run("JSON.stringify(" + src + ")"));

assert.equal(run("typeof refreshObservabilityView"), "function");
assert.equal(run('obsBytes(2097152)'), "2 MiB");
assert.equal(run('obsBytes(1536)'), "1.5 KiB");
assert.equal(run('obsBytes(0)'), "0 B");
assert.equal(run('obsTime("2026-10-29T10:05:00Z")'), "2026-10-29 10:05:00");
assert.equal(run('obsTime("")'), "—");
assert.equal(run('obsShortRev("sha256:0123456789abcdef0123")'), "0123456789ab");
assert.equal(run('obsDropped({ dropped_queue_full: 1, dropped_oversize: 2, dropped_export_error: 9007199254740993n })'), "9007199254740996");
assert.equal(run('obsSubjectText({ team: "eng", user: "alice" })'), "team eng · user alice");
assert.equal(run('obsSubjectText({})'), "—");
assert.equal(run('obsTargetText({ signal: "traces", exporter: "corp", sample_percent: 50, effective_percent: 10 })'), "traces → corp · 50% → 10%");
assert.equal(run('obsTargetText({ signal: "audit", exporter: "corp" })'), "audit → corp");

// Limits body: bootstrap-equal rows send nothing; narrowing rows send only what narrows.
const rows = JSON.stringify([
  { exporter: "corp", declared: ["audit", "traces"], paused: false, signals: ["audit", "traces"], max_content_bytes: "", max_sample_percent: "" },
  { exporter: "vault", declared: ["content"], paused: true, signals: ["content"], max_content_bytes: "1024", max_sample_percent: "" },
  { exporter: "siem", declared: ["audit", "traces"], paused: false, signals: ["traces"], max_content_bytes: "", max_sample_percent: "20" },
]);
assert.deepEqual(json(`obsLimitsBody(${rows}, true, "sha256:a")`), { dry_run: true, expected_revision: "sha256:a", exporters: {
  vault: { paused: true, max_content_bytes: 1024 }, siem: { signals: ["traces"], max_sample_percent: 20 } } });
assert.throws(() => run('obsLimitsBody([{ exporter: "corp", declared: ["audit"], paused: false, signals: [], max_content_bytes: "", max_sample_percent: "" }], true, "r")'), /keep at least one signal/);
assert.throws(() => run('obsLimitsBody([{ exporter: "corp", declared: ["audit"], paused: false, signals: ["audit"], max_content_bytes: "1.5", max_sample_percent: "" }], true, "r")'), /whole number/);
assert.throws(() => run('obsLimitsBody([{ exporter: "corp", declared: ["traces"], paused: false, signals: ["traces"], max_content_bytes: "", max_sample_percent: "101" }], true, "r")'), /1 to 100/);

// Rules body and client-side validation.
assert.equal(run('obsRuleError({ name: "finance", team: "fin", user: "", traces: "", audit: "corp", content: "" })'), "obs.err.ruleName");
assert.equal(run('obsRuleError({ name: "console-fin", team: "", user: "", traces: "", audit: "corp", content: "" })'), "obs.err.ruleSubject");
assert.equal(run('obsRuleError({ name: "console-fin", team: "fin", user: "", traces: "", audit: "", content: "" })'), "obs.err.ruleSignal");
assert.equal(run('obsRuleError({ name: "console-fin", team: "fin", user: "", traces: "corp", sample_percent: "0", audit: "", content: "" })'), "obs.err.percent");
assert.equal(run('obsRuleError({ name: "console-" + "a".repeat(55), team: "fin", user: "", traces: "", audit: "corp", content: "" })'), "");
assert.equal(run('obsRuleError({ name: "console-" + "a".repeat(56), team: "fin", user: "", traces: "", audit: "corp", content: "" })'), "obs.err.ruleName");
assert.deepEqual(json('obsRulesBody([{ name: " console-fin ", team: "fin", user: "", traces: "corp", sample_percent: "20", audit: "corp", content: "" }], false, "sha256:b")'),
  { dry_run: false, expected_revision: "sha256:b", rules: [{ name: "console-fin", subject: { team: "fin" }, traces: { exporter: "corp", sample_percent: 20 }, audit: { exporter: "corp" } }] });
assert.throws(() => run('obsRulesBody([{ name: "finance", team: "fin", user: "", traces: "", sample_percent: "", audit: "corp", content: "" }], true, "r")'), /console-/);

// Diff and error text.
assert.equal(run('obsChangeText({ exporter: "corp", field: "signals", before: ["audit", "traces"], after: ["traces"] })'), "corp · signals: audit, traces → traces");
assert.equal(run('obsChangeText({ exporter: "corp", field: "paused", before: false, after: true })'), "corp · paused: no → yes");
assert.equal(run('obsChangeText({ exporter: "vault", field: "max_content_bytes", before: 2097152, after: 1024 })'), "vault · max content bytes: 2 MiB → 1 KiB");
assert.equal(run('obsEffectText({ policy: "p", rule: "export", signal: "traces", exporter: "corp", before: { exported: true, sample_percent: 20 }, after: { exported: false } })'),
  "p · export · traces → corp: 20% → withheld by limit");
assert.equal(run('obsRuleChangeText({ name: "console-fin", action: "added" })'), "console-fin · added");
assert.equal(run('obsDecisionText({ subject: { team: "fin" }, before: { traces: [], audit: [] }, after: { traces: [{ exporter: "corp", sample_percent: 20 }], audit: ["corp"], content_conflict: true } })'),
  "team fin: none → traces corp 20%, audit corp, content off: rules disagree");
assert.equal(run('obsErrorText({ code: "revision_conflict", error: "x" }, 409)'), "Someone changed this state since it was loaded. Refresh and preview again. — x");
assert.equal(run('obsErrorText(null, 502)'), "Request failed (HTTP 502).");
// D11: a legacy exporter without counters (no observability block) keeps its row and says so.
const noStats = { configured: false, exporters: [], legacy_otel: { endpoint: "otel:4318", protocol: "http", tls: false, sample_ratio: 0.5 } };
const legacyRows = json(`obsSignalRows(${JSON.stringify(noStats)})`);
assert.equal(legacyRows.length, 1);
assert.deepEqual([legacyRows[0].name, legacyRows[0].signal, legacyRows[0].notCounted], ["otel", "traces", true]);
assert.equal(run('obsNotCountedText()'), "not counted (no observability block)");
const withStats = { exporters: [], legacy_otel: { endpoint: "e", protocol: "grpc", tls: true, sample_ratio: 1, stats: { signal: "traces", outcomes: { sent: 3 } } } };
const countedRows = json(`obsSignalRows(${JSON.stringify(withStats)})`);
assert.deepEqual([countedRows[0].name, countedRows[0].notCounted, countedRows[0].stats.outcomes.sent], ["otel", false, 3]);
assert.equal(json(`obsSignalRows(${JSON.stringify({ exporters: [{ name: "corp", stats: [{ signal: "audit" }, { signal: "traces" }] }] })})`).length, 2);
// C3: the data boundary shows in the endpoint cell; absent boundary adds nothing.
assert.match(run('obsEndpointText("collector:4317", "grpc", true, ["authorization"], "internal")'), /collector:4317 · grpc · TLS · headers: authorization · data boundary: internal$/);
assert.equal(run('obsEndpointText("c:1", "http", false, [], "")'), "c:1 · http · plaintext");
// Ruling 4: 403 is translated, never the body's code or text.
assert.equal(run('obsErrorText({ error: "admin only" }, 403)'), "Only full administrators can view or change observability export.");
assert.equal(run('obsErrorText(null, 403)'), "Only full administrators can view or change observability export.");
console.log("observability_console.cjs: ok");
