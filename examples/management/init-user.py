#!/usr/bin/env python3
"""Create one new Linux-only local management example; never overwrite sources."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys


def write_new(path: Path, content: bytes) -> None:
    with path.open("xb") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(content)
        output.flush()
        os.fsync(output.fileno())


def initialize(directory: Path) -> None:
    if sys.platform != "linux":
        raise RuntimeError("This example provisions Linux user files only.")
    openssl = shutil.which("openssl")
    if openssl is None:
        raise RuntimeError("OpenSSL is required to create the local test certificate.")
    directory = directory.absolute()
    # Requiring a new directory makes every generated file exclusive. A failed
    # attempt is left for inspection; it is never silently replaced on retry.
    directory.mkdir(mode=0o700, parents=False, exist_ok=False)
    private = directory / "private"
    private.mkdir(mode=0o700)
    old_umask = os.umask(0o077)
    try:
        principals = []
        for name, role in (("oncall", "operator"), ("observer", "reader")):
            token = secrets.token_hex(32)
            write_new(private / f"{role}.token", (token + "\n").encode("ascii"))
            principals.append({
                "id": f"example/{name}",
                "role": role,
                "token_sha256": hashlib.sha256(token.encode("ascii")).hexdigest(),
            })
        write_new(private / "wrapping.key", secrets.token_bytes(32))
        write_new(private / "principals.yaml", json.dumps({"principals": principals}, indent=2).encode() + b"\n")
        # This certificate serves a local example only. No system trust store
        # changes are made, and no account or external certificate service is used.
        subprocess.run([
            openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(private / "server.key"), "-out", str(private / "server.pem"),
            "-days", "7", "-subj", "/CN=localhost",
            "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1",
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        settings = {
            "storage": {"mode": "raft", "directory": str(directory / "state")},
            "management": {
                "enabled": True,
                "policy_file": str(private / "principals.yaml"),
                "tls": {"cert_file": str(private / "server.pem"), "key_file": str(private / "server.key")},
                "encryption": {"backend": "local", "local": {"active_key_file": str(private / "wrapping.key")}},
            },
        }
        write_new(directory / "runtime.yaml", json.dumps(settings, indent=2).encode() + b"\n")
        write_new(directory / "monitors.yaml", b"monitors: []\n")
        for source in (private / "server.pem", private / "server.key"):
            with source.open("rb") as generated:
                os.fsync(generated.fileno())
        for folder in (private, directory, directory.parent):
            descriptor = os.open(folder, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
    finally:
        os.umask(old_umask)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, help="a new directory under a trusted existing parent")
    args = parser.parse_args()
    try:
        initialize(args.directory)
    except (OSError, RuntimeError, subprocess.SubprocessError):
        print("Example initialization failed. Inspect any new directory; existing files were not replaced. No token or key values are printed.", file=sys.stderr)
        return 1
    print("Created a private local example. Follow examples/management/README.md to validate and start it. Tokens and wrapping keys remain in private files.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
