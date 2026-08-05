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
    loadTeams();
  }

  $("connect").addEventListener("click", () => {
    const tok = $("token").value;
    $("token").value = ""; // wipe the input; the closure holds the only copy
    unlock(tok);
  });
  $("refresh").addEventListener("click", loadTeams);

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
