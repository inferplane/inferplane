import importlib.util
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


hooks = load("docs_hooks")
checker = load("check_docs_site")


class DocumentationTests(unittest.TestCase):
    def test_repository_file_and_fragment_rewrite(self):
        result = hooks.repository_link(
            "../../README.md#status",
            ROOT / "docs/getting-started/quickstart.md",
            ROOT,
            set(),
        )
        self.assertEqual(
            result, "https://github.com/inferplane/inferplane/blob/main/README.md#status"
        )

    def test_published_doc_and_missing_link_stay_for_validation(self):
        source = ROOT / "docs/index.md"
        published = {ROOT / "docs/architecture.md"}
        for target in ("architecture.md", "misspelled.md", "https://example.com/", "#part"):
            self.assertEqual(
                hooks.repository_link(target, source, ROOT, published), target
            )

    def test_directory_resolves_to_published_index(self):
        result = hooks.repository_link(
            "reference/", ROOT / "docs/index.md", ROOT, {ROOT / "docs/reference/INDEX.md"}
        )
        self.assertEqual(result, "reference/INDEX.md")

    def test_excluded_plan_links_to_repository(self):
        target = "specs/2026-06-10-inferplane-gateway-design.md"
        result = hooks.repository_link(target, ROOT / "docs/index.md", ROOT, set())
        self.assertIn("/blob/main/docs/specs/", result)

    def test_rendered_checker_validates_project_base_and_anchors(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "index.html").write_text(
                '<a href="/inferplane/guide/#part">guide</a><img src="mark.svg">'
            )
            (root / "mark.svg").write_text("<svg/>")
            (root / "guide").mkdir()
            (root / "guide/index.html").write_text('<h1 id="part">Guide</h1>')
            self.assertEqual(checker.check_site(root), ([], 2))
            (root / "guide/index.html").write_text('<a href="../missing/">broken</a>')
            errors, _ = checker.check_site(root)
            self.assertTrue(any("missing anchor" in error for error in errors))
            self.assertTrue(any("missing target" in error for error in errors))

    def test_checker_rejects_wrong_base_and_escape(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "index.html").write_text(
                '<a href="/guide/">wrong base</a><a href="../outside">escape</a>'
            )
            errors, _ = checker.check_site(root)
            self.assertEqual(len(errors), 2)


if __name__ == "__main__":
    unittest.main()
