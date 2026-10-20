#!/usr/bin/env python3
"""Synchronize canonical documentation from main into a gh-pages checkout."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
from pathlib import PurePosixPath
import re
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "release"))
from evidence import read_report
import release

ROOT = Path(__file__).resolve().parents[2]
NAVIGATION = ROOT / "scripts/docs/navigation.json"
ANNOUNCEMENT = ROOT / "scripts/docs/announcement.html.j2"
ARCHIVE_PREFIXES = ("candidate/", "implementation/", "review/")


def site_path(site: Path, value: str) -> Path:
    path = site
    for part in PurePosixPath(value).parts:
        path /= part
        if path.is_symlink():
            raise ValueError(f"site path must not contain symlinks: {value}")
    return path


def navigation(source: Path) -> tuple[list, list[str]]:
    entries = json.loads(NAVIGATION.read_bytes())
    paths = []

    def visit(items):
        if not isinstance(items, list) or not items:
            raise ValueError("navigation sections must be nonempty lists")
        for item in items:
            if not isinstance(item, dict) or len(item) != 1:
                raise ValueError("navigation entries must contain one title")
            title, value = next(iter(item.items()))
            if not isinstance(title, str) or not title.strip():
                raise ValueError("navigation titles must be nonempty strings")
            if isinstance(value, list):
                visit(value)
                continue
            if not isinstance(value, str):
                raise ValueError("navigation pages must be relative Markdown paths")
            path = PurePosixPath(value)
            if (path.is_absolute() or ".." in path.parts or not value.endswith(".md")
                    or any(c in value for c in "#?\\") or any(p.startswith(".") for p in path.parts)):
                raise ValueError(f"invalid navigation path: {value}")
            if value in paths:
                raise ValueError(f"duplicate navigation page: {value}")
            paths.append(value)

    visit(entries)
    if any(name.startswith(ARCHIVE_PREFIXES) for name in paths):
        raise ValueError("engineering records cannot appear in public navigation")
    pages = {p.relative_to(source).as_posix() for p in source.rglob("*.md")}
    archived = {name for name in pages if name.startswith(ARCHIVE_PREFIXES)}
    if set(paths) - pages or pages - set(paths) - archived:
        raise ValueError("navigation must cover every Markdown page exactly once: " + json.dumps({
            "missing_pages": sorted(set(paths) - pages), "unlisted_pages": sorted(pages - set(paths) - archived)}))
    return entries, paths


def navigation_config(config: str, entries: list) -> str:
    # JSON is a YAML flow collection. Synchronize only the top-level nav value;
    # theme, redirects and branch-specific version metadata remain untouched.
    block = "nav:\n" + "\n".join("  " + line for line in json.dumps(entries, indent=2, ensure_ascii=False).splitlines()) + "\n"
    pattern = re.compile(r"(?m)^nav:[^\n]*\n(?:[ \t].*\n|\n)*")
    matches = list(pattern.finditer(config))
    if len(matches) != 1:
        raise ValueError("site configuration must contain one top-level nav section")
    config = pattern.sub(lambda _: block, config, count=1)
    # not_in_nav only hides a page from navigation: MkDocs still renders and
    # indexes it. Public source selection below and exclude_docs both enforce
    # the publication boundary, including non-Markdown source/evidence copies.
    archives = "exclude_docs: |\n" + "".join("  /" + prefix + "**\n" for prefix in ARCHIVE_PREFIXES)
    omitted = re.compile(r"(?m)^exclude_docs:[^\n]*(?:\n|$)(?:[ \t].*(?:\n|$)|\n)*")
    matches = list(omitted.finditer(config))
    if len(matches) > 1:
        raise ValueError("site configuration must contain at most one exclude_docs section")
    if matches:
        return omitted.sub(lambda _: archives, config, count=1)
    return config + "\n" + archives


def version_config(config: str, metadata: dict) -> str:
    values = {"source_commit": metadata["source_commit"], "source_date": metadata["updated"],
              "sdk_candidate_date": metadata["updated"],
              "historical_candidate_commit": metadata["release_candidate_commit"]}
    pattern = re.compile(r"(?m)^extra:[^\n]*\n(?:[ \t].*\n|\n)*")
    matches = list(pattern.finditer(config))
    if len(matches) != 1:
        raise ValueError("site configuration must contain one top-level extra section")
    block = matches[0].group()
    for key, value in values.items():
        line = f"  {key}: {json.dumps(value)}\n"
        field = re.compile(r"(?m)^  " + re.escape(key) + r":[^\n]*\n")
        if field.search(block):
            block = field.sub(lambda _: line, block)
        else:
            block += line
    return pattern.sub(lambda _: block, config, count=1)


def announcement_template(template: str) -> str:
    pattern = re.compile(r"{% block announce %}.*?{% endblock %}", re.S)
    if len(list(pattern.finditer(template))) != 1:
        raise ValueError("site template must contain one announce block")
    return pattern.sub(lambda _: ANNOUNCEMENT.read_text().rstrip(), template, count=1)


def frozen_metadata(metadata: dict, path: Path, files: dict[str, Path]) -> dict:
    manifest = read_report(path)
    identity = release.evidence_identity(manifest)
    required = {"docs/source-version.json", "scripts/docs/sync.py", "scripts/docs/navigation.json",
                "scripts/docs/announcement.html.j2"}
    if not required.issubset(manifest["source_files"]):
        raise ValueError("frozen release omits documentation rendering inputs")
    release.check_manifest(manifest)
    # Extra archived pages also count as changed input. The release validator
    # checks recorded bytes; this exact set prevents adding unrecorded guides.
    expected = {name.removeprefix("docs/") for name in manifest["source_files"] if name.startswith("docs/")}
    if set(files) != expected:
        raise ValueError("documentation files differ from the frozen release")
    return dict(metadata, source_commit=manifest["commit"], updated=manifest["source_date"][:10],
                source_commit_role="Frozen private release source; qualification and publication pending",
                release_status=f"Private {manifest['version']} candidate; qualification and publication pending",
                uncommitted_changes=False, release_version=manifest["version"], release_identity=identity)


def sync(site: Path, check: bool = False, release_path: Path | None = None) -> dict:
    source = ROOT / "docs"
    site = site.resolve()
    target = site_path(site, "_sources/docs")
    if not (site / "_sources/mkdocs.yml").is_file():
        raise ValueError("--site must identify a gh-pages checkout with _sources/mkdocs.yml")
    if target.is_relative_to(source) or source.is_relative_to(target):
        raise ValueError("application and documentation checkouts must be separate")
    entries, pages = navigation(source)
    files = {}
    for path in sorted(source.rglob("*")):
        if path.is_symlink():
            raise ValueError(f"documentation must not contain symlinks: {path}")
        if path.is_file():
            files[path.relative_to(source).as_posix()] = path
    config_path = site_path(site, "_sources/mkdocs.yml")
    if config_path.is_symlink():
        raise ValueError("site configuration must not be a symlink")
    config = config_path.read_text()
    updated_config = navigation_config(config, entries)
    metadata_path = source / "source-version.json"
    metadata = json.loads(metadata_path.read_bytes())
    metadata_bytes = metadata_path.read_bytes()
    if release_path is not None:
        metadata = frozen_metadata(metadata, release_path, files)
        metadata_bytes = release.encoded(metadata)
    # Validate the complete frozen input first, then select public output. The
    # repository retains all engineering records; the separate site's canonical
    # source subtree contains only public material.
    excluded_files = sum(name.startswith(ARCHIVE_PREFIXES) for name in files)
    files = {name: path for name, path in files.items() if not name.startswith(ARCHIVE_PREFIXES)}
    for field in ("source_commit", "release_candidate_commit"):
        if not re.fullmatch(r"[a-f0-9]{40}", metadata.get(field, "")):
            raise ValueError(f"source metadata requires a full commit: {field}")
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", metadata.get("updated", "")):
        raise ValueError("source metadata requires an ISO date")
    updated_config = version_config(updated_config, metadata)
    site_metadata = site_path(site, "_sources/source-version.json")
    template_path = site_path(site, "_sources/overrides/main.html")
    if site_metadata.is_symlink() or template_path.is_symlink():
        raise ValueError("site metadata and template must not be symlinks")
    template = template_path.read_text()
    updated_template = announcement_template(template)
    metadata_changed = not site_metadata.is_file() or site_metadata.read_bytes() != metadata_bytes
    def content(name, path):
        return metadata_bytes if name == "source-version.json" else path.read_bytes()
    for path in target.rglob("*"):
        site_path(site, path.relative_to(site).as_posix())
    # This subtree is the complete canonical document set. Removing an obsolete
    # page here is deliberate; generated root files remain owned by rebuild.py.
    obsolete = sorted(p for p in target.rglob("*") if p.is_file() and p.relative_to(target).as_posix() not in files)
    changed = [name for name, path in files.items()
               if not (target / name).is_file() or content(name, path) != (target / name).read_bytes()]
    if check and (obsolete or changed or updated_config != config or metadata_changed or updated_template != template):
        raise ValueError("documentation copies differ: " + json.dumps({
            "changed": changed, "obsolete": [p.relative_to(target).as_posix() for p in obsolete],
            "configuration_changed": updated_config != config, "source_metadata_changed": metadata_changed,
            "announcement_changed": updated_template != template}))
    if not check:
        for path in obsolete:
            path.unlink()
        for name in changed:
            destination = target / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            if name == "source-version.json":
                destination.write_bytes(metadata_bytes)
            else:
                shutil.copyfile(files[name], destination)
        if updated_config != config:
            config_path.write_text(updated_config)
        if metadata_changed:
            site_metadata.write_bytes(metadata_bytes)
        if updated_template != template:
            template_path.write_text(updated_template)
    digest = hashlib.sha256()
    for name, path in files.items():
        digest.update(name.encode() + b"\0" + hashlib.sha256(content(name, path)).digest())
    result = {"files": len(files), "markdown_pages": sum(n.endswith(".md") for n in files),
            "excluded_engineering_files": excluded_files,
            "content_sha256": digest.hexdigest(), "copies_match": True,
            "navigation_pages": len(pages), "navigation_matches": True,
            "navigation_sha256": hashlib.sha256(json.dumps(entries, sort_keys=True).encode()).hexdigest(),
            "source_metadata_matches": True, "announcement_matches": True}
    if release_path is not None:
        result["release_identity"] = metadata["release_identity"]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", required=True, type=Path)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--release", type=Path, help="render metadata from the matching clean, frozen RELEASE.json")
    args = parser.parse_args()
    try:
        result = sync(args.site, args.check, args.release)
    except (OSError, ValueError) as error:
        parser.exit(1, "Documentation synchronization failed: " + str(error) + "\n"
                    "Check the site path and source inputs; regenerate intentionally before using --check.\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
