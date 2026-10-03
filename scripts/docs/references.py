#!/usr/bin/env python3
"""Prepare portable source references and metadata for private evidence links."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = "review/reference-data.json"
SOURCE_INDEX = "review/implementation-sources.md"
EVIDENCE_INDEX = "review/local-evidence.md"
ASSETS = "review/source/"
LINK = re.compile(r"\[[^\]\n]*\]\(([^)\s]+)\)")
SOURCE_PREFIXES = ("internal/", "api/openapi/", "dashboard/src/", "sdk/go/", "scripts/")
SOURCE_SUFFIXES = {".go", ".json", ".md", ".ts", ".tsx", ".py", ".sh"}
MAX_SOURCE_BYTES = 2 * 1024 * 1024
MAX_TOTAL_BYTES = 32 * 1024 * 1024


def encoded(value: object) -> bytes:
    return (json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n").encode()


def safe_path(root: Path, value: str) -> Path:
    relative = PurePosixPath(value)
    if (relative.is_absolute() or not relative.parts or ".." in relative.parts
            or "\\" in value or any(part.startswith(".") for part in relative.parts)):
        raise ValueError(f"unsafe reference path: {value}")
    path = root
    for part in relative.parts:
        path /= part
        if path.is_symlink():
            raise ValueError(f"reference contains a symlink: {value}")
    return path


def source_asset(value: str) -> str:
    if value not in {"Makefile", "main_externaljobs.go"}:
        if not value.startswith(SOURCE_PREFIXES) or Path(value).suffix not in SOURCE_SUFFIXES:
            raise ValueError(f"reference is not an approved source file: {value}")
    if any(part.lower() in {"secrets", "credentials"} for part in PurePosixPath(value).parts):
        raise ValueError(f"credential files cannot become documentation: {value}")
    return ASSETS + value + ".txt"


def record_id(value: str) -> str:
    return "record-" + hashlib.sha256(value.encode()).hexdigest()[:24]


def file_digest(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(64 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def private_record(root: Path, value: str) -> dict:
    if not value.startswith("bin/verification/"):
        raise ValueError(f"not a private verification path: {value}")
    path = safe_path(root, value)
    kind = "file" if path.is_file() else "directory" if path.is_dir() else "missing"
    return {"path": value, "record": record_id(value),
            "available_when_prepared": path.exists(), "kind": kind,
            "sha256": file_digest(path) if kind == "file" else None,
            "bytes": path.stat().st_size if kind == "file" else None,
            "content_included": False, "result_verified": False}


def page(title: str, description: str) -> str:
    return f"---\ntitle: {title}\ndescription: {description}\ncpra_scope: docs\n---\n\n# {title}\n\n"


def source_index(records: list[dict]) -> bytes:
    text = page("Referenced implementation sources", "Content-identified source downloads linked by CPRa implementation records.")
    text += ("These downloads contain the exact source bytes used to prepare this documentation. "
             "They can include uncommitted development changes. They are selected references, "
             "not a complete source distribution, reviewed release, or verification result.\n\n"
             "[Reference inventory](reference-data.json) records each original path, byte count "
             "and SHA-256. Regenerate it when referenced source changes.\n\n"
             "| Repository path | Download | SHA-256 |\n| --- | --- | --- |\n")
    for entry in records:
        text += f"| `{entry['path']}` | [Source]({entry['asset'].removeprefix('review/')}) | `{entry['sha256']}` |\n"
    return text.encode()


def evidence_index(records: list[dict]) -> bytes:
    text = page("Private verification references", "Locations and recorded availability of private CPRa verification artifacts; their contents are not published.")
    text += ("Implementation records link here when an artifact belongs to an ignored local "
             "verification directory. **The artifact contents are not included in this site.** "
             "Availability and hashes describe the files when this index was prepared; "
             "they do not prove a test passed, establish current availability, or qualify a release.\n\n"
             "A missing artifact is explicitly recorded. A directory reference has no content digest. "
             "Use the original records and the applicable evidence reader for a verification decision. "
             "[Reference inventory](reference-data.json) provides the same metadata.\n\n")
    for entry in records:
        text += f"## {entry['path']} {{#{entry['record']}}}\n\n"
        text += f"Recorded availability: {'present' if entry['available_when_prepared'] else 'missing'} ({entry['kind']}). "
        if entry["sha256"]:
            text += f"Bytes: {entry['bytes']}. SHA-256: `{entry['sha256']}`. "
        text += "Contents remain private; this generator did not verify the result.\n\n"
    return text.encode()


def prepare(root: Path = ROOT, check: bool = False) -> dict:
    root = root.resolve()
    docs = root / "docs"
    manifest_path = safe_path(docs, MANIFEST)
    old = json.loads(manifest_path.read_bytes()) if manifest_path.is_file() else {"format_version": 1, "sources": [], "private": []}
    if old.get("format_version") != 1 or not isinstance(old.get("sources"), list) or not isinstance(old.get("private"), list):
        raise ValueError("unsupported documentation reference inventory")
    old_private = {}
    for entry in old["private"]:
        expected_keys = {"path", "record", "available_when_prepared", "kind", "sha256", "bytes", "content_included", "result_verified"}
        value = entry.get("path", "")
        safe_path(root, value)
        if (set(entry) != expected_keys or not value.startswith("bin/verification/")
                or entry["record"] != record_id(value) or entry["content_included"] is not False
                or entry["result_verified"] is not False or type(entry["available_when_prepared"]) is not bool
                or entry["kind"] not in {"file", "directory", "missing"} or value in old_private):
            raise ValueError("invalid private reference metadata")
        if entry["kind"] == "file":
            if (not entry["available_when_prepared"] or not isinstance(entry["sha256"], str)
                    or re.fullmatch(r"[a-f0-9]{64}", entry["sha256"]) is None
                    or type(entry["bytes"]) is not int or entry["bytes"] < 0):
                raise ValueError("invalid private file identity")
        elif (entry["sha256"] is not None or entry["bytes"] is not None
              or entry["available_when_prepared"] != (entry["kind"] == "directory")):
            raise ValueError("invalid private availability metadata")
        old_private[value] = entry
    by_record = {entry["record"]: entry["path"] for entry in old_private.values()}
    sources, private = set(), set()
    output = {}
    for document in sorted(docs.rglob("*.md")):
        name = document.relative_to(docs).as_posix()
        safe_path(docs, name)
        if name in {SOURCE_INDEX, EVIDENCE_INDEX}:
            continue
        fence = None
        lines = []
        for line in document.read_text().splitlines(keepends=True):
            marker = re.match(r"^\s*(`{3,}|~{3,})", line)
            if marker:
                token = marker[1]
                if fence is None:
                    fence = token
                elif token[0] == fence[0] and len(token) >= len(fence):
                    fence = None
                lines.append(line)
                continue
            if fence is not None:
                lines.append(line)
                continue

            def replace(match: re.Match) -> str:
                target = match[1]
                url = urlsplit(target)
                if url.scheme or url.netloc or not url.path:
                    return match[0]
                if Path(unquote(url.path)).is_absolute():
                    raise ValueError(f"absolute reference in {name}: {target}")
                resolved = Path(os.path.abspath(document.parent / unquote(url.path)))
                if not resolved.is_relative_to(root):
                    raise ValueError(f"reference leaves repository in {name}: {target}")
                value = resolved.relative_to(root).as_posix()
                safe_path(root, value)
                if resolved.is_relative_to(docs):
                    asset = resolved.relative_to(docs).as_posix()
                    if asset.startswith(ASSETS):
                        if not asset.endswith(".txt"):
                            raise ValueError("source references must be text downloads")
                        original = asset.removeprefix(ASSETS).removesuffix(".txt")
                        if source_asset(original) != asset:
                            raise ValueError("invalid source download path")
                        sources.add(original)
                    elif asset == EVIDENCE_INDEX and url.fragment:
                        if url.fragment not in by_record:
                            raise ValueError(f"unrecorded private evidence reference: {target}")
                        private.add(by_record[url.fragment])
                    return match[0]
                if url.query or url.fragment:
                    raise ValueError(f"source/evidence references cannot contain query or fragment: {target}")
                if value.startswith("bin/verification/"):
                    private.add(value)
                    destination = docs / EVIDENCE_INDEX
                    portable = os.path.relpath(destination, document.parent) + "#" + record_id(value)
                else:
                    sources.add(value)
                    destination = docs / source_asset(value)
                    portable = os.path.relpath(destination, document.parent)
                return match[0][:match.start(1) - match.start()] + portable + ")"

            lines.append(LINK.sub(replace, line))
        output[name] = "".join(lines).encode()
    source_records, total = [], 0
    for value in sorted(sources):
        path = safe_path(root, value)
        if not path.is_file() or path.stat().st_size > MAX_SOURCE_BYTES:
            raise ValueError(f"missing or oversized source reference: {value}")
        content = path.read_bytes()
        content.decode("utf-8")
        if b"\0" in content:
            raise ValueError(f"binary source reference: {value}")
        total += len(content)
        if total > MAX_TOTAL_BYTES:
            raise ValueError("documentation source downloads exceed byte quota")
        asset = source_asset(value)
        output[asset] = content
        source_records.append({"path": value, "asset": asset, "bytes": len(content),
                               "sha256": hashlib.sha256(content).hexdigest()})
    private_records = []
    for value in sorted(private):
        if check:
            if value not in old_private:
                raise ValueError(f"private reference not prepared: {value}")
            private_records.append(old_private[value])
        else:
            private_records.append(private_record(root, value))
    output[MANIFEST] = encoded({"format_version": 1, "sources": source_records, "private": private_records})
    output[SOURCE_INDEX] = source_index(source_records)
    output[EVIDENCE_INDEX] = evidence_index(private_records)
    obsolete = set()
    owned_assets = set()
    for entry in old["sources"]:
        if source_asset(entry["path"]) != entry["asset"]:
            raise ValueError("invalid owned source download")
        owned_assets.add(entry["asset"])
        if entry["asset"] not in output:
            obsolete.add(entry["asset"])
    for path in (docs / ASSETS).rglob("*"):
        name = path.relative_to(docs).as_posix()
        safe_path(docs, name)
        if path.is_file() and name not in output and name not in owned_assets:
            raise ValueError(f"unrecorded source download: {name}")
    changed = []
    for name, content in output.items():
        destination = safe_path(docs, name)
        if not destination.is_file() or destination.read_bytes() != content:
            changed.append(name)
    if check and (changed or obsolete):
        raise ValueError("documentation references differ: " + json.dumps({"changed": changed, "obsolete": sorted(obsolete)}))
    if not check:
        for name in sorted(obsolete):
            safe_path(docs, name).unlink(missing_ok=True)
        for name in sorted(changed):
            destination = safe_path(docs, name)
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(output[name])
    return {"source_references": len(source_records), "source_bytes": total,
            "private_references": len(private_records), "private_contents_included": False,
            "private_results_verified": False, "references_match": True}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    print(json.dumps(prepare(check=parser.parse_args().check), indent=2))
