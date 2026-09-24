"""Independent standard-library implementation of the documented v1 framing.

Run from any directory. Writes the adjacent v1.json; no CPRa code is imported.
The key and resource values below are public test fixtures, never credentials.
"""

import hashlib
import hmac
import json
from pathlib import Path


KEY = bytes(range(32))


def u64(value):
    return value.to_bytes(8, "big")


def field(value):
    return u64(len(value)) + value


def number(value):
    return field(u64(value))


def mac(domain, *values):
    return hmac.new(KEY, field(domain.encode("ascii")) + b"".join(values), hashlib.sha256).digest()


def source_token(ordinal):
    return f"source.{ordinal:020d}"


def sources_mac(sources):
    fields = [number(len(sources))]
    for ordinal, raw in enumerate(sources, start=1):
        fields += [number(ordinal), field(source_token(ordinal).encode("ascii")), number(len(raw)),
                   field(mac("cpra.collection.source-bytes.v1", raw))]
    return mac("cpra.collection.sources.v1", *fields)


sources = [b"# local source 1\nkind: Monitor\nname: service-api\n", "# source 2\ncredential: fictional-<>&-\u2028\u2029\n".encode("utf-8")]
resources = [
    b'{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"service-api"},"spec":{"n":1.00,"exp":1e+09,"negativeZero":-0,"text":"\\u003c\\u003e\\u0026\\u2028\\u2029"}}',
    b'{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"service-secret"},"spec":{"value":"public-vector-only","escaped\\u004bey":"fictional","integer":18446744073709551615}}',
]
items = []
for ordinal, (identity, raw) in enumerate(zip(["Monitor/service-api", "Credential/service-secret"], resources), start=1):
    position = {"Ordinal": ordinal, "ID": identity,
                "Source": {"Token": source_token(ordinal), "Document": 1, "Item": 1}}
    digest = mac("cpra.collection.item.v1", number(ordinal), field(identity.encode("ascii")),
                 field(source_token(ordinal).encode("ascii")), number(1), number(1), field(raw))
    items.append({"Position": position, "ResourceBytesHex": raw.hex(), "MAC": digest.hex()})

fingerprint = sources_mac(sources)
inventory_fields = [number(len(items)), field(fingerprint)]
for item in items:
    inventory_fields += [number(item["Position"]["Ordinal"]), field(item["Position"]["ID"].encode("ascii")),
                         field(bytes.fromhex(item["MAC"]))]
empty_sources = sources_mac([])
output = {
    "Format": "cpra.collection.hmac-sha256-json-bytes.v1",
    "KeyHex": KEY.hex(),
    "Sources": [{"Token": source_token(i), "BytesHex": raw.hex()} for i, raw in enumerate(sources, start=1)],
    "Items": items,
    "SourceFingerprintHex": fingerprint.hex(),
    "InventoryHex": mac("cpra.collection.inventory.v1", *inventory_fields).hex(),
    "EmptySourcesHex": empty_sources.hex(),
    "EmptyInventoryHex": mac("cpra.collection.inventory.v1", number(0), field(empty_sources)).hex(),
}
Path(__file__).with_name("v1.json").write_text(json.dumps(output, indent=2) + "\n", encoding="utf-8")
