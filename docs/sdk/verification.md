---
title: Verify SDK integrations
description: Run local SDK examples and distinguish contract checks from deployed effects.
cpra_scope: sdk
---

# Verify SDK integrations

Run the example checks after changing an integration. The verifier records source
hashes, compiler, commands, durations and results. Use [versions and availability](../versions.md)
for the current support matrix. Dated engineering reports remain in the repository;
an earlier passing report does not qualify changed source.

## What the examples establish

| Example | Local path | Requires separate verification |
| --- | --- | --- |
| Queue registration | Pending/ack fixture; SDK create/get; duplicate delivery and lost create reply | Actual broker delivery and durable acknowledgments |
| AWS deregistration | AWS SDK serialization, event parsing, SDK conditional changes | EventBridge/SQS accounts, permissions and deployed load balancers |
| Kubernetes Services | Client-go list/watch, Service projection, SDK conditional changes | Cluster DNS/routing, authorization and network policies |
| DAO and SMS | Loopback RPC/SMS, encrypted worker journal, replay of a saved result | Provider accounts and observed gateway/handset delivery |

Fixtures are explicitly labeled. They create no provider account. The custom
worker example is absent from default Go package selection.

## Run the checks

From the repository root, with Go 1.26.10 or Go 1.27.3 (or a newer supported,
patched release) on `PATH`. See [compiler compatibility](../versions.md).

```sh
export GOTOOLCHAIN=local
python3 scripts/sdk/workspace.py --examples --output /tmp/cpra-sdk-examples.work
export GOWORK=/tmp/cpra-sdk-examples.work
python3 scripts/sdk/verify_examples.py --race --out evidence/local/sdk-examples.json
python3 scripts/sdk/reference.py --check
python3 scripts/sdk/sync_guides.py --check
```

Use `--go /absolute/path/to/go` for another compiler. Reports are incremental;
failed commands and changed inputs remain failed results. Race checks require
an appropriate native C toolchain. Root `go test ./...` does not include the
nested SDK, worker or example modules.

Check uncertain results deliberately: retain original identities and operation
handles, reconcile before acknowledging a queue message, and reject stale
configuration versions. Verify default/tagged source selection separately.
A downloaded-module check uses `GOWORK=off` without local replacements; a local
workspace check does not establish public module availability.
