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
// restore_failed: each editor says the change is not running but its file may hold it.
assert.match(run('obsErrorText({ code: "restore_failed", error: "x" }, 500, "limits")'), /^The limits were not installed.*may still hold them.*a restart would install them\. — x$/);
assert.match(run('obsErrorText({ code: "restore_failed", error: "x" }, 500, "rules")'), /^The rules were not installed.*may still hold them.*without a change record\. — x$/);
assert.doesNotMatch(run('obsErrorText({ code: "restore_failed" }, 500, "rules")'), /nothing changed|was restored/);
// D11: a legacy exporter without counters (no observability block) keeps its row and says so.
const noStats = { configured: false, exporters: [], legacy_otel: { endpoint: "otel:4318", protocol: "http", tls: false, sample_ratio: 0.5 } };
const legacyRows = json(`obsSignalRows(${JSON.stringify(noStats)})`);
assert.equal(legacyRows.length, 1);
assert.deepEqual([legacyRows[0].name, legacyRows[0].signal, legacyRows[0].notCounted], ["otel", "traces", true]);
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

// Rendering checks run the shipped app.js + observability.js against a minimal Element stub.
class Element {
  constructor(tag = "div") { this.tag = tag; this.children = []; this.listeners = {}; this.dataset = {}; this.value = ""; this.className = ""; this.hidden = false; this.classList = { toggle() {}, add() {}, remove() {} }; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  append(...nodes) { nodes.forEach(n => this.appendChild(n)); }
  appendChild(n) { this.children.push(n); }
  replaceChildren(...nodes) { this.children = []; this.text = ""; this.append(...nodes); }
  set textContent(v) { this.children = []; this.text = String(v); }
  get textContent() { return (this.text || "") + this.children.map(n => n.textContent).join(" "); }
  setAttribute() {}
  querySelector(sel) { if (sel === "tbody") { let b = this.children.find(c => c.tag === "tbody"); if (!b) { b = new Element("tbody"); this.appendChild(b); } return b; } return null; }
  querySelectorAll() { return []; }
}
function appContext(fetchImpl) {
  const els = new Map();
  const byId = id => { if (!els.has(id)) els.set(id, new Element()); return els.get(id); };
  const c = vm.createContext({
    document: { getElementById: byId, querySelectorAll: () => [], createElement: t => new Element(t), addEventListener() {}, documentElement: new Element(), body: new Element() },
    navigator: { language: "en" }, location: { search: "" }, window: { location: { origin: "http://localhost" } }, URLSearchParams, structuredClone, console, fetch: fetchImpl,
  });
  for (const f of ["i18n.js", "app.js", "observability.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), c, { filename: f });
  return { c, byId, run: src => vm.runInContext(src, c) };
}
const cells = tr => tr.children.map(td => td.textContent);

// D11 + C3: the rendered cells, not just the helpers.
{
  const { byId, run } = appContext(async () => { throw Error("no network"); });
  run(`renderObsSignals(${JSON.stringify(noStats)})`);
  const row = byId("obs-signals").querySelector("tbody").children;
  assert.equal(row.length, 1);
  assert.deepEqual(cells(row[0]), ["otel", "traces", ...Array(4).fill("not counted (no observability block)"), "—", "—"]);
  run(`renderObsSignals(${JSON.stringify(withStats)})`);
  assert.deepEqual(cells(byId("obs-signals").querySelector("tbody").children[0]).slice(0, 4), ["otel", "traces", "3", "0"]);
  const exp = { name: "corp", endpoint: "collector:4317", protocol: "grpc", tls: true, header_names: ["authorization"], data_boundary: "internal",
    signals: ["audit"], effective_signals: ["audit"], limit: {}, queued_bytes: 0 };
  run(`renderObsExporters(${JSON.stringify({ exporters: [exp] })})`);
  const ex = byId("obs-exporters").querySelector("tbody").children[0];
  assert.equal(ex.children.length, 5);
  assert.ok(cells(ex)[1].endsWith("data boundary: internal"), cells(ex)[1]);
  assert.ok(!cells(ex).join("|").includes("secret"));
}

// Apply succeeds, the follow-up status GET fails: the apply result and the error stay visible.
const statusFixture = { configured: true, policy_source: "files", limits: { editable: true, revision: "sha256:r1" },
  rules: { editable: true, revision: "sha256:q1", content_rules: false, console: [] }, exporters: [], decisions: [], selections: [] };
async function applyScenario(getAfterApply, delayPreview) {
  let phase = "load", gate = null;
  const fetchImpl = async (url, opts) => {
    if (opts.method === "GET") {
      if (phase === "load") return { status: 200, text: async () => JSON.stringify(statusFixture) };
      return getAfterApply();
    }
    const body = JSON.parse(opts.body);
    if (body.dry_run && delayPreview) await new Promise(r => { gate = r; });
    const change = [{ exporter: "corp", field: "paused", before: false, after: true }];
    return { status: 200, text: async () => JSON.stringify(body.dry_run ? { changes: change } : { changes: change, result_revision: "sha256:0123456789abcdef" }) };
  };
  const app = appContext(fetchImpl);
  app.run("whoamiIsAdmin = true");
  await app.run("refreshObservabilityView()");
  return { ...app, release: () => gate && gate(), setPhase: p => { phase = p; } };
}
(async () => {
  {
    const a = await applyScenario(async () => { throw Error("network blip"); });
    await a.run("obsSubmitLimits(true)");
    assert.equal(a.byId("obs-limits-apply").disabled, false);
    a.setPhase("after");
    await a.run("obsSubmitLimits(false)");
    assert.match(a.byId("obs-limits-status").textContent, /^Applied\. Revision 0123456789ab\.$/);
    assert.match(a.byId("workspace-error").textContent, /network blip/);
    assert.equal(a.byId("workspace-error").hidden, false);
    assert.equal(a.byId("obs-content").hidden, false, "content (and its Refresh control) stays reachable");
    assert.equal(a.byId("obs-limits-apply").disabled, true);
    // A 403 on the follow-up GET shows the translated text, not the body.
    const b = await applyScenario(async () => ({ status: 403, text: async () => '{"error":"admin only"}' }));
    await b.run("obsSubmitLimits(true)"); b.setPhase("after"); await b.run("obsSubmitLimits(false)");
    assert.match(b.byId("workspace-error").textContent, /Only full administrators can view or change observability export\./);
    assert.match(b.byId("obs-limits-status").textContent, /^Applied\./);
    // A 200 whose body does not parse is a translated error, not a TypeError.
    const c = await applyScenario(async () => ({ status: 200, text: async () => "not json" }));
    await c.run("obsSubmitLimits(true)"); c.setPhase("after"); await c.run("obsSubmitLimits(false)");
    assert.match(c.byId("workspace-error").textContent, /Request failed \(HTTP 200\)\./);
  }
  {
    // An edit made while the dry run is pending must not arm Apply.
    const a = await applyScenario(async () => ({ status: 200, text: async () => JSON.stringify(statusFixture) }), true);
    const pending = a.run("obsSubmitLimits(true)");
    await new Promise(r => setImmediate(r));
    a.run('obsInvalidate("limits")');
    a.release();
    await pending;
    assert.equal(a.byId("obs-limits-apply").disabled, true);
    assert.equal(a.byId("obs-limits-status").textContent, "The form changed after the preview. Preview again.");
  }
  console.log("observability_console.cjs: ok");
})().catch(err => { console.error(err); process.exit(1); });
