"""Keep repository links useful on Pages without copying source into the site."""

from pathlib import Path
import re
from urllib.parse import quote, unquote, urlsplit


def repository_link(target, source, root, published):
    """Rewrite existing repository-only destinations; leave typos for MkDocs."""
    parsed = urlsplit(target)
    if parsed.scheme or parsed.netloc or not parsed.path or target.startswith("/"):
        return target
    resolved = (source.parent / unquote(parsed.path)).resolve()
    if not resolved.is_relative_to(root) or not resolved.exists():
        return target
    if resolved in published:
        return target
    if resolved.is_dir():
        for name in ("index.md", "INDEX.md"):
            if resolved / name in published:
                return target.rstrip("/") + "/" + name + (
                    "#" + parsed.fragment if parsed.fragment else ""
                )
    kind = "tree" if resolved.is_dir() else "blob"
    path = quote(resolved.relative_to(root).as_posix(), safe="/")
    suffix = ("?" + parsed.query if parsed.query else "") + (
        "#" + parsed.fragment if parsed.fragment else ""
    )
    return f"https://github.com/inferplane/inferplane/{kind}/main/{path}{suffix}"


def adr_indexes(root):
    """Materialized indexes also work with plugins that require real source files."""
    root = Path(root)
    decisions = sorted((root / "decisions").glob("ADR-*.md"))
    lines = [
        "# Architecture decisions",
        "",
        "Design history and implementation contracts. Read each record's status; "
        "an accepted design alone does not establish production qualification.",
        "",
        "Start with the [deployment profiles](../getting-started/deployment-profiles.md) "
        "for current operating behavior.",
        "",
    ]
    for decision in decisions:
        title = decision.read_text(encoding="utf-8").splitlines()[0].lstrip("# ")
        lines.append(f"- [{title}]({decision.name})")
    korean = [
        "# 설계 결정 기록",
        "",
        "설계 이력과 구현 계약입니다. 각 기록의 상태를 확인하세요. 설계가 승인되었다고 "
        "운영 환경 검증까지 완료된 것은 아닙니다.",
        "",
        "현재 동작은 [배포 프로파일](../getting-started/deployment-profiles.md)에서 확인하세요. "
        "아래 ADR은 역사적 맥락을 보존하는 **영어 원문**입니다.",
        "",
    ]
    korean.extend(lines[6:])
    return {
        "decisions/index.md": "\n".join(lines) + "\n",
        "decisions/index.ko.md": "\n".join(korean) + "\n",
    }


def on_page_markdown(markdown, page, config, files):
    root = Path(config["docs_dir"]).parent.resolve()
    source = Path(config["docs_dir"]) / page.file.src_uri
    published = {
        (Path(config["docs_dir"]) / getattr(file, "norm_src_uri", file.src_uri)).resolve()
        for file in files
        if file.inclusion.is_included()
    }
    # Split out fenced/inline code: literal examples are never link-rewritten.
    chunks = re.split(r"(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]+`)", markdown)
    for index in range(0, len(chunks), 2):
        chunks[index] = re.sub(
            r"(\[[^\]\n]*\]\()([^\s)]+)(\))",
            lambda match: match[1]
            + repository_link(match[2], source, root, published)
            + match[3],
            chunks[index],
        )
    return "".join(chunks)


if __name__ == "__main__":
    docs = Path(__file__).resolve().parents[1] / "docs"
    for path, content in adr_indexes(docs).items():
        (docs / path).write_text(content, encoding="utf-8")
