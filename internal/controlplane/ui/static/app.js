// inferplane usage console. The bearer token lives in this closure variable
// ONLY — no persistent browser storage of any kind (the mayu-console ADR-001
// storage invariant; mechanically enforced by ui_test.go). CSP forbids
// inline handlers, so everything wires up via addEventListener.
(function () {
  "use strict";

  let token = "";

  const $ = (id) => document.getElementById(id);
  // Integer-string formatting — no float division on money (the µUSD
  // mandate applies to display honesty too; toFixed loses precision).
  const usd = (micros) => {
    const s = String(micros).padStart(7, "0");
    return "$" + s.slice(0, -6) + "." + s.slice(-6, -2);
  };

  function setError(msg) {
    const el = $("error");
    if (!msg) { el.hidden = true; el.textContent = ""; return; }
    el.textContent = msg;
    el.hidden = false;
  }

  function query(params) {
    const qs = new URLSearchParams(params).toString();
    return fetch("/v1alpha1/usage?" + qs, {
      headers: token ? { Authorization: "Bearer " + token } : {},
    }).then((resp) => {
      if (resp.status === 401) throw new Error("unauthorized — check the token");
      if (resp.status === 503) throw new Error("usage store unavailable");
      if (!resp.ok) throw new Error("query failed (HTTP " + resp.status + ")");
      return resp.json();
    });
  }

  function range() {
    const params = {};
    if ($("since").value) params.since = $("since").value;
    if ($("until").value) params.until = $("until").value;
    return params;
  }

  function fillRows(tbody, rows, onRowClick) {
    tbody.textContent = "";
    for (const r of rows) {
      const tr = document.createElement("tr");
      const cells = [r.key, usd(r.spent_micro_usd), r.input_tokens, r.output_tokens,
        r.cache_read_tokens, r.cache_write_5m_tokens, r.cache_write_1h_tokens];
      for (const c of cells) {
        const td = document.createElement("td");
        td.textContent = String(c);
        tr.appendChild(td);
      }
      if (onRowClick) {
        tr.classList.add("clickable");
        tr.addEventListener("click", () => onRowClick(r.key));
      }
      tbody.appendChild(tr);
    }
  }

  function loadTeams() {
    setError("");
    query(Object.assign({ group_by: "team" }, range()))
      .then((res) => {
        $("degraded-banner").hidden = !res.degraded;
        fillRows($("teams").querySelector("tbody"), res.rows || [], loadModels);
        $("teams-section").hidden = false;
        $("models-section").hidden = true;
      })
      .catch((e) => setError(e.message));
  }

  function loadModels(team) {
    setError("");
    query(Object.assign({ group_by: "model", team: team }, range()))
      .then((res) => {
        $("degraded-banner").hidden = !res.degraded;
        $("model-team-label").textContent = "— " + team;
        fillRows($("models").querySelector("tbody"), res.rows || [], null);
        $("models-section").hidden = false;
      })
      .catch((e) => setError(e.message));
  }

  $("connect").addEventListener("click", () => {
    token = $("token").value;
    $("token").value = ""; // wipe the input; the closure holds the only copy
    $("auth-section").hidden = true;
    $("controls").hidden = false;
    loadTeams();
  });
  $("refresh").addEventListener("click", loadTeams);
})();
