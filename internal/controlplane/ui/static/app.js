// Data-free shell; all API data is bearer gated. Both credentials live only in
// this closure. The only persisted values are ADR-037's short-lived PKCE keys.
(function () {
  "use strict";
  const $ = id => document.getElementById(id);
  const views = {
    overview: ["Dashboard", "Dashboard", "Reported costs, token consumption and budget controls."],
    usage: ["Usage & costs", "Usage & costs", "Team spend and token breakdown for the selected period."],
    users: ["Users & costs", "Users & costs", "Compare consumption and set personal limits."],
    budgets: ["Budgets & limits", "Budgets & limits", "Compare team and user limits, then adjust a budget."],
    fleet: ["Data planes", "Inspect the distribution edge.", "Recent heartbeats, generation coverage, and recorded policy rejections."],
    policies: ["Policies", "Govern deliberately.", "Inspect, review, and publish the documents distributed to your data planes."],
  };
  const tokenFields = ["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens"];
  let token = "", writeToken = "", ssoSession = false, connected = false;
  let session = 0, refreshID = 0, modelID = 0, activeView = "overview";
  let controller = new AbortController();
  let oidcRequests = Promise.resolve();
  let usage = null, fleet = null, policies = null, errors = {};
  let userUsage = null, scopedUserUsage = null, userRequestID = 0, userModelID = 0, userModelDegraded = false;
  let appliedRange = null, pendingChange = null, dirty = false;
  let charts = null;

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
  function displayMoney(value) {
    const n = integer(value);
    if (n !== 0n && n < 1000000n) return usd(n);
    const cents = roundEvenDivide(n, 10000n).toString().padStart(3, "0");
    return "$" + cents.slice(0, -2).replace(/\B(?=(\d{3})+(?!\d))/g, ",") + "." + cents.slice(-2);
  }
  function moneyStat(id, value) { $(id).textContent = displayMoney(value); $(id).title = usd(value); }
  function compactTokens(value) {
    const n = integer(value);
    for (const [unit, label] of [[1000000000n, "B"], [1000000n, "M"], [1000n, "K"]]) {
      if (n >= unit) { const x = n * 100n / unit; return (x / 100n) + "." + String(x % 100n).padStart(2, "0") + label; }
    }
    return count(n);
  }
  function compareSpend(a, b) {
    const x = integer(a.spent_micro_usd), y = integer(b.spent_micro_usd);
    return x === y ? String(a.key).localeCompare(String(b.key)) : x > y ? -1 : 1;
  }

  async function request(path, { method = "GET", body, write = false, byteLimit = 0 } = {}) {
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
      let raw;
      if (byteLimit && resp.body?.getReader) {
        const reader = resp.body.getReader(), decoder = new TextDecoder(), parts = []; let size = 0;
        try {
          while (true) {
            const { value, done } = await reader.read(); if (done) break;
            size += value.byteLength;
            if (size > byteLimit) { await reader.cancel(); throw Error("Chart history is too large. Select a shorter reporting period."); }
            parts.push(decoder.decode(value, { stream: true }));
          }
          parts.push(decoder.decode()); raw = parts.join("");
        } finally { reader.releaseLock(); }
      } else {
        raw = await resp.text();
        if (byteLimit && new TextEncoder().encode(raw).byteLength > byteLimit) throw Error("Chart history is too large. Select a shorter reporting period.");
      }
      return parseExactJSON(raw);
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
    $("controls").hidden = !["overview", "usage", "users"].includes(which);
    const incomplete = which === "users" ? scopedUserUsage?.degraded || userModelDegraded
      : usage?.degraded || modelDegraded || which === "overview" && userUsage?.degraded;
    $("degraded-banner").hidden = !incomplete || !["overview", "usage", "users"].includes(which);
  }

  function clearData() {
    usage = fleet = policies = null; errors = {}; dirty = false; pendingChange = null; modelDegraded = false;
    charts = null;
    for (const id of ["stat-spend", "stat-user-average", "stat-tokens"]) $(id).title = "";
    for (const id of ["spend-trend", "model-share", "top-users-list", "trend-status", "model-share-status"]) $(id).textContent = "";
    $("stat-models").textContent = $("chart-range").textContent = "—";
    userUsage = scopedUserUsage = null; userRequestID++; userModelID++; userModelDegraded = false;
    for (const id of ["stat-users", "stat-user-average", "users-total"]) $(id).textContent = "—";
    for (const id of ["user-summary-status", "users-status", "users-write-status", "token-policy-list", "budget-routing-list", "budget-team-options", "budget-user-options"]) $(id).textContent = "";
    $("users-nav-count").hidden = true; $("users-nav-count").textContent = "";
    $("users-team").textContent = ""; $("users-team").value = $("users-search").value = "";
    $("user-models-section").hidden = true; $("user-model-label").textContent = "Models"; $("user-model-status").textContent = "";
    $("new-token-policy").disabled = $("new-budget-routing").disabled = true;
    for (const id of ["stat-spend", "stat-teams", "stat-tokens", "stat-nodes", "usage-total", "fleet-generation", "fleet-api-versions", "fleet-coverage", "policy-generation", "fleet-nav-count", "policy-nav-count", "attention-count", "policy-store-status"]) $(id).textContent = "—";
    for (const id of ["spend-breakdown", "attention-list", "fleet-preview-list", "policies-list", "node-rejections"]) $(id).textContent = "";
    for (const id of ["teams", "models", "fleet-table", "users-table", "user-models", "budget-table"]) $(id).querySelector("tbody").textContent = "";
    for (const id of ["models-section", "node-details", "fleet-nav-count", "policy-nav-count", "degraded-banner"]) $(id).hidden = true;
    $("model-team-label").textContent = "Models"; $("model-status").textContent = "";
    $("node-title").textContent = "Rejection details"; $("policy-review").textContent = ""; $("policy-editor").value = "";
    $("dialog-title").textContent = "Review before publishing"; $("dialog-description").textContent = "";
    message("dialog-error", "");
    $("stat-spend-detail").textContent = "Selected reporting period";
    $("stat-nodes-detail").textContent = "Seen within the last 10 minutes";
    for (const id of ["team-search", "fleet-search", "fleet-state-filter", "policy-search"]) $(id).value = "";
    $("policy-dialog").close(); $("new-policy").disabled = true;
    $("budgets-list").textContent = $("budget-access-status").textContent = "";
    $("budget-nav-count").textContent = ""; $("budget-nav-count").hidden = true;
    $("budget-search").value = $("budget-period-filter").value = "";
    $("new-budget").disabled = true; clearBudgetForm();
    $("new-rule").disabled = true; clearRuleForm(); clearCommand();
    $("change-summary").textContent = ""; $("change-summary").hidden = true;
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
    $("heading-actions").hidden = $("disconnect").hidden = $("open-command").hidden = true;
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
    userRequestID++; userModelID++; userModelDegraded = false; $("user-models-section").hidden = true;
    $("refresh").disabled = true; $("refresh").textContent = "Refreshing…";
    setError(""); message("notice", "");
    const outcomes = await Promise.allSettled([
      request(usagePath({ group_by: "team", ...nextRange })), request("/v1alpha1/dataplanes"), request("/v1alpha1/policies"),
      request(usagePath({ group_by: "user", ...nextRange })),
      requestCharts(nextRange),
    ]);
    if (sid !== session || rid !== refreshID) return;
    $("refresh").disabled = false; $("refresh").textContent = "↻ Refresh";
    const denied = outcomes.find(r => r.status === "rejected" && (r.reason.status === 401 || r.reason.status === 403));
    if (denied) { lock(); setError(denied.reason.message); return; }
    if (connecting && !outcomes.some(r => r.status === "fulfilled")) {
      lock(); setError("Cannot connect: no control-plane data API is available."); return;
    }
    appliedRange = nextRange; errors = {};
    const [u, f, p, users, chartReply] = outcomes.map((r, i) => {
      if (r.status === "fulfilled") return r.value;
      errors[["usage", "fleet", "policies", "users", "charts"][i]] = r.reason.status === 404 ? "API not mounted on this deployment." : r.reason.message;
      return null;
    });
    // Malformed success responses are not fabricated as healthy empty data.
    if (u) {
      try { validateUsage(u); }
      catch (e) { errors.usage = "Invalid usage response: " + e.message; }
    }
    if (f && (!f.dataplanes || typeof f.dataplanes !== "object" || Array.isArray(f.dataplanes))) errors.fleet = "Invalid fleet response.";
    if (p && (!Array.isArray(p.policies) || typeof p.writable !== "boolean")) errors.policies = "Invalid policy response.";
    if (users) {
      try { validateUsage(users); }
      catch (e) { errors.users = "Invalid user usage: " + e.message; }
    }
    usage = errors.usage ? null : u; fleet = errors.fleet ? null : f; policies = errors.policies ? null : p;
    userUsage = errors.users ? null : users; scopedUserUsage = userUsage;
    charts = chartReply;
    if (connecting && !usage && !fleet && !policies && !userUsage) { lock(); setError("No valid control-plane data response."); return; }
    connected = true; dirty = false;
    $("auth-section").hidden = true; $("workspace-content").hidden = false;
    $("heading-actions").hidden = $("disconnect").hidden = $("open-command").hidden = false;
    for (const key of Object.keys(views)) $("nav-" + key).disabled = false;
    setBadge("connection-status", "Connected", "success");
    $("range-summary").textContent = nextRange.since.slice(0, 10) + " → " + $("until").value;
    $("last-updated").textContent = "Fetched " + new Date().toISOString().slice(11, 19) + " UTC";
    try { renderUsage(); renderFleet(); renderPolicies(); renderUserSummary(); renderUserTeams(); renderUsers(); renderCharts(); renderAttention(); showView(activeView); }
    catch (e) { setError("Cannot display response: " + e.message); }
    if ($("users-team").value) loadUsersForTeam();
  }

  function validateUsage(res) {
    if (!Array.isArray(res.rows) || integer(res.total_micro_usd) < 0n) throw Error("Expected nonnegative reported totals.");
    const keys = new Set();
    for (const row of res.rows) {
      if (typeof row.key !== "string") throw Error("Expected a usage grouping key.");
      if (keys.has(row.key)) throw Error("Duplicate usage grouping key."); keys.add(row.key);
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
      $("stat-spend").title = $("stat-tokens").title = "";
      $("stat-spend-detail").textContent = errors.usage || "Usage unavailable";
      const tbody = $("teams").querySelector("tbody"), tr = el("tr"), td = el("td", errors.usage || "Usage unavailable");
      tbody.textContent = ""; td.setAttribute("colspan", "7"); tr.append(td); tbody.append(tr);
      empty($("spend-breakdown"), "Usage unavailable", errors.usage || "No usage API response.");
      $("export-usage").disabled = true; return;
    }
    const total = integer(usage.total_micro_usd);
    moneyStat("stat-spend", total); $("stat-teams").textContent = count(usage.rows.length);
    const tokens = usage.rows.reduce((n, row) => n + tokenFields.reduce((s, k) => s + integer(row[k]), 0n), 0n);
    $("stat-tokens").textContent = compactTokens(tokens); $("stat-tokens").title = count(tokens); $("usage-total").textContent = "Total " + usd(total);
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
      label.append(button, progress);
      const amount = el("span", displayMoney(row.spent_micro_usd), "spend-amount"); amount.title = usd(row.spent_micro_usd);
      item.append(el("span", String(top.indexOf(row) + 1), "team-avatar"), label, amount); $("spend-breakdown").append(item);
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

  function svgNode(tag, attrs = {}) {
    const node = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const [name, value] of Object.entries(attrs)) node.setAttribute(name, String(value));
    return node;
  }
  async function requestCharts(range) {
    try { return await request("/v1alpha1/usage/charts?" + new URLSearchParams(range)); }
    catch (e) {
      // Older CP images already expose the authenticated bounded export. Keep
      // their memory/financial state intact while publishing newer static UI.
      // Only an absent endpoint selects this path; never retry auth/store errors.
      if (e.status !== 404) throw e;
    }
    const rows = await request("/v1alpha1/usage/export?" + new URLSearchParams({ ...range, format: "json" }), { byteLimit: 8 * 1024 * 1024 });
    if (!Array.isArray(rows) || rows.length > 100000) throw Error("Chart history is too large or invalid. Select a shorter period.");
    const days = new Map(), models = new Map(), limit = 9223372036854775807n;
    const add = (a, b) => a + b > limit ? limit : a + b;
    const since = new Date(range.since).getTime(), until = new Date(range.until).getTime();
    let total = 0n;
    for (const row of rows) {
      const start = new Date(row.window_start).getTime(), amount = integer(row.spent_micro_usd);
      if (!Number.isFinite(start) || start < since || start >= until || amount < 0n || typeof row.model !== "string") throw Error("Invalid chart window in retained history.");
      const day = new Date(start).toISOString().slice(0, 10);
      days.set(day, add(days.get(day) || 0n, amount)); models.set(row.model, add(models.get(row.model) || 0n, amount)); total = add(total, amount);
    }
    const ranked = [...models].map(([key, spent_micro_usd]) => ({ key, spent_micro_usd })).sort(compareSpend);
    return {
      total_micro_usd: total, days: [...days].sort(([a], [b]) => a.localeCompare(b)).map(([day, spent_micro_usd]) => ({ day, spent_micro_usd })),
      models: ranked.slice(0, 8), other_micro_usd: ranked.slice(8).reduce((sum, row) => add(sum, row.spent_micro_usd), 0n),
      model_count: models.size, source: "retained-export",
    };
  }
  function renderCharts() {
    $("spend-trend").textContent = $("model-share").textContent = "";
    $("chart-range").textContent = appliedRange ? appliedRange.since.slice(0, 10) + " → " + new Date(new Date(appliedRange.until).getTime() - 86400000).toISOString().slice(0, 10) : "—";
    $("stat-models").textContent = "—";
    $("trend-status").textContent = $("model-share-status").textContent = "";
    if (!charts) {
      empty($("spend-trend"), "Trend unavailable", errors.charts || "No retained chart history.");
      empty($("model-share"), "Distribution unavailable", errors.charts || "No chart API response."); return;
    }
    try {
      if (!Array.isArray(charts.days) || !Array.isArray(charts.models) || charts.days.length > 367 || charts.models.length > 8) throw Error("Invalid chart grouping.");
      const total = integer(charts.total_micro_usd), other = integer(charts.other_micro_usd);
      if (total < 0n || other < 0n) throw Error("Invalid chart cost.");
      for (const day of charts.days) if (!/^\d{4}-\d{2}-\d{2}$/.test(day.day) || integer(day.spent_micro_usd) < 0n) throw Error("Invalid daily cost.");
      for (const row of charts.models) if (typeof row.key !== "string" || integer(row.spent_micro_usd) < 0n) throw Error("Invalid model cost.");
      const modelSum = charts.models.reduce((sum, row) => sum + integer(row.spent_micro_usd), other);
      if (modelSum !== total) throw Error("Distribution totals cannot be reconciled.");
      $("stat-models").textContent = count(charts.model_count) + " reporting models";
      if (!charts.days.length) empty($("spend-trend"), "No spend recorded", "Try a different period.");
      else {
        const svg = svgNode("svg", { viewBox: "0 0 720 240", role: "img", "aria-label": "Daily reported spend in UTC" });
        const maximum = charts.days.reduce((n, d) => integer(d.spent_micro_usd) > n ? integer(d.spent_micro_usd) : n, 1n);
        const first = new Date(appliedRange.since).getTime(), last = new Date(appliedRange.until).getTime() - 86400000;
        for (const y of [20, 80, 140, 200]) svg.append(svgNode("line", { x1: 15, x2: 705, y1: y, y2: y, class: "chart-grid-line" }));
        const points = charts.days.map(d => {
          const x = 15 + Math.max(0, Math.min(1, (new Date(d.day + "T00:00:00Z").getTime() - first) / Math.max(86400000, last - first))) * 690;
          const y = 200 - Number(integer(d.spent_micro_usd) * 18000n / maximum) / 100;
          return { d, x, y };
        });
        const line = points.map(p => p.x.toFixed(2) + "," + p.y.toFixed(2)).join(" ");
        svg.append(svgNode("polygon", { points: points[0].x + ",200 " + line + " " + points.at(-1).x + ",200", class: "chart-area" }),
          svgNode("polyline", { points: line, class: "chart-line" }));
        for (const p of points) {
          const dot = svgNode("circle", { cx: p.x, cy: p.y, r: 4, class: "chart-dot" }), title = svgNode("title");
          title.textContent = p.d.day + " · " + usd(p.d.spent_micro_usd); dot.append(title); svg.append(dot);
        }
        for (const [x, day, anchor] of [[15, charts.days[0].day, "start"], [705, charts.days.at(-1).day, "end"]]) {
          const text = svgNode("text", { x, y: 230, "text-anchor": anchor, class: "chart-label" }); text.textContent = day.slice(5); svg.append(text);
        }
        $("spend-trend").append(svg);
        const detail = el("details", undefined, "chart-data-details"); detail.append(el("summary", "Exact daily values"));
        for (const d of charts.days) detail.append(el("p", d.day + " · " + usd(d.spent_micro_usd), "mono small")); $("spend-trend").append(detail);
      }
      $("trend-status").textContent = "Retained reported windows; missing days are not filled with invented spend.";
      if (!total) { empty($("model-share"), "No model spend", "No cost distribution in this period."); return; }
      const svg = svgNode("svg", { viewBox: "0 0 180 180", role: "img", "aria-label": "Model share of reported spend", class: "donut" });
      const circumference = 2 * Math.PI * 66;
      svg.append(svgNode("circle", { cx: 90, cy: 90, r: 66, class: "donut-track" }));
      const rows = [...charts.models.map(r => ({ ...r, other: false })), ...(other ? [{ key: "Other models", spent_micro_usd: other, other: true }] : [])];
      let offset = 0;
      const legend = el("div", undefined, "chart-legend");
      for (const [i, row] of rows.entries()) {
        const ratio = Number(integer(row.spent_micro_usd) * 10000n / total), length = circumference * ratio / 10000;
        const ring = svgNode("circle", { cx: 90, cy: 90, r: 66, class: "donut-segment chart-color-" + i % 6,
          "stroke-dasharray": length + " " + (circumference - length), "stroke-dashoffset": -offset, transform: "rotate(-90 90 90)" });
        const title = svgNode("title"); title.textContent = row.key + " · " + usd(row.spent_micro_usd); ring.append(title); svg.append(ring); offset += length;
        const item = el("div", undefined, "legend-row"), dot = el("span", undefined, "legend-dot chart-color-" + i % 6);
        const amount = el("span", displayMoney(row.spent_micro_usd), "mono"); amount.title = usd(row.spent_micro_usd);
        item.append(dot, el("span", row.key), amount); legend.append(item);
      }
      const center = svgNode("text", { x: 90, y: 94, "text-anchor": "middle", class: "donut-total" }); center.textContent = displayMoney(total); svg.append(center);
      $("model-share").append(svg, legend);
      $("model-share-status").textContent = "Reported model costs · exact values on hover.";
    } catch (e) {
      $("spend-trend").textContent = $("model-share").textContent = "";
      empty($("spend-trend"), "Chart data unavailable", e.message);
      empty($("model-share"), "Chart data unavailable", e.message);
    }
  }

  function declaredBinding(reported) {
    const matches = (Array.isArray(policies?.identity_bindings) ? policies.identity_bindings : []).filter(b => b.reported_ref === reported);
    const b = matches.length === 1 ? matches[0] : null;
    return b && /^identity-v1:[a-f0-9]{64}$/.test(b.reported_ref) && typeof b.account_ref === "string" && b.account_ref.length
      && ["human", "service"].includes(b.kind) ? b : null;
  }
  function userRows(data) {
    const grouped = new Map();
    for (const row of data?.rows || []) {
      const binding = declaredBinding(row.key), account = binding?.account_ref || (row.key && !row.key.startsWith("identity-v1:") ? row.key : null);
      const key = account ?? row.key;
      let combined = grouped.get(key);
      if (!combined) {
        combined = { key, account_ref: account, report_refs: [], kind: binding?.kind,
          ...Object.fromEntries(["spent_micro_usd", ...tokenFields].map(k => [k, 0n])) };
        grouped.set(key, combined);
      }
      combined.report_refs.push(row.key); combined.kind ||= binding?.kind;
      for (const field of ["spent_micro_usd", ...tokenFields]) combined[field] += integer(row[field]);
    }
    return [...grouped.values()];
  }
  function roundEvenDivide(amount, denominator) {
    const base = amount / denominator, remainder = amount % denominator;
    return base + (remainder * 2n > denominator || remainder * 2n === denominator && base % 2n ? 1n : 0n);
  }
  function renderUserSummary() {
    $("users-nav-count").hidden = !userUsage;
    $("top-users-list").textContent = "";
    if (!userUsage) {
      $("stat-users").textContent = $("stat-user-average").textContent = "—";
      $("stat-user-average").title = "";
      $("users-nav-count").textContent = ""; $("user-summary-status").textContent = errors.users || "User usage unavailable.";
      empty($("top-users-list"), "User costs unavailable", errors.users || "Refresh to try again."); return;
    }
    const rows = userRows(userUsage), attributed = rows.filter(r => r.report_refs.some(Boolean));
    $("stat-users").textContent = count(attributed.length); $("users-nav-count").textContent = String(attributed.length);
    const spend = attributed.reduce((sum, row) => sum + row.spent_micro_usd, 0n);
    if (attributed.length) moneyStat("stat-user-average", roundEvenDivide(spend, BigInt(attributed.length)));
    else $("stat-user-average").textContent = "—";
    if (!attributed.length) empty($("top-users-list"), "No attributed users", "Unattributed spend is excluded.");
    const top = [...attributed].sort(compareSpend).slice(0, 5);
    const peak = top[0]?.spent_micro_usd || 1n;
    for (const [i, row] of top.entries()) {
      const item = el("div", undefined, "spend-row"), label = el("div"), button = el("button", row.key, "team-button");
      button.addEventListener("click", () => { $("users-team").value = ""; scopedUserUsage = userUsage; userRequestID++; $("users-search").value = row.key; renderUsers(); showView("users"); return loadUserModels(row); });
      const bar = el("progress"); bar.max = 10000; bar.value = Number(row.spent_micro_usd * 10000n / peak); bar.setAttribute("aria-label", "Relative reported user spend");
      label.append(button, bar); const amount = el("span", displayMoney(row.spent_micro_usd), "spend-amount"); amount.title = usd(row.spent_micro_usd);
      item.append(el("span", String(i + 1), "team-avatar"), label, amount); $("top-users-list").append(item);
    }
    const unattributed = rows.filter(r => !r.report_refs.some(Boolean)).reduce((sum, row) => sum + row.spent_micro_usd, 0n);
    $("user-summary-status").textContent = (userUsage.degraded ? "Incomplete history · recent fallback only. " : "")
      + "Unattributed spend " + usd(unattributed) + " is excluded from the average. Declared account bindings correlate reporting aliases; totals are separate API snapshots.";
  }
  function renderUserTeams() {
    const selected = $("users-team").value, option = el("option", "All teams"); option.value = "";
    $("users-team").textContent = ""; $("users-team").append(option);
    const names = new Set((usage?.rows || []).map(r => r.key).filter(Boolean));
    for (const view of policies?.policies || []) if (view.policy.spec?.subject?.team) names.add(view.policy.spec.subject.team);
    for (const name of [...names].sort()) { const item = el("option", name); item.value = name; $("users-team").append(item); }
    $("users-team").value = names.has(selected) ? selected : "";
    // Suggestions are fetched declarations/records, not guessed identities.
    for (const id of ["budget-team-options", "budget-user-options"]) $(id).textContent = "";
    for (const name of [...names].sort()) { const item = el("option"); item.value = name; $("budget-team-options").append(item); }
    const accounts = new Set(userRows(userUsage).map(r => r.account_ref).filter(Boolean));
    for (const view of policies?.policies || []) if (view.policy.spec?.subject?.user) accounts.add(view.policy.spec.subject.user);
    for (const account of [...accounts].sort()) { const item = el("option"); item.value = account; $("budget-user-options").append(item); }
  }
  function renderUsers() {
    const tbody = $("users-table").querySelector("tbody"); tbody.textContent = "";
    $("users-write-status").textContent = canWrite() ? "Personal budgets require review before publishing." : "Select a policy write credential or console SSO to set personal budgets.";
    $("users-total").textContent = scopedUserUsage ? "Total " + usd(scopedUserUsage.total_micro_usd) : "—";
    if (!scopedUserUsage) {
      const tr = el("tr"), td = el("td", errors.users || "User usage unavailable or loading."); td.setAttribute("colspan", "9"); tr.append(td); tbody.append(tr); return;
    }
    const term = $("users-search").value.toLowerCase();
    const rows = userRows(scopedUserUsage).filter(row => [row.key, row.account_ref, ...row.report_refs].join(" ").toLowerCase().includes(term))
      .sort($("users-sort").value === "name" ? (a, b) => String(a.key).localeCompare(String(b.key)) : compareSpend);
    $("users-status").textContent = (scopedUserUsage.degraded ? "Incomplete fallback · " : "") + ($("users-team").value ? "Team " + $("users-team").value : "All teams") + " · selected UTC reporting period. Reported identifiers are not proof of identity.";
    if (!rows.length) { const tr = el("tr"), td = el("td", "No reported users match."); td.setAttribute("colspan", "9"); tr.append(td); tbody.append(tr); }
    for (const row of rows) {
      const tr = el("tr"), name = el("td"), actions = el("td"), known = row.report_refs.some(Boolean);
      const button = el("button", row.key || "Unattributed", "user-button team-button"); button.disabled = !known;
      button.addEventListener("click", () => loadUserModels(row)); name.append(button);
      if (row.kind) name.append(badge("Declared " + row.kind, "neutral"));
      if (row.account_ref && row.report_refs.some(ref => ref !== row.account_ref)) name.append(el("small", row.report_refs.join(" · "), "user-ref muted"));
      const budget = el("button", "Set budget", "button secondary user-budget"); budget.disabled = !canWrite() || !known;
      budget.addEventListener("click", () => {
        if (!known) return;
        openBudget(null, null, { account: row.account_ref, reported: row.report_refs.join(" · "), team: $("users-team").value, requireUser: true });
      });
      const tokensAction = el("button", "Token limit", "button secondary user-token-limit"); tokensAction.disabled = !canWrite() || !known;
      tokensAction.addEventListener("click", () => {
        if (!known) return;
        openRule(null, null, "tokenQuota", { account: row.account_ref, reported: row.report_refs.join(" · "), team: $("users-team").value, requireUser: true });
      });
      actions.append(budget, tokensAction);
      const tokens = tokenFields.reduce((sum, field) => sum + row[field], 0n);
      tr.append(name, el("td", usd(row.spent_micro_usd)), el("td", count(tokens)), ...tokenFields.map(field => el("td", count(row[field]))), actions); tbody.append(tr);
    }
  }
  async function loadUsersForTeam() {
    if (!connected) return;
    const sid = session, uid = ++userRequestID, team = $("users-team").value;
    userModelID++; userModelDegraded = false; $("user-models-section").hidden = true;
    if (!team) { scopedUserUsage = userUsage; renderUsers(); showView(activeView); return; }
    scopedUserUsage = null; $("users-status").textContent = "Loading team users…"; renderUsers();
    try {
      const res = await request(usagePath({ group_by: "user", team, ...appliedRange }));
      if (sid !== session || uid !== userRequestID) return;
      validateUsage(res); scopedUserUsage = res; renderUsers(); showView(activeView);
    } catch (e) {
      if (sid !== session || uid !== userRequestID) return;
      if (e.status === 401 || e.status === 403) { lock(); setError(e.message); return; }
      $("users-status").textContent = e.message;
    }
  }
  async function loadUserModels(row) {
    if (!connected || !row.report_refs.length || row.report_refs.some(ref => !ref)) return;
    const sid = session, mid = ++userModelID, team = $("users-team").value;
    $("user-models-section").hidden = false; $("user-model-label").textContent = "Models · " + row.key;
    $("user-models").querySelector("tbody").textContent = ""; $("user-model-status").textContent = "Loading user models…";
    userModelDegraded = false; showView(activeView);
    try {
      const replies = await Promise.all([...new Set(row.report_refs)].map(user => request(usagePath({ group_by: "model", user, ...(team ? { team } : {}), ...appliedRange }))));
      if (sid !== session || mid !== userModelID) return;
      const models = new Map(); let total = 0n;
      for (const res of replies) {
        validateUsage(res); userModelDegraded ||= !!res.degraded; total += integer(res.total_micro_usd);
        for (const item of res.rows) {
          const result = models.get(item.key) || { key: item.key, ...Object.fromEntries(["spent_micro_usd", ...tokenFields].map(k => [k, 0n])) };
          for (const field of ["spent_micro_usd", ...tokenFields]) result[field] += integer(item[field]);
          models.set(item.key, result);
        }
      }
      fillRows("user-models", [...models.values()].sort(compareSpend), false); showView(activeView);
      $("user-model-status").textContent = (userModelDegraded ? "Incomplete fallback · " : "") + "Total " + usd(total) + " · reporting aliases queried separately";
    } catch (e) {
      if (sid !== session || mid !== userModelID) return;
      if (e.status === 401 || e.status === 403) { lock(); setError(e.message); return; }
      $("user-model-status").textContent = e.message;
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
    const filter = $("fleet-state-filter").value;
    const entries = nodeEntries().filter(([id, node]) => id.toLowerCase().includes($("fleet-search").value.toLowerCase())
      && (!filter || filter === "differs" && distribution(node)[0] === "Generation differs"
        || filter === "rejected" && node.rejections?.length || filter === "unknown" && distribution(node)[0] === "Generation unknown"));
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
    if (!recorded.length) empty(list, "No recorded rejections", "Matching generation alone does not establish readiness.");
    for (const item of recorded) {
      const box = el("article", undefined, "rejection"); box.append(el("h3", item.policy + (item.rule ? " / " + item.rule : "")), el("p", item.reason));
      const related = (policies?.policies || []).find(view => view.name === item.policy);
      if (related) {
        const button = el("button", "Inspect policy →", "text-button");
        button.addEventListener("click", () => inspectPolicy(related.name)); box.append(button);
      }
      list.append(box);
    }
  }
  function inspectFleet(filter = "", id = "") {
    if (!connected) return;
    $("fleet-state-filter").value = filter; $("fleet-search").value = id;
    renderFleetTable(); showView("fleet");
  }
  function inspectPolicy(name) {
    if (!connected) return;
    showView("policies");
    if (dirty) { setError("Publish or refresh to discard your form draft before filtering policies."); return; }
    $("policy-search").value = name; renderPolicies();
  }
  function renderAttention() {
    const items = Object.entries(errors).map(([key, detail]) => [views[key]?.[0] || "Usage", detail, "Try refresh", () => refresh()]);
    if (usage?.degraded) items.push(["Usage history is incomplete", "Durable storage is unavailable. Totals use recent in-memory data only.", "Inspect usage", () => showView("usage")]);
    if (userUsage?.degraded) items.push(["User cost history is incomplete", "The user grouping uses recent fallback data; the average is not a complete historical bill.", "Inspect user costs", () => showView("users")]);
    const entries = nodeEntries(), mismatches = entries.filter(([, n]) => distribution(n)[0] === "Generation differs"), rejected = entries.filter(([, n]) => n.rejections?.length);
    if (mismatches.length) items.push([mismatches.length + " generation mismatch" + (mismatches.length === 1 ? "" : "es"), "Inspect data planes before assuming a policy rollout is complete.", "Inspect differences", () => inspectFleet("differs")]);
    if (rejected.length) items.push([rejected.length + " node(s) have recorded rejections", "Inspect the policy and rule reasons. Records may outlive the failure.", "Inspect rejections", () => inspectFleet("rejected")]);
    if (fleet && !entries.length) items.push(["No recent heartbeat observations", "This is not proof that the fleet is offline.", "Inspect inventory", () => inspectFleet()]);
    const list = $("attention-list"); list.textContent = ""; $("attention-count").textContent = String(items.length);
    if (!items.length) empty(list, "No issues in these observations", "This view does not prove readiness, fleet HA, or durable history completeness.");
    for (const [title, detail, action, run] of items) {
      const row = el("div", undefined, "attention-item"), text = el("div"), button = el("button", action + " →", "text-button");
      button.addEventListener("click", run); text.append(el("strong", title), el("p", detail), button);
      row.append(el("span", "!", "attention-icon"), text); list.append(row);
    }
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
    const table = $("budget-table").querySelector("tbody"); table.textContent = "";
    if (!entries.length) empty(list, !policies ? "Budget settings unavailable" : all.length ? "No budgets match" : "No budget rules configured",
      !policies ? errors.policies || "Refresh to try again." : all.length ? "Change the search or window filter." : "Create a budget to set a daily or monthly limit. Other configured limits still apply.");
    if (!entries.length) {
      const tr = el("tr"), td = el("td", !policies ? "Budget settings unavailable." : "No configured limits match."); td.setAttribute("colspan", "7"); tr.append(td); table.append(tr);
    }
    for (const { view, rule, index } of entries) {
      const card = el("article", undefined, "panel budget-card"), heading = el("div", undefined, "panel-heading"), title = el("div");
      const subject = view.policy.spec.subject || {}, budget = rule.budget;
      const row = el("tr"), name = el("td"), scope = el("td"), action = el("td");
      name.append(el("strong", subject.user || subject.team || "Unspecified"));
      if (subject.user && subject.team) name.append(el("small", "Team " + subject.team, "subject-context muted"));
      name.append(el("small", rule.name, "subject-context muted"));
      scope.append(badge(subject.user ? "User" : "Team", subject.user ? "neutral" : "accent"));
      const rowEdit = el("button", "Set budget", "button secondary budget-table-edit"); rowEdit.disabled = !canWrite();
      rowEdit.addEventListener("click", () => budget.unlimited ? openEditor(view) : openBudget(view, index)); action.append(rowEdit);
      row.append(name, scope, el("td", budget.unlimited ? "—" : budget.period === "CalendarDay" ? "Daily" : "Monthly"),
        el("td", budget.unlimited ? "Unlimited declaration" : usd(integer(budget.limitMilliUSD) * 1000n).slice(0, -3)),
        el("td", view.name), el("td", budget.unlimited ? "Other caps apply" : budget.hardCap ? "Block at cap" : "Accounting threshold"), action); table.append(row);
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
    renderTokenPolicies(); renderBudgetRouting();
  }

  function renderTokenPolicies() {
    $("new-token-policy").disabled = !canWrite();
    const list = $("token-policy-list"); list.textContent = "";
    const term = $("budget-search").value.toLowerCase(), period = $("budget-period-filter").value;
    let displayed = 0;
    for (const view of policies?.policies || []) {
      const subject = view.policy.spec.subject || {};
      for (const [index, rule] of view.policy.spec.rules.entries()) {
        if ((!rule.rate && !rule.tokenQuota) || ![view.name, rule.name, subject.team, subject.user].join(" ").toLowerCase().includes(term)) continue;
        if (rule.tokenQuota && period && rule.tokenQuota.period !== period) continue;
        const card = el("article", undefined, "panel budget-card"), heading = el("div", undefined, "panel-heading"), title = el("div");
        title.append(el("p", view.name, "eyebrow"), el("h2", rule.name),
          el("p", "Team: " + (subject.team || "Any") + " · User: " + (subject.user || "All"), "policy-subject"));
        const edit = el("button", "Edit limit", "button secondary token-edit"); edit.disabled = !canWrite(); edit.dataset.rule = rule.name;
        edit.addEventListener("click", () => openRule(view, index)); heading.append(title, edit); card.append(heading);
        const summary = el("div", undefined, "budget-summary");
        if (rule.tokenQuota) summary.append(el("strong", count(rule.tokenQuota.limitTokens) + " tokens", "budget-value"),
          badge(rule.tokenQuota.period === "CalendarDay" ? "Daily quota" : "Monthly quota", "accent"), badge("Shared Postgres required", "warning"), badge("Block · fail closed", "neutral"));
        else {
          summary.append(badge(rule.rate.unlimited ? "Unlimited declaration" : (rule.rate.rpm ? count(rule.rate.rpm) + " requests / min" : "RPM not limited"), "neutral"),
            badge(rule.rate.unlimited ? "Does not override stricter caps" : (rule.rate.tpm ? count(rule.rate.tpm) + " tokens / min" : "TPM not limited"), "accent"));
          if (subject.user || !subject.team) summary.append(badge("Shared Postgres required", "warning"));
        }
        card.append(summary); list.append(card); displayed++;
      }
    }
    if (!displayed) empty(list, policies ? "No matching token policies" : "Token policy settings unavailable", "Use the guide for shared daily/monthly quotas or minute-based throughput. Default/local daily team limits are set on mayu.");
  }
  function tierDiagram(rule) {
    const box = el("div", undefined, "tier-diagram"), tiers = rule.routing?.budgetTiers;
    if (!tiers) return box;
    box.append(el("p", "Budget reference: " + tiers.budgetRef + " · " + (tiers.enforceTargets ? "Targets required on every attempt" : "Original may be served if the target is unusable"), "hint"));
    for (const tier of tiers.tiers || []) for (const [source, target] of Object.entries(tier.substitute)) {
      const step = el("div", undefined, "tier-step");
      step.append(badge("At " + tier.thresholdPercent + "%", "accent"), el("code", source), el("span", "→", "muted"), el("code", target)); box.append(step);
    }
    return box;
  }
  function renderBudgetRouting() {
    $("new-budget-routing").disabled = !canWrite();
    const list = $("budget-routing-list"); list.textContent = "";
    const term = $("budget-search").value.toLowerCase(); let found = 0;
    for (const view of policies?.policies || []) for (const [index, rule] of view.policy.spec.rules.entries()) {
      if (!rule.routing?.budgetTiers || ![view.name, rule.name, view.policy.spec.subject.team, view.policy.spec.subject.user].join(" ").toLowerCase().includes(term)) continue;
      const card = el("article", undefined, "panel budget-card"), heading = el("div", undefined, "panel-heading");
      heading.append(el("h2", rule.name));
      const edit = el("button", "Edit thresholds", "button secondary"); edit.disabled = !canWrite();
      edit.addEventListener("click", () => openRule(view, index)); heading.append(edit);
      card.append(heading, tierDiagram(rule)); list.append(card); found++;
    }
    if (!found) empty(list, "No matching budget substitutions", "Add a numeric budget, then select its policy to create substitution thresholds.");
  }

  const budgetInputs = ["bf-policy", "bf-policy-name", "bf-team", "bf-user", "bf-rule", "bf-period", "bf-limit", "bf-contact", "bf-hard"];
  const budgetSubjectHint = "Select a team, a user, or both. Use the declared account reference for verified identities. Both fields together select that user within that team. Existing policy subjects are preserved. Counter scope and reset time follow the deployment profile; combined reported spend is not remaining admission credit.";
  function newPolicyName(prefix) {
    const names = new Set((policies?.policies || []).map(v => v.name)); let sequence = 1;
    while (names.has(prefix + "-" + sequence)) sequence++;
    return prefix + "-" + sequence;
  }
  function clearBudgetForm() {
    $("budget-form-fields").hidden = true;
    for (const id of budgetInputs) { $(id).value = ""; $(id).disabled = false; }
    $("bf-hard").checked = false; $("bf-policy").textContent = "";
    $("bf-user").required = false; $("budget-subject-hint").textContent = budgetSubjectHint;
    $("budget-names").open = false;
  }
  function budgetPolicySelected() {
    const name = $("bf-policy").value;
    const view = (policies?.policies || []).find(v => v.name === name);
    const subject = view?.policy.spec?.subject || {};
    $("bf-policy-name").value = view?.name || ""; $("bf-policy-name").disabled = !!view;
    $("bf-team").value = subject.team || ""; $("bf-team").disabled = !!view;
    $("bf-user").value = subject.user || ""; $("bf-user").disabled = !!view;
  }
  function openBudget(view = null, index = null, prefill = null) {
    if (!canWrite()) return;
    const rule = index === null ? null : view?.policy.spec.rules[index];
    if (rule && (!rule.budget || rule.budget.unlimited)) return;
    clearBudgetForm();
    pendingChange = { mode: "budget", view, index, session, wasDirty: dirty, prefill };
    dialogBase(rule ? "Edit budget · " + rule.name : "New budget", "Choose a policy and budget window, then review the complete policy before publishing. Other rules and existing lease settings are preserved.");
    $("budget-form-fields").hidden = false; $("policy-review").hidden = true;
    const choice = el("option", "Create a new policy"); choice.value = ""; $("bf-policy").append(choice);
    for (const item of policies.policies) { const option = el("option", item.name); option.value = item.name; $("bf-policy").append(option); }
    $("bf-policy").value = view?.name || ""; $("bf-policy").disabled = !!rule; budgetPolicySelected();
    if (!view) $("bf-policy-name").value = newPolicyName("budget-policy");
    $("bf-rule").value = rule?.name || "monthly-budget"; $("bf-rule").disabled = !!rule;
    $("bf-period").value = rule?.budget.period || "CalendarMonth";
    if (rule) {
      const amount = integer(rule.budget.limitMilliUSD).toString().padStart(4, "0");
      $("bf-limit").value = amount.slice(0, -3) + "." + amount.slice(-3);
    }
    $("bf-hard").checked = rule ? !!rule.budget.hardCap : true;
    $("bf-contact").value = rule?.budget.adminContact || "";
    if (prefill) {
      $("dialog-title").textContent = "Personal budget" + (prefill.account ? " · " + prefill.account : "");
      $("bf-team").value = prefill.team || ""; $("bf-user").value = prefill.account ?? "";
      $("bf-user").required = !!prefill.requireUser;
      $("budget-subject-hint").textContent = "Reported ID: " + prefill.reported + ". "
        + (prefill.account ? "The exact financial account reference is selected. " : "No declared financial binding is available. Enter account_ref from the authorized key record; the reported identity is not guessed as a financial account. ")
        + (!prefill.team ? "All-team usage is combined. Default/local personal budgets count separately per team and gateway; shared Postgres user-only limits can cover teams together. " : "")
        + budgetSubjectHint;
    }
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
      const team = change.prefill?.team === $("bf-team").value ? $("bf-team").value : $("bf-team").value.trim(), user = $("bf-user").value;
      if (change.prefill?.requireUser && !user) throw Error("Select the user's exact financial account reference. A team-only budget is not substituted for a personal budget.");
      if (!team && !user) throw Error("Select a team, a user account reference, or both.");
      doc = { apiVersion: "inferplane.dev/v1alpha1", kind: "GovernancePolicy", metadata: { name },
        spec: { subject: { ...(team ? { team } : {}), ...(user ? { user } : {}) }, rules: [] } };
    }
    if (change.prefill?.requireUser && !doc.spec.subject.user) throw Error("This action requires a user-scoped policy. Select a personal policy or create a new one.");
    if (change.prefill?.account && doc.spec.subject.user !== change.prefill.account) throw Error("This budget action belongs to the selected user's account. Choose a matching policy or create a new personal policy.");
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

  const ruleInputs = ["rf-kind", "rf-policy", "rf-policy-name", "rf-team", "rf-user", "rf-name", "rf-models", "rf-rpm", "rf-tpm", "rf-detected", "rf-internal", "rf-budget-ref", "rf-threshold", "rf-source", "rf-target", "rf-strict", "rf-token-limit", "rf-token-period", "rf-shared", "rf-next-threshold", "rf-next-target"];
  function clearRuleForm() {
    $("rule-form-fields").hidden = true;
    for (const id of ruleInputs) { $(id).value = ""; $(id).disabled = false; }
    $("rf-strict").checked = false; $("rf-policy").textContent = ""; $("rf-budget-ref").textContent = "";
    $("rf-shared").checked = false; $("rf-tier-preview").textContent = "";
    $("rule-names").open = false;
    $("rf-shared-label").hidden = $("rf-shared-hint").hidden = true;
    $("rule-form-hint").textContent = "";
  }
  function rulePolicySelected() {
    const view = (policies?.policies || []).find(v => v.name === $("rf-policy").value), subject = view?.policy.spec?.subject || {};
    $("rf-policy-name").value = view?.name || ""; $("rf-policy-name").disabled = !!view;
    $("rf-team").value = subject.team || ""; $("rf-team").disabled = !!view;
    $("rf-user").value = subject.user || ""; $("rf-user").disabled = !!view;
    const budgets = (view?.policy.spec?.rules || []).filter(r => r.budget && !r.budget.unlimited);
    $("rf-budget-ref").textContent = "";
    for (const rule of budgets) { const option = el("option", rule.name + " · " + usd(integer(rule.budget.limitMilliUSD) * 1000n)); option.value = rule.name; $("rf-budget-ref").append(option); }
    $("rf-budget-ref").value = budgets[0]?.name || "";
    updateGeneratedRuleName();
    updateRuleKind();
  }
  function updateGeneratedRuleName() {
    if (pendingChange?.mode !== "rule" || pendingChange.index !== null) return;
    if ($("rf-name").value && $("rf-name").value !== pendingChange.generatedName) return;
    const kind = $("rf-kind").value;
    const base = kind === "tokenQuota" ? ($("rf-token-period").value === "CalendarDay" ? "daily-tokens" : "monthly-tokens")
      : ({ modelAccess: "model-access", rate: "throughput", privacy: "protected-input", tier: "budget-substitution" })[kind] || "rule";
    const names = new Set((policies?.policies.find(v => v.name === $("rf-policy").value)?.policy.spec.rules || []).map(r => r.name));
    let name = base, n = 2; while (names.has(name)) name = base + "-" + n++;
    $("rf-name").value = pendingChange.generatedName = name;
  }
  function updateRuleKind() {
    const kind = $("rf-kind").value;
    for (const [key, id] of [["modelAccess", "model"], ["rate", "rate"], ["tokenQuota", "quota"], ["privacy", "privacy"], ["tier", "tier"]]) $("rf-" + id + "-fields").hidden = key !== kind;
    $("rf-internal").disabled = $("rf-detected").value !== "InternalOnly";
    $("rf-threshold").max = $("rf-strict").checked ? "100" : "99";
    $("rf-next-threshold").max = $("rf-threshold").max; $("rf-next-tier-fields").hidden = kind !== "tier";
    const shared = kind === "tokenQuota" || kind === "rate" && ($("rf-user").value || !$("rf-team").value)
      || kind === "tier" && $("rf-strict").checked && ($("rf-user").value || !$("rf-team").value);
    $("rf-shared-label").hidden = $("rf-shared-hint").hidden = !shared;
    $("rule-form-hint").textContent = shared
      ? "Review this limit, then inspect node distribution after publishing."
      : kind === "tier" && !$("rf-budget-ref").value ? "Create a numeric budget first, then select its policy here."
      : kind === "modelAccess" ? "Allow lists intersect with existing access limits. Use explicit model names; * allows all models within the subject's existing permissions."
      : "The server validates the document. Verify node generations and recorded rejections after publishing.";
    $("rf-tier-preview").textContent = "";
    if (kind === "tier") {
      const source = $("rf-source").value || "Requested model";
      for (const [threshold, target] of [[$("rf-threshold").value, $("rf-target").value], [$("rf-next-threshold").value, $("rf-next-target").value]]) {
        if (threshold && target) $("rf-tier-preview").append(el("div", "At " + threshold + "% · " + source + " → " + target, "tier-step"));
      }
    }
  }
  function openRule(view = null, index = null, requestedKind = "modelAccess", prefill = null) {
    if (!canWrite()) return;
    const rule = index === null ? null : view?.policy.spec.rules[index];
    const kind = rule ? ["modelAccess", "rate", "tokenQuota", "sensitiveData", "routing"].find(k => rule[k]) : requestedKind;
    const selectedKind = kind === "sensitiveData" ? "privacy" : kind === "routing" ? "tier" : kind;
    const tiers = rule?.routing?.budgetTiers?.tiers || [];
    if (rule?.rate?.unlimited || rule?.routing && (!rule.routing.budgetTiers || tiers.length > 2
      || tiers.some(t => Object.keys(t.substitute).length !== 1) || new Set(tiers.flatMap(t => Object.keys(t.substitute))).size !== 1)) { openEditor(view); return; }
    clearRuleForm(); pendingChange = { mode: "rule", view, index, session, wasDirty: dirty, prefill };
    dialogBase("Add a governance rule", "Choose the behavior and subject. Review the complete resulting policy before publishing.");
    $("rule-form-fields").hidden = false; $("policy-review").hidden = true;
    $("rf-kind").value = selectedKind; $("rf-kind").disabled = !!rule;
    $("rf-detected").value = rule?.sensitiveData?.onDetected || "Block"; $("rf-threshold").value = "80";
    $("rf-token-period").value = rule?.tokenQuota?.period || "CalendarMonth";
    $("rf-token-limit").value = rule?.tokenQuota?.limitTokens ?? "";
    $("rf-rpm").value = rule?.rate?.rpm ?? ""; $("rf-tpm").value = rule?.rate?.tpm ?? "";
    $("rf-models").value = (rule?.modelAccess?.allow || []).join(", ");
    $("rf-internal").value = (rule?.sensitiveData?.internalModels || []).join(", ");
    const fresh = el("option", "Create a new policy"); fresh.value = ""; $("rf-policy").append(fresh);
    for (const item of policies.policies) { const option = el("option", item.name); option.value = item.name; $("rf-policy").append(option); }
    $("rf-policy").value = view?.name || ""; $("rf-policy").disabled = !!rule; rulePolicySelected();
    if (!view) $("rf-policy-name").value = newPolicyName("governance-policy");
    if (rule) $("rf-name").value = rule.name;
    $("rf-name").disabled = !!rule;
    if (prefill) {
      $("rf-team").value = prefill.team || ""; $("rf-user").value = prefill.account ?? "";
      $("dialog-title").textContent = "Token limit" + (prefill.account ? " · " + prefill.account : " · selected user");
      $("dialog-description").textContent = (prefill.account ? "The exact financial account reference is selected. " : "Enter account_ref from the authorized key view; no financial account is guessed from the reported identity. ")
        + "Shared Postgres governance is required for a user token quota.";
    }
    if (tiers.length) {
      const [source, target] = Object.entries(tiers[0].substitute)[0];
      $("rf-source").value = source; $("rf-target").value = target; $("rf-threshold").value = String(tiers[0].thresholdPercent);
      if (tiers[1]) { $("rf-next-target").value = Object.values(tiers[1].substitute)[0]; $("rf-next-threshold").value = String(tiers[1].thresholdPercent); }
      $("rf-budget-ref").value = rule.routing.budgetTiers.budgetRef;
      $("rf-strict").checked = !!rule.routing.budgetTiers.enforceTargets;
    }
    updateRuleKind(); $("dialog-confirm").textContent = "Review rule";
  }
  function modelList(id, wildcards = false) {
    const values = $(id).value.split(",").map(v => v.trim());
    if (!values.length || values.some(v => !v || /\s/.test(v) || /[*?]/.test(v) && !(wildcards && v === "*")) || new Set(values).size !== values.length) {
      throw Error("Enter distinct model names separated by commas" + (wildcards ? "." : ", without wildcards."));
    }
    return values;
  }
  function positiveTokenLimit(value) {
    if (!/^\d+$/.test(value)) throw Error("Enter a positive whole token count without decimals or exponent notation.");
    const count = BigInt(value);
    if (count <= 0n || count > 9223372036854775807n) throw Error("Token limit must be 1–9,223,372,036,854,775,807.");
    return count <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(count) : count;
  }
  function reviewRule() {
    const editing = pendingChange.index !== null, selected = $("rf-policy").value;
    const view = editing ? pendingChange.view : policies.policies.find(v => v.name === selected);
    if (selected && !view) throw Error("Selected policy is unavailable. Refresh and try again.");
    const name = view?.name || $("rf-policy-name").value.trim(), ruleName = $("rf-name").value.trim();
    if (!name || /[\s/]/.test(name)) throw Error("Enter a policy name without spaces or slashes.");
    if (!view && policies.policies.some(v => v.name === name)) throw Error("Select the existing policy to add a rule.");
    if (!ruleName) throw Error("Enter a rule name.");
    const team = pendingChange.prefill?.team === $("rf-team").value ? $("rf-team").value : $("rf-team").value.trim(), user = $("rf-user").value;
    if (!view && !team && !user) throw Error("Select a team, a user account reference, or both.");
    const doc = view ? structuredClone(view.policy) : { apiVersion: "inferplane.dev/v1alpha1", kind: "GovernancePolicy", metadata: { name }, spec: { subject: { ...(team ? { team } : {}), ...(user ? { user } : {}) }, rules: [] } };
    if (pendingChange.prefill?.requireUser && !doc.spec.subject.user) throw Error("Select the user's exact financial account reference; a team-only token policy is not substituted.");
    if (pendingChange.prefill?.account && doc.spec.subject.user !== pendingChange.prefill.account) throw Error("Choose a policy belonging to the selected user's account or create a new personal policy.");
    if (!editing && doc.spec.rules.some(r => r.name === ruleName)) throw Error("That rule name already exists in this policy.");
    const rule = editing ? doc.spec.rules[pendingChange.index] : { name: ruleName, failurePolicy: "FailClosed" }, kind = $("rf-kind").value;
    const subject = doc.spec.subject;
    const shared = kind === "tokenQuota" || kind === "rate" && (subject.user || !subject.team)
      || kind === "tier" && $("rf-strict").checked && (subject.user || !subject.team);
    if (shared && !$("rf-shared").checked) throw Error("Confirm that the relevant data planes use shared Postgres governance. This rule is not enforceable in the default/local profile.");
    if (kind === "modelAccess") rule.modelAccess = { ...rule.modelAccess, allow: modelList("rf-models", true) };
    else if (kind === "rate") {
      const rpm = $("rf-rpm").value ? formInt($("rf-rpm"), rule.rate?.rpm, "RPM") : 0, tpm = $("rf-tpm").value ? formInt($("rf-tpm"), rule.rate?.tpm, "TPM") : 0;
      if (rpm <= 0 && tpm <= 0) throw Error("Set a positive RPM or TPM limit.");
      rule.rate = editing ? { ...rule.rate, rpm, tpm } : { ...(rpm ? { rpm } : {}), ...(tpm ? { tpm } : {}) };
    } else if (kind === "tokenQuota") {
      const period = $("rf-token-period").value;
      if (!["CalendarDay", "CalendarMonth"].includes(period)) throw Error("Select a calendar day or calendar month.");
      rule.failurePolicy = "FailClosed";
      rule.tokenQuota = { ...rule.tokenQuota, limitTokens: positiveTokenLimit($("rf-token-limit").value), period };
    } else if (kind === "privacy") {
      const action = $("rf-detected").value;
      if (!["Block", "InternalOnly"].includes(action)) throw Error("Select a supported privacy action.");
      rule.sensitiveData = { ...rule.sensitiveData, onDetected: action, onUninspectable: rule.sensitiveData?.onUninspectable || "Block", ...(action === "InternalOnly" ? { internalModels: modelList("rf-internal") } : {}) };
    } else if (kind === "tier") {
      const ref = $("rf-budget-ref").value, budget = doc.spec.rules.find(r => r.name === ref && r.budget && !r.budget.unlimited);
      if (!budget) throw Error("Select a numeric budget in the same policy. Create the budget first if needed.");
      const threshold = formInt($("rf-threshold"), undefined, "Threshold"), strict = $("rf-strict").checked;
      if (threshold < 1 || threshold > (strict ? 100 : 99)) throw Error("Threshold must be 1–" + (strict ? "100" : "99") + "%.");
      const source = modelList("rf-source"), target = modelList("rf-target");
      if (source.length !== 1 || target.length !== 1 || source[0] === target[0]) throw Error("Choose one requested model and one different substitute.");
      // Computed property retains model names such as __proto__ as data.
      if (!editing) rule.failurePolicy = "FailOpen";
      const previous = rule.routing?.budgetTiers;
      const tiers = [{ ...previous?.tiers[0], thresholdPercent: threshold, substitute: { [source[0]]: target[0] } }];
      if ($("rf-next-threshold").value || $("rf-next-target").value) {
        const next = formInt($("rf-next-threshold"), undefined, "Next threshold"), targets = modelList("rf-next-target");
        if (next <= threshold || next > (strict ? 100 : 99) || targets.length !== 1 || targets[0] === source[0] || targets[0] === target[0]) throw Error("The next threshold must be higher, within range, and select one distinct substitute.");
        tiers.push({ ...previous?.tiers[1], thresholdPercent: next, substitute: { [source[0]]: targets[0] } });
      }
      rule.routing = { ...rule.routing, budgetTiers: { ...previous, budgetRef: ref, ...(strict ? { enforceTargets: true } : previous?.enforceTargets !== undefined ? { enforceTargets: false } : {}), tiers } };
    } else throw Error("Select a supported rule purpose.");
    if (!editing) doc.spec.rules.push(rule); openReview(name, doc);
  }

  function renderChangeSummary(name, doc) {
    const box = $("change-summary"); box.textContent = ""; box.hidden = false;
    const previous = (policies?.policies || []).find(v => v.name === name)?.policy;
    const subject = doc.spec.subject || {};
    box.append(el("h3", previous ? "Review the changes" : "New policy"),
      el("p", "Applies to team " + (subject.team || "Any") + " · user " + (subject.user || "All team users")));
    const changes = [];
    function diff(before, after, path) {
      if (exactJSON(before) === exactJSON(after)) return;
      if (before !== undefined && after !== undefined && before && after && typeof before === "object" && typeof after === "object" && Array.isArray(before) === Array.isArray(after)) {
        for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) diff(before[key], after[key], path ? path + "." + key : key);
      } else {
        const display = value => value === undefined ? "(absent)" : exactJSON(value).slice(0, 300);
        changes.push(path + ": " + display(before) + " → " + display(after));
      }
    }
    if (previous) diff(previous, doc, "");
    else for (const rule of doc.spec.rules) changes.push("Added rule " + rule.name + " · " + Object.keys(rule).filter(k => !["name", "failurePolicy"].includes(k)).join(", "));
    if (!changes.length) changes.push("No document changes.");
    const list = el("ul");
    for (const text of changes.slice(0, 30)) list.append(el("li", text));
    if (changes.length > 30) list.append(el("li", (changes.length - 30) + " additional changes. Inspect the full document below."));
    box.append(list);
    if ((doc.spec.rules || []).some(r => r.routing?.budgetTiers?.enforceTargets)) box.append(el("p", "Strict substitution requires the target on every attempt and may refuse requests. Independent hard caps still apply.", "review-impact"));
    if ((doc.spec.rules || []).some(r => r.sensitiveData)) box.append(el("p", "Sensitive-input rules may block requests, including uninspectable input. Verify target boundaries and supported rules on every data plane.", "review-impact"));
    if ((doc.spec.rules || []).some(r => r.tokenQuota || r.rate && (doc.spec.subject.user || !doc.spec.subject.team))) box.append(el("p", "Shared Postgres governance is required. This review does not verify the deployed profile. Token admission includes outstanding reservations.", "review-impact"));
  }

  let commandActions = [], commandIndex = 0;
  function clearCommand() {
    $("command-dialog").close(); $("command-input").value = ""; $("command-results").textContent = ""; $("command-status").textContent = "";
    $("command-input").removeAttribute("aria-activedescendant"); commandActions = []; commandIndex = 0;
  }
  function commandSelection() {
    const buttons = $("command-results").querySelectorAll("button");
    buttons.forEach((button, i) => { button.classList.toggle("selected", i === commandIndex); button.setAttribute("aria-selected", i === commandIndex ? "true" : "false"); });
    if (buttons.length) $("command-input").setAttribute("aria-activedescendant", "command-result-" + commandIndex);
    else $("command-input").removeAttribute("aria-activedescendant");
  }
  function runCommand(index) {
    if (!connected || !commandActions[index]) return;
    const run = commandActions[index].run; clearCommand(); return run();
  }
  function renderCommand() {
    if (!connected) return;
    const term = $("command-input").value.trim().toLowerCase();
    const actions = Object.keys(views).map(key => ({ type: "Page", title: views[key][0], detail: views[key][2], run: () => showView(key) }));
    if (canWrite()) actions.push(
      { type: "Action", title: "Create a budget", detail: "Daily or monthly spending limit", run: () => { showView("budgets"); openBudget(); } },
      { type: "Action", title: "Add a governance rule", detail: "Access, throughput, privacy or cost substitution", run: () => { showView("policies"); openRule(); } });
    for (const row of usage?.rows || []) actions.push({ type: "Team", title: row.key || "(unattributed)", detail: usd(row.spent_micro_usd) + " · selected reporting period", run: () => { showView("usage"); return loadModels(row.key); } });
    for (const row of userRows(userUsage)) if (row.report_refs.some(Boolean)) actions.push({ type: "User", title: row.key, detail: row.report_refs.join(" · ") + " · " + usd(row.spent_micro_usd), run: () => {
      $("users-team").value = ""; scopedUserUsage = userUsage; userRequestID++; $("users-search").value = row.key;
      renderUsers(); showView("users"); return loadUserModels(row);
    } });
    for (const view of policies?.policies || []) actions.push({ type: "Policy", title: view.name, detail: "Team " + (view.policy.spec.subject?.team || "Any") + " · user " + (view.policy.spec.subject?.user || "All"), run: () => inspectPolicy(view.name) });
    for (const [id, node] of nodeEntries()) actions.push({ type: "Data plane", title: id, detail: distribution(node)[0], run: () => { inspectFleet("", id); showRejections(id, node.rejections || []); } });
    const matches = actions.filter(a => (a.type + " " + a.title + " " + a.detail).toLowerCase().includes(term));
    commandActions = matches.slice(0, 30); commandIndex = 0;
    const list = $("command-results"); list.textContent = "";
    for (const [i, action] of commandActions.entries()) {
      const button = el("button", undefined, "command-result"); button.id = "command-result-" + i; button.type = "button"; button.tabIndex = -1; button.setAttribute("role", "option");
      const text = el("span"); text.append(el("strong", action.title), el("small", action.detail));
      button.append(badge(action.type, "neutral"), text); button.addEventListener("click", () => runCommand(i)); list.append(button);
    }
    $("command-status").textContent = matches.length ? "Showing " + commandActions.length + " of " + matches.length + " results" : "No fetched page or record matches. Try a different search.";
    commandSelection();
  }
  function openCommand() {
    if (!connected || $("policy-dialog").open) return;
    $("command-input").value = ""; renderCommand(); $("command-dialog").showModal(); $("command-input").focus();
  }

  function renderPolicies() {
    renderBudgets();
    renderUsers();
    const writable = !!policies?.writable;
    setBadge("policy-store-status", policies ? writable ? "Store supports edits" : "File authoritative" : "Policy API unavailable", writable ? "accent" : "neutral");
    $("policies-readonly").hidden = !policies || writable; $("write-controls").hidden = !writable;
    $("policy-generation").textContent = policies ? "Distribution generation · " + policies.generation : errors.policies || "Policy API unavailable";
    $("policy-nav-count").hidden = !policies; $("policy-nav-count").textContent = String(policies?.policies.length || 0);
    $("new-policy").disabled = !canWrite();
    $("new-rule").disabled = !canWrite();
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
    for (const rule of doc.spec?.rules || []) if (rule.routing?.budgetTiers) card.append(tierDiagram(rule));
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
    $("rule-form-fields").hidden = true; $("change-summary").hidden = true;
    $("dialog-back").hidden = true;
    $("policy-review").hidden = false; $("dialog-confirm").disabled = false;
    $("dialog-cancel").disabled = $("dialog-close").disabled = false;
    if (!$("policy-dialog").open) $("policy-dialog").showModal();
  }
  function openReview(name, doc) {
    if (!canWrite()) return;
    const draft = ["budget", "rule", "edit"].includes(pendingChange?.mode) ? pendingChange : null;
    pendingChange = { mode: "publish", name, doc, session, draft, wasDirty: draft?.wasDirty ?? dirty };
    dialogBase("Review · " + name, "Publishing persists and hot-applies this complete document. Distribution occurs on subsequent heartbeats; verify the fleet afterward. Other form drafts are discarded on reload. There is no server-side revision lock.");
    $("policy-review").textContent = exactJSON(doc, 2); $("dialog-confirm").textContent = "Publish policy";
    renderChangeSummary(name, doc);
    $("dialog-back").hidden = !draft;
  }
  function openEditor(view) {
    if (!canWrite()) return;
    pendingChange = { mode: "edit", name: view?.name || "", session, wasDirty: dirty };
    dialogBase(view ? "Edit · " + view.name : "New policy", "Edit the full JSON document, including routing or privacy rules. The server validates all policy semantics. Review it before publishing.");
    $("editor-label").textContent = "GovernancePolicy document (JSON)"; $("policy-editor").rows = 18;
    $("policy-editor").hidden = $("editor-label").hidden = false; $("policy-review").hidden = true;
    $("policy-editor").value = exactJSON(view?.policy || { apiVersion: "inferplane.dev/v1alpha1", kind: "GovernancePolicy", metadata: { name: "new-policy" }, spec: { subject: { team: "your-team" }, rules: [{ name: "throughput", failurePolicy: "FailClosed", rate: { rpm: 60, tpm: 100000 } }] } }, 2);
    $("dialog-confirm").textContent = "Review document";
  }
  function openDelete(view) {
    if (!canWrite()) return;
    pendingChange = { mode: "delete", name: view.name, session, wasDirty: dirty };
    dialogBase("Delete · " + view.name, "Deleting removes this document from the distributed set. Its rules stop being enforced after nodes sync. Type the policy name to confirm.");
    $("editor-label").textContent = "Type the exact policy name"; $("policy-editor").rows = 2;
    $("policy-editor").hidden = $("editor-label").hidden = false; $("policy-editor").value = "";
    $("policy-review").textContent = exactJSON(view.policy, 2); $("dialog-confirm").textContent = "Delete policy";
  }
  function closeDialog() {
    if ($("dialog-confirm").disabled) return;
    $("policy-dialog").close(); dirty = pendingChange?.wasDirty ?? dirty; pendingChange = null; $("policy-editor").value = ""; $("policy-review").textContent = "";
    clearBudgetForm(); clearRuleForm(); $("change-summary").textContent = "";
  }
  $("dialog-confirm").addEventListener("click", async () => {
    const change = pendingChange; if (!change || change.session !== session || !canWrite()) return;
    message("dialog-error", "");
    if (change.mode === "budget") {
      try { reviewBudget(change); } catch (e) { message("dialog-error", e.message); }
      return;
    }
    if (change.mode === "rule") {
      try { reviewRule(); } catch (e) { message("dialog-error", e.message); }
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
    $("dialog-confirm").disabled = $("dialog-cancel").disabled = $("dialog-close").disabled = $("dialog-back").disabled = true;
    try {
      await request("/v1alpha1/policies/" + encodeURIComponent(change.name), { method: change.mode === "delete" ? "DELETE" : "PUT", body: change.doc, write: true });
      if (change.session !== session) return;
      $("dialog-confirm").disabled = false; closeDialog(); dirty = false;
      await refresh();
      if (change.session === session && connected) {
        message("notice", "Policy " + (change.mode === "delete" ? "deleted" : "published") + ". Check data-plane generation coverage after the next heartbeat. ");
        const inspect = el("button", "Inspect distribution →", "text-button"); inspect.addEventListener("click", () => inspectFleet()); $("notice").append(inspect);
      }
    } catch (e) { if (change.session === session) message("dialog-error", e.message); }
    finally { if (change.session === session) $("dialog-confirm").disabled = $("dialog-cancel").disabled = $("dialog-close").disabled = $("dialog-back").disabled = false; }
  });
  $("dialog-back").addEventListener("click", () => {
    const draft = pendingChange?.draft;
    if (!draft || draft.session !== session || $("dialog-confirm").disabled) return;
    pendingChange = draft;
    dialogBase(draft.mode === "budget" ? "Edit budget details" : draft.mode === "rule" ? "Add a governance rule" : "Edit document", "Adjust your draft, then review it again before publishing.");
    $("policy-review").hidden = true;
    if (draft.mode === "budget") $("budget-form-fields").hidden = false;
    else if (draft.mode === "rule") $("rule-form-fields").hidden = false;
    else $("policy-editor").hidden = $("editor-label").hidden = false;
    $("dialog-confirm").textContent = draft.mode === "budget" ? "Review budget" : draft.mode === "rule" ? "Review rule" : "Review document";
  });
  for (const id of ["dialog-close", "dialog-cancel"]) $(id).addEventListener("click", closeDialog);
  $("policy-dialog").addEventListener("cancel", e => { if ($("dialog-confirm").disabled) e.preventDefault(); else closeDialog(); });

  $("connect-form").addEventListener("submit", e => { e.preventDefault(); const tok = $("token").value.trim(); $("token").value = ""; return unlock(tok); });
  $("disconnect").addEventListener("click", () => { lock(); setError(""); });
  $("refresh").addEventListener("click", () => refresh());
  $("apply-range").addEventListener("click", () => refresh());
  for (const key of Object.keys(views)) $("nav-" + key).addEventListener("click", () => showView(key));
  $("open-usage").addEventListener("click", () => showView("usage")); $("open-fleet").addEventListener("click", () => showView("fleet"));
  $("open-users").addEventListener("click", () => showView("users"));
  $("users-open-policies").addEventListener("click", () => showView("policies"));
  $("users-search").addEventListener("input", renderUsers); $("users-sort").addEventListener("change", renderUsers);
  $("users-team").addEventListener("change", loadUsersForTeam);
  $("close-user-models").addEventListener("click", () => { userModelID++; userModelDegraded = false; $("user-models-section").hidden = true; showView(activeView); });
  $("team-search").addEventListener("input", renderTeams); $("usage-sort").addEventListener("change", renderTeams);
  $("fleet-search").addEventListener("input", renderFleetTable);
  $("fleet-state-filter").addEventListener("change", renderFleetTable);
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
  $("new-rule").addEventListener("click", () => openRule());
  $("new-token-policy").addEventListener("click", () => openRule(null, null, "tokenQuota"));
  $("new-budget-routing").addEventListener("click", () => openRule(null, null, "tier"));
  $("quick-rule").addEventListener("click", () => { showView("policies"); if (canWrite()) openRule(); });
  $("quick-budget").addEventListener("click", () => { showView("budgets"); if (canWrite()) openBudget(); });
  $("quick-fleet").addEventListener("click", () => inspectFleet());
  $("new-budget").addEventListener("click", () => openBudget());
  $("budget-open-policies").addEventListener("click", () => showView("policies"));
  $("budget-search").addEventListener("input", renderBudgets);
  $("budget-period-filter").addEventListener("change", renderBudgets);
  $("bf-policy").addEventListener("change", budgetPolicySelected);
  for (const id of budgetInputs) $(id).addEventListener("input", () => { dirty = true; if (!$("dialog-confirm").disabled) message("dialog-error", ""); });
  for (const id of ruleInputs) $(id).addEventListener("input", () => { dirty = true; if (!$("dialog-confirm").disabled) message("dialog-error", ""); updateRuleKind(); });
  $("rf-policy").addEventListener("change", rulePolicySelected);
  for (const id of ["rf-kind", "rf-token-period"]) $(id).addEventListener("change", () => { updateGeneratedRuleName(); updateRuleKind(); });
  for (const id of ["rf-detected", "rf-strict", "rf-user", "rf-team"]) $(id).addEventListener("change", updateRuleKind);
  $("open-command").addEventListener("click", openCommand); $("command-close").addEventListener("click", clearCommand);
  $("command-dialog").addEventListener("cancel", clearCommand);
  $("command-input").addEventListener("input", renderCommand);
  $("command-input").addEventListener("keydown", e => {
    if (e.key === "Escape") { e.preventDefault(); clearCommand(); return; }
    if (e.key === "Enter") { e.preventDefault(); return runCommand(commandIndex); }
    if (["ArrowDown", "ArrowUp"].includes(e.key) && commandActions.length) {
      e.preventDefault(); commandIndex = (commandIndex + (e.key === "ArrowDown" ? 1 : -1) + commandActions.length) % commandActions.length; commandSelection();
      $("command-results").querySelectorAll("button")[commandIndex]?.scrollIntoView?.({ block: "nearest" });
    }
  });
  document.addEventListener("keydown", e => { if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k" && connected) { e.preventDefault(); openCommand(); } });
  for (const [id, days] of [["range-today", 0], ["range-week", 6], ["range-month", null]]) $(id).addEventListener("click", () => {
    if (!connected) return;
    const end = new Date(), start = new Date(end);
    if (days === null) start.setUTCDate(1); else start.setUTCDate(start.getUTCDate() - days);
    $("since").value = start.toISOString().slice(0, 10); $("until").value = end.toISOString().slice(0, 10);
    return refresh();
  });
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
