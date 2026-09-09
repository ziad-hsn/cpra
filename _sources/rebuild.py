"""Build from Markdown and replace only inventoried generated files."""
from pathlib import Path, PurePosixPath
import argparse
import json
import shutil
import subprocess
import sys
import tempfile

from verify_site import verify

SOURCE = Path(__file__).resolve().parent
ROOT = SOURCE.parent


def safe_generated_path(value):
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or not path.parts:
        raise ValueError(f"invalid generated path: {value}")
    if path.parts[0] in (".git", ".github", "_sources", ".gitignore", "CNAME"):
        raise ValueError(f"protected source path: {value}")
    resolved = ROOT.joinpath(*path.parts).resolve()
    if not resolved.is_relative_to(ROOT):
        raise ValueError(f"generated path leaves repository: {value}")
    return resolved


def build():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="write a separate site artifact instead of updating the branch")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="cpra-docs-build-") as temp:
        stage = Path(temp)
        subprocess.run([
            sys.executable, "-m", "mkdocs", "build", "--strict",
            "--config-file", str(SOURCE / "mkdocs.yml"), "--site-dir", str(stage),
        ], check=True)
        (stage / ".nojekyll").touch()
        (stage / "site-version.json").write_text((SOURCE / "source-version.json").read_text())
        (stage / "robots.txt").write_text(
            "User-agent: *\nAllow: /\nSitemap: https://ziad-hsn.github.io/cpra/sitemap.xml\n"
        )
        if (ROOT / "CNAME").exists():
            shutil.copyfile(ROOT / "CNAME", stage / "CNAME")
        verify(stage)
        if args.output:
            target = args.output.resolve()
            if target.exists():
                raise SystemExit(f"output already exists: {target}; choose an empty output path")
            if target == ROOT or target.is_relative_to(SOURCE):
                raise SystemExit("output must not replace the repository or documentation sources")
            shutil.copytree(stage, target)
            print(f"Site artifact: {target}")
            return
        inventory = SOURCE / "generated-files.txt"
        previous = inventory.read_text().splitlines() if inventory.exists() else []
        paths = [safe_generated_path(value) for value in previous]
        generated = sorted(p.relative_to(stage).as_posix() for p in stage.rglob("*") if p.is_file())
        new_paths = [(name, safe_generated_path(name)) for name in generated if name != "CNAME"]
        for path in paths:
            if path.is_file():
                path.unlink()
        for name, target in new_paths:
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(stage / name, target)
        for directory in sorted(ROOT.rglob("*"), key=lambda p: len(p.parts), reverse=True):
            if directory.is_dir() and not any(part.startswith((".", "_")) for part in directory.relative_to(ROOT).parts):
                try:
                    directory.rmdir()
                except OSError:
                    pass
        inventory.write_text("\n".join(name for name in generated if name != "CNAME") + "\n")
        print(f"Updated {len(generated)} generated files in {ROOT}")


if __name__ == "__main__":
    build()
