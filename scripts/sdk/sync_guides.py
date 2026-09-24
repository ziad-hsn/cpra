#!/usr/bin/env python3
"""Copy canonical lessons and their linked source assets into SDK documentation."""
from __future__ import annotations

import argparse
import json
import re
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
LESSONS = ["queue-registration", "elb-deregistration", "kubernetes-services", "dao-sms"]
SOURCE_SUFFIXES = {".go", ".json", ".jsonl", ".yaml", ".yml", ".md", ".txt", ".sh", ".mod", ".sum"}


def lesson(name: str, assets: dict[Path, bytes]) -> str:
    path = ROOT / "examples/sdk" / name / "README.md"
    text = path.read_text()

    def link(match):
        label, target = match.group(1), match.group(2)
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or target.startswith("#"):
            return match.group(0)
        resolved = (path.parent / unquote(parsed.path)).resolve()
        if not resolved.is_relative_to(ROOT):
            raise ValueError("link outside repository: " + target)
        if not resolved.is_file():
            raise ValueError("broken example link: " + str(resolved))
        if resolved == ROOT / "examples/sdk/README.md":
            target = "index.md"
        elif resolved.name == "README.md" and resolved.parent.name in LESSONS:
            target = resolved.parent.name + ".md"
        elif resolved.is_relative_to(ROOT / "docs/sdk") and not resolved.is_relative_to(ROOT / "docs/sdk/source"):
            target = resolved.relative_to(ROOT / "docs/sdk").as_posix()
        else:
            if resolved.suffix not in SOURCE_SUFFIXES or any(part.startswith(".") for part in resolved.relative_to(ROOT).parts):
                raise ValueError("unsupported source asset: " + str(resolved))
            relative = Path("source") / resolved.relative_to(ROOT)
            # Text suffixes avoid undeclared MkDocs pages and Go packages under
            # docs/. The source content remains byte-for-byte identical.
            if relative.suffix in {".md", ".go", ".mod", ".sum", ".sh"}:
                relative = relative.with_name(relative.name + ".txt")
            assets[relative] = resolved.read_bytes()
            target = relative.as_posix()
        if parsed.fragment:
            target += "#" + parsed.fragment
        return "[" + label + "](" + target + ")"

    text = re.sub(r"\[([^\]\n]+)\]\(([^)\s]+)\)", link, text)
    return text + "\n<!-- Generated from examples/sdk/" + name + "/README.md by scripts/sdk/sync_guides.py. -->\n"


def generated() -> dict[Path, bytes]:
    assets: dict[Path, bytes] = {}
    pages = {Path(name + ".md"): lesson(name, assets).encode() for name in LESSONS}
    pages.update(assets)
    pages[Path("source/manifest.json")] = (json.dumps(sorted(str(p) for p in assets), indent=2) + "\n").encode()
    return pages


def sync(check: bool = False, site: Path | None = None):
    outputs = generated()
    root = ROOT / "docs/sdk"
    old_manifest = root / "source/manifest.json"
    if old_manifest.exists():
        for old in json.loads(old_manifest.read_text()):
            old_path = Path(old)
            destination = (root / old_path).resolve()
            if not destination.is_relative_to(root / "source"):
                raise ValueError("source manifest path outside generated assets")
            if old_path not in outputs and destination.exists():
                if check:
                    raise ValueError("stale source asset: " + str(destination))
                destination.unlink()
    for relative, data in outputs.items():
        destination = root / relative
        if check:
            if not destination.exists() or destination.read_bytes() != data:
                raise ValueError("stale SDK guide or source asset: " + str(destination))
        else:
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(data)
    if site:
        target = site.resolve()
        if target.is_relative_to(root):
            raise ValueError("the separate site directory must be outside docs/sdk")
        previous = target / "source/manifest.json"
        if previous.exists():
            for old in json.loads(previous.read_text()):
                relative = Path(old)
                stale = (target / relative).resolve()
                if not stale.is_relative_to(target / "source"):
                    raise ValueError("site source manifest path outside generated assets")
                if not (root / relative).exists() and stale.exists():
                    if check:
                        raise ValueError("stale site source asset: " + str(stale))
                    stale.unlink()
        # Include every page and linked source asset, not just top-level Markdown.
        for path in root.rglob("*"):
            if not path.is_file() or any(part.startswith(".") for part in path.relative_to(root).parts):
                continue
            if path.is_symlink():
                raise ValueError("SDK site sources must not be symlinks: " + str(path))
            destination = target / path.relative_to(root)
            if check:
                if not destination.exists() or destination.read_bytes() != path.read_bytes():
                    raise ValueError("stale site page or source asset: " + str(destination))
            else:
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(path.read_bytes())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--site", type=Path, help="copy the SDK tree into a separate MkDocs docs/sdk directory")
    args = parser.parse_args()
    sync(args.check, args.site)
    print("SDK guide and source copies " + ("verified" if args.check else "updated"))


if __name__ == "__main__":
    main()
