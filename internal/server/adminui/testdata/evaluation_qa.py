"""Browser evaluation through real local AdminMux → DataMux → fake providers.
No AWS calls. Start testdata/evaluation-fixture first. Use a private ~/.tmp folder.
"""
import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--screenshots", required=True, type=Path)
    parser.add_argument("--chromium", required=True)
    args = parser.parse_args()
    args.screenshots.mkdir(parents=True, exist_ok=True)
    base = (args.fixture / "origin").read_text().strip()
    key = (args.fixture / "virtual-key").read_text().strip()
    checks = []
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 1100}, locale="ko-KR", color_scheme="light")
        errors, calls = [], []
        page.on("pageerror", lambda error: errors.append(str(error)))
        page.on("request", lambda req: calls.append(req.url) if req.url.endswith("/admin/evaluations") else None)
        page.goto(base + "/admin/ui/")
        page.locator("#token").fill("fixture-admin")
        page.locator("#token-form button").click()
        page.locator("#shell").wait_for(state="visible")
        page.locator('button.nav[data-view="evaluation"]').click()
        page.locator("#evaluation-form").wait_for(state="visible")
        page.locator("#eval-model-a option").first.wait_for(state="attached")

        def run(a, b, repeats=1, virtual_key=key):
            page.locator("#eval-model-a").select_option(a)
            page.locator("#eval-model-b").select_option(b)
            page.locator("#eval-key").fill(virtual_key)
            page.locator("#eval-prompt").fill("Compare this identical local fixture prompt.")
            page.locator("#eval-repeats").fill(str(repeats))
            page.locator("#eval-run").click()
            page.wait_for_function("() => evaluationController === null && evaluationRows.length > 0")
            return page.evaluate("JSON.parse(exactJSON(evaluationRows))")

        rows = run("fixture-ok", "fixture-refused", 2)
        assert [r["side"] for r in rows] == ["A", "B", "B", "A"]
        assert all(r["result"]["http_status"] == 200 for r in rows)
        assert [r["result"]["response_success"] for r in rows] == [True, False, False, True]
        assert all(r["result"]["accounting_known"] and r["result"]["cost"]["amount_usd_micros"] == 28 for r in rows)
        assert all(r["result"]["ttft_ms"] is None for r in rows)
        assert page.locator("#eval-key").input_value() == ""
        assert "응답 실패: 2/2" in page.locator("#eval-summary").inner_text()
        checks += ["real admin-to-data-plane execution", "AB/BA order", "HTTP 200 refusals counted separately",
                   "integer settlement 28 microUSD per fixture call", "TTFT explicitly unavailable", "virtual key cleared"]
        page.screenshot(path=str(args.screenshots / "comparison-known-and-refused.png"), full_page=True)
        with page.expect_download() as download:
            page.locator("#eval-export").click()
        export_path = args.fixture / "export.json"
        download.value.save_as(export_path)
        exported = export_path.read_text()
        assert key not in exported and "fixture-admin" not in exported
        assert "identical local fixture prompt" not in exported and "Local fixture response" not in exported
        assert len(json.loads(exported)["rows"]) == 4
        checks += ["export excludes credentials, prompts and answer text"]

        rows = run("fixture-uncertain", "fixture-unpriced")
        assert len(rows) == 2 and not any(r["result"]["accounting_known"] for r in rows)
        assert rows[0]["result"]["usage"]["evidence"]["accounting_uncertain"]
        assert rows[1]["result"]["cost"]["pricing_missing"]
        text = page.locator("#eval-results-table").inner_text()
        assert "추정" in text and "확인 불가" in text
        checks += ["uncertain usage labelled estimate", "missing price shown unknown"]
        page.screenshot(path=str(args.screenshots / "comparison-cost-uncertainty.png"), full_page=True)
        for width in (390, 768, 1440):
            page.set_viewport_size({"width": width, "height": 1100})
            assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), f"overflow {width}"
            page.screenshot(path=str(args.screenshots / f"comparison-ko-{width}.png"), full_page=True)
        page.locator("#theme-toggle").click()
        page.screenshot(path=str(args.screenshots / "comparison-dark.png"), full_page=True)
        checks += ["Korean layout at 390/768/1440 px", "dark theme"]

        before = len(calls)
        rows = run("fixture-ok", "fixture-refused", virtual_key="invalid-fixture")
        assert len(rows) == 1 and rows[0]["result"]["http_status"] == 401
        assert rows[0]["result"]["cost"] is None and len(calls) == before + 1
        checks += ["invalid virtual key stops pair; cost unknown"]
        page.locator("#eval-model-a").select_option("fixture-slow")
        page.locator("#eval-key").fill(key)
        page.locator("#eval-run").click()
        page.wait_for_function("() => evaluationController !== null")
        page.wait_for_timeout(150)
        page.locator("#eval-cancel").click()
        page.wait_for_function("() => evaluationController === null")
        rows = page.evaluate("JSON.parse(exactJSON(evaluationRows))")
        assert len(rows) == 1 and rows[0]["result"]["outcome"] == "cancelled"
        assert not rows[0]["result"]["accounting_known"]
        checks += ["cancellation stops pending requests and leaves cost unknown"]
        assert page.evaluate("Object.keys(localStorage).length") == 0
        assert key not in page.evaluate("JSON.stringify(sessionStorage)")
        checks += ["no persisted virtual key"]
        assert not errors, errors
        browser.close()
    (args.screenshots / "evaluation-checks.json").write_text(json.dumps({"passed": len(checks), "checks": checks}, ensure_ascii=False, indent=2))
    print(json.dumps({"passed": len(checks), "browser_errors": errors}, ensure_ascii=False))


if __name__ == "__main__":
    main()
