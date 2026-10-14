#!/usr/bin/env python3
"""Verify SDK consumers using module archives, without workspaces or replace.

The local proxy is deliberately private and uses a temporary module cache. This
tests distribution contents; it is not evidence of a published or qualified API.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import zipfile

from module_archive import build_archiver, committed_source, sha256, stage_module, tool_environment
from prepare_dependencies import copy_prepared_source

ROOT = Path(__file__).resolve().parents[2]
CORE = "github.com/ziad-hsn/cpra/sdk/go"
WORKER = CORE + "/worker"
VERSION = "v0.1.0-rc.1"
MAX_ARCHIVE_BYTES = 500 * 1024 ** 2
FORBIDDEN_IMPORTS = (
    "github.com/ziad-hsn/cpra/internal/", "github.com/mlange-42/ark",
    "github.com/hashicorp/raft", "github.com/aws/aws-sdk-go", "k8s.io/",
    "github.com/spf13/cobra",
)
PUBLIC_PACKAGES = {CORE: "cpra", CORE + "/api": "api",
                   CORE + "/collection": "collection"}


def retain_module(directory, module, version, proxy, downloaded):
    """Keep both source-derived and downloaded bytes for later qualification."""
    role = {CORE: 'sdk', WORKER: 'worker'}[module]
    retained = {}
    for origin in ('source', 'downloaded'):
        files = {}
        for suffix, field in (('.zip', 'Zip'), ('.mod', 'GoMod'), ('.info', 'Info')):
            source = proxy / module / '@v' / (version + suffix) if origin == 'source' else Path(downloaded[field])
            if source.is_symlink() or not source.is_file():
                raise ValueError('Module evidence input must be a regular file')
            limit = MAX_ARCHIVE_BYTES if suffix == '.zip' else 16 * 1024 ** 2
            if source.stat().st_size > limit:
                raise ValueError('Module evidence input exceeds its byte limit')
            name = role + '-' + origin + suffix
            digest = hashlib.sha256()
            descriptor = os.open(directory / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, 'wb') as output, source.open('rb') as incoming:
                size = 0
                while block := incoming.read(64 * 1024):
                    size += len(block)
                    if size > limit:
                        raise ValueError('Module evidence input grew beyond its byte limit')
                    output.write(block)
                    digest.update(block)
                output.flush()
                os.fsync(output.fileno())
            files[field] = {'file': name, 'sha256': digest.hexdigest(), 'bytes': size}
        retained[origin] = files
    return retained


def verify_documentation_archive(module: str, archive_path: Path, version=VERSION):
    """Require readable documentation and executable examples in the payload."""
    prefix = module + "@" + version + "/"
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


def verify_archive_identity(proxy: Path, module: str, downloaded_zip: str, version=VERSION):
    expected = archive_contents(proxy / module / "@v" / (version + ".zip"))
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


def verify_prepared_archives(records, preparation, compiler_sha256):
    for module, record in records.items():
        expected = preparation.get('module_archives', {}).get(module, {})
        fields = ('Path', 'Version', 'Sum', 'GoModSum', 'Requires')
        if any(record.get(key) != expected.get(key) for key in fields):
            raise ValueError('Restaged module content differs from dependency preparation')
        # dirhash binds filenames and bytes. Go versions can encode the same ZIP
        # content differently; exact ZIP bytes require the same compiler recipe.
        if compiler_sha256 == preparation.get('compiler', {}).get('sha256') \
                and record.get('ArchiveSHA256') != expected.get('ArchiveSHA256'):
            raise ValueError('Restaged ZIP differs under the same compiler recipe')


def verify_consumers(go: str, artifacts: Path | None, *, published=False, sdk_version=VERSION, worker_version=VERSION, prepared=None):
    if prepared is not None and published:
        raise ValueError('Working-tree preparation cannot qualify published modules')
    go = str(Path(shutil.which(go) or go).resolve())
    evidence_class = 'working_tree_module_archive' if prepared is not None else \
        ('published_module' if published else 'local_module_archive')
    report = {"evidenceClass": evidence_class,
              "compilerSHA256": sha256(go),
              "versions": {CORE: sdk_version, WORKER: worker_version},
              "serverQualification": "pending", "publicationReady": False,
              "candidateSelection": "committed source archives with golang.org/x/mod/zip@v0.38.0"}
    with tempfile.TemporaryDirectory(prefix="cpra-sdk-consumer-") as temp:
        temp = Path(temp)
        source = temp / 'source'
        if prepared is None:
            commit, epoch = committed_source(ROOT, source)
            report['sourceCommit'] = commit
            source_time = datetime.fromtimestamp(epoch, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
            preparation = None
        else:
            preparation = copy_prepared_source(prepared, source)
            if preparation.get('versions') != {CORE: sdk_version, WORKER: worker_version}:
                raise ValueError('Requested versions differ from prepared module files')
            source_time = preparation['module_info_time']
            report.update(candidateSelection='retained working-tree dependency preparation',
                          baseCommit=preparation['base_commit'], releaseQualified=False,
                          preparationReportSHA256=hashlib.sha256((Path(prepared) / 'report.json').read_bytes()).hexdigest())
        archiver = build_archiver(go, temp / 'archive-tool', source)
        proxy = temp / "proxy"
        records = {
            CORE: stage_module(archiver, proxy, CORE, sdk_version, source / "sdk/go", source_time),
            WORKER: stage_module(archiver, proxy, WORKER, worker_version, source / "sdk/go/worker", source_time),
        }
        if records[WORKER]['Requires'].get(CORE) != sdk_version:
            raise ValueError('Worker go.mod must pin the SDK version being qualified')
        if preparation is not None:
            verify_prepared_archives(records, preparation, report['compilerSHA256'])
        report['moduleArchives'] = records
        env = tool_environment(go, temp / 'consumer-tools')
        env.update({"GOWORK": "off", "GOTOOLCHAIN": "local", "GOENV": "off",
                    "PATH": str(Path(go).parent), "GOVCS": "*:off",
                    "GOFLAGS": "-p=2", "GOEXPERIMENT": "", "GOPRIVATE": "",
                    "GOMAXPROCS": "2", "GOMEMLIMIT": "768MiB",
                    "GONOPROXY": "", "GONOSUMDB": "" if published else CORE + "," + WORKER,
                    "GOSUMDB": "sum.golang.org", "GOMODCACHE": str(temp / "modules"),
                    "GOPROXY": "https://proxy.golang.org" if published else
                    proxy.as_uri() + ",https://proxy.golang.org"})
        for key in ("GOOS", "GOARCH", "GOROOT"):
            env.pop(key, None)
        report["compiler"] = run([go, "version"], ROOT, env).strip()
        consumer = temp / "consumer"
        consumer.mkdir()
        (consumer / "go.mod").write_text("module example.com/cpra-sdk-consumer\n\ngo 1.26.0\n\n"
                                         f"require {CORE} {sdk_version}\n")
        (consumer / "main.go").write_bytes((source / "scripts/sdk/testdata/consumer.go").read_bytes())
        run([go, "mod", "tidy"], consumer, env)
        doccheck = temp / ("doccheck.exe" if os.name == "nt" else "doccheck")
        run([go, "build", "-mod=readonly", "-o", str(doccheck),
             str(source / "scripts/sdk/doccheck/main.go")], consumer, env)
        report["downloadedModules"] = []
        metadata = json.loads(run([go, "mod", "download", "-json", CORE + "@" + sdk_version], consumer, env))
        verify_archive_identity(proxy, CORE, metadata["Zip"], sdk_version)
        report["documentationArchiveInputs"] = {
            CORE: verify_documentation_archive(CORE, Path(metadata["Zip"]), sdk_version)}
        report["downloadedModules"].append({key: metadata[key] for key in ("Path", "Version", "Sum", "GoModSum")})
        if artifacts is not None:
            report['retainedModules'] = {CORE: retain_module(artifacts, CORE, sdk_version, proxy, metadata)}
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
        (consumer / "go.mod").write_text("module example.com/cpra-sdk-consumer\n\ngo 1.26.0\n\n"
                                         f"require (\n {CORE} {sdk_version}\n {WORKER} {worker_version}\n)\n")
        (consumer / "main.go").write_text('package main\nimport _ "' + WORKER + '"\nfunc main() {}\n')
        run([go, "mod", "tidy"], consumer, env)
        metadata = json.loads(run([go, "mod", "download", "-json", WORKER + "@" + worker_version], consumer, env))
        verify_archive_identity(proxy, WORKER, metadata["Zip"], worker_version)
        report["documentationArchiveInputs"][WORKER] = verify_documentation_archive(WORKER, Path(metadata["Zip"]), worker_version)
        report["downloadedModules"].append({key: metadata[key] for key in ("Path", "Version", "Sum", "GoModSum")})
        if artifacts is not None:
            report['retainedModules'][WORKER] = retain_module(artifacts, WORKER, worker_version, proxy, metadata)
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
    return report


def verify(go: str, output: Path | None, *, published=False, sdk_version=VERSION, worker_version=VERSION, prepared=None):
    """Reserve one report and preserve its inputs even when verification fails."""
    stream = None
    artifacts = None
    try:
        if output is not None:
            output.parent.mkdir(parents=True, exist_ok=True)
            descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            stream = os.fdopen(descriptor, 'w', encoding='utf-8')
            artifacts = output.with_name(output.name + '.modules')
            artifacts.mkdir(mode=0o700)
        report = verify_consumers(go, artifacts, published=published,
                                  sdk_version=sdk_version, worker_version=worker_version, prepared=prepared)
    except Exception as error:
        if stream is not None:
            report = {'status': 'failed', 'publicationReady': False,
                      'evidenceClass': 'working_tree_module_archive' if prepared is not None else
                                      ('published_module' if published else 'local_module_archive'),
                      'versions': {CORE: sdk_version, WORKER: worker_version}, 'error': str(error)}
            stream.write(json.dumps(report, indent=2) + '\n')
            stream.flush()
            os.fsync(stream.fileno())
        raise
    else:
        serialized = json.dumps(report, indent=2) + '\n'
        if stream is not None:
            stream.write(serialized)
            stream.flush()
            os.fsync(stream.fileno())
        print(serialized, end='')
        return report
    finally:
        if stream is not None:
            stream.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--published", action="store_true", help="download published versions instead of staging a local proxy")
    parser.add_argument("--sdk-version", default=VERSION)
    parser.add_argument("--worker-version", default=VERSION)
    parser.add_argument('--prepared', type=Path, help='exercise retained working-tree preparation without release qualification')
    args = parser.parse_args()
    if args.prepared is not None and args.published:
        parser.error('--prepared cannot be combined with --published')
    verify(args.go, args.out, published=args.published, sdk_version=args.sdk_version,
           worker_version=args.worker_version, prepared=args.prepared)
