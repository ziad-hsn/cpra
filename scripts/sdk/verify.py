#!/usr/bin/env python3
"""Verify SDK consumers using module archives, without workspaces or replace.

The local proxy is deliberately private and uses a temporary module cache. This
tests distribution contents; it is not evidence of a published or qualified API.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[2]
CORE = "github.com/ziad-hsn/cpra/sdk/go"
WORKER = CORE + "/worker"
VERSION = "v0.1.0-rc.1"
SUFFIXES = {".go", ".mod", ".sum", ".md", ".json", ".yaml", ".yml", ".txt"}
FORBIDDEN_IMPORTS = (
    "github.com/ziad-hsn/cpra/internal/", "github.com/mlange-42/ark",
    "github.com/hashicorp/raft", "github.com/aws/aws-sdk-go", "k8s.io/",
    "github.com/spf13/cobra",
)
PUBLIC_PACKAGES = {CORE: "cpra", CORE + "/api": "api",
                   CORE + "/collection": "collection"}


def verify_documentation_archive(module: str, archive_path: Path):
    """Require readable documentation and executable examples in the payload."""
    prefix = module + "@" + VERSION + "/"
    required = {"README.md", "LICENSE", "go.mod", "go.sum", "doc.go", "example_test.go"}
    if module == CORE:
        required.update(f"{package}/{name}" for package in ("api", "collection")
                        for name in ("doc.go", "example_test.go"))
    with zipfile.ZipFile(archive_path) as archive:
        for name in sorted(required):
            try:
                content = archive.read(prefix + name)
            except KeyError as error:
                raise ValueError(f"{module} archive is missing documentation input: {name}") from error
            if not content.strip():
                raise ValueError(f"{module} archive has empty documentation input: {name}")
    return sorted(required)


def module_files(directory: Path):
    """Select distributable sources, respecting nested Go module boundaries."""
    for current, dirs, files in os.walk(directory, followlinks=False):
        current = Path(current)
        dirs[:] = sorted(d for d in dirs if not d.startswith(".") and
                         d not in {"node_modules", "bin", "dist"} and
                         not (current / d).is_symlink() and
                         not (current / d / "go.mod").exists())
        for name in sorted(files):
            path = current / name
            if path.is_symlink():
                raise ValueError(f"module archive cannot include a symlink: {path}")
            if name.startswith(".") or ":" in name:
                continue
            if path.suffix in SUFFIXES or name in {"LICENSE", "NOTICE", "go.mod", "go.sum"}:
                yield path


def stage_module(proxy: Path, module: str, directory: Path):
    mod = (directory / "go.mod").read_text()
    if re.search(r"(?m)^\s*replace\s", mod):
        raise ValueError(f"{module} contains a replace directive")
    destination = proxy / module / "@v"
    destination.mkdir(parents=True, exist_ok=True)
    (destination / "list").write_text(VERSION + "\n")
    (destination / (VERSION + ".mod")).write_text(mod)
    (destination / (VERSION + ".info")).write_text(json.dumps({
        "Version": VERSION, "Time": "2026-09-13T00:00:00Z"}))
    archive = destination / (VERSION + ".zip")
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as output:
        for path in module_files(directory):
            info = zipfile.ZipInfo(module + "@" + VERSION + "/" + path.relative_to(directory).as_posix())
            info.date_time = (1980, 1, 1, 0, 0, 0)
            info.external_attr = 0o100644 << 16
            output.writestr(info, path.read_bytes())
    return hashlib.sha256(archive.read_bytes()).hexdigest()


def archive_contents(path: Path):
    """Compare all distributed names/bytes independently of ZIP metadata."""
    contents = {}
    with zipfile.ZipFile(path) as archive:
        for entry in archive.infolist():
            if entry.is_dir():
                continue
            if entry.filename in contents:
                raise ValueError("duplicate module archive entry")
            with archive.open(entry) as source:
                digest = hashlib.sha256()
                for block in iter(lambda: source.read(64 << 10), b""):
                    digest.update(block)
                contents[entry.filename] = digest.hexdigest()
    return contents


def verify_archive_identity(proxy: Path, module: str, downloaded_zip: str):
    expected = archive_contents(proxy / module / "@v" / (VERSION + ".zip"))
    actual = archive_contents(Path(downloaded_zip))
    if expected != actual:
        raise RuntimeError(f"downloaded {module} differs from the candidate source archive")


def run(args, cwd, env, *, expected_failure=False, input_text=None):
    result = subprocess.run(args, cwd=cwd, env=env, text=True, capture_output=True, input=input_text)
    if expected_failure:
        if result.returncode == 0:
            raise RuntimeError(f"negative compilation unexpectedly passed: {args}")
        if not any(message in result.stderr for message in (
                "undefined:", "build constraints exclude all Go files", "no Go files")):
            raise RuntimeError("negative test failed for an unrelated reason:\n" + result.stderr)
    elif result.returncode:
        raise RuntimeError(f"{' '.join(args)}:\n{result.stdout}\n{result.stderr}")
    return result.stdout


def verify_package_docs(go, doccheck, consumer, env, packages, *, tagged=False):
    """Render Go documentation and execute examples from downloaded modules."""
    selected = dict(env, GOFLAGS="-tags=externaljobs" if tagged else "")
    rendered = []
    for package, name in packages.items():
        metadata = run([go, "list", "-mod=readonly", "-json", package], consumer, selected)
        overview = run([str(doccheck)], consumer, selected, input_text=metadata)
        if f"Package {name} " not in overview:
            raise RuntimeError(f"{package} has no rendered package overview")
        # go doc does not honor arbitrary build tags. For default packages also
        # check the end-user command; the tagged renderer uses go/doc directly
        # on exactly the files chosen by go list -tags=externaljobs above.
        if not tagged and f"Package {name} " not in run([go, "doc", package], consumer, selected):
            raise RuntimeError(f"{package} overview is missing from go doc")
        result = run([go, "test", "-mod=readonly", "-count=1", "-run", "^Example", "-v", package],
                     consumer, selected)
        if "--- PASS: Example" not in result:
            raise RuntimeError(f"{package} has no passing executable documentation example")
        rendered.append({"package": package, "buildTags": "externaljobs" if tagged else "",
                         "renderer": "go/doc selected sources" if tagged else "go doc and go/doc selected sources",
                         "documentationRendered": True, "examplesExecuted": True})
    return rendered


def verify(go: str, output: Path | None, *, published=False):
    go = str(Path(shutil.which(go) or go).resolve())
    report = {"evidenceClass": "published_module" if published else "local_module_archive",
              "version": VERSION, "serverQualification": "pending", "publicationReady": False,
              "candidateSelection": "explicit SDK source and documentation files; excludes nested modules"}
    with tempfile.TemporaryDirectory(prefix="cpra-sdk-consumer-") as temp:
        temp = Path(temp)
        proxy = temp / "proxy"
        report["candidateArchiveSHA256"] = {
            CORE: stage_module(proxy, CORE, ROOT / "sdk/go"),
            WORKER: stage_module(proxy, WORKER, ROOT / "sdk/go/worker"),
        }
        env = os.environ.copy()
        env.update({"GOWORK": "off", "GOTOOLCHAIN": "local", "GOENV": "off",
                    "PATH": str(Path(go).parent) + os.pathsep + env.get("PATH", ""),
                    "GOFLAGS": "", "GOEXPERIMENT": "", "GOPRIVATE": "",
                    "GONOPROXY": "", "GONOSUMDB": "" if published else CORE + "*",
                    "GOSUMDB": "sum.golang.org", "GOMODCACHE": str(temp / "modules"),
                    "GOPROXY": "https://proxy.golang.org,direct" if published else
                    proxy.as_uri() + ",https://proxy.golang.org"})
        for key in ("GOOS", "GOARCH", "GOROOT"):
            env.pop(key, None)
        report["compiler"] = run([go, "version"], ROOT, env).strip()
        consumer = temp / "consumer"
        consumer.mkdir()
        (consumer / "go.mod").write_text("module example.com/cpra-sdk-consumer\n\ngo 1.25.0\n\n"
                                         f"require {CORE} {VERSION}\n")
        (consumer / "main.go").write_bytes((ROOT / "scripts/sdk/testdata/consumer.go").read_bytes())
        run([go, "mod", "tidy"], consumer, env)
        doccheck = temp / ("doccheck.exe" if os.name == "nt" else "doccheck")
        run([go, "build", "-mod=readonly", "-o", str(doccheck),
             str(ROOT / "scripts/sdk/doccheck/main.go")], consumer, env)
        report["downloadedModules"] = []
        metadata = json.loads(run([go, "mod", "download", "-json", CORE + "@" + VERSION], consumer, env))
        verify_archive_identity(proxy, CORE, metadata["Zip"])
        report["documentationArchiveInputs"] = {
            CORE: verify_documentation_archive(CORE, Path(metadata["Zip"]))}
        report["downloadedModules"].append({key: metadata[key] for key in ("Path", "Version", "Sum", "GoModSum")})
        report["packageDocumentation"] = verify_package_docs(go, doccheck, consumer, env, PUBLIC_PACKAGES)
        report["packageDocumentation"] += verify_package_docs(go, doccheck, consumer, env, PUBLIC_PACKAGES, tagged=True)
        run([go, "run", "-mod=readonly", "."], consumer, env)
        imports = run([go, "list", "-mod=readonly", "-deps", "-f", "{{.ImportPath}}", "."], consumer, env)
        if WORKER in imports or any(prefix in imports for prefix in FORBIDDEN_IMPORTS):
            raise RuntimeError("default SDK imported a forbidden application/worker dependency")
        (consumer / "main.go").write_text('package main\nimport cpra "' + CORE + '"\n'
                                         'var _ = cpra.NewWorkerClient\nfunc main() {}\n')
        run([go, "build", "-mod=readonly", "."], consumer, env, expected_failure=True)
        run([go, "build", "-mod=readonly", "-tags=externaljobs", "."], consumer, env)
        (consumer / "go.mod").write_text("module example.com/cpra-sdk-consumer\n\ngo 1.25.0\n\n"
                                         f"require (\n {CORE} {VERSION}\n {WORKER} {VERSION}\n)\n")
        (consumer / "main.go").write_text('package main\nimport _ "' + WORKER + '"\nfunc main() {}\n')
        run([go, "mod", "tidy"], consumer, env)
        metadata = json.loads(run([go, "mod", "download", "-json", WORKER + "@" + VERSION], consumer, env))
        verify_archive_identity(proxy, WORKER, metadata["Zip"])
        report["documentationArchiveInputs"][WORKER] = verify_documentation_archive(WORKER, Path(metadata["Zip"]))
        report["downloadedModules"].append({key: metadata[key] for key in ("Path", "Version", "Sum", "GoModSum")})
        run([go, "build", "-mod=readonly", "."], consumer, env, expected_failure=True)
        run([go, "build", "-mod=readonly", "-tags=externaljobs", "."], consumer, env)
        report["packageDocumentation"] += verify_package_docs(go, doccheck, consumer, env, {WORKER: "worker"}, tagged=True)
        if "replace " in (consumer / "go.mod").read_text():
            raise RuntimeError("consumer unexpectedly contains replacement dependencies")
        report["checks"] = ["external consumer", "independent default dependency graph",
                            "custom API negative compilation", "worker negative compilation",
                            "tagged consumers", "no workspace or replace", "downloaded source identity",
                            "packaged readmes and licenses", "rendered Go package documentation",
                            "executable examples from downloaded modules"]
        report["candidateContentIdentityMatched"] = True
        report["status"] = "pass"
    serialized = json.dumps(report, indent=2) + "\n"
    if output:
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(serialized)
    print(serialized, end="")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--published", action="store_true", help="download published versions instead of staging a local proxy")
    args = parser.parse_args()
    try:
        verify(args.go, args.out, published=args.published)
    except Exception as error:
        if args.out:
            args.out.parent.mkdir(parents=True, exist_ok=True)
            args.out.write_text(json.dumps({"status": "failed", "publicationReady": False,
                "evidenceClass": "published_module" if args.published else "local_module_archive",
                "version": VERSION, "error": str(error)}, indent=2) + "\n")
        raise
