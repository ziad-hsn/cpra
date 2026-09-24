#!/usr/bin/env python3
"""Verify embedded dashboard inputs and outputs without a frontend toolchain."""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = "dashboard/build-manifest.json"


def inventory(root, paths):
    return {path.relative_to(root).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(set(paths)) if path.is_file()}


def snapshot(root):
    frontend = root / "dashboard"
    assets = root / "internal/httpserver/assets"
    if not (frontend / "src").is_dir() or not (assets / "index.html").is_file():
        raise ValueError("dashboard sources or embedded assets are missing; run make dashboard-build")
    paths = list((frontend / "src").rglob("*")) + list((frontend / "public").rglob("*"))
    paths += [path for path in frontend.iterdir()
              if path.suffix in (".json", ".yaml", ".ts", ".js", ".html")
              and path.name != Path(MANIFEST).name and not path.name.endswith(".tsbuildinfo")]
    paths += list((root / "api").rglob("*.json")) + list((root / "api").rglob("*.yaml"))
    paths += [path for path in (root / "sdk/go").rglob("*.go")
              if not path.name.endswith("_test.go") and "worker" not in path.relative_to(root / "sdk/go").parts]
    paths += [root / name for name in ("sdk/go/go.mod", "sdk/go/go.sum", "sdk/go/LICENSE",
              "scripts/release/recipe.json", "scripts/release/dashboard_build.py", "scripts/release/stage_dashboard.py",
              "scripts/dashboard/build_collection_parser.py", "scripts/dashboard/generate_api.py",
              "scripts/dashboard/inspect_collection_parser.cjs", "scripts/dashboard/asset_manifest.py")]
    paths += list((root / "scripts/dashboard/collectionwasm").glob("*.go"))
    paths += [root / "brand/palette.json", root / "brand/dist/palette.css"]
    outputs = list(assets.rglob("*")) + [root / "LICENSES/dashboard.json", root / "LICENSES/dashboard.txt"]
    return {"schema_version": 1, "inputs": inventory(root, paths), "outputs": inventory(root, outputs)}


def verify(root):
    manifest = root / MANIFEST
    if not manifest.is_file():
        raise ValueError("dashboard build manifest is missing; run make dashboard-build")
    expected = json.loads(manifest.read_text())
    current = snapshot(root)
    if current != expected:
        changed = sorted({name for section in ("inputs", "outputs")
                          for name in set(expected.get(section, {})) | set(current[section])
                          if expected.get(section, {}).get(name) != current[section].get(name)})
        raise ValueError("embedded dashboard is stale; run make dashboard-build; changed: " + ", ".join(changed[:8]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="record inputs and outputs after staging a successful build")
    args = parser.parse_args()
    try:
        if args.write:
            (ROOT / MANIFEST).write_text(json.dumps(snapshot(ROOT), sort_keys=True, indent=2) + "\n")
        else:
            verify(ROOT)
    except (ValueError, OSError) as error:
        parser.exit(1, str(error) + "\n")


if __name__ == "__main__":
    main()
