#!/usr/bin/env python3
"""Regenerate SDK outputs in two isolated source roots and compare all bytes."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def main(go: str, output: Path | None):
    env = dict(os.environ, GO=go, GOENV="off", GOFLAGS="", GOEXPERIMENT="",
               GOTOOLCHAIN="local", GOWORK="off")
    hashes = []
    with tempfile.TemporaryDirectory(prefix="cpra-sdk-generation-") as temporary:
        for name in ("first", "second"):
            root = Path(temporary) / name
            for relative in ("api/openapi", "tools/sdkgen"):
                shutil.copytree(ROOT / relative, root / relative,
                                ignore=shutil.ignore_patterns("__pycache__", ".*"))
            for relative in ("sdk/go/api", "sdk/go/internal/transport", "sdk/go/testdata"):
                (root / relative).mkdir(parents=True)
            subprocess.run(["python3", str(root / "tools/sdkgen/generate.py")],
                           cwd=root, env=env, check=True)
            manifest = json.loads((root / "sdk/go/generation.json").read_text())
            paths = sorted([*manifest["outputs"], "sdk/go/generation.json"])
            candidate = {}
            for relative in paths:
                data = (root / relative).read_bytes()
                if data != (ROOT / relative).read_bytes():
                    raise RuntimeError(f"regeneration differs from checked-in output: {relative}")
                candidate[relative] = hashlib.sha256(data).hexdigest()
            hashes.append(candidate)
    if hashes[0] != hashes[1]:
        raise RuntimeError("SDK generation depends on its source root")
    compiler = subprocess.check_output([go, "version"], env=env, text=True).strip()
    report = {"status": "pass", "compiler": compiler, "sourceRoots": 2,
              "comparedToCheckedInOutputs": True, "outputs": hashes[0]}
    data = json.dumps(report, indent=2) + "\n"
    if output:
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(data)
    print(data, end="")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    main(args.go, args.out)
