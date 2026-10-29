"use strict";

// Audit view (ADR-057): read-only evidence from the audit chain through
// /admin/audit/*. textContent-only rendering; nothing is kept in browser
// storage (ADR-001). Solid badges are certain; dashed badges are estimates.
const AUDIT_KINDS = ["ok", "policy_denied", "privacy_blocked", "privacy_masked", "privacy_internal", "upstream_error", "auto_mode_blocked"];
const AUDIT_KIND_CLASS = { ok: "ok", policy_denied: "err", privacy_blocked: "err", privacy_masked: "warn", privacy_internal: "warn", upstream_error: "warn", auto_mode_blocked: "est" };
const AUDIT_RANGES = { "24h": 86400000, "7d": 7 * 86400000, "30d": 30 * 86400000 };
const AUDIT_TRACE_ID = /^[0-9a-f]{32}$/;
let auditState = { tab: "traces", kinds: new Set(), events: [], next: "", selected: "", keys: new Map(), generation: 0, summary: null };

function auditKindClass(kind) { return AUDIT_KIND_CLASS[kind] || "warn"; }
function auditWindow(range, nowMs) {
  const span = AUDIT_RANGES[range] || AUDIT_RANGES["24h"];
  return { since: new Date(nowMs - span).toISOString(), until: new Date(nowMs).toISOString() };
}
function auditQuery(params) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== "" && v != null) q.set(k, String(v));
  const s = q.toString();
  return s ? "?" + s : "";
}
// Keys have no name field; metadata.name is the operator's label. An
// ambiguous name never silently filters to one key.
function auditResolveKey(text) {
  if (!text) return { keyId: "", ambiguous: false };
  if (auditState.keys.has(text)) return { keyId: text, ambiguous: false };
  const hits = [...auditState.keys.values()].filter(k => k.metadata && k.metadata.name === text);
  if (hits.length === 1) return { keyId: hits[0].key_id, ambiguous: false };
  if (hits.length > 1) return { keyId: "", ambiguous: true };
  return { keyId: text, ambiguous: false };
}
function auditKeyLabel(keyId) {
  const k = auditState.keys.get(keyId);
  return k && k.metadata && k.metadata.name ? k.metadata.name : (keyId || "—");
}
function auditTraceLink(template, traceId) {
  if (!template || !AUDIT_TRACE_ID.test(traceId || "")) return "";
  const url = template.split("{trace_id}").join(encodeURIComponent(traceId));
  return /^https?:\/\/[^/]/.test(url) ? url : "";
}
function auditDuration(ms) {
  if (!ms) return "—";
  return ms >= 1000 ? (ms / 1000).toFixed(ms >= 10000 ? 1 : 2) + "s" : ms + "ms";
}
function auditBar(row, total) {
  if (!total) return { left: 0, width: 100 };
  const left = Math.min(100, Math.max(0, row.start_ms / total * 100));
  const width = Math.max(0.4, Math.min(100 - left, row.dur_ms / total * 100));
  return { left: Math.round(left * 100) / 100, width: Math.round(width * 100) / 100 };
}
function auditScope(code) {
  if (/^team_/.test(code)) return "team";
  if (/^key_/.test(code)) return "key";
  if (/^user_/.test(code)) return "user";
  return "route";
}
function auditSparkPoints(series, w, h) {
  const top = Math.max(1, ...series), step = series.length > 1 ? w / (series.length - 1) : 0;
  return series.map((v, i) => [Math.round(i * step * 10) / 10, Math.round((h - 2 - v / top * (h - 6)) * 10) / 10]);
}

function auditEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}
function auditPill(text, cls) { return auditEl("span", "audit-pill " + cls, text); }
function auditKindPill(kind) { return auditPill(msg("audit.kind." + kind), auditKindClass(kind)); }
function auditCell(tr, content, cls) {
  const td = auditEl("td", cls || "");
  if (content instanceof Node) td.appendChild(content); else td.textContent = content == null ? "" : String(content);
  tr.appendChild(td);
  return td;
}
// "Showing the first N" note for single-page tables; empty when nothing was cut off.
function auditShownNote(count, next) { return next ? msg("audit.showingFirst").replace("{n}", String(count)) : ""; }
function auditTime(ts) { return ts ? ts.replace("T", " ").slice(0, 19) : ""; }

// Query parameters for the filter inputs. An ambiguous key name gives no
// parameters at all: it must never fall back to every key's data.
function auditFilterParams(range, team, keyText, nowMs) {
  const key = auditResolveKey(keyText);
  if (key.ambiguous) return { params: null, ambiguous: true };
  const w = auditWindow(range, nowMs);
  return { params: { since: w.since, until: w.until, team: team, key_id: key.keyId }, ambiguous: false };
}

// Current filters, or null (after clearing the view) when the key name is ambiguous.
function auditFilters() {
  const f = auditFilterParams($("audit-range").value, $("audit-team").value.trim(), $("audit-key").value.trim(), Date.now());
  const note = $("audit-filter-note");
  note.hidden = !f.ambiguous;
  note.textContent = f.ambiguous ? msg("audit.keyAmbiguous") : "";
  if (f.ambiguous) auditClearView();
  return f.params;
}

// Empties cards, tables and details so nothing from an earlier query stays on screen.
function auditClearView() {
  auditState.events = []; auditState.next = ""; auditState.selected = ""; auditState.summary = null;
  $("audit-cards").textContent = "";
  $("audit-coverage").textContent = "";
  for (const id of ["audit-trace-table", "audit-session-table", "audit-pii-table", "audit-deny-table", "audit-auto-table"]) $(id).querySelector("tbody").textContent = "";
  for (const id of ["audit-detail", "audit-session-detail", "audit-pii-cats", "audit-pii-actions", "audit-sessions-note", "audit-pii-note", "audit-auto-note"]) $(id).textContent = "";
  for (const id of ["audit-more", "audit-class-hint", "audit-auto-warning"]) $(id).hidden = true;
}

async function refreshAuditView() {
  const gen = ++auditState.generation;
  const on = capOn("audit_console");
  $("audit-admin-only").hidden = !on || whoamiIsAdmin;
  $("audit-content").hidden = !on || !whoamiIsAdmin;
  if (!on || !whoamiIsAdmin) return;
  const keys = await api("GET", "/admin/keys");
  auditState.keys = new Map(((keys && keys.data) || []).map(k => [k.key_id, k]));
  const f = auditFilters();
  if (!f) return;
  const [summary, health] = await Promise.all([
    api("GET", "/admin/audit/summary" + auditQuery(f)),
    api("GET", "/admin/analytics/health", null, true),
  ]);
  if (gen !== auditState.generation) return;
  auditState.summary = summary;
  renderAuditHeader(summary, health);
  renderAuditCards(summary);
  renderAuditKinds();
  await auditShowTab(auditState.tab);
}

function renderAuditHeader(summary, health) {
  const fresh = health && health !== DISABLED && health.last_ingest_ts ? health.last_ingest_ts : "";
  $("audit-freshness").textContent = fresh ? msg("audit.freshness") + " " + auditTime(fresh) : msg("audit.lagHint");
  $("audit-coverage").textContent = summary.coverage_start_ms
    ? msg("audit.coverage") + " " + auditTime(new Date(summary.coverage_start_ms).toISOString())
    : msg("audit.coverageEmpty");
}

function renderAuditCards(s) {
  const box = $("audit-cards");
  box.textContent = "";
  const total = Object.values(s.by_kind || {}).reduce((a, b) => a + b, 0);
  const denials = (s.by_kind.policy_denied || 0) + (s.by_kind.privacy_blocked || 0);
  const pii = (s.pii.masked || 0) + (s.pii.internal || 0) + (s.pii.blocked || 0) + (s.pii.shadow || 0);
  const auto = s.auto_mode || { block: 0, allow: 0, unknown: 0 };
  const cards = [
    ["audit.cardRequests", total, msg("audit.kind.ok") + " " + (s.by_kind.ok || 0), "requests", "", false],
    ["audit.cardDenials", denials, msg("audit.policyDenialsHint"), "denials", "alert", false],
    ["audit.cardPII", pii, msg("audit.piiMasked") + " " + (s.pii.masked || 0) + " · " + msg("audit.piiInternal") + " " + (s.pii.internal || 0), "pii", "warn", false],
    ["audit.cardAuto", auto.block, msg("audit.verdictAllow") + " " + auto.allow + " · " + msg("audit.verdictBlock") + " " + auto.block + " · " + msg("audit.verdictUnknown") + " " + auto.unknown, "auto_blocked", "", true],
  ];
  for (const [label, value, sub, series, cls, estimate] of cards) {
    const card = auditEl("div", "card audit-stat " + cls);
    const k = auditEl("div", "audit-stat-k", msg(label));
    if (estimate) k.appendChild(auditPill(msg("audit.estimate"), "est"));
    card.append(k, auditEl("div", "audit-stat-v", String(value)), auditEl("div", "audit-stat-s", sub));
    card.appendChild(auditSpark((s.buckets || []).map(b => b[series] || 0)));
    box.appendChild(card);
  }
}

function auditSpark(series) {
  const ns = "http://www.w3.org/2000/svg", w = 120, h = 32;
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 " + w + " " + h);
  svg.setAttribute("class", "audit-spark");
  svg.setAttribute("aria-hidden", "true");
  if (!series.length) return svg;
  const pts = auditSparkPoints(series, w, h);
  const line = document.createElementNS(ns, "polyline");
  line.setAttribute("points", pts.map(p => p.join(",")).join(" "));
  svg.appendChild(line);
  return svg;
}

function renderAuditKinds() {
  const bar = $("audit-kinds");
  bar.textContent = "";
  const all = auditEl("button", "audit-chip", msg("audit.all"));
  all.type = "button";
  all.setAttribute("aria-pressed", String(auditState.kinds.size === 0));
  all.addEventListener("click", () => { auditState.kinds.clear(); renderAuditKinds(); auditLoadEvents(false); });
  bar.appendChild(all);
  for (const kind of AUDIT_KINDS) {
    const b = auditEl("button", "audit-chip", msg("audit.kind." + kind));
    b.type = "button";
    b.setAttribute("aria-pressed", String(auditState.kinds.has(kind)));
    b.addEventListener("click", () => {
      if (auditState.kinds.has(kind)) auditState.kinds.delete(kind); else auditState.kinds.add(kind);
      renderAuditKinds();
      auditLoadEvents(false);
    });
    bar.appendChild(b);
  }
}

async function auditShowTab(tab) {
  auditState.tab = tab;
  for (const t of ["traces", "sessions", "pii", "denials"]) {
    $("audit-tab-" + t).setAttribute("aria-selected", String(t === tab));
    $("audit-panel-" + t).hidden = t !== tab;
  }
  if (tab === "traces") return auditLoadEvents(false);
  if (tab === "sessions") return auditLoadSessions();
  if (tab === "pii") return auditLoadPII();
  return auditLoadDenials();
}

async function auditLoadEvents(append) {
  const gen = auditState.generation;
  const f = auditFilters();
  if (!f) return;
  const out = await api("GET", "/admin/audit/events" + auditQuery({ ...f, kind: [...auditState.kinds].join(","), limit: 100, before: append ? auditState.next : "" }));
  if (gen !== auditState.generation) return;
  auditState.events = append ? auditState.events.concat(out.events) : out.events;
  auditState.next = out.next || "";
  renderAuditTraces();
}

function renderAuditTraces() {
  const tbody = $("audit-trace-table").querySelector("tbody");
  tbody.textContent = "";
  $("audit-more").hidden = !auditState.next;
  if (!auditState.events.length) {
    $("audit-class-hint").hidden = true;
    const tr = auditEl("tr", "static");
    const td = auditCell(tr, msg("audit.empty"), "hint");
    td.colSpan = 8;
    tbody.appendChild(tr);
    renderAuditDetailEmpty();
    return;
  }
  $("audit-class-hint").hidden = auditState.events.some(e => e.request_class);
  for (const e of auditState.events) {
    const tr = auditEl("tr", e.id === auditState.selected ? "sel" : "");
    tr.tabIndex = 0;
    auditCell(tr, auditTime(e.ts), "mono nowrap");
    const kind = auditEl("span");
    kind.appendChild(auditKindPill(e.kind));
    if (e.auto_verdict === "allow") kind.appendChild(auditPill(msg("audit.autoAllow"), "est"));
    auditCell(tr, kind);
    const who = auditEl("span", "", e.team);
    who.appendChild(auditEl("span", "sub", auditKeyLabel(e.key_id)));
    auditCell(tr, who);
    auditCell(tr, e.request_class ? auditPill(e.request_class, "cls") : "—");
    const model = auditEl("span", "", e.model_requested);
    if (e.model_actual && e.model_actual !== e.model_requested) model.appendChild(auditEl("span", "sub", "→ " + e.model_actual));
    if (!e.model_actual && e.event === "request_started" && !e.attempt_count) model.appendChild(auditEl("span", "sub", msg("audit.deniedBefore")));
    auditCell(tr, model, "model");
    auditCell(tr, e.input_tokens + " / " + e.output_tokens, "num");
    auditCell(tr, e.cost_micros ? usd(e.cost_micros) : "—", "num");
    auditCell(tr, auditDuration(e.total_ms), "num");
    const pick = () => { auditState.selected = e.id; renderAuditTraces(); auditLoadDetail(e.id); };
    tr.addEventListener("click", pick);
    tr.addEventListener("keydown", ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } });
    tbody.appendChild(tr);
  }
  if (!auditState.selected) renderAuditDetailEmpty();
}

function renderAuditDetailEmpty() {
  const d = $("audit-detail");
  d.textContent = "";
  d.appendChild(auditEl("p", "hint", msg("audit.selectRow")));
}

async function auditLoadDetail(id) {
  const d = $("audit-detail");
  d.textContent = msg("common.loading");
  try {
    renderAuditDetail(await api("GET", "/admin/audit/events/" + encodeURIComponent(id)));
  } catch (err) {
    d.textContent = String(err.message || err);
  }
  auditReveal(d);
}

// Below the split breakpoint the detail sits under the list; bring it into view after a pick.
function auditReveal(el) {
  const r = el.getBoundingClientRect();
  if (r.top >= window.innerHeight || r.bottom <= 0) el.scrollIntoView({ block: "start" });
}

function auditKV(dl, label, value) {
  dl.appendChild(auditEl("dt", "", msg(label)));
  const dd = auditEl("dd");
  if (value instanceof Node) dd.appendChild(value); else dd.textContent = value;
  dl.appendChild(dd);
}

function renderAuditDetail(detail) {
  const e = detail.event, rec = detail.record || {}, d = $("audit-detail");
  d.textContent = "";
  d.appendChild(auditEl("p", "microlabel", msg("audit.detailTitle")));
  d.appendChild(auditEl("h2", "", e.model_requested + (e.model_actual && e.model_actual !== e.model_requested ? " → " + e.model_actual : "")));
  d.appendChild(auditEl("p", "mono hint", msg("audit.record") + " " + e.id));
  d.appendChild(renderAuditWaterfall(detail.waterfall || { rows: [] }, rec));

  const actor = auditEl("dl", "audit-kv");
  const key = auditEl("span", "", auditKeyLabel(e.key_id) + " · " + e.key_id + " ");
  key.appendChild(auditPill(msg("audit.fromAudit"), "ev"));
  if (e.key_id) {
    const open = auditEl("button", "linklike", msg("audit.openKey"));
    open.type = "button";
    open.addEventListener("click", () => {
      showView("keys");
      $("key-search").value = e.key_id;
      applyRecordFilters();
    });
    key.appendChild(open);
  }
  auditKV(actor, "audit.keyRow", key);
  const team = auditEl("span", "", e.team + " ");
  team.appendChild(auditPill(msg("audit.fromAudit"), "ev"));
  auditKV(actor, "audit.team", team);
  const info = auditState.keys.get(e.key_id);
  const owner = auditEl("span", "", info ? (info.owner || "—") + " " : msg("audit.notInStore") + " ");
  if (info) owner.appendChild(auditPill(msg("audit.fromStore"), "reg"));
  auditKV(actor, "audit.owner", owner);
  const ident = rec.principal && rec.principal.identity ? JSON.stringify(rec.principal.identity) : "";
  auditKV(actor, "audit.identity", ident || msg("audit.identityOff"));
  d.appendChild(auditSection(msg("audit.actor"), actor));

  const facts = auditEl("dl", "audit-kv");
  auditKV(facts, "audit.session", e.session_ref || msg("audit.noSession"));
  auditKV(facts, "audit.provider", e.provider || "—");
  auditKV(facts, "audit.tokens", e.input_tokens + " / " + e.output_tokens);
  auditKV(facts, "audit.cost", e.cost_micros ? usd(e.cost_micros) : "—");
  d.appendChild(auditSection("", facts));

  if (e.pii) {
    const p = auditEl("dl", "audit-kv");
    auditKV(p, "audit.result", auditKindPill(e.kind));
    const cats = auditEl("span");
    for (const c of e.categories || []) cats.appendChild(auditPill(c, "cat"));
    auditKV(p, "audit.category", cats);
    auditKV(p, "audit.policy", e.policy_ref || "—");
    auditKV(p, "audit.boundary", e.planned_boundary ? e.planned_boundary + " → " + (e.actual_boundary || "—") : msg("audit.deniedBefore"));
    auditKV(p, "audit.maskedRow", msg(e.masked ? "audit.maskedYes" : "audit.maskedNo"));
    d.appendChild(auditSection(msg("audit.privacy"), p));
  }
  if (e.deny_code) {
    const p = auditEl("dl", "audit-kv");
    auditKV(p, "audit.code", e.deny_code + " · HTTP " + e.status);
    auditKV(p, "audit.policy", e.policy_ref || msg("audit.policyNotRecorded"));
    if (e.event === "request_started" && !e.attempt_count) auditKV(p, "audit.billing", msg("audit.noBilling"));
    d.appendChild(auditSection(msg("audit.denial"), p));
  }
  if (e.auto_verdict) {
    const p = auditEl("dl", "audit-kv");
    auditKV(p, "audit.stage", e.auto_stage || "—");
    const v = auditEl("span", "", e.auto_verdict + " ");
    v.appendChild(auditPill(msg("audit.estimate") + " · " + e.auto_signature, "est"));
    auditKV(p, "audit.verdict", v);
    d.appendChild(auditSection(msg("audit.autoMode"), p));
  }

  const body = auditEl("div", "audit-callout");
  if (rec.body_ref && capOn("logs_bodies")) {
    const btn = auditEl("button", "primary", msg("audit.openBody"));
    btn.type = "button";
    const box = auditEl("div");
    btn.addEventListener("click", async () => {
      box.textContent = msg("common.loading");
      try {
        const b = await api("GET", "/admin/bodies/" + encodeURIComponent(rec.body_ref), null, true);
        if (b === DISABLED) { box.textContent = msg("body.storeNotEnabled"); return; }
        renderBodyDrawer(box, rec.body_ref, b);
      } catch (err) { box.textContent = String(err.message || err); }
    });
    body.append(btn, auditEl("p", "hint", msg("audit.bodyAudited")), box);
  } else {
    body.appendChild(auditEl("p", "hint", msg("audit.noBody")));
  }
  d.appendChild(auditSection(msg("audit.body"), body));

  const chain = auditEl("dl", "audit-kv");
  const tl = auditTraceLink(caps && caps.trace_link_template, e.trace_id);
  if (tl) {
    const a = auditEl("a", "", msg("audit.openTrace"));
    a.href = tl; a.target = "_blank"; a.rel = "noopener noreferrer";
    const span = auditEl("span", "mono", e.trace_id + " ");
    span.appendChild(a);
    auditKV(chain, "audit.traceId", span);
  } else {
    auditKV(chain, "audit.traceId", e.trace_id || "—");
  }
  auditKV(chain, "audit.prevHash", rec.prev_hash || "—");
  auditKV(chain, "audit.recordHash", detail.hash || "—");
  d.appendChild(auditSection(msg("audit.chainPos"), chain));
}

function auditSection(title, child) {
  const s = auditEl("section", "audit-section");
  if (title) s.appendChild(auditEl("p", "microlabel", title));
  s.appendChild(child);
  return s;
}

// A record with latency but no attempts was answered by the gateway itself
// only when it names no provider and has no usage (the count_tokens shape).
// Responses records written before attempt timing existed have both.
function auditAnsweredLocally(rec) {
  return !(rec && rec.request && rec.request.provider) && !(rec && rec.usage);
}

function renderAuditWaterfall(w, rec) {
  const box = auditEl("div", "audit-wf");
  box.appendChild(auditEl("p", "microlabel", msg("audit.timeline") + " · " + auditDuration(w.total_ms)));
  if (!w.total_ms) {
    box.appendChild(auditEl("p", "hint", msg("audit.noTiming")));
    return box;
  }
  if ((!w.rows || !w.rows.length) && !auditAnsweredLocally(rec)) {
    // Provider time is inside total_ms, so no "gateway work" bar is drawn for it.
    box.appendChild(auditEl("p", "hint", msg("audit.attemptTimingMissing")));
    return box;
  }
  if (!w.rows || !w.rows.length) {
    // Current record without provider attempts (e.g. count_tokens): not a legacy gap.
    const row = auditEl("div", "audit-wf-row");
    const track = auditEl("div", "audit-wf-track");
    const bar = auditEl("span", "audit-wf-bar gate");
    bar.style.left = "0%";
    bar.style.width = "100%";
    track.appendChild(bar);
    row.append(auditEl("span", "lab", msg("audit.wf.gateway")), track, auditEl("span", "ms", auditDuration(w.total_ms)));
    box.append(row, auditEl("p", "hint", msg("audit.noAttempt")));
    return box;
  }
  for (const r of w.rows) {
    const label = r.kind === "attempt" ? msg("audit.attempt") + " · " + r.provider + (r.http ? " · " + r.http : "") + (r.status && r.status !== "ok" ? " · " + r.status : "")
      : msg("audit.wf." + r.kind);
    const row = auditEl("div", "audit-wf-row");
    row.appendChild(auditEl("span", "lab", label));
    const track = auditEl("div", "audit-wf-track");
    const bar = auditEl("span", "audit-wf-bar " + (r.kind === "denied" || (r.kind === "attempt" && r.status !== "ok") ? "fail" : r.kind === "attempt" ? "ok" : "gate"));
    const g = auditBar(r, w.total_ms);
    bar.style.left = g.left + "%";
    bar.style.width = g.width + "%";
    track.appendChild(bar);
    if (w.ttft_ms && r.kind === "attempt" && r.status === "ok" && w.ttft_ms >= r.start_ms && w.ttft_ms <= r.start_ms + r.dur_ms) {
      const mark = auditEl("span", "audit-wf-mark");
      mark.style.left = auditBar({ start_ms: w.ttft_ms, dur_ms: 0 }, w.total_ms).left + "%";
      mark.title = msg("audit.firstToken") + " " + auditDuration(w.ttft_ms);
      track.appendChild(mark);
    }
    row.append(track, auditEl("span", "ms", auditDuration(r.dur_ms)));
    box.appendChild(row);
  }
  if (w.ttft_ms) box.appendChild(auditEl("p", "hint", msg("audit.firstToken") + " " + auditDuration(w.ttft_ms)));
  return box;
}

async function auditLoadSessions() {
  const f = auditFilters();
  if (!f) return;
  const out = await api("GET", "/admin/audit/sessions" + auditQuery({ ...f, limit: 100 }));
  const tbody = $("audit-session-table").querySelector("tbody");
  tbody.textContent = "";
  $("audit-session-detail").textContent = "";
  $("audit-sessions-note").textContent = auditShownNote(out.sessions.length, out.next);
  if (!out.sessions.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.sessionsEmpty"), "hint").colSpan = 6;
    tbody.appendChild(tr);
    return;
  }
  for (const s of out.sessions) {
    const tr = auditEl("tr");
    tr.tabIndex = 0;
    auditCell(tr, s.session_ref.slice(0, 11) + "…", "mono nowrap");
    const who = auditEl("span", "", s.team);
    who.appendChild(auditEl("span", "sub", auditKeyLabel(s.key_id)));
    auditCell(tr, who);
    auditCell(tr, auditTime(s.first_ts).slice(11, 16) + "–" + auditTime(s.last_ts).slice(11, 16), "nowrap");
    auditCell(tr, s.requests, "num");
    auditCell(tr, s.cost_micros ? usd(s.cost_micros) : "—", "num");
    const flags = auditEl("span");
    if (s.denials) flags.appendChild(auditPill(msg("audit.denialsShort") + " " + s.denials, "err"));
    if (s.pii) flags.appendChild(auditPill(msg("audit.piiShort") + " " + s.pii, "warn"));
    if (s.auto_blocked) flags.appendChild(auditPill(msg("audit.autoShort") + " " + s.auto_blocked, "est"));
    if (s.subagents) flags.appendChild(auditPill(msg("audit.subagents") + " " + s.subagents, "cls"));
    auditCell(tr, flags);
    const pick = () => auditLoadSession(s);
    tr.addEventListener("click", pick);
    tr.addEventListener("keydown", ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } });
    tbody.appendChild(tr);
  }
}

async function auditLoadSession(s) {
  const d = $("audit-session-detail");
  d.textContent = msg("common.loading");
  const out = await api("GET", "/admin/audit/sessions/" + encodeURIComponent(s.session_ref) + auditQuery({ limit: 500 }));
  d.textContent = "";
  d.append(auditEl("p", "microlabel", msg("audit.sessionTimeline")), auditEl("h2", "mono", s.session_ref.slice(0, 11) + "…"));
  if (out.events.length && !out.events.some(e => e.request_class)) d.appendChild(auditEl("p", "hint", msg("audit.classNotReported")));
  const list = auditEl("div", "audit-tl");
  for (const e of out.events) {
    const item = auditEl("button", "audit-tl-item" + (e.request_class === "subagent" ? " indent" : ""));
    item.type = "button";
    item.append(auditEl("span", "mono", auditTime(e.ts).slice(11)), auditPill(e.request_class || "—", "cls"),
      auditEl("span", "", e.model_requested), auditKindPill(e.kind));
    item.addEventListener("click", async () => {
      auditState.selected = e.id;
      await auditShowTab("traces");
      auditLoadDetail(e.id);
    });
    list.appendChild(item);
  }
  d.appendChild(list);
  if (out.next) d.appendChild(auditEl("p", "hint", msg("audit.sessionTruncated")));
  auditReveal(d);
}

function auditBars(box, rows) {
  box.textContent = "";
  const top = Math.max(1, ...rows.map(r => r[1]));
  for (const [label, count, cls] of rows) {
    const row = auditEl("div", "audit-bar-row");
    const track = auditEl("span", "audit-bar-track");
    const fill = auditEl("span", "audit-bar-fill " + (cls || ""));
    fill.style.width = (count / top * 100).toFixed(1) + "%";
    track.appendChild(fill);
    row.append(auditEl("span", "mono", label), track, auditEl("span", "num", String(count)));
    box.appendChild(row);
  }
}

async function auditLoadPII() {
  const s = auditState.summary, f = auditFilters();
  if (!f || !s) return;
  auditBars($("audit-pii-cats"), (s.categories || []).map(c => [c.name, c.count, "warn"]));
  auditBars($("audit-pii-actions"), [[msg("audit.piiMasked"), s.pii.masked, "warn"], [msg("audit.piiInternal"), s.pii.internal, "warn"],
    [msg("audit.piiBlocked"), s.pii.blocked, "err"], [msg("audit.piiShadow"), s.pii.shadow, "dim"]]);
  const out = await api("GET", "/admin/audit/events" + auditQuery({ ...f, pii: "true", limit: 100 }));
  const tbody = $("audit-pii-table").querySelector("tbody");
  tbody.textContent = "";
  $("audit-pii-note").textContent = auditShownNote(out.events.length, out.next);
  if (!out.events.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.piiEmpty"), "hint").colSpan = 6;
    tbody.appendChild(tr);
    return;
  }
  for (const e of out.events) {
    const tr = auditEl("tr", "static");
    auditCell(tr, auditTime(e.ts), "mono nowrap");
    const who = auditEl("span", "", e.team);
    who.appendChild(auditEl("span", "sub", auditKeyLabel(e.key_id)));
    auditCell(tr, who);
    const cats = auditEl("span");
    for (const c of e.categories || []) cats.appendChild(auditPill(c, "cat"));
    auditCell(tr, cats);
    auditCell(tr, e.would_restrict && e.kind === "ok" ? auditPill(msg("audit.piiShadow"), "cls") : auditKindPill(e.kind));
    auditCell(tr, e.policy_ref || "—");
    auditCell(tr, e.planned_boundary ? e.planned_boundary + " → " + (e.actual_boundary || "—") : msg("audit.deniedBefore"));
    tbody.appendChild(tr);
  }
}

async function auditLoadDenials() {
  const s = auditState.summary, f = auditFilters();
  if (!f || !s) return;
  const tbody = $("audit-deny-table").querySelector("tbody");
  tbody.textContent = "";
  if (!s.denials.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.denialsEmpty"), "hint").colSpan = 5;
    tbody.appendChild(tr);
  }
  for (const x of s.denials) {
    const tr = auditEl("tr", "static");
    auditCell(tr, x.code, "mono");
    auditCell(tr, x.policy || msg("audit.policyNotRecorded"));
    const scope = auditScope(x.code);
    auditCell(tr, msg("audit.scope." + scope) + " " + (scope === "team" ? x.team : auditKeyLabel(x.key_id)));
    auditCell(tr, x.count, "num");
    auditCell(tr, auditTime(x.last_ts), "mono nowrap");
    tbody.appendChild(tr);
  }
  const auto = s.auto_mode;
  const warn = $("audit-auto-warning");
  warn.hidden = !auto.unknown;
  warn.textContent = auto.unknown ? msg("audit.signatureWarn") + " (" + auto.unknown + ")" : "";
  const out = await api("GET", "/admin/audit/events" + auditQuery({ ...f, auto: "true", limit: 100 }));
  const ab = $("audit-auto-table").querySelector("tbody");
  ab.textContent = "";
  $("audit-auto-note").textContent = auditShownNote(out.events.length, out.next);
  if (!out.events.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.autoNotObserved"), "hint").colSpan = 5;
    ab.appendChild(tr);
    return;
  }
  for (const e of out.events) {
    const tr = auditEl("tr", "static");
    auditCell(tr, auditTime(e.ts), "mono nowrap");
    const who = auditEl("span", "mono", e.session_ref ? e.session_ref.slice(0, 11) + "…" : "—");
    who.appendChild(auditEl("span", "sub", auditKeyLabel(e.key_id)));
    auditCell(tr, who);
    auditCell(tr, auditPill(e.auto_stage || "—", "cls"));
    auditCell(tr, auditPill(e.auto_verdict, "est"));
    auditCell(tr, e.model_actual || e.model_requested, "model");
    ab.appendChild(tr);
  }
}

async function auditVerifyChain() {
  const out = $("audit-verify-result");
  out.textContent = msg("common.loading");
  try {
    const r = await api("GET", "/admin/audit/verify");
    const sinks = (r && r.sinks) || [];
    if (!sinks.length) { out.textContent = msg("audit.verifyNone"); return; }
    out.textContent = sinks.map(s => s.ok ? msg("audit.verifyOk") + " · " + (s.records || 0) : msg("audit.verifyBroken") + " " + (s.broken_at || s.reason || "")).join(" · ");
  } catch (err) {
    out.textContent = String(err.message || err);
  }
}

if (typeof window !== "undefined" && document.getElementById("audit-range")) {
  for (const id of ["audit-range", "audit-team", "audit-key"]) $(id).addEventListener("change", () => refreshAuditView().catch(showWorkspaceError));
  for (const t of ["traces", "sessions", "pii", "denials"]) $("audit-tab-" + t).addEventListener("click", () => auditShowTab(t).catch(showWorkspaceError));
  $("audit-more").addEventListener("click", () => auditLoadEvents(true).catch(showWorkspaceError));
  $("audit-verify").addEventListener("click", auditVerifyChain);
}
