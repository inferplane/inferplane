"use strict";

// Observability view (sub-project B): governed OTLP export status and runtime
// edits. Full administrators only — the server enforces that on every route,
// reads included. Rendering is textContent-only and nothing is kept in browser
// storage (ADR-001): the last status lives in page memory and is re-fetched on
// every visit. Edits are dry run first; Apply sends exactly the body the last
// preview validated, with the revision that preview saw.
let obsState = null;
let obsPreviews = { limits: null, rules: null };
let obsBound = false;
// obsGen bumps whenever an editor changes or is invalidated, so a dry run that
// finishes after an edit never arms Apply for a body it did not validate.
const obsGen = { limits: 0, rules: 0 };
// obsBusy marks an editor whose request (and the re-fetch after an apply) is
// in flight: its Preview and Apply are off and a second submit sends nothing.
const obsBusy = { limits: false, rules: false };

const OBS_DROPPED = ["dropped_queue_full", "dropped_oversize", "dropped_export_error"];
const OBS_DECISIONS = ["selected", "no_rule", "content_conflict", "withheld_limit"];
const OBS_RULE_NAME = /^console-[a-z0-9]([-a-z0-9]{0,53}[a-z0-9])?$/;

// app.js's showView names this function in its refreshers literal, as it
// names refreshAuditView (audit.js).
// keepVisible leaves the loaded view on screen while the status is re-fetched
// (Refresh, and the re-fetch after Apply), so a failed fetch never strands the
// admin on an empty page without the Refresh control or the apply result.
async function refreshObservabilityView(keepVisible) {
  obsBind();
  $("obs-admin-only").hidden = whoamiIsAdmin;
  if (keepVisible !== true || !whoamiIsAdmin) $("obs-content").hidden = true;
  if (!whoamiIsAdmin) return;
  const { status, data } = await obsRequest("GET", "/admin/observability");
  if (status !== 200 || !data) throw new Error(obsErrorText(data, status));
  obsState = data;
  obsPreviews = { limits: null, rules: null };
  renderObservability();
  $("obs-content").hidden = false;
}

/* ---------- pure helpers (testdata/observability_console.cjs) ---------- */

function obsTime(iso) { return iso ? String(iso).replace("T", " ").replace(/\.\d+/, "").replace("Z", "") : "—"; }
function obsShortRev(rev) { return String(rev || "").replace(/^sha256:/, "").slice(0, 12); }
function obsBytes(n) {
  const v = Number(n || 0), mib = 1024 * 1024;
  if (v >= mib) return (v % mib === 0 ? String(v / mib) : (v / mib).toFixed(1)) + " MiB";
  if (v >= 1024) return (v % 1024 === 0 ? String(v / 1024) : (v / 1024).toFixed(1)) + " KiB";
  return v + " B";
}
function obsDropped(outcomes) {
  let n = 0n;
  for (const k of OBS_DROPPED) n += BigInt((outcomes || {})[k] || 0);
  return n.toString();
}
function obsSubjectText(subject) {
  const parts = [];
  if (subject && subject.team) parts.push(msg("obs.team") + " " + subject.team);
  if (subject && subject.user) parts.push(msg("obs.user") + " " + subject.user);
  return parts.join(" · ") || "—";
}
function obsTargetText(t) {
  if (t.signal !== "traces") return t.signal + " → " + t.exporter;
  const pct = t.effective_percent !== t.sample_percent ? t.sample_percent + "% → " + t.effective_percent + "%" : t.sample_percent + "%";
  return t.signal + " → " + t.exporter + " · " + pct;
}
function obsValue(field, v) {
  if (Array.isArray(v)) return v.length ? v.join(", ") : msg("obs.none");
  if (typeof v === "boolean") return msg(v ? "obs.yes" : "obs.no");
  if (field === "max_content_bytes") return obsBytes(v);
  if (field === "max_sample_percent") return String(v) + "%";
  return String(v);
}
function obsChangeText(c) { return c.exporter + " · " + msg("obs.field." + c.field) + ": " + obsValue(c.field, c.before) + " → " + obsValue(c.field, c.after); }
function obsStateText(s) { return s.exported ? (s.sample_percent ? s.sample_percent + "%" : msg("obs.exported")) : msg("obs.withheld"); }
function obsEffectText(e) { return e.policy + " · " + e.rule + " · " + e.signal + " → " + e.exporter + ": " + obsStateText(e.before) + " → " + obsStateText(e.after); }
function obsRuleChangeText(c) { return c.name + " · " + msg("obs.action." + c.action); }
function obsDecisionSummary(d) {
  const parts = [];
  for (const t of d.traces || []) parts.push("traces " + t.exporter + " " + t.sample_percent + "%");
  for (const a of d.audit || []) parts.push("audit " + a);
  if (d.content) parts.push("content " + d.content);
  if (d.content_conflict) parts.push(msg("obs.conflict"));
  return parts.join(", ") || msg("obs.none");
}
function obsDecisionText(d) { return obsSubjectText(d.subject) + ": " + obsDecisionSummary(d.before) + " → " + obsDecisionSummary(d.after); }
// kind ("limits" | "rules") selects an editor's own text for a code when it
// has one (restore_failed means different things for the two files).
function obsErrorText(data, status, kind) {
  // 403 comes from the shared admin middleware, whose body is not our error shape.
  if (status === 403) return msg("obs.adminOnly");
  const code = (data && data.code) || "";
  const key = [kind ? "obs.err." + code + "." + kind : "", "obs.err." + code].find(k => k && Object.hasOwn(MSG, k));
  const base = key ? msg(key) : msg("obs.err.generic").replace("{status}", String(status));
  return data && data.error ? base + " — " + data.error : base;
}
function obsInt(text, exporter) {
  const t = String(text == null ? "" : text).trim();
  if (!t) return 0;
  if (!/^\d+$/.test(t) || !Number.isSafeInteger(Number(t)) || Number(t) < 1) throw Error(msg("obs.err.number").replace("{exporter}", exporter));
  return Number(t);
}
// obsLimitsBody turns editor rows into the PUT body. A row equal to the
// bootstrap (every declared signal, no caps, not paused) sends nothing for its
// exporter; the server canonicalizes the same way, so the revisions agree.
function obsLimitsBody(rows, dryRun, revision) {
  const exporters = {};
  for (const r of rows) {
    const limit = {};
    if (r.paused) limit.paused = true;
    if (r.signals.length !== r.declared.length) {
      if (!r.signals.length && !r.paused) throw Error(msg("obs.err.noSignals").replace("{exporter}", r.exporter));
      if (r.signals.length) limit.signals = [...r.signals].sort();
    }
    const bytes = obsInt(r.max_content_bytes, r.exporter), percent = obsInt(r.max_sample_percent, r.exporter);
    if (percent > 100) throw Error(msg("obs.err.percent"));
    if (bytes) limit.max_content_bytes = bytes;
    if (percent) limit.max_sample_percent = percent;
    if (Object.keys(limit).length) exporters[r.exporter] = limit;
  }
  return { dry_run: dryRun, expected_revision: revision, exporters };
}
// obsRuleError is the client-side check of one editor row (the server checks
// again); it returns an i18n key, or "" when the row is acceptable.
function obsRuleError(row) {
  if (!OBS_RULE_NAME.test(row.name)) return "obs.err.ruleName";
  if (!row.team && !row.user) return "obs.err.ruleSubject";
  if (!row.traces && !row.audit && !row.content) return "obs.err.ruleSignal";
  if (row.traces && !/^(100|[1-9][0-9]?)$/.test(String(row.sample_percent).trim())) return "obs.err.percent";
  return "";
}
function obsRulesBody(rows, dryRun, revision) {
  const rules = rows.map(row => {
    const r = { ...row, name: row.name.trim(), team: row.team.trim(), user: row.user.trim() };
    const err = obsRuleError(r);
    if (err) throw Error(msg(err) + " (" + (r.name || "—") + ")");
    const rule = { name: r.name, subject: {} };
    if (r.team) rule.subject.team = r.team;
    if (r.user) rule.subject.user = r.user;
    if (r.traces) rule.traces = { exporter: r.traces, sample_percent: Number(String(r.sample_percent).trim()) };
    if (r.audit) rule.audit = { exporter: r.audit };
    if (r.content) rule.content = { exporter: r.content };
    return rule;
  });
  return { dry_run: dryRun, expected_revision: revision, rules };
}

/* ---------- rendering ---------- */

function obsNum(v) { const e = td(v == null ? "0" : String(v)); e.className = "num"; return e; }

function obsEndpointText(endpoint, protocol, tls, headers, boundary) {
  let text = endpoint + " · " + protocol + " · " + (tls ? "TLS" : msg("obs.plaintext"));
  if (headers && headers.length) text += " · " + msg("obs.headers") + ": " + headers.join(", ");
  if (boundary) text += " · " + msg("obs.boundary").replace("{b}", boundary);
  return text;
}

// obsSignalRows lists the records-by-signal rows. A legacy exporter without
// counters (no observability block) keeps one traces row flagged notCounted:
// a zero would misreport live traffic and omitting it would hide the exporter.
function obsSignalRows(s) {
  const rows = [];
  for (const e of s.exporters || []) for (const st of e.stats || []) rows.push({ name: e.name, signal: st.signal, stats: st, notCounted: false });
  const l = s.legacy_otel;
  if (l) rows.push(l.stats ? { name: "otel", signal: "traces", stats: l.stats, notCounted: false } : { name: "otel", signal: "traces", stats: null, notCounted: true });
  return rows;
}

function renderObservability() {
  const s = obsState;
  $("obs-since").textContent = s.configured ? msg("obs.since").replace("{t}", obsTime(s.counters_since)) : msg("obs.notConfigured");
  // Limits, console rules and counters belong to the process that answered.
  $("obs-instance").textContent = s.instance ? msg("obs.instance").replace("{i}", s.instance) : "";
  $("obs-instance").hidden = !s.instance;
  $("obs-source").textContent = msg("obs.sourceLabel") + ": " + msg("obs.source." + s.policy_source);
  renderObsExporters(s);
  renderObsSignals(s);
  renderObsDecisions(s);
  renderObsSelections(s);
  renderObsLimits(s);
  renderObsRules(s);
}

function renderObsExporters(s) {
  const tbody = $("obs-exporters").querySelector("tbody");
  tbody.replaceChildren();
  for (const e of s.exporters || []) {
    const now = e.limit && e.limit.paused ? msg("obs.paused") : (e.effective_signals.length ? e.effective_signals.join(", ") : "—");
    const cap = e.max_content_bytes ? obsBytes(e.effective_max_content_bytes) +
      (e.effective_max_content_bytes !== e.max_content_bytes ? " (" + obsBytes(e.max_content_bytes) + ")" : "") : "—";
    const tr = document.createElement("tr");
    tr.append(td(e.name), td(obsEndpointText(e.endpoint, e.protocol, e.tls, e.header_names, e.data_boundary)), td(e.signals.join(", ") + " → " + now),
      td(e.queue_limit_bytes ? obsBytes(e.queued_bytes) + " / " + obsBytes(e.queue_limit_bytes) : "—"), td(cap));
    tbody.appendChild(tr);
  }
  if (s.legacy_otel) {
    const l = s.legacy_otel, tr = document.createElement("tr");
    tr.append(td("otel · " + msg("obs.legacy")), td(obsEndpointText(l.endpoint, l.protocol, l.tls, [])),
      td("traces · " + Math.round(l.sample_ratio * 100) + "%"), td("—"), td("—"));
    tbody.appendChild(tr);
  }
  if (!tbody.children.length) tbody.appendChild(emptyRow(5, msg("obs.noExporters")));
}

function renderObsSignals(s) {
  const tbody = $("obs-signals").querySelector("tbody");
  tbody.replaceChildren();
  const rows = obsSignalRows(s);
  for (const row of rows) {
    const tr = document.createElement("tr");
    if (row.notCounted) {
      tr.append(td(row.name), td(row.signal), ...[0, 1, 2, 3].map(() => td(msg("obs.notCounted"))), td("—"), td("—"));
    } else {
      const st = row.stats, o = st.outcomes || {};
      tr.append(td(row.name), td(row.signal), obsNum(o.sent), obsNum(obsDropped(o)), obsNum(o.withheld_limit), obsNum(o.sampled_out),
        td(st.last_success_at ? obsTime(st.last_success_at) : "—"), td(st.last_error ? obsTime(st.last_error_at) + " · " + st.last_error : "—"));
    }
    tbody.appendChild(tr);
  }
  if (!rows.length) tbody.appendChild(emptyRow(8, msg("obs.noExporters")));
}

function renderObsDecisions(s) {
  const tbody = $("obs-decisions").querySelector("tbody");
  tbody.replaceChildren();
  for (const d of s.decisions || []) {
    const tr = document.createElement("tr");
    tr.append(td(d.signal), ...OBS_DECISIONS.map(k => obsNum((d.decisions || {})[k])));
    tbody.appendChild(tr);
  }
  if (!tbody.children.length) tbody.appendChild(emptyRow(5, msg("obs.noDecisions")));
  // The legacy otel baseline is not a policy selection, so its spans count as
  // no_rule here; say so when that block is configured.
  const legacy = $("obs-decisions-legacy");
  legacy.textContent = msg("obs.decisionsLegacyHint");
  legacy.hidden = !s.legacy_otel;
}

function renderObsSelections(s) {
  const tbody = $("obs-selections").querySelector("tbody");
  tbody.replaceChildren();
  for (const sel of s.selections || []) {
    for (const t of sel.targets || []) {
      const tr = document.createElement("tr");
      tr.append(td(sel.policy + " · " + sel.rule), td(msg("obs.source." + sel.source)), td(obsSubjectText(sel.subject)),
        td(obsTargetText(t)), td(t.exported ? msg("obs.exported") : msg("obs.withheld")));
      tbody.appendChild(tr);
    }
  }
  if (!tbody.children.length) tbody.appendChild(emptyRow(5, msg("obs.noSelections")));
}

function obsCheck(field, label, checked, enabled) {
  const wrap = document.createElement("label"); wrap.className = "obs-check";
  const input = document.createElement("input"); input.type = "checkbox"; input.dataset.field = field;
  input.checked = checked; input.disabled = !enabled;
  const span = document.createElement("span"); span.textContent = label;
  wrap.append(input, span);
  return wrap;
}

function obsNumberField(field, label, value, placeholder, max, enabled) {
  const wrap = document.createElement("label"); wrap.className = "form-field";
  const span = document.createElement("span"); span.textContent = label;
  const input = document.createElement("input"); input.type = "number"; input.dataset.field = field;
  input.min = "1"; input.max = String(max); input.placeholder = String(placeholder);
  input.value = value ? String(value) : ""; input.disabled = !enabled;
  wrap.append(span, input);
  return wrap;
}

function renderObsLimits(s) {
  const box = $("obs-limits-rows"), note = $("obs-limits-readonly");
  const editable = !!(s.limits && s.limits.editable);
  box.replaceChildren();
  note.hidden = editable;
  note.textContent = editable ? "" : msg("obs.reason." + ((s.limits && s.limits.reason) || "not_configured"));
  for (const e of s.exporters || []) {
    const set = document.createElement("fieldset"); set.className = "obs-limit"; set.dataset.exporter = e.name;
    const legend = document.createElement("legend"); legend.textContent = e.name; set.appendChild(legend);
    const limit = e.limit || {};
    set.appendChild(obsCheck("paused", msg("obs.paused"), !!limit.paused, editable));
    const kept = new Set(limit.signals || e.signals);
    for (const sig of e.signals) set.appendChild(obsCheck("signal:" + sig, sig, kept.has(sig), editable));
    if (e.signals.includes("content")) set.appendChild(obsNumberField("max_content_bytes", msg("obs.maxContent"), limit.max_content_bytes, e.max_content_bytes, e.max_content_bytes, editable));
    if (e.signals.includes("traces")) set.appendChild(obsNumberField("max_sample_percent", msg("obs.maxSample"), limit.max_sample_percent, 100, 100, editable));
    box.appendChild(set);
  }
  $("obs-limits-preview").disabled = !editable || obsBusy.limits;
  $("obs-limits-apply").disabled = true;
  $("obs-limits-status").textContent = "";
  $("obs-limits-diff").replaceChildren();
}

function obsReadLimitRows() {
  return [...$("obs-limits-rows").querySelectorAll("fieldset.obs-limit")].map(set => {
    const e = obsState.exporters.find(x => x.name === set.dataset.exporter);
    const field = name => set.querySelector('[data-field="' + name + '"]');
    return {
      exporter: e.name, declared: e.signals, paused: field("paused").checked,
      signals: e.signals.filter(sig => field("signal:" + sig).checked),
      max_content_bytes: field("max_content_bytes") ? field("max_content_bytes").value : "",
      max_sample_percent: field("max_sample_percent") ? field("max_sample_percent").value : "",
    };
  });
}

function obsExportersFor(signal) { return (obsState.exporters || []).filter(e => e.signals.includes(signal)).map(e => e.name); }

function obsRuleRow(rule, editable) {
  const tr = document.createElement("tr"); tr.className = "obs-rule";
  const cell = node => { const c = document.createElement("td"); c.appendChild(node); return c; };
  const text = (field, value, label) => {
    const i = document.createElement("input"); i.type = "text"; i.dataset.field = field; i.value = value || "";
    i.autocomplete = "off"; i.spellcheck = false; i.disabled = !editable; i.setAttribute("aria-label", msg(label));
    return i;
  };
  const select = (field, signal, value, enabled, label, names) => {
    const sel = document.createElement("select"); sel.dataset.field = field; sel.disabled = !enabled; sel.setAttribute("aria-label", msg(label));
    for (const name of ["", ...(names || obsExportersFor(signal))]) {
      const o = document.createElement("option"); o.value = name; o.textContent = name || "—"; sel.appendChild(o);
    }
    sel.value = value || "";
    return sel;
  };
  const pct = document.createElement("input"); pct.type = "number"; pct.min = "1"; pct.max = "100"; pct.dataset.field = "sample_percent";
  pct.value = rule.traces ? String(rule.traces.sample_percent) : "100"; pct.disabled = !editable; pct.setAttribute("aria-label", msg("obs.maxSample"));
  const traces = document.createElement("div"); traces.className = "obs-inline";
  traces.append(select("traces", "traces", rule.traces && rule.traces.exporter, editable, "obs.colTraces"), pct);
  const remove = document.createElement("button"); remove.type = "button"; remove.className = "ghost";
  remove.textContent = msg("obs.remove"); remove.disabled = !editable;
  remove.addEventListener("click", () => { tr.remove(); obsInvalidate("rules"); });
  const subject = rule.subject || {};
  const contentEditable = editable && (obsState.rules.content_rules || !!rule.content);
  // Without content_rules an existing content rule can only keep its exporter
  // or drop it ("—"); the server refuses any other choice, so none is offered.
  const contentNames = !obsState.rules.content_rules && rule.content ? [rule.content.exporter] : null;
  tr.append(cell(text("name", rule.name, "obs.colName")), cell(text("team", subject.team, "obs.colTeam")), cell(text("user", subject.user, "obs.colUser")),
    cell(traces), cell(select("audit", "audit", rule.audit && rule.audit.exporter, editable, "obs.colAudit")),
    cell(select("content", "content", rule.content && rule.content.exporter, contentEditable, "obs.colContent", contentNames)), cell(remove));
  return tr;
}

function renderObsRules(s) {
  const r = s.rules || {}, editable = !!r.editable;
  $("obs-rules-readonly").hidden = editable;
  $("obs-rules-readonly").textContent = editable ? "" : msg("obs.reason." + (r.reason || "not_configured"));
  const tbody = $("obs-rules-table").querySelector("tbody");
  tbody.replaceChildren();
  for (const rule of r.console || []) tbody.appendChild(obsRuleRow(rule, editable));
  if (!tbody.children.length) tbody.appendChild(emptyRow(7, msg("obs.noRules")));
  $("obs-rules-add").disabled = !editable;
  $("obs-rules-preview").disabled = !editable || obsBusy.rules;
  $("obs-rules-apply").disabled = true;
  $("obs-rules-status").textContent = "";
  $("obs-rules-diff").replaceChildren();
}

function obsReadRuleRows() {
  return [...$("obs-rules-table").querySelectorAll("tr.obs-rule")].map(tr => {
    const v = field => { const e = tr.querySelector('[data-field="' + field + '"]'); return e ? e.value : ""; };
    return { name: v("name"), team: v("team"), user: v("user"), traces: v("traces"), sample_percent: v("sample_percent"), audit: v("audit"), content: v("content") };
  });
}

/* ---------- edits ---------- */

// obsRequest is the edit-route fetch: unlike api() it returns the status and
// the error body, whose stable code becomes translated text.
async function obsRequest(method, path, body) {
  const resp = await fetch(path, {
    method,
    headers: { "Authorization": "Bearer " + adminToken, ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : exactJSON(body), cache: "no-store", credentials: "omit", redirect: "error",
  });
  if (resp.status === 401) throw new Error("unauthorized — check the admin token");
  let data = null;
  try { data = parseExactJSON(await resp.text()); } catch { data = null; }
  return { status: resp.status, data };
}

function obsInvalidate(kind) {
  obsGen[kind]++;
  obsPreviews[kind] = null;
  $("obs-" + kind + "-apply").disabled = true;
}

function obsShowResult(kind, text, lines) {
  $("obs-" + kind + "-status").textContent = text;
  $("obs-" + kind + "-diff").replaceChildren(...lines.map(line => { const li = document.createElement("li"); li.textContent = line; return li; }));
}

// obsSubmit runs one dry run or apply for an editor. Apply refuses a body that
// differs from the last previewed one. While the request is in flight the
// editor's Preview and Apply are off; afterwards Preview follows the editor's
// editable state and Apply stays off unless a dry run with changes armed it.
async function obsSubmit(kind, dryRun, build, path, describe) {
  if (obsBusy[kind]) return;
  const previewBtn = $("obs-" + kind + "-preview"), applyBtn = $("obs-" + kind + "-apply");
  obsBusy[kind] = true;
  previewBtn.disabled = true;
  applyBtn.disabled = true;
  try {
    await obsSend(kind, dryRun, build, path, describe);
  } finally {
    obsBusy[kind] = false;
    previewBtn.disabled = !(obsState && obsState[kind] && obsState[kind].editable);
  }
}

async function obsSend(kind, dryRun, build, path, describe) {
  const status = $("obs-" + kind + "-status");
  let body;
  try {
    body = build(dryRun);
  } catch (err) {
    status.textContent = String(err.message || err);
    return;
  }
  const preview = obsPreviews[kind];
  if (!dryRun && (!preview || preview.body !== exactJSON({ ...body, dry_run: true }))) {
    status.textContent = msg(preview ? "obs.formChanged" : "obs.previewFirst");
    return;
  }
  const gen = obsGen[kind];
  const { status: code, data } = await obsRequest("PUT", path, body);
  if (code !== 200 || !data) {
    status.textContent = obsErrorText(data, code, kind);
    obsInvalidate(kind);
    return;
  }
  const lines = describe(data);
  if (dryRun) {
    if (gen !== obsGen[kind]) { status.textContent = msg("obs.formChanged"); return; } // edited while the dry run was pending
    obsPreviews[kind] = { body: exactJSON(body) };
    obsShowResult(kind, data.changes.length ? msg("obs.previewed").replace("{n}", String(data.changes.length)) : msg("obs.noChanges"), lines);
    $("obs-" + kind + "-apply").disabled = !data.changes.length;
    return;
  }
  const applied = msg("obs.applied").replace("{r}", obsShortRev(data.result_revision));
  try {
    await refreshObservabilityView(true);
  } catch (err) {
    // The change is saved; the view still shows the old state. Keep the apply
    // result and the editors, drop both previews (the revision moved) and show
    // the failure where it is visible.
    obsInvalidate("limits"); obsInvalidate("rules");
    obsShowResult(kind, applied, lines);
    showWorkspaceError(err);
    return;
  }
  obsShowResult(kind, applied, lines);
}

function obsSubmitLimits(dryRun) {
  return obsSubmit("limits", dryRun, d => obsLimitsBody(obsReadLimitRows(), d, obsState.limits.revision), "/admin/observability/limits",
    data => (data.changes || []).map(obsChangeText).concat((data.effects || []).map(obsEffectText)));
}

function obsSubmitRules(dryRun) {
  return obsSubmit("rules", dryRun, d => obsRulesBody(obsReadRuleRows(), d, obsState.rules.revision), "/admin/observability/rules",
    data => (data.changes || []).map(obsRuleChangeText).concat((data.decisions || []).map(obsDecisionText)));
}

function obsBind() {
  if (obsBound) return;
  obsBound = true;
  const report = kind => err => { $("obs-" + kind + "-status").textContent = String(err.message || err); };
  $("obs-refresh").addEventListener("click", () => refreshObservabilityView(true).catch(showWorkspaceError));
  $("obs-limits-preview").addEventListener("click", () => obsSubmitLimits(true).catch(report("limits")));
  $("obs-limits-apply").addEventListener("click", () => obsSubmitLimits(false).catch(report("limits")));
  $("obs-rules-preview").addEventListener("click", () => obsSubmitRules(true).catch(report("rules")));
  $("obs-rules-apply").addEventListener("click", () => obsSubmitRules(false).catch(report("rules")));
  $("obs-rules-add").addEventListener("click", () => {
    const tbody = $("obs-rules-table").querySelector("tbody");
    tbody.querySelectorAll("td.empty").forEach(cell => cell.parentNode.remove());
    tbody.appendChild(obsRuleRow({ name: "console-", subject: {} }, true));
    obsInvalidate("rules");
  });
  for (const [kind, id] of [["limits", "obs-limits-rows"], ["rules", "obs-rules-table"]]) {
    $(id).addEventListener("input", () => obsInvalidate(kind));
    $(id).addEventListener("change", () => obsInvalidate(kind));
  }
}
