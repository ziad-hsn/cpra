# SDK example verification

The examples are an uncommitted candidate on `codex/go-sdk`, based on application
commit `410fbfb0092d01277b3884cd04151c27443a4226`. The parent commit alone does not
identify these changes. The example verifier hashes its SDK, schema, example, and
reference inputs and records the compiler, commands, durations, and results.

## Observation boundary

| Example | Exercised path | What remains unverified |
| --- | --- | --- |
| Queue registration | In-memory pending/ack queue; real SDK HTTP create/get; duplicate delivery and a lost create reply | Redis/Kafka wire protocol, durable broker operations, production v2 server |
| AWS deregistration | Production AWS SDK serializers against local HTTP fixtures; event parsing; actual SDK monitor create/get/patch | Live CloudTrail/EventBridge/SQS delivery, account permissions, deployed load balancers |
| Kubernetes Services | Production client-go list/watch HTTP; Service-to-monitor projection; actual SDK conditional writes | Real cluster DNS/routing, cluster authorization, production v2 server |
| DAO and internal SMS | Real loopback JSON-RPC/SMS HTTP; real encrypted worker journal; repeated delivery of a recorded outcome after a lost receipt | Production worker dispatcher, blockchain provider account, gateway/handset delivery |

Local fixtures are part of the lessons, and their output labels them as such.
The custom worker example is absent from normal package selection. No provider
account is created or contacted by these finite demos.

## Reproduce the checks

From the repository root, with Go 1.25 or later on `PATH`:

```bash
export GOTOOLCHAIN=local
python3 scripts/sdk/workspace.py --examples --output /tmp/cpra-sdk-examples.work
export GOWORK=/tmp/cpra-sdk-examples.work
python3 scripts/sdk/verify_examples.py --race --out evidence/local/sdk-examples.json
python3 scripts/sdk/reference.py --check
python3 scripts/sdk/sync_guides.py --check
```

Use `--go /absolute/path/to/go` to repeat with a second compiler. Reports are
written incrementally. A failed command or changed candidate input marks the run
failed, preserving the observed output. A missing live configuration never counts
as a passing live result.

## Independent review corrections

Reviewers other than each example's author checked ownership, conditional changes,
input limits, external effects, fixture accuracy, and teaching claims. Corrections
and regressions included:

- A lost create reply is reconciled by the original monitor ID before queue ack;
  a foreign, changed, or mismatched monitor remains pending.
- Queue quotas count actual input bytes, including CRLF, and reject reused event
  IDs with changed content before any request.
- The HTTP fixture rejects duplicate JSON keys and item IDs. Further uploads
  invalidate preflight, and activation checks the captured versions.
- Kubernetes check patches remove obsolete fields without changing disabled,
  snoozed, or other operator control state. Unsupported driver observations are
  held for review.
- AWS custom endpoint overrides cannot be reported as evidence from AWS.
  Unsupported QUIC target identity is rejected explicitly.
- Local provider token validation rejects control characters without exposing
  the token in its error.
- Reference generation excludes private methods and identifies tagged symbols;
  source links resolve to packaged local files rather than an unpublished branch.

The implementation's earlier SDK transport, collection, encrypted-journal, and
v1 server-handler checks are recorded in the repository's
`docs/implementation/go-sdk-verification.md`. They remain separate from the
example evidence and do not qualify the pending v2 server.

## Executed candidate results — 13 September 2026

Environment: Linux amd64 on the current WSL host, with explicit `GOTOOLCHAIN=local`
and the opt-in examples workspace. Both complete verification reports identify
the same source-content SHA-256:

`bf49c6707c6bc220e10a523d9417788ce2bf2708cfbb4c16cf5f18c2066b05e2`

| Check | Result |
| --- | --- |
| Go 1.25.0, default and `externaljobs` tests and vet | Passed |
| Go 1.27.1, default and `externaljobs` race tests and vet | Passed |
| Four finite demos under each compiler | Passed |
| Default worker/DAO exclusion and no application-internal imports | Passed |
| Candidate module selection, readonly module declarations, unchanged dependency graph | Passed |
| Module checksum verification in the candidate workspace | Passed |
| Example dependencies, `govulncheck` v1.7.0 with `externaljobs` on Go 1.27.1 | No vulnerabilities found |
| Python documentation, workspace, archive, and evidence helpers | 15 tests passed |
| Go exported-declaration extractor | Two tests passed |
| All 66 inventory entries resolve to exported SDK methods; reference and guide regeneration | Passed |
| Existing CLI and actual v1 server-handler integration | Passed |
| GitHub Actions workflow, actionlint v1.7.12 | Passed |
| Strict MkDocs site build and link/asset validation | 52 HTML pages, 9,321 links/assets, zero errors |
| Chromium rendering of all ten SDK pages and a packaged source link | Passed; mobile page width stayed within 390 pixels |

The two local reports are `evidence/local/sdk-examples-go1.25.0.json` and
`evidence/local/sdk-examples-go1.27.1.json`. Browser observations are in
`evidence/local/sdk-docs-browser.json`. These reports are local development
evidence, not published release attestations.

An independent `GOWORK=off` lookup correctly could not resolve the unpublished
`sdk/go/v0.1.0-rc.1` tag. Public module download and production v2 integration
remain pending; workspace checks do not replace those gates.

## Package documentation follow-up — 13 September 2026

The earlier results above identify the previous example revision. The README and
package-documentation follow-up adds code walkthroughs, executable Go examples,
module-archive documentation checks, and the [publishing guide](publishing.md).
Both refreshed example reports pass against source-content SHA-256:

`1465b606fa908b0de7224bf3991e2e3afca07564c51af36f7faa30d67d1e325d`

| Follow-up check | Result |
| --- | --- |
| All four finite demos, default/tagged example tests and vet on Go 1.25.0 and 1.27.1 | Passed; 15 checks per compiler, including race builds on 1.27.1 |
| Complete SDK and tagged worker race tests and vet on Go 1.27.1 | Passed |
| Local-proxy module downloads with fresh caches, `GOWORK=off`, and no `replace`, on both compilers | Passed; both versions consumed matching candidate archive contents |
| Required README, license, package overview, and executable example files in each module archive | Passed |
| Nine package/build contexts: four core packages default and tagged, plus the tagged worker | Overviews rendered and executable examples passed from downloaded sources |
| Complete root SDK and worker README Go programs | Compiled; network usage is not claimed to have run against a v2 server |
| Pinned generated models and transport, rebuilt in two independent roots | Identical outputs |
| Documentation/archive/evidence Python helpers and Go overview renderer | 17 Python tests and two Go tests passed |
| Workflow actionlint, source formatting, reference generation, and guide/source synchronization | Passed |

The refreshed reports are `evidence/local/sdk-pkg-examples-go1.25.0.json`,
`sdk-pkg-examples-go1.27.1.json`, `sdk-pkg-go1.25.0.json`,
`sdk-pkg-go1.27.1.json`, and `sdk-pkg-generation.json` in that same directory.
The archive reports record `publicationReady: false` and
`serverQualification: pending` even when their local checks pass.

The new documentation check caught a duplicate collection package overview, which
was removed. Testing also established that `go doc` ignores the custom build tag
in the tested toolchains. Default documentation is checked with `go doc`; tagged
overviews use standard-library `go/doc` on files selected by `go list`. No
untagged worker stub was added. Independent review checked the publishing guide,
archive verifier, renderer, generator change, and teaching claims against source.
