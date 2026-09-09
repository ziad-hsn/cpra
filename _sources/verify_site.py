"""Validate generated documentation links, page metadata and search coverage."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit
import json
import sys

BASE = "https://ziad-hsn.github.io/cpra/"


class Page(HTMLParser):
    def __init__(self, html):
        super().__init__(convert_charrefs=True)
        self.ids = set()
        self.links = []
        self.meta = {}
        self.canonical = None
        self.h1 = 0
        self.has_title = False
        self.feed(html)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get("id"):
            self.ids.add(attrs["id"])
        if tag == "a" and "name" in attrs:
            self.ids.add(attrs["name"])
        if tag in ("a", "link") and attrs.get("href"):
            self.links.append(attrs["href"])
        if tag in ("script", "img", "iframe") and attrs.get("src"):
            self.links.append(attrs["src"])
        if tag == "meta":
            key = attrs.get("name", attrs.get("http-equiv", "")).lower()
            self.meta[key] = attrs.get("content", "")
        if tag == "link" and attrs.get("rel") == "canonical":
            self.canonical = attrs.get("href")
        self.h1 += tag == "h1"
        self.has_title |= tag == "title"


def verify(root):
    root = Path(root).resolve()
    paths = sorted(
        p for p in root.rglob("*.html")
        if not any(part.startswith(("_", ".")) for part in p.relative_to(root).parts)
    )
    pages = {p: Page(p.read_text()) for p in paths}
    errors = []
    links = 0
    content_pages = 0
    redirects = 0
    for path, page in pages.items():
        rel = path.relative_to(root).as_posix()
        if "refresh" in page.meta:
            redirects += 1
        elif rel != "404.html":
            content_pages += 1
            expected = BASE + rel.removesuffix("index.html")
            if page.canonical != expected:
                errors.append(f"{rel}: canonical {page.canonical!r}, expected {expected!r}")
            if not page.has_title or not page.meta.get("description") or page.h1 != 1:
                errors.append(f"{rel}: missing title/description or incorrect H1 count")
        for href in page.links:
            target = urlsplit(urljoin(BASE + rel, href))
            if target.netloc != urlsplit(BASE).netloc or target.scheme not in ("http", "https"):
                continue
            if not target.path.startswith("/cpra/"):
                errors.append(f"{rel}: project prefix missing in {href}")
                continue
            local = root / unquote(target.path.removeprefix("/cpra/"))
            if local.is_dir():
                local /= "index.html"
            local = local.resolve()
            if not local.is_relative_to(root) or not local.is_file():
                errors.append(f"{rel}: missing target {href}")
                continue
            if target.fragment and local in pages and unquote(target.fragment) not in pages[local].ids:
                errors.append(f"{rel}: missing anchor {href}")
            links += 1
    search = json.loads((root / "search/search_index.json").read_text())
    searchable = " ".join(item.get("text", "") for item in search["docs"])
    for term in ("Allen", "cpractl", "maintenance", "mongodb"):
        if term.lower() not in searchable.lower():
            errors.append(f"search index is missing {term}")
    result = {
        "html_pages": len(pages), "content_pages": content_pages,
        "redirects": redirects, "internal_links_and_assets": links, "errors": errors,
    }
    print(json.dumps(result, indent=2))
    if errors:
        raise SystemExit(1)
    return result


if __name__ == "__main__":
    verify(sys.argv[1] if len(sys.argv) > 1 else ".")
