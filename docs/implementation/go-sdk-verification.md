# Go SDK implementation verification

This records local source verification for `codex/go-sdk`, based on application
commit `410fbfb0092d01277b3884cd04151c27443a4226`. The implementation is an
uncommitted candidate at the time of these checks. Its module archive and
generation reports contain content hashes; the parent commit alone does not
identify the changed SDK source.

## Executed checks

Environment: Linux amd64 under the current WSL host. Compiler selection was
explicit with `GOTOOLCHAIN=local`: Go 1.25.0 and Go 1.27.1. Source composition used
a temporary workspace for the application and worker's unpublished SDK
dependency. Independent SDK checks and archive-consumer checks also ran with
`GOWORK=off`.

| Check | Result and boundary |
| --- | --- |
| Application `go test ./...` | Passed on Go 1.25.0 and 1.27.1 |
| Application all-built-in-driver race suite | Passed on Go 1.27.1 with `redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio` |
| Application `go vet ./...` | Passed on Go 1.27.1 |
| Existing API and CLI integration | Passed against actual v1 server handlers, including authentication, numeric monitor routes, state/history/SLO, metrics, and readiness |
| SDK default and `externaljobs` tests | Passed on Go 1.25.0 and 1.27.1 |
| SDK race tests | Passed on Go 1.27.1 in both build variants, including collections |
| Worker tagged tests and race tests | Passed on Go 1.25.0 and Go 1.27.1; race instrumentation on 1.27.1 |
| Worker process termination | Passed at reserved intent, started marker, external-success fixture, and committed-outcome boundaries |
| Generator | Ten outputs match checked-in bytes; two separate generation roots produced matching outputs |
| Private downloaded module consumers | Passed on Go 1.25.0 and 1.27.1 with fresh module caches, `GOWORK=off`, no `replace`, actual HTTP fixture requests, frozen collection loading, and source-content comparison |
| Exclusion | Default consumers fail to compile custom-job APIs and worker imports; tagged consumers compile; default embedded schema excludes extension definitions |
| Vulnerability checks | `govulncheck` v1.7.0 found no vulnerabilities in core default, core tagged, or worker tagged paths on Go 1.27.1 |
| Cross-builds | Windows amd64 collection/worker test binaries and macOS arm64 worker test binary compiled; this is not native execution |
| Release helper contracts | 17 existing release-script tests passed |
| Archive verifier contracts | Four tests passed, including changed/extra downloaded content and indented replacement rejection |
| Workflow and formatting | Actionlint, Go formatting, and whitespace checks passed |

The local machine-readable reports are written to
`evidence/local/sdk-consumer.json`, `evidence/local/sdk-consumer-go1.25.0.json`,
and `evidence/local/sdk-generation.json`.
They are development evidence, not signed release attestations. Public downloads
are tested separately with `scripts/sdk/verify.py --published`; that gate has not
passed because these SDK versions have not been published.

## Independent reviews and corrections

Separate implementers reviewed the core client, worker, collections, and root
integration. The primary review also traced the worker's journal and execution
lifecycle and the SDK transport/input paths. Corrections covered:

- Existing empty, truncated, or incomplete journals cannot silently bootstrap a
  fresh state and lose held actions.
- Admission accounts for remaining record and byte capacity; integer overflow
  cannot bypass source quotas or turn a long retry interval into an immediate retry.
- Definitive heartbeat revocation cancels cooperative work and stops admission;
  oversized receipt identities cannot exceed reserved journal capacity.
- Optional configuration zero/false/empty values survive serialization;
  incomplete successful mutation responses remain uncertain outcomes.
- Iterators retain initial cursors, JSON document streams retain attribution,
  and activation requires validation of the matching frozen content identity.
- Native CI explicitly enters both nested modules; private/public module checks
  compare downloaded contents instead of inferring identity from a local hash.

## Unfinished gates

The production server still implements v1 read operations only. All 66 management
and extension operations are draft SDK contracts tested using HTTP/protocol
fixtures. Those fixtures do not establish durable server CAS, encryption of
server staging, authorization, runtime feature controls, or actual external job
delivery. The worker crash tests terminate a real worker process around local
fixture effects; they do not certify any provider account.

Real v2/external-worker server implementation and interoperability, native
Windows/macOS/arm64 execution, public SDK prereleases, final release artifact
notices/attestations, and downloaded published-version verification remain open.
The new CI jobs describe checks to run; their presence is not evidence that hosted
CI already passed. Provider-account and million-monitor endurance gates are
separate and were not run for this SDK task.
