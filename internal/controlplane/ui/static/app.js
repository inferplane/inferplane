// Data-free shell; all API data is bearer gated. Both credentials live only in
// this closure. The only persisted values are ADR-037's short-lived PKCE keys.
(function () {
  "use strict";
  const $ = id => document.getElementById(id);
  const views = {
    overview: ["Overview", "Your fleet. One control plane.", "Consumption, policy distribution, and the nodes reporting in."],
    usage: ["Usage & costs", "Know where consumption goes.", "Reported settlements by team and model, with cache classes kept separate."],
    budgets: ["Budgets", "Set spending limits.", "Create and adjust team or user budgets, with independent daily and monthly windows."],
    fleet: ["Data planes", "Inspect the distribution edge.", "Recent heartbeats, generation coverage, and recorded policy rejections."],
    policies: ["Policies", "Govern deliberately.", "Inspect, review, and publish the documents distributed to your data planes."],
  };
  const tokenFields = ["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens"];
  let token = "", writeToken = "", ssoSession = false, connected = false;
  let session = 0, refreshID = 0, modelID = 0, activeView = "overview";
  let controller = new AbortController();
  let oidcRequests = Promise.resolve();
  let usage = null, fleet = null, policies = null, errors = {};
  let appliedRange = null, pendingChange = null, dirty = false;

  function el(tag, text, cls) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = String(text);
    if (cls) node.className = cls;
    return node;
  }
  function message(id, text) { $(id).textContent = text || ""; $(id).hidden = !text; }
  function setError(msg) { message("error", msg); }
  function badge(text, kind = "neutral") { return el("span", text, "badge " + kind); }
  function setBadge(id, text, kind = "neutral") { $(id).textContent = text; $(id).className = "badge " + kind; }
  function empty(parent, title, detail) {
    const box = el("div", undefined, "empty-state");
    box.append(el("strong", title), el("span", detail)); parent.append(box);
  }

  // JSON numbers otherwise round int64 µUSD before BigInt ever sees them.
  // Parse the JSON grammar, retaining unsafe integers as BigInt. Strings are
  // decoded by JSON.parse, never by eval or DOM markup. No tagged-string guesses.
  function parseExactJSON(raw) {
    const tokens = raw.match(/"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null|[{}\[\]:,]/g) || [];
    // Validate the entire original grammar too (reject unmatched garbage).
    JSON.parse(raw);
    let at = 0;
    const take = expected => { if (tokens[at++] !== expected) throw Error("Invalid JSON response"); };
    function value() {
      const t = tokens[at++];
      if (t === "{") {
        const obj = Object.create(null);
        if (tokens[at] !== "}") while (true) {
          const key = JSON.parse(tokens[at++]); take(":"); obj[key] = value();
          if (tokens[at] !== ",") break; at++;
        }
        take("}"); return obj;
      }
      if (t === "[") {
        const arr = [];
        if (tokens[at] !== "]") while (true) { arr.push(value()); if (tokens[at] !== ",") break; at++; }
        take("]"); return arr;
      }
      if (/^-?\d+$/.test(t)) {
        const n = BigInt(t);
        return n > BigInt(Number.MAX_SAFE_INTEGER) || n < BigInt(Number.MIN_SAFE_INTEGER) ? n : Number(t);
      }
      if (/^-?\d/.test(t)) throw Error("Use plain integer JSON numbers; decimals and exponent notation are not supported.");
      return JSON.parse(t);
    }
    return value();
  }
  function exactJSON(value, indent = 0) {
    function encode(v, depth) {
      if (typeof v === "bigint") return v.toString();
      if (v === null || typeof v !== "object") return JSON.stringify(v);
      const array = Array.isArray(v);
      const entries = array ? v : Object.keys(v).filter(k => v[k] !== undefined);
      const parts = entries.map(k => array ? encode(k, depth + 1) : JSON.stringify(k) + (indent ? ": " : ":") + encode(v[k], depth + 1));
      const [left, right] = array ? ["[", "]"] : ["{", "}"];
      return left + (parts.length ? (indent ? "\n" + " ".repeat((depth + 1) * indent) : "") + parts.join(indent ? ",\n" + " ".repeat((depth + 1) * indent) : ",") + (indent ? "\n" + " ".repeat(depth * indent) : "") : "") + right;
    }
    return encode(value, 0);
  }
  function integer(value) {
    if (typeof value === "number" && !Number.isSafeInteger(value)) throw Error("Usage contains an unsafe integer");
    if (!/^-?\d+$/.test(String(value))) throw Error("Usage contains an invalid integer");
    return BigInt(value);
  }
  function usd(micros) {
    const n = integer(micros), s = (n < 0n ? -n : n).toString().padStart(7, "0");
    return (n < 0n ? "-$" : "$") + s.slice(0, -6).replace(/\B(?=(\d{3})+(?!\d))/g, ",") + "." + s.slice(-6);
  }
  function count(value) { return integer(value).toLocaleString("en-US"); }
  function compareSpend(a, b) {
    const x = integer(a.spent_micro_usd), y = integer(b.spent_micro_usd);
    return x === y ? String(a.key).localeCompare(String(b.key)) : x > y ? -1 : 1;
  }

  async function request(path, { method = "GET", body, write = false } = {}) {
    const auth = write ? writeToken || token : token;
    const sid = session, signal = controller.signal;
    async function send() {
      if (sid !== session || signal.aborted) {
        const err = Error("Request canceled after disconnect."); err.name = "AbortError"; throw err;
      }
      const resp = await fetch(path, {
        method, signal, cache: "no-store", credentials: "omit", redirect: "error",
        headers: { ...(auth ? { Authorization: "Bearer " + auth } : {}), ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
        body: body !== undefined ? exactJSON(body) : undefined,
      });
      if (resp.status === 204) return null;
      if (!resp.ok) {
        let msg = "Request failed (HTTP " + resp.status + ")";
        if (resp.status === 401) msg = "Unauthorized — reconnect with a valid credential.";
        else if (resp.status === 403) msg = write ? "Policy write refused. Select a dedicated write token or use console SSO." : "Access refused by the control plane.";
        else if (resp.status === 503) msg = "Store unavailable (HTTP 503).";
        if (write && resp.status !== 401 && resp.status !== 403) {
          try { const data = await resp.json(); if (typeof data.error === "string") msg = data.error; } catch { /* generic error */ }
        }
        const err = Error(msg); err.status = resp.status; throw err;
      }
      return parseExactJSON(await resp.text());
    }
    // The deployed CP's missLimitedKeySet refuses another signature check
    // while one is in flight. Queue OIDC API reads/writes (including initial
    // bootstrap and refresh), not just the token exchange. Never retry a 401
    // or weaken server verification. Static-token requests remain independent.
    if (!ssoSession) return send();
    const pending = oidcRequests.then(send);
    oidcRequests = pending.catch(() => {}); // scheduling only; caller still receives the error
    return pending;
  }
  function dateRange() {
    const since = $("since").value, until = $("until").value;
    if (!/^\d{4}-\d{2}-\d{2}$/.test(since) || !/^\d{4}-\d{2}-\d{2}$/.test(until)) throw Error("Select both reporting dates.");
    const start = new Date(since + "T00:00:00Z"), end = new Date(until + "T00:00:00Z");
    if (!Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || start > end || start.toISOString().slice(0, 10) !== since || end.toISOString().slice(0, 10) !== until) throw Error("Select valid dates with From on or before To.");
    end.setUTCDate(end.getUTCDate() + 1);
    return { since: start.toISOString(), until: end.toISOString() };
  }
  const usagePath = params => "/v1alpha1/usage?" + new URLSearchParams(params);
  function canWrite() { return connected && !!policies?.writable && (!!writeToken || ssoSession || token === ""); }

  function showView(which) {
    if (!connected) return;
    activeView = which;
    for (const key of Object.keys(views)) {
      $(key + "-view").hidden = key !== which;
      $("nav-" + key).classList.toggle("active", key === which);
      if (key === which) $("nav-" + key).setAttribute("aria-current", "page");
      else $("nav-" + key).removeAttribute("aria-current");
    }
    $("breadcrumb").textContent = views[which][0];
    $("page-title").textContent = views[which][1];
    $("page-description").textContent = views[which][2];
    $("controls").hidden = which !== "overview" && which !== "usage";
    $("degraded-banner").hidden = !(usage?.degraded || modelDegraded) || (which !== "overview" && which !== "usage");
  }

  function clearData() {
    usage = fleet = policies = null; errors = {}; dirty = false; pendingChange = null; modelDegraded = false;
    for (const id of ["stat-spend", "stat-teams", "stat-tokens", "stat-nodes", "usage-total", "fleet-generation", "fleet-api-versions", "fleet-coverage", "policy-generation", "fleet-nav-count", "policy-nav-count", "attention-count", "policy-store-status"]) $(id).textContent = "—";
    for (const id of ["spend-breakdown", "attention-list", "fleet-preview-list", "policies-list", "node-rejections"]) $(id).textContent = "";
    for (const id of ["teams", "models", "fleet-table"]) $(id).querySelector("tbody").textContent = "";
    for (const id of ["models-section", "node-details", "fleet-nav-count", "policy-nav-count", "degraded-banner"]) $(id).hidden = true;
    $("model-team-label").textContent = "Models"; $("model-status").textContent = "";
    $("node-title").textContent = "Rejection details"; $("policy-review").textContent = ""; $("policy-editor").value = "";
    $("dialog-title").textContent = "Review before publishing"; $("dialog-description").textContent = "";
    message("dialog-error", "");
    $("stat-spend-detail").textContent = "Selected reporting period";
    $("stat-nodes-detail").textContent = "Seen within the last 10 minutes";
    for (const id of ["team-search", "fleet-search", "policy-search"]) $(id).value = "";
    $("policy-dialog").close(); $("new-policy").disabled = true;
    $("budgets-list").textContent = $("budget-access-status").textContent = "";
    $("budget-nav-count").textContent = ""; $("budget-nav-count").hidden = true;
    $("budget-search").value = $("budget-period-filter").value = "";
    $("new-budget").disabled = true; clearBudgetForm();
    $("last-updated").textContent = "Not refreshed";
    $("write-status").textContent = "No dedicated write credential selected.";
    message("notice", "");
  }
  function lock() {
    session++; refreshID++; modelID++; controller.abort(); controller = new AbortController();
    oidcRequests = Promise.resolve();
    token = writeToken = ""; connected = ssoSession = false;
    $("token").value = $("write-token").value = "";
    clearData();
    $("auth-section").hidden = false; $("workspace-content").hidden = true;
    $("heading-actions").hidden = $("disconnect").hidden = true;
    for (const key of Object.keys(views)) {
      $("nav-" + key).disabled = true;
      $("nav-" + key).classList.toggle("active", key === "overview");
      if (key === "overview") $("nav-" + key).setAttribute("aria-current", "page");
      else $("nav-" + key).removeAttribute("aria-current");
    }
    activeView = "overview"; $("breadcrumb").textContent = "Overview";
    $("page-title").textContent = views.overview[1]; $("page-description").textContent = views.overview[2];
    setBadge("connection-status", "Not connected");
  }
  async function unlock(tok, fromSSO = false) {
    lock(); setError(""); token = tok; ssoSession = fromSSO || /^[^.]+\.[^.]+\.[^.]+$/.test(tok);
    $("connect").disabled = true; $("connect").textContent = "Connecting…";
    const expected = session;
    try { await refresh(true); }
    finally {
      if (expected === session || !connected) { $("connect").disabled = false; $("connect").textContent = "Connect to workspace →"; }
    }
  }

  async function refresh(connecting = false) {
    if (!connecting && !connected) return;
    let nextRange;
    try { nextRange = dateRange(); } catch (e) { setError(e.message); return; }
    if (dirty && !confirm("Refreshing discards unpublished form changes. Continue?")) return;
    const sid = session, rid = ++refreshID;
    modelID++; modelDegraded = false; $("models-section").hidden = true; $("node-details").hidden = true;
    $("refresh").disabled = true; $("refresh").textContent = "Refreshing…";
    setError(""); message("notice", "");
    const outcomes = await Promise.allSettled([
      request(usagePath({ group_by: "team", ...nextRange })), request("/v1alpha1/dataplanes"), request("/v1alpha1/policies"),
    ]);
    if (sid !== session || rid !== refreshID) return;
    $("refresh").disabled = false; $("refresh").textContent = "↻ Refresh";
    const denied = outcomes.find(r => r.status === "rejected" && (r.reason.status === 401 || r.reason.status === 403));
    if (denied) { lock(); setError(denied.reason.message); return; }
    if (connecting && !outcomes.some(r => r.status === "fulfilled")) {
      lock(); setError("Cannot connect: no control-plane data API is available."); return;
    }
    appliedRange = nextRange; errors = {};
    const [u, f, p] = outcomes.map((r, i) => {
      if (r.status === "fulfilled") return r.value;
      errors[["usage", "fleet", "policies"][i]] = r.reason.status === 404 ? "API not mounted on this deployment." : r.reason.message;
      return null;
    });
    // Malformed success responses are not fabricated as healthy empty data.
    if (u) {
      try { validateUsage(u); }
      catch (e) { errors.usage = "Invalid usage response: " + e.message; }
    }
    if (f && (!f.dataplanes || typeof f.dataplanes !== "object" || Array.isArray(f.dataplanes))) errors.fleet = "Invalid fleet response.";
    if (p && (!Array.isArray(p.policies) || typeof p.writable !== "boolean")) errors.policies = "Invalid policy response.";
    usage = errors.usage ? null : u; fleet = errors.fleet ? null : f; policies = errors.policies ? null : p;
    if (connecting && !usage && !fleet && !policies) { lock(); setError("No valid control-plane data response."); return; }
    connected = true; dirty = false;
    $("auth-section").hidden = true; $("workspace-content").hidden = false;
    $("heading-actions").hidden = $("disconnect").hidden = false;
    for (const key of Object.keys(views)) $("nav-" + key).disabled = false;
    setBadge("connection-status", "Connected", "success");
    $("range-summary").textContent = nextRange.since.slice(0, 10) + " → " + $("until").value;
    $("last-updated").textContent = "Fetched " + new Date().toISOString().slice(11, 19) + " UTC";
    try { renderUsage(); renderFleet(); renderPolicies(); renderAttention(); showView(activeView); }
    catch (e) { setError("Cannot display response: " + e.message); }
  }

  function validateUsage(res) {
    if (!Array.isArray(res.rows) || integer(res.total_micro_usd) < 0n) throw Error("Expected nonnegative reported totals.");
    for (const row of res.rows) {
      if (typeof row.key !== "string") throw Error("Expected a usage grouping key.");
      for (const key of ["spent_micro_usd", ...tokenFields]) if (integer(row[key]) < 0n) throw Error("Negative usage class.");
    }
  }

  function fillRows(tableID, rows, clickable) {
    const tbody = $(tableID).querySelector("tbody"); tbody.textContent = "";
    if (!rows.length) { const row = el("tr"), cell = el("td", "No reported usage in this range."); cell.setAttribute("colspan", "7"); row.append(cell); tbody.append(row); return; }
    for (const row of rows) {
      const tr = el("tr"), name = el("td");
      if (clickable) {
        const button = el("button", row.key || "(unattributed)", "team-button");
        button.addEventListener("click", () => { showView("usage"); return loadModels(row.key); }); name.append(button);
      } else name.textContent = row.key || "(unattributed)";
      tr.append(name, el("td", usd(row.spent_micro_usd)), ...tokenFields.map(k => el("td", count(row[k])))); tbody.append(tr);
    }
  }
  function renderTeams() {
    if (!usage) { $("teams").querySelector("tbody").textContent = ""; return; }
    let rows = usage.rows.filter(r => String(r.key).toLowerCase().includes($("team-search").value.toLowerCase()));
    rows = [...rows].sort($("usage-sort").value === "name" ? (a, b) => String(a.key).localeCompare(String(b.key)) : compareSpend);
    fillRows("teams", rows, true);
  }
  function renderUsage() {
    $("spend-breakdown").textContent = "";
    if (!usage) {
      for (const id of ["stat-spend", "stat-teams", "stat-tokens", "usage-total"]) $(id).textContent = "—";
      $("stat-spend-detail").textContent = errors.usage || "Usage unavailable";
      const tbody = $("teams").querySelector("tbody"), tr = el("tr"), td = el("td", errors.usage || "Usage unavailable");
      tbody.textContent = ""; td.setAttribute("colspan", "7"); tr.append(td); tbody.append(tr);
      empty($("spend-breakdown"), "Usage unavailable", errors.usage || "No usage API response.");
      $("export-usage").disabled = true; return;
    }
    const total = integer(usage.total_micro_usd);
    $("stat-spend").textContent = usd(total); $("stat-teams").textContent = count(usage.rows.length);
    const tokens = usage.rows.reduce((n, row) => n + tokenFields.reduce((s, k) => s + integer(row[k]), 0n), 0n);
    $("stat-tokens").textContent = count(tokens); $("usage-total").textContent = "Total " + usd(total);
    $("stat-spend-detail").textContent = usage.degraded ? "Incomplete · recent fallback only" : "Selected reporting period · µUSD precision";
    $("export-usage").disabled = false;
    const top = [...usage.rows].sort(compareSpend).slice(0, 5);
    if (!top.length) empty($("spend-breakdown"), "No usage reported", "Try a different period or check telemetry ingestion.");
    for (const row of top) {
      const item = el("div", undefined, "spend-row"), label = el("div"), button = el("button", row.key || "(unattributed)", "team-button");
      button.addEventListener("click", () => { showView("usage"); return loadModels(row.key); });
      const progress = el("progress"); progress.max = 10000;
      // Integer ratio; only the bounded presentation value crosses to Number.
      progress.value = total > 0n ? Number(integer(row.spent_micro_usd) * 10000n / total) : 0;
      progress.setAttribute("aria-label", "Share of reported spend for " + row.key);
      label.append(button, progress); item.append(el("span", String(row.key || "?").slice(0, 1).toUpperCase(), "team-avatar"), label, el("span", usd(row.spent_micro_usd), "spend-amount")); $("spend-breakdown").append(item);
    }
    renderTeams();
  }
  let modelDegraded = false;
  async function loadModels(team) {
    const sid = session, mid = ++modelID;
    modelDegraded = false; showView(activeView);
    $("models-section").hidden = false; $("model-team-label").textContent = "Models · " + (team || "(unattributed)");
    $("models").querySelector("tbody").textContent = ""; $("model-status").textContent = "Loading model usage…";
    try {
      // An empty team is not a valid API filter (it means all teams).
      if (!team) throw Error("The API cannot isolate an empty team identifier; no all-team total is substituted.");
      const res = await request(usagePath({ group_by: "model", team, ...appliedRange }));
      if (sid !== session || mid !== modelID) return;
      validateUsage(res);
      modelDegraded = !!res.degraded; showView(activeView);
      fillRows("models", [...res.rows].sort(compareSpend), false);
      $("model-status").textContent = (res.degraded ? "Incomplete fallback · " : "") + "Total " + usd(res.total_micro_usd);
    } catch (e) {
      if (sid !== session || mid !== modelID) return;
      if (e.status === 401 || e.status === 403) { lock(); setError(e.message); return; }
      $("model-status").textContent = e.message;
    }
  }

  const nodeEntries = () => fleet ? Object.entries(fleet.dataplanes).sort(([a], [b]) => a.localeCompare(b)) : [];
  function distribution(node) {
    return fleet?.generation && node.generation ? node.generation === fleet.generation ? ["Generation matches", "success"] : ["Generation differs", "warning"] : ["Generation unknown", "neutral"];
  }
  function heartbeat(value) {
    const d = new Date(value);
    return Number.isFinite(d.getTime()) ? d.toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC") : "Unknown";
  }
  function renderFleet() {
    const entries = nodeEntries();
    $("fleet-nav-count").hidden = !fleet; $("fleet-nav-count").textContent = String(entries.length);
    $("stat-nodes").textContent = fleet ? count(entries.length) : "—";
    $("stat-nodes-detail").textContent = fleet ? "Seen within the last 10 minutes" : errors.fleet || "Fleet unavailable";
    $("fleet-generation").textContent = fleet?.generation || "Unavailable";
    $("fleet-api-versions").textContent = (fleet?.apiVersions || []).join(", ") || "Unavailable";
    const matching = entries.filter(([, node]) => distribution(node)[0] === "Generation matches").length;
    setBadge("fleet-coverage", fleet ? matching + " / " + entries.length + " generations match" : "Fleet unavailable", fleet && matching === entries.length && entries.length ? "success" : "neutral");
    const preview = $("fleet-preview-list"); preview.textContent = "";
    if (!entries.length) empty(preview, fleet ? "No recent data planes" : "Fleet unavailable", errors.fleet || "The API does not report nodes absent for more than 10 minutes.");
    for (const [id, node] of entries.slice(0, 4)) {
      const row = el("div", undefined, "fleet-preview-row"); row.append(el("code", id), badge(...distribution(node)), el("span", heartbeat(node.lastSeen), "muted")); preview.append(row);
    }
    renderFleetTable();
  }
  function renderFleetTable() {
    const tbody = $("fleet-table").querySelector("tbody"); tbody.textContent = "";
    const entries = nodeEntries().filter(([id]) => id.toLowerCase().includes($("fleet-search").value.toLowerCase()));
    if (!entries.length) { const tr = el("tr"), cell = el("td", errors.fleet || "No recent data planes match."); cell.setAttribute("colspan", "6"); tr.append(cell); tbody.append(tr); }
    for (const [id, node] of entries) {
      const tr = el("tr"), state = el("td"), gen = el("td"), rejections = el("td");
      state.append(badge(...distribution(node))); gen.append(el("code", node.generation || "—"));
      const recorded = node.rejections || [];
      if (recorded.length) {
        const button = el("button", recorded.length + " · inspect", "text-button"); button.addEventListener("click", () => showRejections(id, recorded)); rejections.append(button);
      } else rejections.textContent = "None recorded";
      tr.append(el("td", id), state, gen, el("td", (node.apiVersions || []).join(", ") || "—"), el("td", heartbeat(node.lastSeen)), rejections); tbody.append(tr);
    }
  }
  function showRejections(id, recorded) {
    $("node-title").textContent = "Recorded rejections · " + id; $("node-details").hidden = false;
    const list = $("node-rejections"); list.textContent = "";
    for (const item of recorded) {
      const box = el("article", undefined, "rejection"); box.append(el("h3", item.policy + (item.rule ? " / " + item.rule : "")), el("p", item.reason)); list.append(box);
    }
  }
  function renderAttention() {
    const items = Object.entries(errors).map(([key, detail]) => [views[key]?.[0] || "Usage", detail]);
    if (usage?.degraded) items.push(["Usage history is incomplete", "Durable storage is unavailable. Totals use recent in-memory data only."]);
    const entries = nodeEntries(), mismatches = entries.filter(([, n]) => distribution(n)[0] === "Generation differs"), rejected = entries.filter(([, n]) => n.rejections?.length);
    if (mismatches.length) items.push([mismatches.length + " generation mismatch" + (mismatches.length === 1 ? "" : "es"), "Inspect data planes before assuming a policy rollout is complete."]);
    if (rejected.length) items.push([rejected.length + " node(s) have recorded rejections", "Inspect the policy and rule reasons. Records may outlive the failure."]);
    if (fleet && !entries.length) items.push(["No recent heartbeat observations", "This is not proof that the fleet is offline."]);
    const list = $("attention-list"); list.textContent = ""; $("attention-count").textContent = String(items.length);
    if (!items.length) empty(list, "No issues in these observations", "This view does not prove readiness, fleet HA, or durable history completeness.");
    for (const [title, detail] of items) { const row = el("div", undefined, "attention-item"), text = el("div"); text.append(el("strong", title), el("p", detail)); row.append(el("span", "!", "attention-icon"), text); list.append(row); }
  }

  function renderBudgets() {
    $("new-budget").disabled = !canWrite();
    $("budget-access-status").textContent = !policies ? "Budget settings unavailable. " + (errors.policies || "")
      : !policies.writable ? "Read-only: this deployment manages policies from files."
      : !canWrite() ? "Read-only: select a dedicated write credential in Policies, or sign in with console SSO."
      : "Changes require review and server authorization before publishing.";
    const all = (policies?.policies || []).flatMap(view => (view.policy.spec?.rules || [])
      .flatMap((rule, index) => rule.budget ? [{ view, rule, index }] : []));
    $("budget-nav-count").textContent = String(all.length); $("budget-nav-count").hidden = !policies;
    const term = $("budget-search").value.toLowerCase(), period = $("budget-period-filter").value;
    const entries = all.filter(({ view, rule }) => {
      const subject = view.policy.spec.subject || {};
      return [view.name, rule.name, subject.team, subject.user].join(" ").toLowerCase().includes(term)
        && (!period || period === (rule.budget.unlimited ? "unlimited" : rule.budget.period || "CalendarMonth"));
    });
    const list = $("budgets-list"); list.textContent = "";
    if (!entries.length) empty(list, !policies ? "Budget settings unavailable" : all.length ? "No budgets match" : "No budget rules configured",
      !policies ? errors.policies || "Refresh to try again." : all.length ? "Change the search or window filter." : "Create a budget to set a daily or monthly limit. Other configured limits still apply.");
    for (const { view, rule, index } of entries) {
      const card = el("article", undefined, "panel budget-card"), heading = el("div", undefined, "panel-heading"), title = el("div");
      const subject = view.policy.spec.subject || {}, budget = rule.budget;
      title.append(el("p", view.name, "eyebrow"), el("h2", rule.name),
        el("p", "Team: " + (subject.team || "Any") + " · User: " + (subject.user || "All team users"), "policy-subject"));
      const edit = el("button", budget.unlimited ? "Edit document" : "Edit budget", "button secondary budget-edit");
      edit.disabled = !canWrite(); edit.addEventListener("click", () => budget.unlimited ? openEditor(view) : openBudget(view, index));
      heading.append(title, edit); card.append(heading);
      const detail = el("div", undefined, "budget-summary");
      detail.append(el("strong", budget.unlimited ? "Unlimited declaration" : usd(integer(budget.limitMilliUSD) * 1000n).slice(0, -3), "budget-value"),
        badge(budget.unlimited ? "No window" : budget.period === "CalendarDay" ? "Daily" : "Monthly", "neutral"),
        badge(budget.unlimited ? "Does not override other caps" : budget.hardCap ? "Hard cap" : "Soft threshold", budget.hardCap ? "accent" : "neutral"));
      card.append(detail); list.append(card);
    }
  }

  const budgetInputs = ["bf-policy", "bf-policy-name", "bf-team", "bf-user", "bf-rule", "bf-period", "bf-limit", "bf-contact", "bf-hard"];
  function clearBudgetForm() {
    $("budget-form-fields").hidden = true;
    for (const id of budgetInputs) { $(id).value = ""; $(id).disabled = false; }
    $("bf-hard").checked = false; $("bf-policy").textContent = "";
  }
  function budgetPolicySelected() {
    const name = $("bf-policy").value;
    const view = (policies?.policies || []).find(v => v.name === name);
    const subject = view?.policy.spec?.subject || {};
    $("bf-policy-name").value = view?.name || ""; $("bf-policy-name").disabled = !!view;
    $("bf-team").value = subject.team || ""; $("bf-team").disabled = !!view;
    $("bf-user").value = subject.user || ""; $("bf-user").disabled = !!view;
  }
  function openBudget(view = null, index = null) {
    if (!canWrite()) return;
    const rule = index === null ? null : view?.policy.spec.rules[index];
    if (rule && (!rule.budget || rule.budget.unlimited)) return;
    clearBudgetForm();
    pendingChange = { mode: "budget", view, index, session };
    dialogBase(rule ? "Edit budget · " + rule.name : "New budget", "Choose a policy and budget window, then review the complete policy before publishing. Other rules and existing lease settings are preserved.");
    $("budget-form-fields").hidden = false; $("policy-review").hidden = true;
    const choice = el("option", "Create a new policy"); choice.value = ""; $("bf-policy").append(choice);
    for (const item of policies.policies) { const option = el("option", item.name); option.value = item.name; $("bf-policy").append(option); }
    $("bf-policy").value = view?.name || ""; $("bf-policy").disabled = !!rule; budgetPolicySelected();
    $("bf-rule").value = rule?.name || ""; $("bf-rule").disabled = !!rule;
    $("bf-period").value = rule?.budget.period || "CalendarMonth";
    if (rule) {
      const amount = integer(rule.budget.limitMilliUSD).toString().padStart(4, "0");
      $("bf-limit").value = amount.slice(0, -3) + "." + amount.slice(-3);
    }
    $("bf-hard").checked = rule ? !!rule.budget.hardCap : true;
    $("bf-contact").value = rule?.budget.adminContact || "";
    $("dialog-confirm").textContent = "Review budget";
  }
  function budgetMilliUSD(value) {
    const match = /^(\d+)(?:\.(\d{1,3}))?$/.exec(value.trim());
    if (!match) throw Error("Enter a positive USD amount with up to three decimal places; do not use exponent notation.");
    const amount = BigInt(match[1]) * 1000n + BigInt((match[2] || "").padEnd(3, "0"));
    // Match policy's int64 microUSD ceiling, without float multiplication.
    if (amount <= 0n || amount > 9223372036854775n) throw Error("Budget must be above $0 and at most $9,223,372,036,854.775.");
    return amount <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(amount) : amount;
  }
  function reviewBudget(change) {
    const editing = change.index !== null;
    const selected = $("bf-policy").value;
    const view = editing ? change.view : policies.policies.find(v => v.name === selected);
    if (selected && !view) throw Error("Selected policy is unavailable. Refresh and try again.");
    const name = view?.name || $("bf-policy-name").value.trim();
    if (!name || /[\s/]/.test(name)) throw Error("Enter a policy name without spaces or slashes.");
    if (!view && policies.policies.some(v => v.name === name)) throw Error("That policy already exists. Select it from the Policy list to add a budget.");
    let doc;
    if (view) doc = structuredClone(view.policy);
    else {
      const team = $("bf-team").value.trim(), user = $("bf-user").value.trim();
      if (!team && !user) throw Error("Select a team, a user account reference, or both.");
      doc = { apiVersion: "inferplane.dev/v1alpha1", kind: "GovernancePolicy", metadata: { name },
        spec: { subject: { ...(team ? { team } : {}), ...(user ? { user } : {}) }, rules: [] } };
    }
    const ruleName = editing ? doc.spec.rules[change.index].name : $("bf-rule").value.trim();
    if (!ruleName) throw Error("Enter a budget name.");
    if (!editing && doc.spec.rules.some(r => r.name === ruleName)) throw Error("That rule name already exists in the selected policy. Choose a different budget name.");
    const period = $("bf-period").value;
    if (!["CalendarDay", "CalendarMonth"].includes(period)) throw Error("Select a calendar day or calendar month window.");
    const amount = budgetMilliUSD($("bf-limit").value), hard = $("bf-hard").checked, contact = $("bf-contact").value;
    const rule = editing ? doc.spec.rules[change.index] : { name: ruleName, failurePolicy: hard ? "FailClosed" : "FailOpen", budget: {} };
    const prev = rule.budget;
    if (!editing || integer(amount) !== integer(prev.limitMilliUSD)) prev.limitMilliUSD = amount;
    if (!editing || period !== (prev.period || "CalendarMonth")) prev.period = period;
    if (!editing || hard !== !!prev.hardCap) prev.hardCap = hard;
    if (contact !== (prev.adminContact || "")) prev.adminContact = contact;
    if (hard) rule.failurePolicy = "FailClosed";
    if (!editing) doc.spec.rules.push(rule);
    openReview(name, doc);
  }

  function renderPolicies() {
    renderBudgets();
    const writable = !!policies?.writable;
    setBadge("policy-store-status", policies ? writable ? "Store supports edits" : "File authoritative" : "Policy API unavailable", writable ? "accent" : "neutral");
    $("policies-readonly").hidden = !policies || writable; $("write-controls").hidden = !writable;
    $("policy-generation").textContent = policies ? "Distribution generation · " + policies.generation : errors.policies || "Policy API unavailable";
    $("policy-nav-count").hidden = !policies; $("policy-nav-count").textContent = String(policies?.policies.length || 0);
    $("new-policy").disabled = !canWrite();
    $("write-status").textContent = writeToken ? "Dedicated write credential selected in memory; the server checks it when publishing." : ssoSession ? "Console OIDC credential selected; the server checks write authorization when publishing." : token === "" && connected ? "Loopback-development session; the server still decides write admission." : "No dedicated write credential selected. Read token does not authorize writes.";
    const list = $("policies-list"); list.textContent = "";
    const term = $("policy-search").value.toLowerCase();
    const entries = (policies?.policies || []).filter(view => [view.name, view.policy.spec?.subject?.team, view.policy.spec?.subject?.user].join(" ").toLowerCase().includes(term));
    if (!entries.length) empty(list, policies ? "No policies match" : "Policy API unavailable", errors.policies || "Create a document or change the filter.");
    for (const view of entries) list.append(policyCard(view));
  }
  function field(parent, name, text, type, value) {
    const label = el("label", text, type === "checkbox" ? "checkbox-label" : "");
    const input = el("input"); input.type = type; input.dataset.field = name;
    if (type === "checkbox") input.checked = !!value;
    else input.value = value == null ? "" : String(value);
    if (type === "number") { input.min = "0"; input.step = "1"; }
    input.disabled = !canWrite(); input.addEventListener("input", () => { dirty = true; });
    label.append(input); parent.append(label); return input;
  }
  function formInt(input, previous, label) {
    if (input.value === String(previous ?? "")) return previous ?? 0;
    if (!/^\d+$/.test(input.value) || !Number.isSafeInteger(Number(input.value))) throw Error(label + " must be a nonnegative safe integer (no decimals).");
    return Number(input.value);
  }
  function policyCard(view) {
    const doc = view.policy, card = el("article", undefined, "panel policy-card");
    const header = el("div", undefined, "panel-heading"), heading = el("div"), subject = doc.spec?.subject || {};
    heading.append(el("h3", view.name), el("p", "Team: " + (subject.team || "—") + " · User: " + (subject.user || "—"), "policy-subject"));
    const edit = el("button", "Edit document", "button secondary policy-edit"); edit.disabled = !canWrite(); edit.addEventListener("click", () => openEditor(view));
    header.append(heading, edit); card.append(header);
    const tags = el("div", undefined, "policy-badges");
    for (const rule of doc.spec?.rules || []) {
      const kind = ["budget", "rate", "modelAccess", "routing", "sensitiveData", "tokenQuota"].find(k => rule[k]);
      let text = rule.name + " · " + (kind || "unknown");
      if (rule.budget) text += rule.budget.unlimited ? " · unlimited" : " · " + (rule.budget.period || "CalendarMonth") + " · " + usd(integer(rule.budget.limitMilliUSD || 0) * 1000n);
      if (rule.budget?.hardCap) text += " · hard cap";
      tags.append(badge(text, rule.failurePolicy === "FailClosed" ? "accent" : "neutral"));
    }
    card.append(tags);
    const quick = el("details", undefined, "policy-editor-details"); quick.append(el("summary", "Budget, rate & model access settings"));
    const builders = [];
    for (let i = 0; i < (doc.spec?.rules || []).length; i++) {
      const rule = doc.spec.rules[i], group = el("fieldset", undefined, "field-group"), name = rule.name;
      if (!rule.budget && !rule.rate && !rule.modelAccess) continue;
      group.append(el("legend", name + " · " + rule.failurePolicy));
      if (rule.budget?.unlimited || rule.rate?.unlimited) { group.append(el("p", "Explicitly unlimited. Use the document editor to change this declaration.", "hint")); quick.append(group); continue; }
      if (rule.budget) {
        const prev = rule.budget, lease = prev.lease || {};
        const limit = field(group, name + "-limit", "Limit (mUSD · 1000 = $1)", "number", prev.limitMilliUSD);
        const hard = field(group, name + "-hard", "Hard cap · fail closed", "checkbox", prev.hardCap);
        const grant = field(group, name + "-grant", "Lease grant (mUSD · 0 = default)", "number", lease.grantMilliUSD || 0);
        const renew = field(group, name + "-renew", "Renew interval (Go duration)", "text", lease.renewInterval || "");
        const contact = field(group, name + "-contact", "Admin contact", "text", prev.adminContact || "");
        group.append(el("p", "Window: " + (prev.period || "CalendarMonth") + ". Unedited fields and failure policy are retained.", "hint"));
        builders.push(copy => {
          const r = copy.spec.rules[i];
          // Retain the entire fetched body, including omitted defaults and
          // prev.period. Only deliberate changes enter the reviewed document.
          const limitValue = formInt(limit, prev.limitMilliUSD, "Budget limit");
          const grantValue = formInt(grant, lease.grantMilliUSD || 0, "Lease grant");
          if (limitValue !== prev.limitMilliUSD) r.budget.limitMilliUSD = limitValue;
          if (hard.checked !== !!prev.hardCap) r.budget.hardCap = hard.checked;
          if (grantValue !== (lease.grantMilliUSD || 0) || renew.value.trim() !== (lease.renewInterval || "")) {
            r.budget.lease = { ...lease, grantMilliUSD: grantValue, renewInterval: renew.value.trim() };
          }
          if (contact.value !== (prev.adminContact || "")) r.budget.adminContact = contact.value;
          if (hard.checked) r.failurePolicy = "FailClosed";
        });
      } else if (rule.rate) {
        const prev = rule.rate;
        const rpm = field(group, name + "-rpm", "Requests / minute", "number", prev.rpm || 0), tpm = field(group, name + "-tpm", "Tokens / minute", "number", prev.tpm || 0);
        builders.push(copy => {
          const r = copy.spec.rules[i], rpmValue = formInt(rpm, prev.rpm || 0, "RPM"), tpmValue = formInt(tpm, prev.tpm || 0, "TPM");
          if (rpmValue !== (prev.rpm || 0)) r.rate.rpm = rpmValue;
          if (tpmValue !== (prev.tpm || 0)) r.rate.tpm = tpmValue;
        });
      } else {
        const allow = field(group, name + "-allow", "Allowed models (comma-separated · * = all)", "text", rule.modelAccess.allow.join(", "));
        builders.push(copy => { const parsed = allow.value.split(",").map(s => s.trim()).filter(Boolean); if (!parsed.length) throw Error("Model access allow list cannot be empty."); copy.spec.rules[i].modelAccess = { ...rule.modelAccess, allow: parsed }; });
      }
      quick.append(group);
    }
    const actions = el("div", undefined, "policy-actions"), save = el("button", "Review changes", "button primary policy-save"), del = el("button", "Delete policy", "button danger-button policy-delete");
    save.disabled = !canWrite() || !builders.length; del.disabled = !canWrite();
    save.addEventListener("click", () => {
      setError(""); if (!canWrite()) return;
      try { const copy = structuredClone(doc); for (const build of builders) build(copy); openReview(view.name, copy); }
      catch (e) { setError(e.message); }
    });
    del.addEventListener("click", () => openDelete(view)); actions.append(save, del); quick.append(actions); card.append(quick);
    const raw = el("details", undefined, "document-details"); raw.append(el("summary", "View full document"), el("pre", exactJSON(doc, 2))); card.append(raw);
    return card;
  }

  function dialogBase(title, description) {
    $("dialog-title").textContent = title; $("dialog-description").textContent = description;
    message("dialog-error", ""); $("policy-editor").hidden = $("editor-label").hidden = true;
    $("budget-form-fields").hidden = true;
    $("policy-review").hidden = false; $("dialog-confirm").disabled = false;
    $("dialog-cancel").disabled = $("dialog-close").disabled = false;
    if (!$("policy-dialog").open) $("policy-dialog").showModal();
  }
  function openReview(name, doc) {
    if (!canWrite()) return;
    pendingChange = { mode: "publish", name, doc, session };
    dialogBase("Review · " + name, "Publishing persists and hot-applies this complete document. Distribution occurs on subsequent heartbeats; verify the fleet afterward. Other form drafts are discarded on reload. There is no server-side revision lock.");
    $("policy-review").textContent = exactJSON(doc, 2); $("dialog-confirm").textContent = "Publish policy";
  }
  function openEditor(view) {
    if (!canWrite()) return;
    pendingChange = { mode: "edit", name: view?.name || "", session };
    dialogBase(view ? "Edit · " + view.name : "New policy", "Edit the full JSON document, including routing or privacy rules. The server validates all policy semantics. Review it before publishing.");
    $("editor-label").textContent = "GovernancePolicy document (JSON)"; $("policy-editor").rows = 18;
    $("policy-editor").hidden = $("editor-label").hidden = false; $("policy-review").hidden = true;
    $("policy-editor").value = exactJSON(view?.policy || { apiVersion: "inferplane.dev/v1alpha1", kind: "GovernancePolicy", metadata: { name: "new-policy" }, spec: { subject: { team: "your-team" }, rules: [{ name: "throughput", failurePolicy: "FailClosed", rate: { rpm: 60, tpm: 100000 } }] } }, 2);
    $("dialog-confirm").textContent = "Review document";
  }
  function openDelete(view) {
    if (!canWrite()) return;
    pendingChange = { mode: "delete", name: view.name, session };
    dialogBase("Delete · " + view.name, "Deleting removes this document from the distributed set. Its rules stop being enforced after nodes sync. Type the policy name to confirm.");
    $("editor-label").textContent = "Type the exact policy name"; $("policy-editor").rows = 2;
    $("policy-editor").hidden = $("editor-label").hidden = false; $("policy-editor").value = "";
    $("policy-review").textContent = exactJSON(view.policy, 2); $("dialog-confirm").textContent = "Delete policy";
  }
  function closeDialog() {
    if ($("dialog-confirm").disabled) return;
    $("policy-dialog").close(); pendingChange = null; $("policy-editor").value = ""; $("policy-review").textContent = "";
    clearBudgetForm();
  }
  $("dialog-confirm").addEventListener("click", async () => {
    const change = pendingChange; if (!change || change.session !== session || !canWrite()) return;
    message("dialog-error", "");
    if (change.mode === "budget") {
      try { reviewBudget(change); } catch (e) { message("dialog-error", e.message); }
      return;
    }
    if (change.mode === "edit") {
      try {
        const doc = parseExactJSON($("policy-editor").value), name = doc.metadata?.name;
        if (typeof name !== "string" || !name.trim() || doc.kind !== "GovernancePolicy" || !doc.spec || !Array.isArray(doc.spec.rules)) throw Error("Provide a named GovernancePolicy with spec.rules.");
        if (change.name && name !== change.name) throw Error("Renaming is not supported. Create a separate policy instead.");
        if (!change.name && policies.policies.some(v => v.name === name)) throw Error("That policy already exists. Edit it explicitly instead of overwriting it.");
        openReview(name, doc);
      } catch (e) { message("dialog-error", e.message); }
      return;
    }
    if (change.mode === "delete" && $("policy-editor").value !== change.name) { message("dialog-error", "The policy name does not match."); return; }
    $("dialog-confirm").disabled = $("dialog-cancel").disabled = $("dialog-close").disabled = true;
    try {
      await request("/v1alpha1/policies/" + encodeURIComponent(change.name), { method: change.mode === "delete" ? "DELETE" : "PUT", body: change.doc, write: true });
      if (change.session !== session) return;
      $("dialog-confirm").disabled = false; closeDialog(); dirty = false;
      await refresh();
      if (change.session === session && connected) message("notice", "Policy " + (change.mode === "delete" ? "deleted" : "published") + ". Check data-plane generation coverage after the next heartbeat.");
    } catch (e) { if (change.session === session) message("dialog-error", e.message); }
    finally { if (change.session === session) $("dialog-confirm").disabled = $("dialog-cancel").disabled = $("dialog-close").disabled = false; }
  });
  for (const id of ["dialog-close", "dialog-cancel"]) $(id).addEventListener("click", closeDialog);
  $("policy-dialog").addEventListener("cancel", e => { if ($("dialog-confirm").disabled) e.preventDefault(); else closeDialog(); });

  $("connect-form").addEventListener("submit", e => { e.preventDefault(); const tok = $("token").value.trim(); $("token").value = ""; return unlock(tok); });
  $("disconnect").addEventListener("click", () => { lock(); setError(""); });
  $("refresh").addEventListener("click", () => refresh());
  $("apply-range").addEventListener("click", () => refresh());
  for (const key of Object.keys(views)) $("nav-" + key).addEventListener("click", () => showView(key));
  $("open-usage").addEventListener("click", () => showView("usage")); $("open-fleet").addEventListener("click", () => showView("fleet"));
  $("team-search").addEventListener("input", renderTeams); $("usage-sort").addEventListener("change", renderTeams);
  $("fleet-search").addEventListener("input", renderFleetTable);
  $("policy-search").addEventListener("input", () => { if (!dirty) renderPolicies(); else setError("Publish or refresh to discard your form draft before filtering policies."); });
  $("close-models").addEventListener("click", () => { modelID++; modelDegraded = false; $("models-section").hidden = true; showView(activeView); });
  $("close-node").addEventListener("click", () => { $("node-details").hidden = true; });
  $("write-token-form").addEventListener("submit", e => {
    e.preventDefault(); const nextToken = $("write-token").value.trim(); $("write-token").value = "";
    if (dirty && !confirm("Changing the write credential discards unpublished form changes. Continue?")) return;
    writeToken = nextToken; dirty = false; renderPolicies();
  });
  $("clear-write-token").addEventListener("click", () => {
    $("write-token").value = "";
    if (dirty && !confirm("Clearing the write credential discards unpublished form changes. Continue?")) return;
    writeToken = ""; dirty = false; closeDialog(); renderPolicies();
  });
  $("new-policy").addEventListener("click", () => openEditor(null));
  $("new-budget").addEventListener("click", () => openBudget());
  $("budget-open-policies").addEventListener("click", () => showView("policies"));
  $("budget-search").addEventListener("input", renderBudgets);
  $("budget-period-filter").addEventListener("change", renderBudgets);
  $("bf-policy").addEventListener("change", budgetPolicySelected);
  for (const id of budgetInputs) $(id).addEventListener("input", () => { dirty = true; });
  $("theme-toggle").addEventListener("click", () => {
    const root = document.documentElement;
    const dark = root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    root.dataset.theme = dark ? "light" : "dark";
    $("theme-toggle").textContent = dark ? "Dark theme" : "Light theme";
  });
  $("export-usage").addEventListener("click", async () => {
    const sid = session; $("export-usage").disabled = true; setError("");
    try {
      const data = await request("/v1alpha1/usage/export?" + new URLSearchParams({ ...appliedRange, format: "json" }));
      if (sid !== session) return;
      const blob = new Blob([exactJSON(data, 2)], { type: "application/json" }), url = URL.createObjectURL(blob), link = el("a");
      link.href = url; link.download = "inferplane-usage-" + appliedRange.since.slice(0, 10) + ".json";
      document.body.append(link); link.click(); link.remove(); URL.revokeObjectURL(url);
      message("notice", "Exported finest-granularity usage for all teams in the selected UTC period; table filters do not narrow the export.");
    } catch (e) { if (sid === session) { if (e.status === 401 || e.status === 403) lock(); setError(e.message); } }
    finally { if (sid === session) $("export-usage").disabled = !usage; }
  });
  const today = new Date().toISOString().slice(0, 10);
  $("since").value = today.slice(0, 8) + "01"; $("until").value = today;
  /* ---------- console SSO (ADR-037, ported from mayu's adminui) ---------- */

  function clearSSOState() {
    sessionStorage.removeItem("ip_sso_verifier");
    sessionStorage.removeItem("ip_sso_state");
    sessionStorage.removeItem("ip_sso_nonce");
  }

  function base64url(bytes) {
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function randomBase64url() {
    const bytes = new Uint8Array(32);
    crypto.getRandomValues(bytes);
    return base64url(bytes);
  }

  async function pkceChallenge(verifier) {
    const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
    return base64url(new Uint8Array(digest));
  }

  function ssoRedirectURI() {
    return location.origin + "/ui/";
  }

  async function loadSSOConfig() {
    const resp = await fetch("/ui/auth/config");
    if (resp.status === 404) return null;
    if (!resp.ok) throw new Error("SSO configuration is unavailable");
    const config = await resp.json();
    if (!config || !config.sso) return null;
    if (!config.issuer || !config.client_id) throw new Error("SSO configuration is incomplete");
    return config;
  }

  async function discoverSSO(issuer) {
    const discoveryURL = issuer.replace(/\/+$/, "") + "/.well-known/openid-configuration";
    const resp = await fetch(discoveryURL);
    if (!resp.ok) throw new Error("OpenID discovery failed");
    const discovery = await resp.json();
    if (!discovery.authorization_endpoint || !discovery.token_endpoint) {
      throw new Error("OpenID discovery response is incomplete");
    }
    // Defense-in-depth: the discovery document is fetched over TLS from the
    // configured issuer, but a misconfigured IdP could still advertise a
    // plain http endpoint — refuse to redirect to / POST credentials over one.
    for (const ep of [discovery.authorization_endpoint, discovery.token_endpoint]) {
      if (!/^https:\/\//i.test(ep)) throw new Error("OpenID endpoints must be https");
    }
    return discovery;
  }

  async function startSSO(config) {
    try {
      const discovery = await discoverSSO(config.issuer);
      const verifier = randomBase64url();
      const state = randomBase64url();
      const nonce = randomBase64url();
      const challenge = await pkceChallenge(verifier);

      sessionStorage.setItem("ip_sso_verifier", verifier);
      sessionStorage.setItem("ip_sso_state", state);
      sessionStorage.setItem("ip_sso_nonce", nonce);

      const target = new URL(discovery.authorization_endpoint);
      const query = new URLSearchParams({
        response_type: "code",
        client_id: config.client_id,
        redirect_uri: ssoRedirectURI(),
        scope: "openid",
        state: state,
        nonce: nonce,
        code_challenge: challenge,
        code_challenge_method: "S256",
      });
      for (const [key, value] of query) target.searchParams.set(key, value);
      location.assign(target.toString());
    } catch {
      clearSSOState();
      setError("Unable to start SSO sign-in.");
    }
  }

  function decodeIDTokenPayload(idToken) {
    const parts = idToken.split(".");
    if (parts.length < 2) throw new Error("Invalid ID token");
    const encoded = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const padded = encoded + "=".repeat((4 - encoded.length % 4) % 4);
    const bytes = Uint8Array.from(atob(padded), (char) => char.charCodeAt(0));
    return JSON.parse(new TextDecoder().decode(bytes));
  }

  async function handleSSOCallback(params, configPromise) {
    const hasCode = params.has("code");
    const hasError = params.has("error");
    const hasState = params.has("state");

    try {
      if (hasCode && hasError) {
        setError("Invalid SSO callback.");
        return;
      }
      if (!hasState) {
        setError("Invalid SSO callback: missing state.");
        return;
      }

      const state = params.get("state");
      if (state !== sessionStorage.getItem("ip_sso_state")) {
        setError("Invalid SSO callback: state mismatch.");
        return;
      }
      if (hasError) {
        setError(params.get("error_description") || params.get("error") || "SSO sign-in failed.");
        return;
      }
      if (!hasCode) {
        setError("Invalid SSO callback: missing code.");
        return;
      }

      history.replaceState({}, "", location.pathname);

      const config = await configPromise;
      if (!config) throw new Error("SSO is not configured");
      const discovery = await discoverSSO(config.issuer);
      const verifier = sessionStorage.getItem("ip_sso_verifier");
      if (!verifier) throw new Error("SSO session expired");

      const tokenResp = await fetch(discovery.token_endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({
          grant_type: "authorization_code",
          client_id: config.client_id,
          code: params.get("code"),
          redirect_uri: ssoRedirectURI(),
          code_verifier: verifier,
        }).toString(),
      });
      if (!tokenResp.ok) throw new Error("SSO token exchange failed");
      const tokens = await tokenResp.json();
      if (!tokens.id_token) throw new Error("SSO token response did not include an ID token");

      const payload = decodeIDTokenPayload(tokens.id_token);
      if (payload.nonce !== sessionStorage.getItem("ip_sso_nonce")) {
        throw new Error("SSO token nonce mismatch");
      }

      await unlock(tokens.id_token, true);
    } catch (err) {
      setError(String(err.message || err));
    } finally {
      clearSSOState();
    }
  }

  async function initSSO() {
    const params = new URLSearchParams(location.search);
    const isCallback = params.has("code") || params.has("error") || params.has("state");
    const configPromise = loadSSOConfig();

    if (isCallback) await handleSSOCallback(params, configPromise);

    try {
      const config = await configPromise;
      if (!config) return;
      $("sso-button").hidden = false;
      $("sso-divider").hidden = false;
      $("sso-button").addEventListener("click", () => startSSO(config));
    } catch {
      // Non-SSO and unavailable-config deployments keep the opt-in button hidden.
      // Callback exchange failures are reported by handleSSOCallback.
    }
  }

  initSSO();
})();
