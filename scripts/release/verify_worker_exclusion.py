#!/usr/bin/env python3
"""Compile the application's worker exclusion contract with selected Go inputs.

This is local build-boundary evidence, not protocol execution or publication
qualification. A development GOWORK is explicit; no modules are rewritten.
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
import time

ROOT = Path(__file__).resolve().parents[2]
MODULE = "github.com/ziad-hsn/cpra"
CASES = (
    ("internal/persistence", "WorkerOffer", "var _ excluded.WorkerOffer"),
    ("internal/persistence", "WorkerExecutionLifecycle", "var _ excluded.WorkerExecutionLifecycle"),
    ("internal/persistence", "WorkerOfferFormatVersion", "var _ = excluded.WorkerOfferFormatVersion"),
    ("internal/persistence", "WorkerOfferLifetime", "var _ = excluded.WorkerOfferLifetime"),
    ("internal/persistence", "WorkerStartRequest", "var _ excluded.WorkerStartRequest"),
    ("internal/persistence", "WorkerStartResponse", "var _ excluded.WorkerStartResponse"),
    ("internal/persistence", "WorkerStartCommand", "var _ excluded.WorkerStartCommand"),
    ("internal/persistence", "CommitWorkerStart", "var _ = (*excluded.Store).CommitWorkerStart"),
    ("internal/persistence", "VerifyWorkerPollResponse", "var _ = (*excluded.Store).VerifyWorkerPollResponse"),
    ("internal/persistence", "WorkerExecutions", "var _ = (*excluded.Store).WorkerExecutions"),
    ("internal/management", "WorkerAssignments", "var _ = (*excluded.Catalog).WorkerAssignments"),
    ("internal/persistence", "WorkerExecutionIntent", "var _ excluded.WorkerExecutionIntent"),
    ("internal/persistence", "WorkerExecutionRecord", "var _ excluded.WorkerExecutionRecord"),
    ("internal/persistence", "WorkerExecutionCommand", "var _ excluded.WorkerExecutionCommand"),
    ("internal/persistence", "WorkerExecutionPage", "var _ excluded.WorkerExecutionPage"),
    ("internal/persistence", "WorkerNotificationSource", "var _ excluded.WorkerNotificationSource"),
    ("internal/persistence", "WorkerNotificationSources", "var _ = excluded.Policy{}.WorkerNotificationSources"),
    ("internal/persistence", "CommitWorkerExecution", "var _ = (*excluded.Store).CommitWorkerExecution"),
    ("internal/persistence", "WorkerExecution", "var _ = (*excluded.Store).WorkerExecution"),
    ("internal/persistence", "WorkerExecutionsReady", "var _ = (*excluded.Store).WorkerExecutionsReady"),
    ("internal/manifest", "ExternalJobIdentity", "var _ excluded.ExternalJobIdentity"),
    ("internal/manifest", "ExternalRuntimeKey", "var _ excluded.ExternalRuntimeKey"),
    ("internal/manifest", "ExternalJobDescriptor", "var _ excluded.ExternalJobDescriptor"),
    ("internal/manifest", "NewExternalJobDescriptor", "var _ = excluded.NewExternalJobDescriptor"),
    ("internal/manifest", "ExternalRuntimeBinding", "var _ excluded.ExternalRuntimeBinding"),
    ("internal/manifest", "ExternalPulseConfig", "var _ excluded.ExternalPulseConfig"),
    ("internal/manifest", "ExternalInterventionConfig", "var _ excluded.ExternalInterventionConfig"),
    ("internal/manifest", "ExternalNotificationConfig", "var _ excluded.ExternalNotificationConfig"),
    ("internal/controller/components", "ExternalJobStorage", "var _ excluded.ExternalJobStorage"),
    ("internal/controller/components", "ExternalJobs", "var _ = excluded.JobStorage{}.ExternalJobs"),
    ("internal/controller/entities", "PrepareMonitorExternal", "var _ = excluded.PrepareMonitorExternal"),
    ("internal/management", "WorkerOutcomeBinding", "var _ excluded.WorkerOutcomeBinding"),
    ("internal/management", "WorkerOutcomeDisposition", "var _ excluded.WorkerOutcomeDisposition"),
    ("internal/management", "PreparedWorkerOutcome", "var _ excluded.PreparedWorkerOutcome"),
    ("internal/management", "PrepareWorkerOutcome", "var _ = (*excluded.Catalog).PrepareWorkerOutcome"),
    ("internal/management", "PrepareWorkerExecution", "var _ = (*excluded.Catalog).PrepareWorkerExecution"),
    ("internal/management", "OpenWorkerExecution", "var _ = (*excluded.Catalog).OpenWorkerExecution"),
    ("internal/persistence", "WorkerServerIdentity", "var _ excluded.WorkerServerIdentity"),
    ("internal/persistence", "WorkerSessionCapability", "var _ excluded.WorkerSessionCapability"),
    ("internal/persistence", "WorkerPollRequest", "var _ excluded.WorkerPollRequest"),
    ("internal/persistence", "WorkerPollResponse", "var _ excluded.WorkerPollResponse"),
    ("internal/persistence", "WorkerProtocolIdentity", "var _ = (*excluded.Store).WorkerProtocolIdentity"),
    ("internal/persistence", "CommitWorkerPoll", "var _ = (*excluded.Store).CommitWorkerPoll"),
    ("internal/persistence", "JobTypeReference", "var _ excluded.JobTypeReference"),
    ("internal/persistence", "JobTypeVersionView", "var _ excluded.JobTypeVersionView"),
    ("internal/persistence", "CanonicalJobTypeReferences", "var _ = excluded.CanonicalJobTypeReferences"),
    ("internal/persistence", "LookupJobTypeVersion", "var _ = (*excluded.Store).LookupJobTypeVersion"),
    ("internal/persistence", "JobTypeReferences", "var _ = excluded.CatalogRecord{}.JobTypeReferences"),
    ("internal/persistence", "WorkerPolicyCommand", "var _ excluded.WorkerPolicyCommand"),
    ("internal/persistence", "WorkerAuthority", "var _ excluded.WorkerAuthority"),
    ("internal/persistence", "WorkerPolicyState", "var _ excluded.WorkerPolicyState"),
    ("internal/persistence", "AuthenticateWorker", "var _ = (*excluded.Store).AuthenticateWorker"),
    ("internal/persistence", "CheckWorkerAuthority", "var _ = (*excluded.Store).CheckWorkerAuthority"),
    ("internal/persistence", "CommitWorkerPolicy", "var _ = (*excluded.Store).CommitWorkerPolicy"),
    ("internal/httpauth", "WorkerPolicyReader", "var _ excluded.WorkerPolicyReader"),
    ("internal/httpauth", "AuthorizeWorker", "var _ = (*excluded.Authorizer).AuthorizeWorker"),
    ("internal/localadmin", "WorkerAuthenticationRequest", "var _ excluded.WorkerAuthenticationRequest"),
    ("internal/localadmin", "AdministerWorkerAuthentication", "var _ = excluded.AdministerWorkerAuthentication"),
)
PACKAGES = tuple(sorted({case[0] for case in CASES})) + (
    "internal/cpractl/cli", "internal/httpserver", "sdk/go/api",
)
BOUNDARY_TESTS = "^Test(WorkerDefaultBuildExcludesSchemaAndRoutes|LocalExtensionsExcludeWorkerAuthentication)$"


def builtin_tags(makefile: str) -> str:
    matches = re.findall(r"(?m)^ALL_DRIVER_TAGS\s*=\s*([^\n]+)$", makefile)
    if len(matches) != 1:
        raise ValueError("one literal ALL_DRIVER_TAGS assignment is required")
    tags = matches[0].split()
    if not tags or len(tags) != len(set(tags)) or any(
            not re.fullmatch(r"[A-Za-z0-9_]+", tag) or tag == "externaljobs" for tag in tags):
        raise ValueError("all built-in driver tags must exclude externaljobs")
    return " ".join(tags)


def require_symbol_absent(result: subprocess.CompletedProcess, symbol: str) -> None:
    """A compiler failure is useful only when this exact reference is absent."""
    output = result.stdout + result.stderr
    expected = (
        rf"undefined: excluded\.{re.escape(symbol)}\b",
        rf"\(\*excluded\.(?:Store|Authorizer|Catalog)\)\.{re.escape(symbol)} undefined "
        rf"\(type \*[A-Za-z0-9_]+\.(?:Store|Authorizer|Catalog) has no field or method {re.escape(symbol)}\)",
        rf"excluded\.(?:CatalogRecord|Policy|JobStorage)\{{\}}\.{re.escape(symbol)} undefined "
        rf"\(type [A-Za-z0-9_]+\.(?:CatalogRecord|Policy|JobStorage) has no field or method {re.escape(symbol)}\)",
    )
    if result.returncode == 0 or not any(re.search(pattern, output) for pattern in expected):
        raise RuntimeError("expected exact unavailable symbol " + symbol + ":\n" + output)


def json_stream(raw: str):
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        value, end = decoder.raw_decode(raw)
        yield value
        raw = raw[end:]


def verify(go: str, workspace: str | None, output: Path | None) -> dict:
    selected_go = Path(shutil.which(go) or go).resolve()
    if not selected_go.is_file():
        raise ValueError("selected Go compiler does not exist")
    tags = builtin_tags((ROOT / "Makefile").read_text())
    env = dict(os.environ, GOTOOLCHAIN="local", GOENV="off", GOFLAGS="", GOEXPERIMENT="",
               GOWORK=str(Path(workspace).resolve()) if workspace and workspace != "off" else "off")
    for key in ("GOOS", "GOARCH", "GOROOT"):
        env.pop(key, None)
    env["PATH"] = str(selected_go.parent) + os.pathsep + env.get("PATH", "")
    report = {"evidenceClass": "local_worker_build_exclusion", "publicationReady": False,
              "workerExecutionQualified": False, "workspace": env["GOWORK"], "checks": []}

    def run(args, *, absent=None):
        started = time.monotonic()
        result = subprocess.run([str(selected_go), *args], cwd=ROOT, env=env,
                                capture_output=True, text=True, timeout=180)
        item = {"command": [str(selected_go), *args], "exitCode": result.returncode,
                "seconds": round(time.monotonic() - started, 3),
                "stdout": result.stdout, "stderr": result.stderr}
        report["checks"].append(item)
        if absent:
            require_symbol_absent(result, absent)
            item["expectedUnavailableSymbol"] = absent
        elif result.returncode:
            raise RuntimeError("build-boundary command failed:\n" + result.stdout + result.stderr)
        return result.stdout

    try:
        report["compiler"] = run(["version"]).strip()
        scratch_parent = ROOT / "bin"
        scratch_parent.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="worker-exclusion-", dir=scratch_parent) as temporary:
            scratch = Path(temporary)
            source = scratch / "main.go"
            artifact = scratch / ("consumer.exe" if os.name == "nt" else "consumer")

            def build_source(package, declaration, selected_tags, absent=None):
                source.write_text('package main\nimport excluded "' + MODULE + '/' + package +
                                  '"\n' + declaration + '\nfunc main() {}\n')
                return run(["build", "-mod=readonly", "-p=2", "-tags=" + selected_tags,
                            "-o", str(artifact), str(source)], absent=absent)

            # Every negative fixture must compile in the explicitly tagged build.
            # This prevents a misspelled or removed API from proving exclusion.
            for package, _, declaration in CASES:
                build_source(package, declaration, "externaljobs")

            for selected_tags in ("", tags):
                # An unrelated package error must not become exclusion evidence.
                for package, typename in (("internal/persistence", "Store"),
                                          ("internal/httpauth", "Authorizer"),
                                          ("internal/localadmin", "AuthenticationRequest")):
                    build_source(package, "var _ *excluded." + typename, selected_tags)
                for package, symbol, declaration in CASES:
                    build_source(package, declaration, selected_tags, absent=symbol)

                raw = run(["list", "-mod=readonly", "-tags=" + selected_tags, "-json",
                           *[MODULE + "/" + package for package in PACKAGES]])
                selected_sources = {}
                for package in json_stream(raw):
                    sources = package.get("GoFiles", []) + package.get("CgoFiles", [])
                    if any(name.startswith("worker_") and name.endswith("_external.go")
                           or name == "worker_external.go" for name in sources):
                        raise RuntimeError("default build selected worker implementation: " + package["ImportPath"])
                    for name in sources:
                        path = Path(package["Dir"]) / name
                        selected_sources[str(path.relative_to(ROOT))] = hashlib.sha256(path.read_bytes()).hexdigest()
                report.setdefault("selectedSources", []).append({"tags": selected_tags, "sha256": selected_sources})
                dependencies = run(["list", "-mod=readonly", "-tags=" + selected_tags,
                                    "-deps", "-f", "{{.ImportPath}}", "./cmd/cpractl", "."])
                if any(line == MODULE + "/sdk/go/worker" or line.startswith(MODULE + "/sdk/go/worker/")
                       for line in dependencies.splitlines()):
                    raise RuntimeError("default application imported the external worker module")
                run(["test", "-mod=readonly", "-p=2", "-tags=" + selected_tags, "-run", BOUNDARY_TESTS,
                     "-count=1", "-timeout=120s", "./internal/httpserver", "./internal/cpractl/cli"])
        report["status"] = "pass"
    except Exception as error:
        report.update(status="failed", error=str(error))
        raise
    finally:
        if output:
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(json.dumps(report, indent=2) + "\n")
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--workspace", default=os.environ.get("GOWORK", "off"))
    parser.add_argument("--out", type=Path)
    options = parser.parse_args()
    result = verify(options.go, options.workspace, options.out)
    print(json.dumps({key: result[key] for key in ("status", "compiler", "evidenceClass")}, indent=2))
