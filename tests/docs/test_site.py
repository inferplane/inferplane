import importlib.util
import hashlib
from pathlib import Path
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


hooks = load("docs_hooks")
checker = load("check_docs_site")
translations = load("check_docs_translations")


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

    def test_translation_requires_companion_and_tracks_source_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            docs = Path(temp)
            source = "# Guide\n\n```bash\nmayu --help\n```\n"
            original = docs / "guide.md"
            original.write_text(source, encoding="utf-8")
            errors, count = translations.check_translations(docs)
            self.assertEqual(count, 1)
            self.assertIn("missing Korean companion", errors[0])
            companion = (
                "---\ntranslation_source: guide.md\ntranslation_source_sha256: "
                + hashlib.sha256(source.encode()).hexdigest()
                + "\n---\n\n# 가이드 {#guide}\n\n```bash\nmayu --help\n```\n"
            )
            (docs / "guide.ko.md").write_text(companion, encoding="utf-8")
            self.assertEqual(translations.check_translations(docs), ([], 1))
            original.write_text(source + "\nNew operating condition.\n", encoding="utf-8")
            errors, _ = translations.check_translations(docs)
            self.assertTrue(any("English source changed" in error for error in errors))

    def test_translated_commands_must_match_and_archives_are_exempt(self):
        with tempfile.TemporaryDirectory() as temp:
            docs = Path(temp)
            (docs / "decisions").mkdir()
            (docs / "decisions/ADR-001.md").write_text("# Original record\n")
            self.assertEqual(translations.check_translations(docs), ([], 0))
            original = "# Guide\n\n````bash\nmayu serve\n````\n"
            (docs / "guide.md").write_text(original, encoding="utf-8")
            translated = (
                "---\ntranslation_source: guide.md\ntranslation_source_sha256: "
                + hashlib.sha256(original.encode()).hexdigest()
                + "\n---\n\n# 가이드\n\n````bash\nmayu reset\n````\n"
            )
            (docs / "guide.ko.md").write_text(translated, encoding="utf-8")
            errors, _ = translations.check_translations(docs)
            self.assertTrue(any("code examples differ" in error for error in errors))

    def test_bilingual_adr_indexes_are_current(self):
        self.assertEqual(translations.check_indexes(ROOT / "docs"), [])


if __name__ == "__main__":
    unittest.main()
