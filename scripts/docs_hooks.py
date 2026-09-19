"""Keep repository links useful on Pages without copying source into the site."""

from pathlib import Path
import re
from urllib.parse import quote, unquote, urlsplit

from mkdocs.structure.files import File


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


def on_files(files, config):
    root = Path(config["docs_dir"])
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
        title = decision.read_text().splitlines()[0].lstrip("# ")
        lines.append(f"- [{title}]({decision.name})")
    files.append(File.generated(config, "decisions/index.md", content="\n".join(lines)))
    return files


def on_page_markdown(markdown, page, config, files):
    root = Path(config["docs_dir"]).parent.resolve()
    source = Path(config["docs_dir"]) / page.file.src_uri
    published = {
        (Path(config["docs_dir"]) / file.src_uri).resolve()
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
