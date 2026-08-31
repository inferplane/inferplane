// inferplane usage console. The bearer token lives in this closure variable
// ONLY — no persistent browser storage of any kind, EXCEPT the three
// short-lived PKCE values ip_sso_verifier/ip_sso_state/ip_sso_nonce; all
// three are cleared as soon as the SSO round trip completes or fails (ADR-037
// console SSO, ported from mayu's adminui/ADR-026 — a plain JS variable does
// not survive the full-page navigation to the IdP and back). The id_token
// itself is NEVER persisted anywhere. CSP forbids inline handlers, so
// everything wires up via addEventListener.
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

  function unlock(tok) {
    token = tok;
    $("auth-section").hidden = true;
    $("controls").hidden = false;
    $("nav").hidden = false;
    loadTeams();
  }

  $("connect").addEventListener("click", () => {
    const tok = $("token").value;
    $("token").value = ""; // wipe the input; the closure holds the only copy
    unlock(tok);
  });
  $("refresh").addEventListener("click", loadTeams);

  /* ---------- Policies tab ---------- */

  // api is the shared fetch wrapper for the policy endpoints: it surfaces the
  // server's own {"error":…} text (those messages are sanitized and secret-free)
  // and returns null for 204. Mirrors mayu's adminui api() helper.
  async function api(method, path, body) {
    const resp = await fetch(path, {
      method,
      headers: Object.assign(
        token ? { Authorization: "Bearer " + token } : {},
        body ? { "Content-Type": "application/json" } : {}
      ),
      body: body ? JSON.stringify(body) : undefined,
    });
    if (resp.status === 401) throw new Error("unauthorized — check the token");
    if (resp.status === 204) return null;
    if (!resp.ok) {
      let msg = "request failed (HTTP " + resp.status + ")";
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch { /* keep generic */ }
      throw new Error(msg);
    }
    return resp.json();
  }

  let policiesLoaded = false;

  function showView(which) {
    setError("");
    $("usage-view").hidden = which !== "usage";
    $("policies-view").hidden = which !== "policies";
    if (which === "policies" && !policiesLoaded) {
      policiesLoaded = true;
      loadPolicies();
    }
  }
  $("nav-usage").addEventListener("click", () => showView("usage"));
  $("nav-policies").addEventListener("click", () => showView("policies"));

  function loadPolicies() {
    return api("GET", "/v1alpha1/policies")
      .then((res) => {
        const list = $("policies-list");
        list.textContent = "";
        $("policies-readonly").hidden = !!res.writable;
        for (const view of res.policies || []) {
          list.appendChild(policyCard(view, res.writable));
        }
      })
      .catch((e) => setError(e.message));
  }

  // firstRule returns the first rule object carrying the given kind field
  // ("budget" | "rate" | "modelAccess"), or null. Rules are a list and a rule
  // may be named anything, so the KIND field — not the name — identifies it.
  function firstRule(doc, kind) {
    for (const r of (doc.spec.rules || [])) if (r[kind]) return r;
    return null;
  }

  // applyRule edits one rule kind in place: unchecked removes the rule
  // entirely, checked updates the existing rule or appends a new one named
  // defaultName. Every other rule in the document is left untouched. build
  // receives the existing rule body (or null) so a field the form does not
  // render can be carried through.
  function applyRule(doc, kind, enabled, defaultName, build, failurePolicy) {
    const existing = firstRule(doc, kind);
    if (!enabled) {
      if (existing) doc.spec.rules = doc.spec.rules.filter((r) => r !== existing);
      return;
    }
    if (existing) {
      existing[kind] = build(existing[kind]);
      if (failurePolicy) existing.failurePolicy = failurePolicy;
      return;
    }
    const rule = { name: defaultName, failurePolicy: failurePolicy || "FailOpen" };
    rule[kind] = build(null);
    doc.spec.rules.push(rule);
  }

  // saveCard mutates a structuredClone of the FETCHED document and PUTs the
  // whole thing — never a document rebuilt from the inputs. A policy may carry
  // rules this form does not render (e.g. routing) and any future field; the
  // clone-and-mutate discipline is what lets them survive the round trip.
  function saveCard(view, refs) {
    if (refs.modelsOn.checked) {
      const parsed = refs.allow.value.split(",").map((s) => s.trim()).filter((s) => s !== "");
      if (parsed.length === 0) {
        setError("model access allow list cannot be empty — use * to allow every model");
        return;
      }
    }
    const doc = structuredClone(view.policy);
    doc.spec.rules = doc.spec.rules || [];
    // The form does not render period, and build() replaces the whole budget
    // object — so carry the fetched rule's period through explicitly or a
    // console save silently turns a CalendarDay cap into a CalendarMonth one.
    applyRule(doc, "budget", refs.budgetOn.checked, "budget", (prev) => ({
      ...(prev && prev.period ? { period: prev.period } : {}),
      limitMilliUSD: Number(refs.limit.value),
      hardCap: refs.hardCap.checked,
      lease: { grantMilliUSD: Number(refs.grant.value), renewInterval: refs.renew.value },
      adminContact: refs.contact.value,
    }), refs.hardCap.checked ? "FailClosed" : "FailOpen");
    applyRule(doc, "rate", refs.rateOn.checked, "throughput", () => ({
      rpm: Number(refs.rpm.value), tpm: Number(refs.tpm.value),
    }), "FailOpen");
    applyRule(doc, "modelAccess", refs.modelsOn.checked, "models", () => ({
      allow: refs.allow.value.split(",").map((s) => s.trim()).filter((s) => s !== ""),
    }), "FailOpen");
    return api("PUT", "/v1alpha1/policies/" + encodeURIComponent(view.name), doc)
      .then(loadPolicies)
      .catch((e) => setError(e.message));
  }

  function policyCard(view, writable) {
    const doc = view.policy;
    const card = document.createElement("section");
    card.className = "card";

    const title = document.createElement("h3");
    title.textContent = view.name;
    card.appendChild(title);

    const subject = (doc.spec && doc.spec.subject) || {};
    const hint = document.createElement("p");
    hint.className = "hint";
    let hintText = "team: " + (subject.team || "—") + " · user: " + (subject.user || "—");
    if (view.updated_at) hintText += " · updated " + view.updated_at;
    hint.textContent = hintText;
    card.appendChild(hint);

    const inputs = [];
    const field = (parent, labelText, type, value, placeholder) => {
      const label = document.createElement("label");
      label.textContent = labelText;
      const input = document.createElement("input");
      input.type = type;
      if (placeholder) input.placeholder = placeholder;
      if (type === "checkbox") input.checked = !!value;
      else input.value = value == null ? "" : String(value);
      label.appendChild(input);
      parent.appendChild(label);
      inputs.push(input);
      return input;
    };
    const group = (titleText, exists) => {
      const div = document.createElement("div");
      div.className = "field-group";
      const on = field(div, titleText, "checkbox", exists);
      card.appendChild(div);
      return { div, on };
    };

    const budgetRule = firstRule(doc, "budget");
    const b = (budgetRule && budgetRule.budget) || {};
    const bLease = b.lease || {};
    const budgetGroup = group("Budget", !!budgetRule);
    const limit = field(budgetGroup.div, "limitMilliUSD", "number", b.limitMilliUSD);
    const hardCap = field(budgetGroup.div, "hardCap", "checkbox", b.hardCap);
    const grant = field(budgetGroup.div, "lease.grantMilliUSD", "number", bLease.grantMilliUSD);
    const renew = field(budgetGroup.div, "lease.renewInterval", "text", bLease.renewInterval, "10s");
    const contact = field(budgetGroup.div, "adminContact", "text", b.adminContact);

    const rateRule = firstRule(doc, "rate");
    const rt = (rateRule && rateRule.rate) || {};
    const rateGroup = group("Rate", !!rateRule);
    const rpm = field(rateGroup.div, "rpm", "number", rt.rpm);
    const tpm = field(rateGroup.div, "tpm", "number", rt.tpm);

    const modelsRule = firstRule(doc, "modelAccess");
    const ma = (modelsRule && modelsRule.modelAccess) || {};
    const modelsGroup = group("Model access", !!modelsRule);
    const allow = field(modelsGroup.div, "allow (comma-separated)", "text",
      (ma.allow || []).join(", "), "*");

    if (!writable) {
      for (const input of inputs) input.disabled = true;
      return card;
    }

    const refs = {
      budgetOn: budgetGroup.on, limit: limit, hardCap: hardCap, grant: grant,
      renew: renew, contact: contact,
      rateOn: rateGroup.on, rpm: rpm, tpm: tpm,
      modelsOn: modelsGroup.on, allow: allow,
    };

    const save = document.createElement("button");
    save.textContent = "Save";
    save.addEventListener("click", () => {
      setError("");
      saveCard(view, refs);
    });
    card.appendChild(save);

    const del = document.createElement("button");
    del.textContent = "Delete";
    del.addEventListener("click", () => {
      if (!confirm('Delete policy "' + view.name + '"? Its rules stop being enforced.')) return;
      setError("");
      api("DELETE", "/v1alpha1/policies/" + encodeURIComponent(view.name))
        .then(loadPolicies)
        .catch((e) => setError(e.message));
    });
    card.appendChild(del);

    return card;
  }

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

      unlock(tokens.id_token);
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
