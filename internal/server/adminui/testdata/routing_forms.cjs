// Runs the shipped app.js and its submit listeners with a small DOM boundary.
// No browser/package install or source-text assertions. Browser/CSP/layout QA is
// separate; this harness observes the JSON passed to the real fetch boundary.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

class Element {
  constructor(tag = "input") {
    this.tag = tag; this.value = ""; this.checked = false; this.children = [];
    this.listeners = {}; this.className = ""; this.dataset = {};
    this.classList = { toggle() {}, add() {}, remove() {} };
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  async fire(type) { for (const fn of this.listeners[type] || []) await fn({ preventDefault() {}, target: this }); }
  append(...children) { for (const c of children) this.appendChild(c); }
  appendChild(child) {
    child.parent = this; this.children.push(child);
    if (this.tag === "select" && (this.children.length === 1 || child.selected)) this.value = child.value;
  }
  set textContent(v) { this.children = []; this.text = v; }
  get textContent() { return this.text || ""; }
  setAttribute(key, value) { (this.attributes ||= {})[key] = value; }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  querySelectorAll(selector) {
    return this.children.flatMap(c => [
      ...(selector.startsWith(".") && c.className.split(" ").includes(selector.slice(1)) ? [c] : []),
      ...c.querySelectorAll(selector),
    ]);
  }
  querySelector(selector) {
    if (selector === "tbody") {
      let body = this.children.find(c => c.tag === "tbody");
      if (!body) { body = new Element("tbody"); this.appendChild(body); }
      return body;
    }
    return this.querySelectorAll(selector)[0] || null;
  }
  reset() {
    // Native reset dispatches its event before restoring default control values.
    for (const fn of this.listeners.reset || []) fn({ target: this });
    for (const [id, el] of elements) if (id.startsWith(this.prefix)) {
      el.value = defaults[id] || ""; el.checked = false;
    }
  }
}
const elements = new Map();
const defaults = { "pf-type": "anthropic", "pf-refkind": "env" };
const get = id => {
  if (!elements.has(id)) elements.set(id, new Element());
  return elements.get(id);
};
get("provider-form").prefix = "pf-";
get("model-form").prefix = "mf-";
get("provider-form").reset();
const writes = [];
const reads = [];
const provider = {
  name: "private", type: "anthropic", base_url: "https://private.invalid",
  auth: "bearer · env:PRIVATE_KEY", region: "us-east-1", data_boundary: "internal",
};
const providerConfig = {
  type: "anthropic", base_url: "https://private.invalid", region: "us-east-1",
  api_key_ref: { env: "PRIVATE_KEY" }, auth_header: "bearer", data_boundary: "internal",
};
const model = {
  name: "private", context_window: 32768,
  capabilities: ["tools", "vision", "reasoning", "structured_output"],
  aliases: ["private-alias"],
  targets: [{ provider: "private", model: "upstream", api: "invoke_model" },
    { provider: "private", model: "backup" }],
};
let exportFailure = false;
let exportExact = false;
let exportWait = null;
let writeFailure = false;
const context = vm.createContext({
  document: { getElementById: get, querySelectorAll: () => [], createElement: tag => new Element(tag), createElementNS: (_, tag) => new Element(tag), addEventListener() {}, documentElement: new Element("html"), body: new Element("body") },
  LANG: "en", applyLang() {}, msg: key => key, URLSearchParams, structuredClone,
  location: { search: "" }, window: { location: { origin: "http://localhost" } }, console,
  fetch: async (url, options = {}) => {
    reads.push(url);
    if (options.method === "PUT" || options.method === "POST") {
      writes.push({ path: url, body: JSON.parse(options.body) });
      if (writeFailure) return { ok: false, status: 400, text: async () => '{"error":"invalid configuration"}', json: async () => ({ error: "invalid configuration" }) };
    }
    if (url === "/fixture/exact") return { ok: true, status: 200, text: async () => '{"cost_micros":9007199254740993}' };
    if (url === "/admin/keys" && options.method === "POST") return { ok: true, status: 200, text: async () => '{"plaintext":"once-only-fixture"}' };
    if (url === "/admin/config/export") {
      if (exportWait) await exportWait;
      if (exportFailure) throw Error("export unavailable");
      return { ok: true, status: 200, text: async () => exportExact
        ? '{"providers":{},"int64":9007199254740993}'
        : JSON.stringify({ providers: { private: providerConfig } }), json: async () => ({ providers: { private: providerConfig } }) };
    }
    if (url === "/admin/config") throw Error("background refresh disabled in form unit test");
    return { ok: true, status: 200, text: async () => '{}', json: async () => ({}) };
  },
});
vm.runInContext(fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8"), context);
vm.runInContext(fs.readFileSync(path.join(__dirname, "../static/evaluation.js"), "utf8"), context);
const run = code => vm.runInContext(code, context);
context.fixtureProvider = provider;
context.fixtureModel = model;
run("lastProviders = [fixtureProvider]");

async function editProvider() {
  const cell = run("providerActions(fixtureProvider)");
  await cell.children[0].fire("click");
}
async function saveProvider() { await get("provider-form").fire("submit"); return writes.at(-1); }
async function saveModel() { await get("model-form").fire("submit"); return writes.at(-1); }

(async () => {
  await editProvider();
  const savedProvider = await saveProvider();
  run("fillModelForm(fixtureModel)");
  const savedModel = await saveModel();
  if (process.argv.includes("--views")) {
    run('caps = { teams_records: true, analytics_index: "A" }; showView("teams")');
    for (let i = 0; i < 30; i++) await Promise.resolve();
    assert(reads.includes("/admin/teams"), "team navigation did not read its records");
    assert(reads.includes("/admin/users"), "team navigation did not read derived users");
    const beforeLogs = reads.length;
    run('showView("logs")');
    for (let i = 0; i < 30; i++) await Promise.resolve();
    assert(reads.slice(beforeLogs).includes("/admin/logs"), "log navigation did not read its records");
    assert.equal(get("view-logs").hidden, false);
    assert.equal(get("view-teams").hidden, true);
    assert.equal(get("workspace-error").hidden, true);
    console.log("Gateway navigation checks passed: shipped view dispatch reads team/user and log APIs.");
    return;
  }
  if (process.argv.includes("--money")) {
    assert.equal(run('usd(5)'), '$0.000005', 'small settled cost disappeared');
    assert.equal(run('usd(9007199254740993n)'), '$9,007,199,254.740993');
    assert.equal((await run('api("GET", "/fixture/exact")')).cost_micros, 9007199254740993n, 'API rounded the int64 before display');
    exportExact = true;
    await get("export-btn").fire("click");
    assert.match(get("export-out").textContent, /9007199254740993/, "export failed or rounded an exact parsed int64");
    exportExact = false;
    run('renderSparkline(document.getElementById("stat-spend-spark"), [0, 9007199254740993n])');
    assert.equal(get("stat-spend-spark").children[0].children[0].attributes.d, 'M2,34 L158,2', 'chart lost exact cost ratio or crossed pixel bounds');
    assert.equal(run('budgetMicros("12.345678")'), 12345678);
    for (const invalid of ['-1', '1.1234567', '1e6', 'Infinity', '1000000001'])
      assert.throws(() => run('budgetMicros(' + JSON.stringify(invalid) + ')'));
    get("team-select").hidden = true; get("team").value = "engineering"; get("models").value = "private";
    get("kf-budget").value = "12.345678";
    get("kf-budget-day").value = "0.000001";
    await get("create-form").fire("submit");
    assert.equal(writes.at(-1).path, "/admin/keys");
    assert.equal(writes.at(-1).body.budget_usd_micros, 12345678, "key form changed the decimal amount");
    assert.equal(writes.at(-1).body.budget_usd_micros_per_day, 1, "key daily cap was omitted or rounded");
    run('whoamiIsAdmin = true');
    get("kf-attribution").value = "account_ref"; get("kf-account-ref").value = " legacy-account ";
    get("kf-owner").value = "stale-owner"; get("kf-tpm").value = "12345";
    await get("create-form").fire("submit");
    assert.equal(writes.at(-1).body.account_ref, " legacy-account ", "key form changed the registered financial selector");
    assert.equal(writes.at(-1).body.owner, undefined, "key form sent contradictory identity selectors");
    get("kf-attribution").value = "service_account"; get("kf-service-account").value = "ci-runner";
    await get("create-form").fire("submit");
    assert.equal(writes.at(-1).body.service_account, "ci-runner");
    assert.equal(writes.at(-1).body.account_ref, undefined);
    get("tf-name").value = "engineering"; get("tf-budget").value = "0.000001";
    get("tf-budget-day").value = "2.345678";
    await get("team-form").fire("submit");
    assert.equal(writes.at(-1).body.budget_usd_micros, 1, "team form rounded microUSD");
    assert.equal(writes.at(-1).body.budget_usd_micros_per_day, 2345678, "team form lost its daily budget");
    const before = writes.length; get("tf-budget").value = "1.1234567";
    await get("team-form").fire("submit");
    assert.equal(writes.length, before, "excess precision submitted a rounded cap");
    get("tf-budget").value = ""; get("tf-tpd").value = "1000.5";
    await get("team-form").fire("submit");
    assert.equal(writes.length, before, "daily token quota was silently truncated");
    get("kf-attribution").value = "owner"; get("kf-rpm").value = "1e2";
    await get("create-form").fire("submit");
    assert.equal(writes.length, before, "request rate accepted exponent notation through parseInt");
    get("kf-rpm").value = ""; get("kf-attribution").value = "account_ref"; get("kf-account-ref").value = "person";
    run('whoamiIsAdmin = false');
    await get("create-form").fire("submit");
    assert.equal(writes.length, before, "non-admin selected a privileged identity field");
    console.log("Gateway money checks passed: exact API int64, display and key/team form amounts.");
    return;
  }
  // Emit mode is consumed by the Go real PUT/readback/routing regression. Its
  // assertions also run against pre-fix code, exposing the original data loss.
  if (process.argv.includes("--payloads")) {
    process.stdout.write(JSON.stringify({ provider: savedProvider, model: savedModel }));
    return;
  }
  assert.equal(savedProvider.body.data_boundary, "internal", "ordinary provider save erased boundary");
  assert.deepEqual(savedProvider.body.api_key_ref, { env: "PRIVATE_KEY" });
  assert.equal(savedProvider.body.auth_header, "bearer");
  assert.equal(savedProvider.body.region, "us-east-1");
  assert.equal(savedModel.body.context_window, 32768, "ordinary model save erased context");
  assert.deepEqual(savedModel.body.capabilities, ["tools", "vision", "reasoning", "structured_output"]);
  assert.deepEqual(savedModel.body.aliases, ["private-alias"]);
  assert.deepEqual(savedModel.body.targets, model.targets);
  assert.equal(get("pf-data-boundary").value, "", "successful save did not reset boundary");
  assert.equal(get("mf-context-window").value, "", "successful save did not reset context");
  for (const cap of ["tools", "vision", "reasoning", "structured-output"])
    assert.equal(get("mf-cap-" + cap).checked, false, "successful save did not reset capability");

  await editProvider();
  assert.equal(get("pf-data-boundary").value, "internal");
  get("pf-data-boundary").value = "";
  get("pf-refkind").value = "none";
  get("pf-baseurl").value = "";
  const clearedProvider = await saveProvider();
  assert.equal(clearedProvider.body.data_boundary, "");
  assert.equal(clearedProvider.body.api_key_ref, undefined, "explicit ref clear was ignored");
  assert.equal(clearedProvider.body.base_url, undefined, "explicit endpoint clear was ignored");
  run("fillModelForm(fixtureModel)");
  assert.equal(Number(get("mf-context-window").value), 32768);
  get("mf-context-window").value = "";
  for (const cap of ["tools", "vision", "reasoning", "structured-output"]) get("mf-cap-" + cap).checked = false;
  const clearedModel = await saveModel();
  assert.equal(clearedModel.body.context_window, 0);
  assert.deepEqual(clearedModel.body.capabilities, []);
  assert.deepEqual(clearedModel.body.aliases, ["private-alias"]);

  await editProvider();
  get("pf-data-boundary").value = "external";
  assert.equal((await saveProvider()).body.data_boundary, "external");
  run("fillModelForm(fixtureModel)");
  get("mf-context-window").value = "16384";
  get("mf-cap-reasoning").checked = false;
  const changedModel = await saveModel();
  assert.equal(changedModel.body.context_window, 16384);
  assert.deepEqual(changedModel.body.capabilities, ["tools", "vision", "structured_output"]);
  run("fillModelForm(fixtureModel)");
  for (const invalid of ["-1", "1.5", "9007199254740992"]) {
    get("mf-context-window").value = invalid;
    const before = writes.length;
    await saveModel();
    assert.equal(writes.length, before, "invalid context submitted a replacement");
  }
  get("mf-context-window").value = "32768";
  writeFailure = true;
  await saveModel();
  assert.equal(get("mf-name").value, "private", "failed save discarded the draft");
  writeFailure = false;
  assert.deepEqual((await saveModel()).body.aliases, ["private-alias"], "retry lost aliases");

  // A new entry after editing must not inherit invisible aliases/auth settings.
  await editProvider();
  get("provider-form").reset();
  get("pf-name").value = "new-provider";
  const freshProvider = await saveProvider();
  assert.equal(freshProvider.body.auth_header, undefined);
  assert.equal(freshProvider.body.api_key_ref, undefined);
  assert.equal(freshProvider.body.data_boundary, "");
  run("fillModelForm(fixtureModel)");
  get("model-form").reset();
  get("mf-name").value = "new-model";
  get("mf-targets").querySelector(".t-model").value = "new-upstream";
  const freshModel = await saveModel();
  assert.deepEqual(freshModel.body.aliases, []);
  assert.equal(freshModel.body.context_window, 0);
  assert.deepEqual(freshModel.body.capabilities, []);
  run("fillModelForm(fixtureModel)");
  get("mf-name").value = "copied-model";
  assert.deepEqual((await saveModel()).body.aliases, [], "renaming a draft copied conflicting aliases");

  providerConfig.api_key_ref = { file: "/run/secrets/private-key" };
  await editProvider();
  assert.equal(get("pf-refkind").value, "file");
  assert.deepEqual((await saveProvider()).body.api_key_ref, { file: "/run/secrets/private-key" });

  // IAM profile and guardrail are preserved even though the profile has no UI.
  Object.assign(provider, { type: "bedrock", auth: "IAM · profile", guardrail_id: "guard", guardrail_version: "2" });
  Object.assign(providerConfig, { type: "bedrock", auth: { mode: "profile", profile: "development" },
    guardrail_id: "guard", guardrail_version: "2" });
  delete providerConfig.api_key_ref; delete providerConfig.auth_header;
  await editProvider();
  const iam = await saveProvider();
  assert.deepEqual(iam.body.auth, { mode: "profile", profile: "development" });
  assert.equal(iam.body.guardrail_id, "guard"); assert.equal(iam.body.guardrail_version, "2");

  // Reset while the export is in flight must invalidate the edit callback.
  let release;
  exportWait = new Promise(resolve => { release = resolve; });
  const pendingEdit = editProvider();
  get("provider-form").reset();
  release();
  await pendingEdit;
  exportWait = null;
  assert.equal(get("pf-name").value, "", "late export refilled a new draft");
  assert.equal(get("pf-data-boundary").value, "");

  exportFailure = true;
  await editProvider();
  assert.equal(get("pf-name").value, "", "failed baseline load must not open a destructive edit");
  assert.match(get("provider-form-status").textContent, /export unavailable/);
  process.stdout.write("routing form behavior: PASS\n");
})().catch(err => { console.error(err); process.exitCode = 1; });
