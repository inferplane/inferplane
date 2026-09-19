"""Check rendered links/assets, including project-base paths and fragments."""

from html.parser import HTMLParser
from pathlib import Path
import sys
from urllib.parse import unquote, urlsplit


class Document(HTMLParser):
    def __init__(self, content):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.links = []
        self.feed(content)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if "id" in attrs:
            self.ids.add(attrs["id"])
        if tag == "a" and "name" in attrs:
            self.ids.add(attrs["name"])
        for attr in ("href", "src"):
            if attr in attrs:
                self.links.append(attrs[attr])


def check_site(root, base="/inferplane/"):
    root = root.resolve()
    documents = {
        path: Document(path.read_text(encoding="utf-8"))
        for path in root.rglob("*.html")
    }
    errors = []
    for source, doc in documents.items():
        for link in doc.links:
            url = urlsplit(link)
            if url.scheme or url.netloc:
                continue
            path = unquote(url.path)
            if path.startswith("/"):
                if not path.startswith(base):
                    errors.append(f"{source.relative_to(root)}: outside site base: {link}")
                    continue
                target = root / path.removeprefix(base)
            else:
                target = source.parent / path if path else source
            target = target.resolve()
            if target.is_dir():
                target /= "index.html"
            if not target.is_relative_to(root) or not target.is_file():
                errors.append(f"{source.relative_to(root)}: missing target: {link}")
            elif url.fragment and target in documents:
                fragment = unquote(url.fragment)
                if fragment not in documents[target].ids:
                    errors.append(f"{source.relative_to(root)}: missing anchor: {link}")
    return errors, len(documents)


if __name__ == "__main__":
    errors, count = check_site(Path(sys.argv[1] if len(sys.argv) > 1 else "site"))
    if not count:
        errors.append("No built HTML pages found")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        sys.exit(1)
    print(f"Validated links and assets in {count} HTML pages.")
