"use strict";

// Same-origin admin bridge; a separate, page-memory virtual key is mandatory.
// No endpoint URLs, credentials, prompts or output are stored or exported.
let evaluationRows = [];
let evaluationController = null;
let evaluationGeneration = 0;

async function refreshEvaluation() {
  $("evaluation-form").hidden = !whoamiIsAdmin;
  $("eval-admin-only").hidden = whoamiIsAdmin;
  if (!whoamiIsAdmin) return;
  if (!evaluationController) {
    const topology = await api("GET", "/admin/config");
    for (const id of ["eval-model-a", "eval-model-b"]) {
      const select = $(id), previous = select.value;
      select.textContent = "";
      for (const model of topology.models || []) {
        const option = document.createElement("option");
        option.value = model.name; option.textContent = model.name;
        select.appendChild(option);
      }
      if ([...select.options].some(option => option.value === previous)) select.value = previous;
    }
  }
  renderEvaluation();
}

function evaluationBody(protocol, model, prompt, maxTokens) {
  switch (protocol) {
    case "bedrock-converse": return { messages: [{ role: "user", content: [{ text: prompt }] }], inferenceConfig: { maxTokens } };
    case "anthropic": return { model, max_tokens: maxTokens, messages: [{ role: "user", content: prompt }] };
    case "responses": return { model, max_output_tokens: maxTokens, input: prompt };
    case "openai": return { model, max_completion_tokens: maxTokens, messages: [{ role: "user", content: prompt }] };
    default: throw Error("Unsupported protocol");
  }
}

function evaluationCost(result) {
  if (!result.cost || result.cost.pricing_missing) return msg("eval.unknown");
  return usd(result.cost.amount_usd_micros) + (result.accounting_known ? "" : " · " + msg("eval.estimate"));
}

function renderEvaluation() {
  const tbody = $("eval-results-table").querySelector("tbody");
  tbody.textContent = ""; $("eval-outputs").textContent = ""; $("eval-summary").textContent = "";
  for (const row of evaluationRows) {
    const r = row.result, tr = document.createElement("tr");
    const rate = r.usage && r.usage.evidence && r.usage.evidence.output_known && r.duration_ms > 0 ?
      (Number(r.usage.output_tokens) / (r.duration_ms / 1000)).toFixed(2) : "—";
    for (const value of [String(row.sequence), row.side + " · " + row.model, row.protocol,
      r.http_status == null ? "—" : String(r.http_status), msg("eval.outcome." + r.outcome),
      r.duration_ms == null ? "—" : r.duration_ms.toFixed(1), rate, evaluationCost(r)]) tr.appendChild(td(value));
    tbody.appendChild(tr);
    const details = document.createElement("details"), summary = document.createElement("summary"), pre = document.createElement("pre");
    summary.textContent = "#" + row.sequence + " · " + row.side + " · " + row.model;
    pre.textContent = exactJSON({
      stop_reason: r.stop_reason || null, usage: r.usage || null, cost: r.cost || null,
      accounting_known: !!r.accounting_known, selected_model: r.selected_model || null,
      fallback: r.fallback || null, attempts: r.attempts, unsettled_attempts: r.unsettled_attempts, output: r.output || "",
    }, 2);
    details.append(summary, pre); $("eval-outputs").appendChild(details);
  }
  for (const side of ["A", "B"]) {
    const rows = evaluationRows.filter(row => row.side === side);
    if (!rows.length) continue;
    const card = document.createElement("div"); card.className = "card";
    const title = document.createElement("h3"); title.textContent = side + " · " + rows[0].model; card.appendChild(title);
    const httpErrors = rows.filter(row => row.result.http_status == null || row.result.http_status < 200 || row.result.http_status >= 300).length;
    const responseErrors = rows.filter(row => !row.result.response_success).length;
    const known = rows.filter(row => row.result.accounting_known);
    const cost = rows.reduce((sum, row) => sum + BigInt(row.result.cost && !row.result.cost.pricing_missing ? row.result.cost.amount_usd_micros : 0), 0n);
    const values = [
      [msg("eval.httpErrors"), httpErrors + "/" + rows.length],
      [msg("eval.responseErrors"), responseErrors + "/" + rows.length],
      [msg("eval.knownCost"), known.length + "/" + rows.length],
      [msg("eval.reportedCost"), (rows.some(row => row.result.cost && !row.result.cost.pricing_missing) ? usd(cost) : "—") +
        (known.length === rows.length ? "" : " · " + msg("eval.incomplete"))],
    ];
    for (const [label, value] of values) {
      const line = document.createElement("p"); line.textContent = label + ": " + value; card.appendChild(line);
    }
    $("eval-summary").appendChild(card);
  }
  $("eval-export").disabled = !evaluationRows.length || !!evaluationController;
}

$("evaluation-form").addEventListener("submit", async event => {
  event.preventDefault();
  if (evaluationController || !whoamiIsAdmin) return;
  const key = $("eval-key").value.trim(), prompt = $("eval-prompt").value;
  const maxTokens = Number($("eval-max-tokens").value), repeats = Number($("eval-repeats").value);
  if (!key || !prompt || !Number.isInteger(maxTokens) || maxTokens < 16 || maxTokens > 4096 ||
    !Number.isInteger(repeats) || repeats < 1 || repeats > 5) return;
  const targets = ["a", "b"].map(side => ({ side: side.toUpperCase(), model: $("eval-model-" + side).value, protocol: $("eval-protocol-" + side).value }));
  if (targets.some(target => !target.model)) return;
  const generation = ++evaluationGeneration;
  evaluationController = new AbortController();
  const controller = evaluationController;
  evaluationRows = []; $("eval-error").hidden = true;
  $("eval-key").value = ""; // captured only in this active request closure
  for (const field of $("evaluation-form").elements) field.disabled = field.id !== "eval-cancel";
  renderEvaluation();
  let stop = false;
  try {
    for (let round = 0; round < repeats && !stop; round++) {
      const order = round % 2 ? [...targets].reverse() : targets;
      for (const target of order) {
        if (controller.signal.aborted || generation !== evaluationGeneration) { stop = true; break; }
        $("eval-status").textContent = msg("eval.running") + " " + (evaluationRows.length + 1) + "/" + (repeats * 2);
        let result;
        try {
          const response = await fetch("/admin/evaluations", {
            method: "POST", headers: { Authorization: "Bearer " + adminToken, "X-Inferplane-Virtual-Key": key, "Content-Type": "application/json" },
            body: exactJSON({ protocol: target.protocol, model: target.model, body: evaluationBody(target.protocol, target.model, prompt, maxTokens) }),
            cache: "no-store", credentials: "omit", redirect: "error", signal: controller.signal,
          });
          if (!response.ok) {
            result = { http_status: response.status, response_success: false, outcome: "http_error", accounting_known: false };
            // Admin/session/unsupported bridge failures cannot produce a useful paired trial.
            stop = true;
          } else {
            result = parseExactJSON(await response.text());
            if ([401, 403, 429, 503].includes(result.http_status)) stop = true;
          }
        } catch (err) {
          result = { http_status: null, response_success: false, outcome: controller.signal.aborted ? "cancelled" : "network_error", accounting_known: false };
          stop = true;
        }
        evaluationRows.push({ sequence: evaluationRows.length + 1, round: round + 1, ...target, result });
        renderEvaluation();
        if (stop) break;
      }
    }
  } finally {
    evaluationController = null;
    for (const field of $("evaluation-form").elements) field.disabled = false;
    $("eval-cancel").disabled = true;
    $("eval-status").textContent = (stop ? msg("eval.stopped") : msg("eval.done")) + " · " + evaluationRows.length;
    renderEvaluation();
  }
});

$("eval-cancel").addEventListener("click", () => { if (evaluationController) evaluationController.abort(); });
$("disconnect").addEventListener("click", () => { if (evaluationController) evaluationController.abort(); $("eval-key").value = ""; });
$("eval-export").addEventListener("click", () => {
  const rows = evaluationRows.map(row => {
    const { output, ...result } = row.result;
    return { ...row, result };
  });
  const blob = new Blob([exactJSON({ schema: 1, method: "sequential alternating AB, non-streaming E2E; no quality score or invoice reconciliation", rows }, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob), link = document.createElement("a");
  link.href = url; link.download = "inferplane-comparison.json"; link.click(); URL.revokeObjectURL(url);
});
