#!/usr/bin/env python3
"""Synchronize canonical documentation from main into a gh-pages checkout."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import shutil

ROOT = Path(__file__).resolve().parents[2]


def sync(site: Path, check: bool = False) -> dict:
    source = ROOT / "docs"
    site = site.resolve()
    target = site / "_sources/docs"
    if not (site / "_sources/mkdocs.yml").is_file():
        raise ValueError("--site must identify a gh-pages checkout with _sources/mkdocs.yml")
    if target.is_relative_to(source) or source.is_relative_to(target):
        raise ValueError("application and documentation checkouts must be separate")
    files = {}
    for path in sorted(source.rglob("*")):
        if path.is_symlink():
            raise ValueError(f"documentation must not contain symlinks: {path}")
        if path.is_file():
            files[path.relative_to(source).as_posix()] = path
    # This subtree is the complete canonical document set. Removing an obsolete
    # page here is deliberate; generated root files remain owned by rebuild.py.
    obsolete = sorted(p for p in target.rglob("*") if p.is_file() and p.relative_to(target).as_posix() not in files)
    changed = [name for name, path in files.items()
               if not (target / name).is_file() or path.read_bytes() != (target / name).read_bytes()]
    if check and (obsolete or changed):
        raise ValueError("documentation copies differ: " + json.dumps({
            "changed": changed, "obsolete": [p.relative_to(target).as_posix() for p in obsolete]}))
    if not check:
        for path in obsolete:
            path.unlink()
        for name in changed:
            destination = target / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(files[name], destination)
    digest = hashlib.sha256()
    for name, path in files.items():
        digest.update(name.encode() + b"\0" + hashlib.sha256(path.read_bytes()).digest())
    return {"files": len(files), "markdown_pages": sum(n.endswith(".md") for n in files),
            "content_sha256": digest.hexdigest(), "copies_match": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", required=True, type=Path)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    print(json.dumps(sync(args.site, args.check), indent=2))


if __name__ == "__main__":
    main()
