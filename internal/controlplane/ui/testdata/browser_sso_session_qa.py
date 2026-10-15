"""Browser SSO lifecycle QA with synthetic tokens and intercepted IdP/API traffic."""
import argparse
import base64
import importlib.util
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlparse

from playwright.sync_api import sync_playwright

STATIC = Path(__file__).resolve().parent.parent / "static"
spec = importlib.util.spec_from_file_location("synthetic_dashboard", Path(__file__).with_name("browser_qa.py"))
qa = importlib.util.module_from_spec(spec)
spec.loader.exec_module(qa)


class Assets(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        name = urlparse(self.path).path.removeprefix("/ui/") or "index.html"
        if name not in ("index.html", "app.js", "style.css"):
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", {"index.html": "text/html", "app.js": "text/javascript", "style.css": "text/css"}[name])
        self.send_header("Content-Security-Policy", "default-src 'self'; connect-src 'self' https://fixture-idp.invalid; frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write((STATIC / name).read_bytes())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--screenshots", type=Path)
    args = parser.parse_args()
    if args.screenshots:
        args.screenshots.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(("127.0.0.1", 0), Assets)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_port}"
    state = {"authorizations": 0, "renewals": 0, "cookie_valid": True, "tokens": set()}
    errors = []
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 1000})
        page.on("pageerror", lambda error: errors.append(str(error)))
        page.on("console", lambda message: errors.append(message.text) if "Content Security Policy" in message.text else None)

        def route(request):
            url = urlparse(request.request.url)
            if url.path == "/ui/auth/config":
                request.fulfill(status=200, json={"sso": True, "issuer": "https://fixture-idp.invalid", "client_id": "fixture-client"})
            elif url.hostname == "fixture-idp.invalid":
                if url.path == "/.well-known/openid-configuration":
                    request.fulfill(status=200, json={"authorization_endpoint": "https://fixture-idp.invalid/auth", "token_endpoint": "https://fixture-idp.invalid/token"})
                elif url.path == "/auth":
                    state["authorizations"] += 1
                    query = parse_qs(url.query)
                    returned = {"state": query["state"][0]}
                    returned.update({"code": "synthetic-code"} if state["cookie_valid"] else {"error": "login_required"})
                    request.fulfill(status=302, headers={"Location": base + "/ui/?" + urlencode(returned)})
                elif url.path == "/token":
                    grant = parse_qs(request.request.post_data)
                    refresh = grant["grant_type"] == ["refresh_token"]
                    if refresh:
                        state["renewals"] += 1
                    nonce = page.evaluate("sessionStorage.getItem('ip_sso_nonce')") if not refresh else "renewed"
                    claims = {"iss": "https://fixture-idp.invalid", "aud": "fixture-client", "sub": "synthetic-operator",
                              "nonce": nonce, "token_use": "id", "exp": int(time.time()) + (3600 if refresh else 65)}
                    encoded = base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip("=")
                    token = "synthetic." + encoded + ".signature"
                    state["tokens"].add(token)
                    request.fulfill(status=200, json={"id_token": token, "refresh_token": "synthetic-refresh-" + str(state["renewals"])})
                else:
                    request.abort()
            elif url.path.startswith("/v1alpha1/"):
                token = request.request.headers.get("authorization", "").removeprefix("Bearer ")
                assert token in state["tokens"], "An unrecognized credential reached synthetic protected APIs"
                if url.path == "/v1alpha1/policies":
                    request.fulfill(status=200, json={"generation": "synthetic-generation", "writable": False, "policies": [{"name": qa.POLICY["metadata"]["name"], "policy": qa.POLICY}]})
                elif url.path == "/v1alpha1/dataplanes":
                    request.fulfill(status=200, json={"generation": "synthetic-generation", "dataplanes": {}, "apiVersions": ["inferplane.dev/v1alpha1"]})
                elif url.path == "/v1alpha1/usage/charts":
                    request.fulfill(status=200, json={"total_micro_usd": 157685945, "other_micro_usd": 0, "model_count": 1,
                                                   "models": [{"key": "synthetic-model", "spent_micro_usd": 157685945}],
                                                   "days": [{"day": qa.NOW[:10], "spent_micro_usd": 157685945}]})
                else:
                    rows = qa.USER_ROWS if parse_qs(url.query).get("group_by") == ["user"] else qa.ROWS
                    request.fulfill(status=200, json={"total_micro_usd": sum(r["spent_micro_usd"] for r in rows), "rows": rows, "degraded": False})
            elif url.netloc == urlparse(base).netloc:
                assert "authorization" not in request.request.headers
                request.continue_()
            else:
                request.abort()

        page.route("**/*", route)
        page.goto(base + "/ui/")
        page.locator("#sso-button").click()
        page.locator("#workspace-content").wait_for(state="visible")
        assert state["authorizations"] == 1
        storage = page.evaluate("JSON.stringify({...sessionStorage})")
        assert "synthetic-refresh" not in storage and "signature" not in storage and "synthetic-operator" not in storage
        assert page.evaluate("localStorage.length") == 0
        page.reload()
        page.locator("#workspace-content").wait_for(state="visible")
        assert state["authorizations"] == 2, "Reload did not perform PKCE reauthentication"
        page.wait_for_function("() => document.getElementById('workspace-content').hidden === false")
        deadline = time.monotonic() + 15
        while state["renewals"] < 1 and time.monotonic() < deadline:
            page.wait_for_timeout(100)
        assert state["renewals"] == 1, "Expiring token was not renewed"
        assert page.locator("#workspace-content").is_visible()
        if args.screenshots:
            page.screenshot(path=str(args.screenshots / "resumed-and-renewed.png"), full_page=True)
        page.locator("#disconnect").click()
        page.reload()
        page.locator("#sso-button").wait_for(state="visible")
        assert state["authorizations"] == 2, "Logout was undone by automatic reauthentication"
        assert page.evaluate("sessionStorage.length") == 0
        page.locator("#sso-button").click()
        page.locator("#workspace-content").wait_for(state="visible")
        state["cookie_valid"] = False
        page.reload()
        page.locator("#auth-section").wait_for(state="visible")
        page.wait_for_timeout(500)
        assert not page.evaluate("sessionStorage.getItem('ip_sso_session')")
        count = state["authorizations"]
        page.reload()
        page.wait_for_timeout(500)
        assert state["authorizations"] == count, "Expired IdP session caused a redirect loop"
        assert not errors, errors
        browser.close()
    server.shutdown()
    print("Browser SSO QA passed: PKCE sign-in, reload restore, renewal, token-free storage, logout and expired-cookie loop prevention.")


if __name__ == "__main__":
    main()
