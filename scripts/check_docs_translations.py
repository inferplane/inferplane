"""Require reviewed Korean companions for published product documentation."""

import hashlib
from pathlib import Path
import re
import sys

from docs_hooks import adr_indexes


ARCHIVES = {"decisions", "specs", "superpowers", "overrides"}
EXCLUDED = {"customer-issue-analysis.md"}
FENCES = re.compile(r"(?ms)^([ \t]*)(`{3,}|~{3,})[^\n]*\n.*?^\1\2[ \t]*(?:\n|$)")


def product_sources(docs):
    for path in sorted(docs.rglob("*.md")):
        relative = path.relative_to(docs)
        if (
            relative.parts[0] in ARCHIVES
            or relative.as_posix() in EXCLUDED
            or any(part.startswith(".") for part in relative.parts)
            or path.name.endswith(".ko.md")
        ):
            continue
        yield path


def metadata(text):
    match = re.match(r"\A---\n(.*?)\n---(?:\n|$)", text, re.S)
    if not match:
        return {}
    return dict(re.findall(r"(?m)^(translation_source(?:_sha256)?):\s*(\S+)\s*$", match[1]))


def check_translations(docs):
    errors = []
    count = 0
    for original in product_sources(docs):
        count += 1
        relative = original.relative_to(docs).as_posix()
        translated = original.with_suffix(".ko.md")
        if not translated.is_file():
            errors.append(f"{relative}: missing Korean companion")
            continue
        source = original.read_text(encoding="utf-8")
        text = translated.read_text(encoding="utf-8")
        info = metadata(text)
        if info.get("translation_source") != relative:
            errors.append(f"{translated.name}: incorrect translation_source")
        expected = hashlib.sha256(original.read_bytes()).hexdigest()
        if info.get("translation_source_sha256") != expected:
            errors.append(f"{translated.name}: English source changed; review/update the translation")
        if not re.search("[가-힣]", text):
            errors.append(f"{translated.name}: no Korean content")
        source_examples = [m.group().strip() for m in FENCES.finditer(source)]
        translated_examples = [m.group().strip() for m in FENCES.finditer(text)]
        if source_examples != translated_examples:
            errors.append(f"{translated.name}: code examples differ from the English source")
    return errors, count


def check_indexes(docs):
    return [
        f"{name}: run python scripts/docs_hooks.py to refresh the ADR index"
        for name, content in adr_indexes(docs).items()
        if not (docs / name).is_file()
        or (docs / name).read_text(encoding="utf-8") != content
    ]


if __name__ == "__main__":
    root = Path(__file__).resolve().parents[1] / "docs"
    errors, count = check_translations(root)
    errors.extend(check_indexes(root))
    if not count:
        errors.append("No product documentation found")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        sys.exit(1)
    print(f"Validated {count} Korean/English document pairs and both ADR indexes.")
