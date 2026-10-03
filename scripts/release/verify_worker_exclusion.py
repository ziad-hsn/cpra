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
import stat
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
MODULE = "github.com/ziad-hsn/cpra"
SDK_MODULE = MODULE + "/sdk/go"
CASES = (
    ("internal/persistence", "RecoveryNotificationWorkerFormatVersion", "var _ = excluded.RecoveryNotificationWorkerFormatVersion"),
    ("internal/persistence", "AgentWorkerFormatVersion", "var _ = excluded.AgentWorkerFormatVersion"),
    ("internal/persistence", "CheckWorkerExecutionCapacity", "var _ = (*excluded.Store).CheckWorkerExecutionCapacity"),
    ("internal/persistence", "WorkerScheduleTicket", "var _ excluded.WorkerScheduleTicket"),
    ("internal/persistence", "WorkerScheduleCommand", "var _ excluded.WorkerScheduleCommand"),
    ("internal/persistence", "WorkerScheduleFormatVersion", "var _ = excluded.WorkerScheduleFormatVersion"),
    ("internal/persistence", "ScheduleWorkerCheck", "var _ = (*excluded.Store).ScheduleWorkerCheck"),
    ("internal/persistence", "AbandonWorkerCheck", "var _ = (*excluded.Store).AbandonWorkerCheck"),
    ("internal/persistence", "FinishWorkerSchedules", "var _ = (*excluded.Store).FinishWorkerSchedules"),
    ("internal/persistence", "WorkerScheduleCheckpoint", "var _ = (*excluded.Store).WorkerScheduleCheckpoint"),
    ("internal/persistence", "WorkerExpireCommand", "var _ excluded.WorkerExpireCommand"),
    ("internal/persistence", "WorkerExpiryFormatVersion", "var _ = excluded.WorkerExpiryFormatVersion"),
    ("internal/persistence", "ExpireWorkerAction", "var _ = (*excluded.Store).ExpireWorkerAction"),
    ("internal/persistence", "WorkerDispatchState", "var _ excluded.WorkerDispatchState"),
    ("internal/persistence", "FindWorkerExecution", "var _ = (*excluded.Store).FindWorkerExecution"),
    ("internal/persistence", "WorkerDispatchStatus", "var _ = (*excluded.Store).WorkerDispatchStatus"),
    ("internal/management", "WorkerExecutionTimeout", "var _ = (*excluded.Catalog).WorkerExecutionTimeout"),
    ("internal/persistence", "WorkerStatus", "var _ excluded.WorkerStatus"),
    ("internal/persistence", "WorkerStatuses", "var _ = (*excluded.Store).WorkerStatuses"),
    ("sdk/go/api", "WorkerObservation", "var _ excluded.WorkerObservation"),
    ("sdk/go/api", "WorkerObservationList", "var _ excluded.WorkerObservationList"),
    ("sdk/go", "Workers", "var _ = (*excluded.Client).Workers"),
    ("sdk/go", "IterateWorkers", "var _ = (*excluded.Client).IterateWorkers"),
    ("internal/persistence", "WorkerCheckRetentionFormatVersion", "var _ = excluded.WorkerCheckRetentionFormatVersion"),
    ("internal/persistence", "WorkerNotificationFormatVersion", "var _ = excluded.WorkerNotificationFormatVersion"),
    ("internal/persistence", "WorkerCheckAllocateCommand", "var _ excluded.WorkerCheckAllocateCommand"),
    ("internal/persistence", "WorkerCheckRetireCommand", "var _ excluded.WorkerCheckRetireCommand"),
    ("internal/persistence", "AllocateWorkerCheckID", "var _ = (*excluded.Store).AllocateWorkerCheckID"),
    ("internal/persistence", "RetireWorkerChecks", "var _ = (*excluded.Store).RetireWorkerChecks"),
    ("internal/persistence", "ErrWorkerCheckExpired", "var _ = excluded.ErrWorkerCheckExpired"),
    ("internal/persistence", "MaxWorkerCheckReservations", "var _ = excluded.MaxWorkerCheckReservations"),
    ("internal/persistence", "MaxWorkerCheckReservationBytes", "var _ = excluded.MaxWorkerCheckReservationBytes"),
    ("internal/persistence", "MaxWorkerCheckReceipts", "var _ = excluded.MaxWorkerCheckReceipts"),
    ("internal/persistence", "MaxWorkerCheckRetirements", "var _ = excluded.MaxWorkerCheckRetirements"),
    ("internal/persistence", "WorkerCheckReservationLifetime", "var _ = excluded.WorkerCheckReservationLifetime"),
    ("internal/persistence", "WorkerCheckReceiptLifetime", "var _ = excluded.WorkerCheckReceiptLifetime"),
    ("sdk/go", "ErrWorkerCheckExpired", "var _ = excluded.ErrWorkerCheckExpired"),
    ("internal/persistence", "WorkerSLOFormatVersion", "var _ = excluded.WorkerSLOFormatVersion"),
    ("internal/persistence", "WorkerSchedulingFormatVersion", "var _ = excluded.WorkerSchedulingFormatVersion"),
    ("internal/persistence", "WorkerRequeueCommand", "var _ excluded.WorkerRequeueCommand"),
    ("internal/persistence", "RequeueWorkerExecutions", "var _ = (*excluded.Store).RequeueWorkerExecutions"),
    ("internal/persistence", "WorkerObservationCommand", "var _ excluded.WorkerObservationCommand"),
    ("internal/persistence", "WorkerResultObservation", "var _ excluded.WorkerResultObservation"),
    ("internal/persistence", "WorkerFinalization", "var _ excluded.WorkerFinalization"),
    ("internal/persistence", "WorkerFinalizeCommand", "var _ excluded.WorkerFinalizeCommand"),
    ("internal/persistence", "WorkerFinalizationFormatVersion", "var _ = excluded.WorkerFinalizationFormatVersion"),
    ("internal/persistence", "MaxWorkerFinalizationsPerTick", "var _ = excluded.MaxWorkerFinalizationsPerTick"),
    ("internal/persistence", "FinalizeWorkerExecutions", "var _ = (*excluded.Store).FinalizeWorkerExecutions"),
    ("internal/persistence", "WorkerHeartbeatRequest", "var _ excluded.WorkerHeartbeatRequest"),
    ("internal/persistence", "WorkerHeartbeatResponse", "var _ excluded.WorkerHeartbeatResponse"),
    ("internal/persistence", "WorkerHeartbeatState", "var _ excluded.WorkerHeartbeatState"),
    ("internal/persistence", "WorkerHeartbeatCommand", "var _ excluded.WorkerHeartbeatCommand"),
    ("internal/persistence", "WorkerLifecycleFormatVersion", "var _ = excluded.WorkerLifecycleFormatVersion"),
    ("internal/persistence", "CommitWorkerHeartbeat", "var _ = (*excluded.Store).CommitWorkerHeartbeat"),
    ("internal/persistence", "WorkerEvidenceInput", "var _ excluded.WorkerEvidenceInput"),
    ("internal/persistence", "WorkerEvidenceRecord", "var _ excluded.WorkerEvidenceRecord"),
    ("internal/persistence", "WorkerEvidenceCommand", "var _ excluded.WorkerEvidenceCommand"),
    ("internal/persistence", "MaxWorkerEvidencePerExecution", "var _ = excluded.MaxWorkerEvidencePerExecution"),
    ("internal/persistence", "MaxWorkerEvidenceBytes", "var _ = excluded.MaxWorkerEvidenceBytes"),
    ("internal/persistence", "CommitWorkerEvidence", "var _ = (*excluded.Store).CommitWorkerEvidence"),
    ("internal/persistence", "WorkerEvidenceExecution", "var _ = (*excluded.Store).WorkerEvidenceExecution"),
    ("internal/management", "CommitWorkerEvidence", "var _ = (*excluded.Catalog).CommitWorkerEvidence"),
    ("internal/management", "ErrWorkerEvidenceInvalid", "var _ = excluded.ErrWorkerEvidenceInvalid"),
    ("internal/persistence", "WorkerReceipt", "var _ excluded.WorkerReceipt"),
    ("internal/persistence", "WorkerResultInput", "var _ excluded.WorkerResultInput"),
    ("internal/persistence", "WorkerResultRecord", "var _ excluded.WorkerResultRecord"),
    ("internal/persistence", "WorkerResultCommand", "var _ excluded.WorkerResultCommand"),
    ("internal/persistence", "WorkerResultFormatVersion", "var _ = excluded.WorkerResultFormatVersion"),
    ("internal/persistence", "MaxWorkerResultRecordBytes", "var _ = excluded.MaxWorkerResultRecordBytes"),
    ("internal/persistence", "MaxWorkerResultStateBytes", "var _ = excluded.MaxWorkerResultStateBytes"),
    ("internal/persistence", "CommitWorkerResult", "var _ = (*excluded.Store).CommitWorkerResult"),
    ("internal/persistence", "WorkerResultExecution", "var _ = (*excluded.Store).WorkerResultExecution"),
    ("internal/management", "CommitWorkerOutcome", "var _ = (*excluded.Catalog).CommitWorkerOutcome"),
    ("internal/persistence", "WorkerOffer", "var _ excluded.WorkerOffer"),
    ("internal/persistence", "WorkerExecutionLifecycle", "var _ excluded.WorkerExecutionLifecycle"),
    ("internal/persistence", "WorkerOfferFormatVersion", "var _ = excluded.WorkerOfferFormatVersion"),
    ("internal/persistence", "WorkerOfferLifetime", "var _ = excluded.WorkerOfferLifetime"),
    ("internal/persistence", "WorkerStartRequest", "var _ excluded.WorkerStartRequest"),
    ("internal/persistence", "WorkerStartResponse", "var _ excluded.WorkerStartResponse"),
    ("internal/persistence", "WorkerStartCommand", "var _ excluded.WorkerStartCommand"),
    ("internal/persistence", "CommitWorkerStart", "var _ = (*excluded.Store).CommitWorkerStart"),
    ("internal/persistence", "VerifyWorkerPollResponse", "var _ = (*excluded.Store).VerifyWorkerPollResponse"),
    ("internal/persistence", "WaitWorkerPoll", "var _ = (*excluded.Store).WaitWorkerPoll"),
    ("internal/persistence", "WorkerExecutions", "var _ = (*excluded.Store).WorkerExecutions"),
    ("internal/management", "WorkerAssignments", "var _ = (*excluded.Catalog).WorkerAssignments"),
    ("internal/management", "ExternalBindings", "var _ = excluded.RuntimePreparation{}.ExternalBindings"),
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
    ("internal/persistence", "LookupBootstrapJobTypeVersion", "var _ = (*excluded.Store).LookupBootstrapJobTypeVersion"),
    ("internal/management", "ExternalJobsEnabled", "var _ = excluded.CatalogOptions{}.ExternalJobsEnabled"),
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
    "internal/controller/systems", "internal/cpractl/cli", "internal/httpserver", "sdk/go/api",
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
        rf"\(\*excluded\.(?P<receiver>Store|Authorizer|Catalog|Client)\)\.{re.escape(symbol)} undefined "
        rf"\(type \*[A-Za-z0-9_]+\.(?P=receiver) has no field or method {re.escape(symbol)}\)",
        rf"excluded\.(?P<field_receiver>CatalogRecord|Policy|JobStorage|RuntimePreparation|CatalogOptions)\{{\}}\.{re.escape(symbol)} undefined "
        rf"\(type [A-Za-z0-9_]+\.(?P=field_receiver) has no field or method {re.escape(symbol)}\)",
    )
    if result.returncode == 0 or not any(re.search(pattern, output) for pattern in expected):
        raise RuntimeError("expected exact unavailable symbol " + symbol + ":\n" + output)


def require_symbols_absent(result: subprocess.CompletedProcess, symbols: tuple[str, ...]) -> None:
    if not symbols or len(set(symbols)) != len(symbols):
        raise ValueError("negative compilation requires distinct expected symbols")
    for symbol in symbols:
        require_symbol_absent(result, symbol)


def json_stream(raw: str):
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        value, end = decoder.raw_decode(raw)
        yield value
        raw = raw[end:]


def selected_source_files(package: dict, root: Path = ROOT) -> dict[str, Path]:
    """Map local and downloaded SDK files to their canonical source identity."""
    module = package.get("Module", {})
    name, module_name = package.get("ImportPath"), module.get("Path")
    if module_name not in (MODULE, SDK_MODULE) or not isinstance(name, str):
        raise ValueError("unexpected module in build-boundary package selection")
    module_dir, package_dir = Path(module["Dir"]), Path(package["Dir"])
    for path in (module_dir, package_dir):
        if not path.is_absolute() or path.resolve(strict=True) != path or not path.is_dir():
            raise ValueError("package directory is not a canonical regular directory")
    if module_name == MODULE and module_dir != root.absolute():
        raise ValueError("application sources do not belong to the selected checkout")
    relative = package_dir.relative_to(module_dir).as_posix()
    expected_import = module_name + ("/" + relative if relative != "." else "")
    if name != expected_import:
        raise ValueError("selected package directory and import path differ")
    prefix = Path("sdk/go") if module_name == SDK_MODULE else Path(".")
    files = package.get("GoFiles", []) + package.get("CgoFiles", [])
    if not isinstance(files, list) or len(files) != len(set(files)):
        raise ValueError("selected Go source inventory is invalid")
    selected = {}
    for filename in files:
        if not isinstance(filename, str) or Path(filename).name != filename or not filename.endswith(".go"):
            raise ValueError("selected Go source is not a basename")
        path = package_dir / filename
        if not stat.S_ISREG(path.lstat().st_mode) or path.resolve(strict=True) != path:
            raise ValueError("selected Go source is not a regular file")
        selected[(prefix / relative / filename).as_posix()] = path
    return selected


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
              "workerExecutionQualified": False, "workspace": env["GOWORK"], "checks": [],
              "goEnvironment": {name: env.get(name, "") for name in (
                  "GOTOOLCHAIN", "GOENV", "GOFLAGS", "GOEXPERIMENT", "GOWORK",
                  "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOSUMDB", "GONOPROXY")}}

    def run(args, *, absent=None, source_text=None):
        started = time.monotonic()
        result = subprocess.run([str(selected_go), *args], cwd=ROOT, env=env,
                                capture_output=True, text=True, timeout=180)
        item = {"command": [str(selected_go), *args], "exitCode": result.returncode,
                "seconds": round(time.monotonic() - started, 3),
                "stdout": result.stdout, "stderr": result.stderr}
        if source_text is not None:
            item.update(source=source_text, sourceSHA256=hashlib.sha256(source_text.encode()).hexdigest())
        report["checks"].append(item)
        if absent:
            require_symbols_absent(result, absent)
            item["expectedUnavailableSymbols"] = list(absent)
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
                contents = ('package main\nimport excluded "' + MODULE + '/' + package +
                            '"\n' + declaration + '\nfunc main() {}\n')
                source.write_text(contents)
                # Report every independent declaration, rather than stopping at
                # the compiler's usual error limit. Each expected name must have
                # its own exact unavailable-symbol diagnostic to pass.
                diagnostics = ["-gcflags=-e"] if absent else []
                result = run(["build", "-mod=readonly", "-p=1", *diagnostics, "-tags=" + selected_tags,
                              "-o", str(artifact), str(source)], absent=absent, source_text=contents)
                return result

            # Compile every declaration in the enabled build. Grouping independent
            # declarations by package avoids linking the same dependency graph for
            # each symbol. The negative build must report every expected missing
            # name; a group with even one available API fails.
            positive = {}
            for package, symbol, declaration in CASES:
                positive.setdefault(package, []).append((symbol, declaration))
            report["symbolCount"] = len(CASES)
            for package, declarations in positive.items():
                build_source(package, "\n".join(item[1] for item in declarations), "externaljobs")
                report["checks"][-1]["expectedAvailableSymbols"] = [item[0] for item in declarations]

            for selected_tags in ("", tags):
                # An unrelated package error must not become exclusion evidence.
                for package, typename in (("internal/persistence", "Store"),
                                          ("internal/httpauth", "Authorizer"),
                                          ("internal/localadmin", "AuthenticationRequest")):
                    build_source(package, "var _ *excluded." + typename, selected_tags)
                for package, declarations in positive.items():
                    build_source(package, "\n".join(item[1] for item in declarations), selected_tags,
                                 absent=tuple(item[0] for item in declarations))

                raw = run(["list", "-mod=readonly", "-tags=" + selected_tags, "-json",
                           *[MODULE + "/" + package for package in PACKAGES]])
                selected_sources = {}
                for package in json_stream(raw):
                    sources = package.get("GoFiles", []) + package.get("CgoFiles", [])
                    if any(name.startswith("worker_") and name.endswith("_external.go")
                           or name in {"worker_external.go", "catalog_jobs_external.go"} for name in sources):
                        raise RuntimeError("default build selected worker implementation: " + package["ImportPath"])
                    for name, path in selected_source_files(package).items():
                        if name in selected_sources:
                            raise RuntimeError("duplicate selected source: " + name)
                        selected_sources[name] = hashlib.sha256(path.read_bytes()).hexdigest()
                report.setdefault("selectedSources", []).append({"tags": selected_tags, "sha256": selected_sources})
                dependencies = run(["list", "-mod=readonly", "-tags=" + selected_tags,
                                    "-deps", "-f", "{{.ImportPath}}", "./cmd/cpractl", "."])
                if any(line == MODULE + "/sdk/go/worker" or line.startswith(MODULE + "/sdk/go/worker/")
                       for line in dependencies.splitlines()):
                    raise RuntimeError("default application imported the external worker module")
                run(["test", "-mod=readonly", "-p=1", "-tags=" + selected_tags, "-run", BOUNDARY_TESTS,
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
