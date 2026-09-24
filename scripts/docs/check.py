#!/usr/bin/env python3
"""Check canonical CPRa documentation and its source-contract coverage."""
from pathlib import Path
from urllib.parse import unquote, urlsplit
import argparse
import json
import re

ROOT = Path(__file__).resolve().parents[2]


def source_path(source: Path, *names: str) -> Path:
    """Support both the current layout and the pinned pre-refactor candidate."""
    for name in names:
        path = source / name
        if path.exists():
            return path
    raise FileNotFoundError(f"missing source contract input in {source}: {', '.join(names)}")


def check_links(document: Path, docs: Path, errors: list, local_evidence: list):
    for target in re.findall(r"\[[^\]\n]*\]\(([^)\s]+)\)", document.read_text()):
        url = urlsplit(target)
        if url.scheme or url.netloc or not url.path:
            continue
        relative = Path(unquote(url.path))
        path = (document.parent / relative).resolve()
        implementation = document.is_relative_to(docs / "implementation")
        # Implementation records intentionally refer to repository source and
        # ignored local evidence. Evidence is not distributed with a checkout;
        # record its availability without claiming that it was verified here.
        if implementation and not relative.is_absolute() and path.is_relative_to(ROOT / "bin/verification"):
            local_evidence.append({"document": document.relative_to(ROOT).as_posix(),
                                   "target": target, "available": path.exists()})
            continue
        boundary = ROOT if implementation else docs
        if relative.is_absolute() or not path.is_relative_to(boundary) or not path.exists():
            errors.append(f"{document.relative_to(ROOT)}: missing or nonportable link {target}")


def driver_fields(source: Path, document: Path, errors: list, label: str):
    schemas = {}
    schema = source_path(source, "internal/manifest", "internal/loader/schema")
    for path in schema.glob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        for name, body in re.findall(r"^type (\w+) struct \{(.*?)^\}", path.read_text(), re.S | re.M):
            match = re.fullmatch(r"Pulse(\w+)Config|InterventionTarget(\w+)|CodeNotification(\w+)", name)
            if not match:
                continue
            category = "Checks" if match[1] else "Recovery actions" if match[2] else "Notifications"
            driver = next(value for value in match.groups() if value).lower()
            schemas[(category, driver)] = set(re.findall(r'yaml:"([^",]+)', body)) - {"-"}
    if not schemas:
        errors.append(f"{label}: no driver schemas found in {schema}")
    sections = {}
    category = driver = None
    for line in document.read_text().splitlines():
        if line.startswith("## "):
            category, driver = line[3:], None
        elif line.startswith("### "):
            driver = line[4:]
            sections[(category, driver)] = set()
        elif driver and (match := re.match(r"\| `([^`]+)` \|", line)):
            sections[(category, driver)].add(match[1])
    for key in schemas.keys() | sections.keys():
        expected, actual = schemas.get(key, set()), sections.get(key, set())
        if key not in schemas or key not in sections or expected != actual:
            errors.append(f"{label}: driver {key}: missing {sorted(expected - actual)}, extra {sorted(actual - expected)}")
    return {"drivers": len(schemas), "driver_fields": sum(map(len, schemas.values()))}


def check(candidate: Path | None = None):
    docs = ROOT / "docs"
    errors = []
    local_evidence = []
    pages = sorted(docs.rglob("*.md"))
    required = ["versions.md", "review/latest-changes.md", "maintaining-docs.md",
                "candidate/reference/runtime-config.md", "candidate/durability.md",
                "candidate/native-installation.md", "candidate/container-helm.md",
                "candidate/provider-testing.md", "candidate/release-engineering.md",
                "sdk/index.md", "sdk/api-reference.md", "sdk/wire-types.md",
                "sdk/go-reference.md", "implementation/api-management-plan.md"]
    for name in required:
        if not (docs / name).is_file():
            errors.append("missing required document: " + name)
    for p in pages:
        text = p.read_text()
        if text.startswith("---\n"):
            end = text.find("\n---", 4)
            if end < 0:
                errors.append(f"{p.relative_to(ROOT)}: unclosed metadata")
            else:
                for line in text[4:end].splitlines():
                    if line.strip() and not re.match(r"[a-z_]+: ", line):
                        errors.append(f"{p.relative_to(ROOT)}: prose or malformed metadata: {line}")
        fence = None
        for line in text.splitlines():
            m = re.match(r"^\s*(`{3,}|~{3,})", line)
            if m:
                token = m[1]
                if fence is None:
                    fence = token
                elif token[0] == fence[0] and len(token) >= len(fence):
                    fence = None
        if fence:
            errors.append(f"{p.relative_to(ROOT)}: unclosed code fence")
        check_links(p, docs, errors, local_evidence)
    contracts = []
    for label, source, prefix in [("main", ROOT, ""), ("candidate", candidate, "candidate/")]:
        if source is None:
            continue
        cli = (docs / (prefix + "reference/cli.md")).read_text()
        flags = sorted(set(re.findall(r'flag\.\w+\([^\n]*?"([a-z][a-z.-]+)"', (source / "main.go").read_text())))
        for name in flags:
            if "`-" + name + "`" not in cli:
                errors.append(f"{label}: missing server flag -{name}")
        api = (docs / (prefix + "reference/api-reference.md")).read_text()
        server = source_path(source, "internal/httpserver/server.go", "internal/web/server/server.go")
        routes = sorted(set(re.findall(r'mux\.HandleFunc\("(?:GET )?(/api/v1/[^" ]+|/metrics)"', server.read_text())))
        for route in routes:
            if route not in api:
                errors.append(f"{label}: missing API route {route}")
        fields = driver_fields(source, docs / (prefix + "reference/jobs-reference.md"), errors, label)
        contracts.append({"source": label, "flags": len(flags), "routes": len(routes), **fields})
    # The two public surfaces use the same generated palette and original assets.
    if (ROOT / "brand/dist/palette.css").read_bytes() != (docs / "assets/stylesheets/palette.css").read_bytes():
        errors.append("canonical documentation palette differs from brand output")
    result = {"markdown_pages": len(pages), "source_contracts": contracts,
              "local_evidence_links": local_evidence, "errors": errors}
    print(json.dumps(result, indent=2))
    if errors:
        raise SystemExit(1)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate-root", type=Path)
    check(parser.parse_args().candidate_root)
