#!/usr/bin/env python3
"""Reproducibly build/check the base SDK parser, matching runtime and licenses."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / "dashboard/src/import/generated"
FLAGS = ["-trimpath", "-buildvcs=false", "-mod=readonly", "-pgo=off", "-tags=", "-ldflags="]


def encoded(value):
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()


def build_environment():
    # Deliberate allowlist: ambient Go tool options, credentials, NODE_OPTIONS,
    # preloads and frontend environment values are not build inputs.
    environment = {key: os.environ[key] for key in ("PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "SystemRoot", "TEMP", "TMP", "TMPDIR") if key in os.environ}
    recipe = json.loads((ROOT / "scripts/release/recipe.json").read_text())
    environment.update(recipe["environment"])
    environment.update(GOOS="js", GOARCH="wasm", GOWASM="", GOMAXPROCS="2", TZ="UTC")
    return environment


def objects(text):
    decoder = json.JSONDecoder()
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        yield value
        text = text[end:]


def source_digest(go, env):
    sdk = ROOT / "sdk/go"
    selected = {"go.mod", "go.sum", "LICENSE"}
    packages = subprocess.check_output([go, "list", "-mod=readonly", "-deps", "-json", str(ROOT / "scripts/dashboard/collectionwasm/main.go")], cwd=sdk, env=env, text=True)
    for package in objects(packages):
        directory = Path(package.get("Dir", "/"))
        if not directory.is_relative_to(sdk):
            continue
        for name in package.get("GoFiles", []) + package.get("EmbedFiles", []):
            selected.add(str((directory / name).relative_to(sdk)))
    digest = hashlib.sha256()
    for name in sorted(selected):
        digest.update(name.encode() + b"\0" + hashlib.sha256((sdk / name).read_bytes()).digest())
    return digest.hexdigest()


def dependency_notices(go, env, information, sdk_digest):
    inventory, texts = [], {}
    local = [module for module in information.get("Deps", []) if module.get("Path") == "github.com/ziad-hsn/cpra/sdk/go"]
    if information.get("Main") or len(local) != 1 or local[0].get("Version") != "(devel)" or local[0].get("Replace"):
        raise ValueError("Unexpected parser source module; inspect its source provenance")
    main = local[0]
    inventory.append({"ecosystem": "go", "scope": "dashboard-wasm", "name": main["Path"], "version": "source",
                      "source_sha256": sdk_digest, "declared_license": "MIT", "license_files": ["sdk/go/LICENSE"]})
    texts[main["Path"] + "@source/LICENSE"] = (ROOT / "sdk/go/LICENSE").read_bytes()
    for module in sorted(information.get("Deps", []), key=lambda value: value["Path"]):
        if module["Path"] == main["Path"]:
            continue
        if module.get("Replace") or not module.get("Version") or not module.get("Sum"):
            raise ValueError("Parser linked module lacks immutable identity: " + module["Path"])
        resolved = json.loads(subprocess.check_output([go, "mod", "download", "-json", module["Path"] + "@" + module["Version"]], cwd=ROOT / "sdk/go", env=env, text=True))
        if resolved.get("Sum") != module["Sum"]:
            raise ValueError("Parser module checksum differs: " + module["Path"])
        directory = Path(resolved["Dir"])
        licenses = sorted(path for path in directory.iterdir() if path.is_file() and re.match(r"(?i)^(licen[cs]e|copying|notice|copyright)([.\-_].*)?$", path.name))
        if not licenses:
            raise ValueError("Missing linked parser license: " + module["Path"])
        identity = module["Path"] + "@" + module["Version"]
        inventory.append({"ecosystem": "go", "scope": "dashboard-wasm", "name": module["Path"], "version": module["Version"],
                          "module_sum": module["Sum"], "license_files": [path.name for path in licenses]})
        for path in licenses:
            texts[identity + "/" + path.name] = path.read_bytes()
    return inventory, texts


def qualify_normalization(go, node, env, goroot, wasm, temporary):
    """Check immutable wire vectors, not just parity between current decoders."""
    host = json.loads(subprocess.check_output([go, "env", "-json", "GOHOSTOS", "GOHOSTARCH"],
                                             cwd=ROOT / "sdk/go", env=env, text=True))
    native_env = dict(env, GOOS=host["GOHOSTOS"], GOARCH=host["GOHOSTARCH"])
    paths = []
    for tag in ("", "externaljobs"):
        artifact = Path(temporary) / ("native-" + (tag or "base") + (".exe" if host["GOHOSTOS"] == "windows" else ""))
        flags = [flag for flag in FLAGS if not flag.startswith("-tags=")] + ["-tags=" + tag]
        subprocess.run([go, "build", *flags, "-o", str(artifact), str(ROOT / "scripts/dashboard/collectionwasm/native_fixture.go")],
                       cwd=ROOT / "sdk/go", env=native_env, check=True)
        paths.append(str(artifact))
    result = json.loads(subprocess.check_output([node, str(ROOT / "scripts/dashboard/verify_collection_normalization.cjs"),
                                                str(goroot / "lib/wasm/wasm_exec.js"), str(wasm), *paths],
                                               cwd=ROOT, env=env, text=True, timeout=60))
    fixture_bytes = (ROOT / "sdk/go/collection/testdata/file-base-v1.json").read_bytes()
    fixtures = json.loads(fixture_bytes)
    if result.get("result") != "passed" or result.get("profile") != "cpra.file.base.v1" or result.get("nativeBuilds") != 2 or result.get("wasmBuilds") != 1 or result.get("fixedCases") != len(fixtures["cases"]) or len(fixtures["cases"]) < 30:
        raise ValueError("File normalization qualification did not complete")
    return hashlib.sha256(fixture_bytes).hexdigest()


def build(go, node, check=False):
    recipe = json.loads((ROOT / "scripts/release/recipe.json").read_text())
    env = build_environment()
    actual = subprocess.check_output([go, "env", "GOVERSION"], env=env, text=True).strip()
    if actual != recipe["go_version"]:
        raise ValueError(f"Parser requires {recipe['go_version']}; found {actual}")
    if subprocess.check_output([node, "--version"], env=env, text=True).strip() != recipe["frontend"]["node_version"]:
        raise ValueError("Parser inspection requires the pinned Node compiler helper")
    goroot = Path(subprocess.check_output([go, "env", "GOROOT"], env=env, text=True).strip())
    sdk = ROOT / "sdk/go"
    modules_before = {name: (sdk / name).read_bytes() for name in ("go.mod", "go.sum")}
    subprocess.run([go, "mod", "verify"], cwd=sdk, env=env, check=True, stdout=subprocess.PIPE)
    with tempfile.TemporaryDirectory(prefix="cpra-browser-parser-") as temporary:
        wasm = Path(temporary) / "collection-parser.wasm"
        subprocess.run([go, "build", *FLAGS, "-o", str(wasm), str(ROOT / "scripts/dashboard/collectionwasm/main.go")], cwd=sdk, env=env, check=True)
        information = json.loads(subprocess.check_output([node, str(ROOT / "scripts/dashboard/inspect_collection_parser.cjs"),
                                                         str(goroot / "lib/wasm/wasm_exec.js"), str(wasm)], env=env, text=True, timeout=30))
        if information.get("GoVersion") != recipe["go_version"]:
            raise ValueError("Linked parser compiler identity differs")
        if information.get("NormalizationProfile") != "cpra.file.base.v1":
            raise ValueError("Linked parser normalization profile is unsupported")
        normalization_fixtures = qualify_normalization(go, node, env, goroot, wasm, temporary)
        digest = source_digest(go, env)
        inventory, notices = dependency_notices(go, env, information, digest)
        runtime = (goroot / "lib/wasm/wasm_exec.js").read_bytes()
        inventory.append({"ecosystem": "go-toolchain", "scope": "dashboard-wasm", "name": "Go browser runtime", "version": actual,
                          "declared_license": "BSD-3-Clause", "license_files": ["GO-LICENSE.txt"],
                          "sha256": hashlib.sha256(runtime).hexdigest(), "source_file": "dashboard/src/import/generated/wasm_exec.js", "source_identity": actual})
        notices["Go/" + actual + "/LICENSE"] = (goroot / "LICENSE").read_bytes()
        notice = b"Licenses for the linked browser collection parser and matching Go runtime.\n"
        for name, raw in sorted(notices.items()):
            notice += b"\n" + b"=" * 72 + b"\n" + name.encode() + b"\n" + b"=" * 72 + b"\n" + raw + b"\n"
        contents = {"collection-parser.wasm": wasm.read_bytes(), "wasm_exec.js": runtime,
                    "GO-LICENSE.txt": (goroot / "LICENSE").read_bytes(), "DEPENDENCIES.json": encoded(inventory), "LICENSES.txt": notice}
        metadata = {"compiler": actual, "target": "js/wasm", "externaljobs": False, "recipe": FLAGS,
                    "normalization_profile": information["NormalizationProfile"],
                    "normalization_fixtures_sha256": normalization_fixtures,
                    "sdk_source_sha256": digest, "linked_modules": information.get("Deps", []),
                    "files": {name: {"bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()} for name, raw in contents.items()}}
        contents["BUILD.json"] = encoded(metadata)
        if any((sdk / name).read_bytes() != raw for name, raw in modules_before.items()):
            raise ValueError("Parser build modified the SDK module files")
        for name, raw in contents.items():
            target = OUTPUT / name
            if check:
                if not target.exists() or target.read_bytes() != raw:
                    raise ValueError("Generated parser input differs: " + name)
            else:
                OUTPUT.mkdir(parents=True, exist_ok=True)
                target.write_bytes(raw)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--node", default="node")
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    build(args.go, args.node, args.check)
