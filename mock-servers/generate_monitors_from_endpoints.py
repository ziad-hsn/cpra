#!/usr/bin/env python3
"""
Generate monitor definitions from a list of endpoints using a fixed pulse distribution.

The script expects an endpoints source (text file with one endpoint per line or URL)
and produces a YAML file compatible with the monitor schema used in samples. Endpoints
are assigned without repetition and distributed as:
  * 80% HTTP pulse checks
  * 10% TCP pulse checks
  * 10% ICMP pulse checks
"""
import argparse
import json
import math
import sys
from dataclasses import dataclass
from typing import Iterable, List, Sequence
from urllib.parse import urlparse

import requests
import yaml


class NoAliasDumper(yaml.SafeDumper):
    def ignore_aliases(self, data):
        return True


HTTP_TEMPLATE = {
    "name": "",
    "pulse_check": {
        "type": "http",
        "interval": "5s",
        "timeout": "3s",
        "max_failures": 3,
        "config": {
            "retries": 2,
            "method": "GET",
            "url": "",
        },
    },
    "codes": {
        "red": {
            "dispatch": True,
            "notify": "pagerduty",
            "config": {"url": "pager"},
        },
        "yellow": {
            "dispatch": True,
            "notify": "log",
            "config": {"file": "test-code.txt"},
        },
    },
}

TCP_TEMPLATE = {
    "name": "",
    "pulse_check": {
        "type": "tcp",
        "interval": "10s",
        "timeout": "5s",
        "max_failures": 3,
        "config": {
            "retries": 3,
            "host": "",
            "port": 0,
        },
    },
    "codes": {
        "red": {
            "dispatch": True,
            "notify": "pagerduty",
            "config": {"url": "pager"},
        },
        "yellow": {
            "dispatch": True,
            "notify": "log",
            "config": {"file": "test-code.txt"},
        },
    },
}

ICMP_TEMPLATE = {
    "name": "",
    "pulse_check": {
        "type": "icmp",
        "interval": "15s",
        "timeout": "5s",
        "max_failures": 3,
        "config": {
            "retries": 3,
            "host": "",
        },
    },
    "codes": {
        "red": {
            "dispatch": True,
            "notify": "log",
            "config": {"file": "critical_alerts.log"},
        },
        "yellow": {
            "dispatch": True,
            "notify": "log",
            "config": {"file": "warning_alerts.log"},
        },
    },
}


@dataclass
class Endpoint:
    scheme: str
    host: str
    port: int
    path: str
    raw: str

    @classmethod
    def parse(cls, raw: str) -> "Endpoint":
        parsed = urlparse(raw.strip())
        if not parsed.scheme or not parsed.netloc:
            raise ValueError(f"Invalid endpoint '{raw}'. Expected absolute URL.")

        if parsed.port is None:
            if parsed.scheme == "http":
                port = 80
            elif parsed.scheme == "https":
                port = 443
            else:
                raise ValueError(f"Endpoint '{raw}' missing explicit port.")
        else:
            port = parsed.port

        return cls(
            scheme=parsed.scheme,
            host=parsed.hostname or "",
            port=port,
            path=parsed.path or "/",
            raw=raw.strip(),
        )


def load_endpoints_from_url(url: str) -> List[str]:
    response = requests.get(url, timeout=30)
    response.raise_for_status()
    return [line.strip() for line in response.text.splitlines() if line.strip()]


def load_endpoints_from_file(path: str) -> List[str]:
    with open(path, "r", encoding="utf-8") as fh:
        return [line.strip() for line in fh if line.strip()]


def compute_distribution(total: int) -> Sequence[int]:
    # 80% HTTP, 10% TCP, 10% ICMP while covering total endpoints
    http = int(math.floor(total * 0.8))
    tcp = int(math.floor(total * 0.1))
    icmp = int(math.floor(total * 0.1))

    remainder = total - (http + tcp + icmp)
    # assign remainder to HTTP first, then TCP, then ICMP to preserve bias
    buckets = [http, tcp, icmp]
    idx = 0
    while remainder > 0:
        buckets[idx] += 1
        remainder -= 1
        idx = (idx + 1) % len(buckets)
    return buckets


def chunk_endpoints(endpoints: Sequence[Endpoint], counts: Sequence[int]) -> Sequence[Sequence[Endpoint]]:
    slices = []
    start = 0
    for count in counts:
        end = start + count
        slices.append(endpoints[start:end])
        start = end
    if start != len(endpoints):
        raise ValueError("Distribution counts do not match endpoint total.")
    return slices


def build_http_monitors(endpoints: Iterable[Endpoint], offset: int = 0) -> List[dict]:
    monitors = []
    for idx, endpoint in enumerate(endpoints, start=1 + offset):
        spec = json.loads(json.dumps(HTTP_TEMPLATE))
        spec["name"] = f"HTTP Monitor {idx:05d} (port {endpoint.port})"
        spec["pulse_check"]["config"]["url"] = endpoint.raw
        monitors.append(spec)
    return monitors


def build_tcp_monitors(endpoints: Iterable[Endpoint], offset: int = 0) -> List[dict]:
    monitors = []
    for idx, endpoint in enumerate(endpoints, start=1 + offset):
        spec = json.loads(json.dumps(TCP_TEMPLATE))
        spec["name"] = f"TCP Monitor {idx:05d} (port {endpoint.port})"
        spec["pulse_check"]["config"]["host"] = endpoint.host
        spec["pulse_check"]["config"]["port"] = endpoint.port
        monitors.append(spec)
    return monitors


def build_icmp_monitors(endpoints: Iterable[Endpoint], offset: int = 0) -> List[dict]:
    monitors = []
    for idx, endpoint in enumerate(endpoints, start=1 + offset):
        spec = json.loads(json.dumps(ICMP_TEMPLATE))
        spec["name"] = f"Ping Monitor {idx:05d} (port {endpoint.port})"
        spec["pulse_check"]["config"]["host"] = endpoint.host
        spec.setdefault("metadata", {})["port_hint"] = endpoint.port
        monitors.append(spec)
    return monitors


def generate_monitors(endpoints: Sequence[str]) -> List[dict]:
    parsed = [Endpoint.parse(ep) for ep in endpoints]
    counts = compute_distribution(len(parsed))
    http_eps, tcp_eps, icmp_eps = chunk_endpoints(parsed, counts)

    http_monitors = build_http_monitors(http_eps)
    tcp_monitors = build_tcp_monitors(tcp_eps)
    icmp_monitors = build_icmp_monitors(icmp_eps)

    return http_monitors + tcp_monitors + icmp_monitors


def main():
    parser = argparse.ArgumentParser(description="Generate monitor YAML from endpoints.")
    parser.add_argument("endpoints_source", help="Path to endpoints text file or URL (when --from-url used).")
    parser.add_argument("output_yaml", help="Path for the generated YAML file.")
    parser.add_argument("--from-url", action="store_true", help="Treat endpoints_source as URL.")
    args = parser.parse_args()

    if args.from_url:
        raw_endpoints = load_endpoints_from_url(args.endpoints_source)
    else:
        raw_endpoints = load_endpoints_from_file(args.endpoints_source)

    if not raw_endpoints:
        print("No endpoints provided.", file=sys.stderr)
        sys.exit(1)

    try:
        monitors = generate_monitors(raw_endpoints)
    except ValueError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(1)

    with open(args.output_yaml, "w", encoding="utf-8") as fh:
        yaml.dump({"monitors": monitors}, fh, Dumper=NoAliasDumper, sort_keys=False)

    print(f"Wrote {len(monitors)} monitors to {args.output_yaml}.")


if __name__ == "__main__":
    main()
