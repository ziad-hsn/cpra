#!/usr/bin/env python3
"""Create an explicit development workspace; never change published go.mod files."""
import argparse
import json
import os
from pathlib import Path
import re
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def module_metadata(module):
    text = (module / "go.mod").read_text()
    name = re.search(r"(?m)^module\s+(\S+)", text)
    version = re.search(r"(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$", text)
    if name is None or version is None:
        raise ValueError("module and Go version are required in " + str(module / "go.mod"))
    requirements = []
    in_require = False
    for line in text.splitlines():
        fields = line.split("//", 1)[0].strip().split()
        if fields == ["require", "("]:
            in_require = True
        elif fields == [")"]:
            in_require = False
        elif in_require and len(fields) == 2:
            requirements.append(tuple(fields))
        elif len(fields) == 3 and fields[0] == "require":
            requirements.append(tuple(fields[1:]))
    return name.group(1).strip('"'), version.group(1), requirements


def workspace_content(modules):
    metadata = [module_metadata(module) for module in modules]
    local = {name: module for module, (name, _, _) in zip(modules, metadata)}
    go_version = max((version for _, version, _ in metadata), key=lambda v: tuple(map(int, v.split('.'))))
    requirements = sorted({(name, version) for _, _, requirements in metadata
                           for name, version in requirements if name in local})
    return ("go " + go_version + "\n\nuse (\n" + "".join(
        "\t" + json.dumps(str(module)) + "\n" for module in modules) + ")\n\n" + "".join(
        "replace " + name + " " + version + " => " + json.dumps(str(local[name])) + "\n"
        for name, version in requirements))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--examples", action="store_true", help="include the integrations module and its AWS/Kubernetes dependency versions")
    parser.add_argument("--no-github-env", action="store_true", help="do not export this workspace to subsequent GitHub Actions steps")
    args = parser.parse_args()
    destination = args.output.resolve()
    modules = [ROOT, ROOT / "sdk/go", ROOT / "sdk/go/worker"]
    if args.examples:
        modules.append(ROOT / "examples/sdk")
    for module in modules:
        if not (module / "go.mod").is_file():
            parser.error(f"missing module: {module}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    # The version-specific replacements also cover unpruned dependency-graph
    # inspection before the candidate tags exist. They live only in this
    # explicit temporary workspace, never a distributable go.mod.
    content = workspace_content(modules)
    # A shared Make prerequisite handles parallel targets in one invocation.
    # Atomic replacement also protects readers in separate Make invocations.
    if not destination.exists() or destination.read_text() != content:
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", dir=destination.parent, prefix=destination.name + ".", delete=False) as output:
                temporary = Path(output.name)
                output.write(content)
            temporary.replace(destination)
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
    if not args.no_github_env and os.environ.get("GITHUB_ENV"):
        with open(os.environ["GITHUB_ENV"], "a") as output:
            output.write("GOWORK=" + str(destination) + "\n")
    print(destination)


if __name__ == "__main__":
    main()
