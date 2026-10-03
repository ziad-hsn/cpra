# Release engineering

The [dashboard finalization and shipping plan](implementation/dashboard-shipping-plan.md)
adds the integrated management, SDK/worker, provider and performance gates and the
decision to keep candidates private until those gates pass. Publication is now
locked while that combined gate is unfinished. Release qualification requires
an explicitly private repository and `publish=false`; missing visibility,
public/internal repositories and publication requests fail before checkout or
artifact creation. The publisher also rejects direct invocation before reading
release payloads or running external operations. This temporary lock is not a
completed evidence gate or proof that any release requirement passed.

CPRa contains three separately versioned Go modules: the application, `sdk/go`, and
`sdk/go/worker`. The distribution also includes two command-line programs, six
native archives, four Linux packages, a two-platform Linux OCI image, production
Compose configuration and an OCI Helm chart. A build or a cross-compilation alone
does not establish platform support. The release workflow requires the
corresponding native execution, installation, storage and deployment evidence
before publishing an installable version tag.

Provider account verification and comparative million-monitor performance remain separate gates. On 1 October 2026 the user removed the 24-hour endurance test from the requested work and mandatory completion checks. No 24-hour endurance result is established. Packaging tests must not be described as provider certification, a capacity result, an SLA or distributed failover evidence.

The selected first candidate is `v0.1.0-rc.1` for the application, SDK and worker,
with chart version `0.1.0-rc.1`. Its application and image tag retain the leading
`v`; the nested module tags are `sdk/go/v0.1.0-rc.1` and
`sdk/go/worker/v0.1.0-rc.1`. These are selected versions, not available downloads.
Source review, candidate freeze and qualification must finish before publication.

## Private evidence assessment

`scripts/release/qualification.py` collects the packaging, SDK consumer, external
worker, Go build matrix, management, A2A, security, browser, accessibility,
storage-upgrade, provider, comparative-performance, publication-procedure and
independent-review readers. It reads their original
retained inputs rather than accepting a summary with `status: pass`. The reader
implementation must match the frozen source inventory. Component identities and
input bytes are checked again after inspection. Performance evidence must also
match the controller bytes in the final native archive.

Supply a private JSON configuration, with paths relative to that file:

```json
{
  "schema_version": 1,
  "components": {
    "packaging": {"directory": "artifacts"},
    "sdk": {
      "dependencies": "dependencies",
      "consumers": ["consumers/minimum.json", "consumers/release.json"]
    },
    "external_worker": {"campaigns": ["workers/minimum.json", "workers/release.json"]},
    "build_matrix": {"campaigns": ["builds/minimum.json", "builds/release.json"]},
    "security": {"campaigns": ["builds/minimum.json", "builds/release.json"]},
    "management": {"campaigns": ["builds/minimum.json", "builds/release.json"]},
    "a2a": {
      "campaigns": ["builds/minimum.json", "builds/release.json"],
      "build": "provider-build",
      "reports": ["providers/agent-effects.json"]
    },
    "browser": {"campaigns": ["browser/minimum-base.json", "browser/minimum-external.json", "browser/release-base.json", "browser/release-external.json"]},
    "storage_upgrade": {
      "previous_release": "previous-artifacts/RELEASE.json",
      "previous_distribution": "previous-artifacts",
      "campaigns": ["upgrades/upgrade-linux_amd64.json", "upgrades/upgrade-linux_arm64.json"]
    },
    "providers": {"build": "provider-build", "reports": ["providers/account-effects.json"]},
    "benchmark": {"builds": "benchmark/builds", "comparison": "benchmark/comparison", "faults": "benchmark/faults"},
    "publication_plan": {"plan": "/private/plans/publication.json", "dependencies": "/private/dependencies"}
  }
}
```

Run:

```sh
make release-assess RELEASE_DIR=/private/artifacts \
  QUALIFICATION_CONFIG=/private/qualification.json \
  QUALIFICATION_OUT=/private/assessments/result.json
```

The output
must be outside the retained input directories and use a new filename. This
command runs no providers, containers, benchmarks or publication actions. It
neither requires nor starts an endurance test.

The report keeps absent and invalid evidence separate. All fourteen component
readers exist; a reader does not establish execution or review. Complete management/A2A qualification,
accessibility, security, execution of the complete frozen build matrix, actual storage upgrades,
authenticated independent review and the publication procedure remain explicit
requirements. This collection step is not the completed release gate. It always
records `release_gate_passed: false` and `publication_locked: true`; exit status
1 means invalid evidence and 2 means incomplete qualification. Both the workflow
and direct publisher remain locked.

The private workflow now runs an assessment after its build, compatibility,
native, package, image, chart, documentation, SDK-consumer and worker jobs finish.
The worker jobs explicitly run the 37 local crash/capacity cases and five actual
minutes of mixed load on both compilers, with race instrumentation on the release
compiler. They retain original Go logs, source inventories, SDK archive bytes,
dependency graphs and compiler identities. These are test executables, not proof
that a native release service or a million-monitor fleet was exercised.

The assessment downloads each artifact into its own directory and reads the raw
SDK, worker and build-matrix reports and original module payloads. Management
inspection uses those same build records and their retained canonical API inputs;
it requires operation-level SDK results and separate server/controller/CLI checks.
The A2A assessment uses the same original build records, their separately built
upstream reference executable and 21 interoperability cases. Its configured
`build` and `reports` fields are optional only as a pair: without both, actual
agent effects remain incomplete. Private workflow staging supplies the build
records but does not invent provider configuration or live-effect reports.
Packaging files are staged without rewriting their contents;
conflicting filenames or case aliases are rejected. A failed or missing upstream
job remains visible in the assessment. An SDK summary alone cannot satisfy the
SDK component.

For an already-downloaded private workflow, the equivalent local command is:

```sh
python3 -B scripts/release/qualification.py \
  --release /private/artifacts/release-metadata/RELEASE.json \
  --artifacts /private/artifacts \
  --stage /private/assessment-stage \
  --out /private/assessments/result.json
```

Use a new staging directory and result filename, outside the original artifact
directories. This workflow connection does not establish completed worker
execution or provide missing management, accessibility, security, provider, comparative-performance or review
evidence and does not unlock publication. The optional benchmark preparation
requires the same frozen metadata and source inventory as the release, and its
schema-3 report retains the candidate's release linker fields.

The compatibility job uses the extracted frozen source and records all seven
application/SDK/worker module and tag combinations, including all built-in drivers
together with `externaljobs`. Both compilers run race-enabled tests and vet.
The release compiler also records two-root SDK regeneration, formatting, pinned
frontend tools, frozen-lockfile installation, dashboard types/lint/tests and a
matching embedded-asset rebuild. The [build verification procedure](validation.md#frozen-build-verification)
explains the retained inputs and coverage boundaries. Adding this recorder and
reader does not establish that the complete frozen workflow has executed.

The optional `previous_run_id` dispatch input enables the native storage-upgrade
job on Linux amd64 and arm64. It selects a completed manual release-workflow run
from the same private repository. Its source must match the previous manifest,
and its artifact-build job must have succeeded; this does not qualify the
previous release as a whole. Before running either owner, the job validates the
frozen candidate sources and compares the staged executables with both native
archives. The job retains the previous metadata/archive, original schema-2
results and copied process logs. Fixture keys and databases are removed.

The storage reader checks log-only and snapshot-plus-log recovery, preserved
identities, incident ownership and immutable events, held interrupted actions,
confirmed delivery, stopped backup/restore and independent target counters.
Missing native platforms keep its result incomplete. Same-format upgrades and
optional worker state need separate scenarios. Omitting the previous run keeps
this gate pending. Hash consistency does not authenticate execution or replace
independent review, and no endurance run is started.

## Publication procedure and independent review

[publication_evidence.py](review/source/scripts/release/publication_evidence.py.txt) checks a
plan against the complete checksummed distribution and the original SDK/worker
`.zip`, `.mod` and `.info` payloads. Module versions must agree with the dependency
graph and executed SDK consumer reports. SDK and worker tags use their nested
module prefixes; the application tag uses the application version. The plan also
binds the source-owned procedure, image index and both platform subjects, chart
version/image digest, signing identity, issuer, offline inputs and resume rules.

Prepare a plan only after final private assembly has recorded `OCI.json`, the
chart and required SBOMs. The command reads existing bytes and writes one new
private JSON file; it performs no signing, upload or publication:

```sh
python3 -B scripts/release/publication_evidence.py \
  --release /private/distribution/RELEASE.json \
  --distribution /private/distribution \
  --dependencies /private/dependencies \
  --out /private/plans/publication.json
```

The private workflow assembles the image layout, inventories both platforms and
packages the chart before assessment. Frozen execution and readback evidence must
complete the graph before a plan can pass inspection. The retained publisher also needs exact-blob upload, nested
module steps and interruption handling. Preparing or reading a plan does not
qualify these executions. Missing final OCI/chart bytes are an error.

The offline [image assembler](review/source/scripts/release/oci_assembly.py.txt)
uses both retained build transports and the saved runtime image reference for
each Linux architecture. It rechecks their actual blobs and uncompressed layer
digests, preserves the platform manifests and creates one deterministic index.
This follows the [OCI image layout](https://github.com/opencontainers/image-spec/blob/v1.1.1/image-layout.md)
and [image index](https://github.com/opencontainers/image-spec/blob/v1.1.1/image-index.md)
contracts. It performs no registry, provider or Docker operation.

```sh
python3 -B scripts/release/oci_assembly.py \
  --release /private/metadata/RELEASE.json \
  --reports /private/image-transports \
  --out /private/image-assembly
python3 -B scripts/release/oci_assembly.py --verify \
  --release /private/metadata/RELEASE.json \
  --reports /private/image-transports \
  --out /private/image-assembly
```

The reports directory must retain `image-repro-amd64.json`,
`image-repro-arm64.json` and each report's saved reference and two build archives.
Output must be new, separate from the inputs and free of path aliases. The default
output quota is 1.5 GiB; `--max-bytes` changes it explicitly for a measured release
workspace. The layout, `OCI.json` and `RELEASE.json` are retained alongside the
assembly record. Copy these artifacts and the original transports together when
moving private evidence. Verification re-reads their bytes rather than accepting
the record's status field.

[Chart packaging](review/source/scripts/release/chart.py.txt) checks the source
inventory and packages against this already calculated index. Its optional
`--report` retains source hashes and the final package digest. It refuses an
existing package and bounds both compressed input and physical archive expansion.
The private assessment keeps the OCI layout separate from distribution assets
and compares the final distribution's subjects with that layout. Missing assembly
leaves packaging incomplete. Static Helm lint/package results do not establish
Kubernetes storage, probe, rollout or upgrade behavior.
For offline `helm template`, select an intended supported Kubernetes version
explicitly, such as `--kube-version 1.35.8`. Helm's built-in default can be newer
than the chart's declared support range; it does not establish a tested cluster.

The [image inventory tool](review/source/scripts/release/image_sbom.py.txt) reads
the verified OCI layout directly using checksum-pinned Syft 1.51.1. It selects
`linux/amd64` and `linux/arm64` explicitly, avoiding the scanner's default platform.
This uses Syft's documented [local OCI scan target](https://oss.anchore.com/docs/guides/sbom/scan-targets/).
No Docker import, image tag resolution or registry credentials are required.

```sh
python3 -B scripts/release/image_sbom.py \
  --release /private/metadata/RELEASE.json \
  --assembly /private/image-assembly \
  --reports /private/image-transports \
  --syft /private/tools/syft \
  --out /private/image-sboms
python3 -B scripts/release/image_sbom.py --verify \
  --release /private/metadata/RELEASE.json \
  --assembly /private/image-assembly \
  --reports /private/image-transports \
  --out /private/image-sboms
```

Generation requires a new output directory. It has a 512 MiB disk quota, including
the temporary scanner cache, and a 120-second deadline for each platform. Change
the quota explicitly with `--max-bytes` for a measured release workspace. Ordinary
JSON documents and scanner logs are limited to 16 MiB. Scanner processes receive
only the basic execution environment and their private temporary/cache paths;
provider credentials and ambient scanner overrides are not inherited. A failed
generation removes its owned output and stops its scanner.

Retain the complete directory: original native Syft and SPDX documents for each
platform, scanner logs, fixed scanner configuration, frozen release metadata,
dashboard dependency inventory, normalized final SPDX documents and the inventory
record. Verification checks the selected manifest/configuration digests and bytes,
configuration architecture, package identities in both scanner formats and the
frozen dashboard inputs. Syft's empty architecture summary for this layout cannot
substitute for the original configuration. Source time and stable ordering make
the final documents independent of scanner time and working-directory location;
the original scanner documents remain unchanged.

Only `cpra-image-linux-amd64.spdx.json` and `cpra-image-linux-arm64.spdx.json` join
the distribution. The private assessment compares those exact bytes with their
original scans. Missing originals leave packaging incomplete; a forged or changed
inventory is invalid evidence. An SBOM is a dependency inventory, and its successful
generation does not qualify native execution, provider behavior, performance,
independent review or publication.

The ordered publication procedure is:

1. Freeze and independently review the source, complete execution evidence and
   artifact graph. Keep candidates private while any required gate is open.
2. Check absence of every proposed Go tag, application/chart version and public
   release. An uncertain response does not establish absence.
3. Sign the exact final checksums and image subjects; attest every intended
   artifact digest. Retain the bundles and trusted roots for offline verification.
4. Download and verify the private staged bytes, signatures and provenance
   against the expected repository, workflow, issuer, source and subject digests.
5. Publish `sdk/go/vSDK_VERSION`, then verify the public module download against
   the qualified module bytes with `GOWORK=off`, public checksum verification and
   no local replacement or private checksum exception.
6. Publish `sdk/go/worker/vWORKER_VERSION`, then verify its downloaded bytes and
   SDK dependency in the same way.
7. Publish the application tag only after both dependency readbacks pass.
   Attach verified assets before publishing the immutable release.
8. Publish the tested image/index and chart aliases. Verify fresh public
   downloads, exact digests and signatures across every distribution route.
9. Publish documentation identifying the same source and evidence.

These stages are represented by the fourteen identifiers in the reader's
`STEPS` contract. They describe the required procedure, not completed actions.
Progress must be recorded privately. Resume only the same source and artifact
digests, recheck every already-created object, and stop on any mismatch. Never
move a published tag or replace a released asset. Corrections require a new
version. Signature and attestation verification have separate roles; see the
[Sigstore verification documentation](https://docs.sigstore.dev/cosign/verifying/verify/)
and [GitHub offline attestation procedure](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline).

### OCI transfer and restart

The implementation is in [registry.py](review/source/scripts/release/registry.py.txt),
[publish.py](review/source/scripts/release/publish.py.txt) and the
[transfer tests](review/source/scripts/release/test_registry.py.txt).

The publisher's transfer helper uses checksum-pinned ORAS 1.3.4 to upload the
preserved configs and layers, each platform manifest, and the original index,
in that order. Every upload addresses the recorded digest. It creates no tags or
aliases and does not reconstruct the image through Docker. Readback hashes the
entire returned object and compares its size and manifest media type. Header or
tag resolution alone does not establish matching bytes. This follows the
[OCI distribution contract](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md)
and ORAS's [manifest upload interface](https://oras.land/docs/commands/oras_manifest_push/).

Before starting ORAS, the transport copies both the reviewed executable and one
payload into its private working directory, checking the recorded size and
SHA-256 during the copy. Only these copies are passed to the subprocess. Source
links, hard links, special files and changes during copying fail before upload.
Copies use read-only permissions; the executable also has owner execute access.
Existing workspace usage and both copies count against the 1.5 GiB quota, and
copying consumes the upload deadline. This requires enough free workspace for
one additional payload and tool copy. Staging files are removed when the attempt
returns; original progress and remote byte verification remain authoritative
after interruption. The private workflow and filesystem owner remain trusted.

A private, exclusively locked progress file binds the registry, source identity
and complete object graph. Started and verified markers are synchronously
flushed. After interruption, the helper rechecks remote bytes even for completed
markers. An accepted upload whose reply was lost is reused after verification;
a missing previously verified object or any differing bytes stops continuation.
A partially written final marker may be removed only after the original header
and complete preceding records are validated. Progress is bounded to 8 MiB, and
space for both markers is reserved before another upload.

Authentication uses a temporary file readable only by its owner. Child processes
receive an isolated environment and bounded storage/output/deadline settings.
Registry authorization never follows a redirect. Blob reads can use a signed
HTTPS redirect to `pkg-containers.githubusercontent.com` without forwarding
registry credentials; other destinations and further redirects fail. GitHub
lists this host in its [container-package network requirements](https://docs.github.com/en/actions/reference/runners/self-hosted-runners).

The explicit local integration suite installs no container engine and uses a
checksum-pinned Distribution 3.0.0 process bound to an ephemeral loopback port:

```sh
python3 -B scripts/release/install_tools.py --out /absolute/private/tools oras distribution helm3 helm4
python3 -B scripts/release/test_registry.py --integration \
  --tools /absolute/private/tools --work /absolute/private/work
```

CI runs this suite explicitly. It forces process termination after actual remote
acceptance, verifies restart and a second readback, and checks that only the
requested local image/chart versions exist. The digest-only image transfer creates
no tags. Its small OCI and Helm fixtures do not establish runnable CPRa images,
Kubernetes operation or GHCR-account qualification. The public CLI and workflow
remain locked. Real-account signing, final artifact qualification and execution
of the complete protected workflow remain open.

### Chart publication and restart

The chart helper in [chart.py](review/source/scripts/release/chart.py.txt) preserves
the tested `.tgz` package. It reads bounded chart metadata and checks its chart
name/version, application version, source commit and immutable image binding.
Duplicate YAML keys, aliases, oversized metadata, archive links, traversal and
missing required files are rejected before registry access. It creates a
deterministic OCI configuration and manifest, using the original source timestamp
instead of publication time. The configuration and chart layer use Helm's
documented [OCI media types](https://helm.sh/docs/topics/registries/#helm-chart-manifest).

Configuration and archive blobs precede the manifest and the explicit chart
version. An exclusively locked, synchronously flushed `chart.jsonl` journal binds
their original digests to the release and registry. Every resumed object is read
and hashed again. A matching version without original intent, conflicting bytes,
a missing completed object or an uncertain registry response stops continuation.
The chart's version has no `v` prefix; accepting chart versions does not relax
application image version validation. The protected workflow must remain the
sole writer because registry tag creation is not conditional.

The workflow's `chart` phase follows immutable release verification and the
original image-version phase. It verifies downloaded signed assets and the
complete image graph before publishing the chart. It uses the shared exact-byte
ORAS transport rather than running another Helm push. The chart journal is
retained with the other original progress records and revalidated on import.
All mutating phases remain locked pending the full private qualification gate.

The [chart publication contracts](review/source/scripts/release/test_chart_publication.py.txt)
cover lost replies, source changes, version conflicts, missing objects, journal
ordering, quotas and concurrent owners. The explicit local registry suite kills
separate processes after configuration and version acceptance, then verifies
continuation without additional uploads. Actual Helm 3.22.0 and 4.0.4 pull the
identical package by version and by digest. Digest pulls use a different output
filename; the retained failed test and diagnosis distinguish that filename
assumption from byte integrity. These are local minimal-chart transport tests,
not production-account signing or CPRa cluster execution evidence.

### Protected workflow and progress restoration

The protected publication job now consumes the retained OCI assembly directly.
It does not import the image into Docker, push reserialized platform manifests or
rebuild the index. The original `OCI.json`, image inventories and tested chart
archive are copied into the distribution only after their source, size and digest
checks pass. The release compiler, frozen private SDK/worker dependency payloads,
reviewed ORAS binary and private working/progress directories are supplied to
every publisher phase. The private-assessment dependency and all existing
publication locks remain enforced.

The image-version helper in
[publish.py](review/source/scripts/release/publish.py.txt) binds one explicit
application version to the exact original index. It rechecks the complete remote
image graph and records intent before the tag write. Existing matching tags
require original progress; conflicting bytes and missing completed tags cannot
be replaced. A lost reply or process kill resumes through byte readback. ORAS's
[manifest push operation](https://oras.land/docs/commands/oras_manifest_push/)
accepts the original manifest file for either a digest or a tag. There is no
atomic create-if-absent registry operation here: the protected workflow must
retain sole publication ownership. Mutable aliases such as `latest` are not
introduced by this helper.

An image-version phase also requires the original application Git tag,
non-draft immutable release and repeated verification of downloaded signed
assets. The workflow keeps registry credentials in its owned temporary directory;
only progress journals are retained. Credentials are provided to login operations
through standard input and removed during cleanup.

Release drafts and assets use the bounded HTTP transport in
[release_publication.py](review/source/scripts/release/release_publication.py.txt).
Its [recovery tests](review/source/scripts/release/test_release_publication.py.txt)
exercise real loopback HTTP and forced process termination. Intent precedes each
remote mutation. Verified receipts retain the original release or asset ID;
resumption rechecks that identity, metadata, size and downloaded digest. Changed
objects, missing completed assets, unrelated drafts and partial uploads stop the
operation. GitHub documents that an upstream upload failure may leave an empty
`starter` asset; this publisher holds it for explicit review rather than deleting
and replacing it automatically. [GitHub release asset API](https://docs.github.com/en/rest/releases/assets).

Each asset is copied and digest-verified in an explicitly private workspace
before its POST. The HTTP body reads the opened verified copy after its temporary
filename is removed, so changing the source cannot change the bytes being sent.
One asset is staged at a time, with a 1 GiB asset ceiling and 1.5 GiB working-space
quota. An uncertain POST is never automatically repeated. Source changes still
stop the enclosing publication operation; readback and the original journal
decide whether that same remote asset can be reused.

All staged assets must pass checksum, signature, provenance and package-evidence
verification before SDK publication. The module helper then verifies the SDK,
worker and application tags in dependency order. The original application draft
is published once; an uncertain publish reply resumes by checking the immutable
release and every downloaded asset. Cleanup deletes only the original temporary
draft, after final verification, and never deletes its Git reference. Read-only
verification before image promotion uses the same bounded byte transport.

The progress artifact also retains a `signed` directory containing the original
checksum file, Sigstore checksum/provenance bundles, public trusted-root snapshot
and source/asset ledger. This is public verification material, with a 64 MiB cache
limit; it contains no registry credentials. Local `capture-signing` and
`restore-signing` phases preserve those bytes without enabling publication.
Changed unsigned inputs, cache corruption, links or conflicting bundles fail
before restoration. Fresh unsigned checksums may be replaced only when they are
the exact checksum list derived from those same unsigned files. The protected
workflow restores original material before OCI/release continuation and verifies
signatures again. It cannot treat the retained ledger as signing approval.

`previous_publication_run_id` selects an explicit completed run of the same
manually dispatched `main` workflow. Authenticated run metadata must match the
repository identity, workflow, source commit and release inputs before an
artifact is selected. The artifact name also identifies the run attempt.
[GitHub's artifact client](https://github.com/actions/toolkit/blob/main/packages/artifact/README.md)
supports cross-run downloads with `actions:read` permission. Downloaded progress
is imported into a new private directory with mode `0700` and regular files with
mode `0600`. Unexpected files, links, excessive sizes, different source identities
or existing destinations fail before copying. The original journal bytes are
preserved; each stage validates them under its exclusive lock and rechecks remote
state. A marker alone cannot establish successful publication.

These local recovery checks do not establish real GitHub/GHCR, keyless Sigstore
identity or frozen CPRa candidate qualification. The image-bundle and referrer
recovery contract below has local verification; the complete private assessment
and independent review still require completion.
Public CLI and workflow locks remain enforced.

The workflow retains these journals after success or failure and removes its own
working credentials/caches. If cancellation prevents retention or the artifact
has expired, continuation cannot assume that a missing local journal means an
unused version. An unrelated matching tag remains a conflict requiring operator
review.

The OCI, module, release-asset, signing-cache, image-referrer and chart helpers
implement continuation using their original identities and bytes. This does not
complete real-account publication, the entire signing workflow, frozen
qualification or independent review. The executable CLI blocks every mutating phase. Its local
`prepare` phase only validates and imports progress; it has no credentials or
network operations. The
[publisher tests](review/source/scripts/release/test_publish.py.txt) cover the
source/run boundaries, lock behavior and workflow payload selection. Real local
ORAS/Distribution crash tests exercise exact index tagging; they do not qualify
GitHub, GHCR, signatures or runnable CPRa images.

### Original release-asset signatures and provenance

The publisher retains the selected trusted-root snapshot and exact checksum
manifest before requesting asset provenance. Its separate `asset-signing.jsonl`
journal commits each generation intent synchronously. Original provenance is
saved before checksum signing begins; the original checksum signature is saved
and verified before a complete signing cache can be captured. Partial progress
survives workflow restoration, including its original inputs and sealed outputs.

Continuation reuses those bytes and never invokes the signer again for an
interrupted generation. If intent exists without recoverable output, the operation
stops for inspection of the original run. A corrupt or conflicting output cannot
be replaced automatically. The checksum signer cryptographically verifies the
original provenance first; binding records alone cannot authorize signing.
Independent release/image/chart promotion requires complete original asset
generation progress and still verifies downloaded signatures and provenance.

The pinned [GitHub attestation action](https://github.com/actions/attest/blob/1e69f48acb82d1966a394da916b4c1698aa569d6/src/main.ts)
can contact signing services before its output bundle is written. CPRa records
intent before invoking it, captures its original output immediately afterward,
and holds an interrupted generation whose output was not retained. A rerun does
not establish that an earlier signing request had no external effect.

Imported and copied signing material has explicit byte limits, exact file
inventories and inspected digests. Active progress uses private owned directories,
restrictive files and an exclusive journal lock. The local preparation/capture
phases perform no external signing or publication. Actual signing and publication
phases remain locked behind the complete release gate.

The mandatory local signing-recovery fixture uses ephemeral keys and synthetic
release bytes. It does not qualify the GitHub signing identity, transparency-log
inclusion, production trust roots or final CPRa artifacts.

### Original image signatures and provenance

[image_signing.py](review/source/scripts/release/image_signing.py.txt) keeps three
image signatures and the multiarchitecture provenance bundle in the private
progress directory before uploading their OCI envelopes. Each generation has a
synchronously committed started marker. Verified original bytes receive a sealed
digest/size record before the completed marker. The identity includes the source,
release settings and exact original image descriptors. Signatures bind the source
commit and a deterministic release-identity digest; provenance must match the
canonical repository, workflow, main ref and commit.

The protected workflow asks the pinned attestation action to return its bundle
without pushing it or creating a GitHub storage record. The publisher seals that
original output before registry writes. Completed generations are verified and
reused. If generation started but its original output is unavailable, continuation
stops for review of the original run; it does not sign again automatically.
An interrupted action may have reached a remote signing service even when its
local output is missing. This contract does not claim that remote generation is
exactly once. [Pinned attestation action inputs](https://github.com/actions/attest/blob/1e69f48acb82d1966a394da916b4c1698aa569d6/action.yml).

Original bundles become deterministic OCI objects. Their source-bound transfer
journal records intent before upload, hashes remote bytes on continuation and
rejects missing completed objects. Discovery uses the native Referrers API or
the specified digest-tag fallback when that API returns 404. Fallback indexes
require their original intent; unrelated indexes cannot be overwritten. The
registry must have one protected publication owner. [OCI referrer discovery and fallback](https://github.com/opencontainers/distribution-spec/blob/main/spec.md#unavailable-referrers-api).

Every subsequent release, module, image-version and chart promotion rechecks
completed image-bundle progress, remote bytes, discovery and cryptographic
verification. That check performs no upload. Imported artifacts may have readable
directory modes; active restored progress is recreated privately and rejects
links, extra files, changed identities and altered sealed bytes.

The explicit local suite is:

```bash
python3 -B scripts/release/install_tools.py cosign oras distribution helm3 helm4
python3 -B scripts/release/test_registry.py --integration --image-signatures \
  --tools "$PWD/bin/release-tools" --work /tmp/cpra-registry-fixture
```

CI runs this suite with pinned tools and ephemeral keys. Separate processes are
killed after signature output and accepted bundle, referrer-manifest and fallback
index writes. Continuation preserves the original bundles, and a final read-only
promotion check verifies all signatures and provenance without another upload.
The Distribution fixture exercises fallback discovery; native discovery has
bounded HTTP contract tests. These synthetic images and local keys do not qualify
GitHub workflow identity, transparency-log inclusion, real-account publication
or runnable CPRa images. Full workflow execution and independent review remain
open, and publication remains locked.

### Module publication and restart

[module_publication.py](review/source/scripts/release/module_publication.py.txt) reads the frozen
SDK/worker dependency graph and uses create-only Git references. The SDK tag is
followed by a fresh public download; the worker tag and its download follow; only
then is the application tag created. Nested tag names follow the
[Go module source rules](https://go.dev/doc/modules/managing-source). The retained
publisher calls this sequence after its private signed readback. Its executable
lock still prevents any public action.

The helper establishes authenticated access to the canonical public repository
and the intended source commit before interpreting a reference's 404 as absence.
It validates every proposed name before the first tag, records started intent
before creation, and verifies the returned and subsequently fetched reference.
It uses GitHub's [create-reference operation](https://docs.github.com/en/rest/git/refs#create-a-reference)
and never updates or deletes a tag. Redirects and uncertain network/authentication
responses stop the operation. An existing version without original progress,
a different commit or a missing previously verified tag cannot be replaced.

The private, exclusively locked progress file binds the source, API origin,
complete module graph and dependency versions. A lost creation reply or process
termination resumes by fetching the original tag. Every continuation rechecks
both module downloads, including previously completed steps. Changed retained
inputs stop the operation; dependency tags are rechecked before another tag and
at final readback. The separate asset, image-signing and chart helpers retain their
own continuation records; complete real-account workflow execution and the final
private release gate remain open.

Downloads use the recorded Go compiler in an empty private cache with
`GOWORK=off`, `GOENV=off`, `GOTOOLCHAIN=local`, the public HTTPS proxy and
`GOSUMDB=sum.golang.org`. There are no replacements or private checksum exceptions.
`GOAUTH=off` and `GOVCS=*:off` prevent ambient credential helpers and VCS commands;
GitHub credentials are not inherited by the download process. See the
[Go authentication controls](https://pkg.go.dev/cmd/go#hdr-GOAUTH_environment_variable).
Child output, download sizes, storage and lifetime are bounded, and its cache is
removed on completion or failure.

The public `Sum` and `GoModSum`, module declaration, source time, complete file set
and each file's bytes must match the qualified private module. Different ZIP
compression is permitted because Go authenticates module contents rather than
ZIP metadata. This follows the [Go module authentication contract](https://go.dev/ref/mod#authenticating).
A failed SDK download leaves worker/application tags unpublished; a failed worker
download leaves the application tag unpublished. Cancellation or unavailable
proxy/checksum service is not permission to proceed.

The local tests use loopback API fixtures, native Git and real process termination.
A separate real public Go download of an existing third-party dependency tests
the strict transport policy. Those observations do not verify CPRa's own public
module versions, GitHub write credentials, official release binaries or the full
release workflow. The final qualification and independent review remain open.

[review_evidence.py](review/source/scripts/release/review_evidence.py.txt) verifies detached
OpenSSH review signatures over the exact original assessment. The signing
namespace is `cpra-release-review@ziad-hsn.github.io`. Supply the trust policy
outside candidate source and downloaded review artifacts. Its authors and
reviewers are explicit principals with public keys; author identities and keys
cannot also be reviewers. Group/other-writable policy files, duplicate keys,
unsigned records, changed original inputs and incomplete scope are rejected.
Removing a reviewer from the current policy revokes that reviewer's eligibility.
Key ownership and the complete author list require independent, out-of-band
verification. A signature authenticates a statement; it does not prove an
adequate review occurred. [OpenSSH signing and verification](https://man.openbsd.org/ssh-keygen).

The operator-owned policy has this shape. Replace every identity and public key
with independently verified values; these placeholders cannot qualify a review:

```json
{
  "schema_version": 1,
  "repository": "ziad-hsn/cpra",
  "authors": [{"principal": "author@example.org", "public_key": "ssh-ed25519 AUTHOR_BASE64_PUBLIC_KEY"}],
  "reviewers": [{"principal": "reviewer@example.org", "public_key": "ssh-ed25519 REVIEWER_BASE64_PUBLIC_KEY"}],
  "minimum_reviewers": 1
}
```

First create a preliminary assessment from a configuration without
`independent_review`. Preserve that configuration, report and every original
input. After reviewing them, the reviewer creates a record containing
`schema_version: 1`, its complete `release_identity`, the original report's
`assessment_sha256`, `principal`, timezone-aware `reviewed_at`, all thirteen
other component names, `conclusion`, substantive `notes` and `findings`.
The conclusion is `approve` or `changes_requested`. Every finding names its
component, ID, `open`/`resolved` status, notes and original evidence fingerprints.
An approval cannot contain unresolved findings. Sign the unchanged JSON bytes:

```sh
ssh-keygen -Y sign -f /protected/reviewer-key \
  -n cpra-release-review@ziad-hsn.github.io /private/reviews/reviewer.json
```

This creates `reviewer.json.sig`. The reader uses only the public key and never
loads a private key, invokes an agent or accesses a provider. Its implementation
compares every reviewed component outcome with a fresh assessment and rehashes
all original inputs. A valid signature cannot qualify missing execution evidence.
Keep every original reader entry in a separate copy of the configuration and add
this entry inside `components`:

```json
{
  "independent_review": {
    "assessment": "/private/assessments/preliminary.json",
    "policy": "/protected/release-review-policy.json",
    "records": ["/private/reviews/reviewer.json"]
  }
}
```

The reader accepts at most sixteen records, one disposition per reviewer.
Any changes-requested disposition keeps review incomplete. An ordinary
assessment always keeps publication locked. A separate `--decision` invocation
rechecks all fourteen readers, their original inputs and the signed independent
review. It uses an operator-selected trust-policy digest; candidate configuration
cannot select a different policy. A stored ready report is not an admission
credential.

After retaining the complete frozen evidence and independent review, the local
decision command is:

```bash
python3 -B scripts/release/qualification.py \
  --release /srv/cpra-release/distribution/RELEASE.json \
  --config /srv/cpra-release/qualification.json \
  --decision \
  --review-policy /etc/cpra-release/reviewers.json \
  --review-policy-sha256 "$CPRA_RELEASE_REVIEW_POLICY_SHA256" \
  --out /srv/cpra-decisions/release-decision.json
```

Select that digest through the operator's trusted release configuration. Do not
derive it from a candidate-supplied policy. Exit status 0 means all private
prerequisites are ready, 1 means invalid evidence, and 2 means incomplete gates.
The new report records `prepublication_gate_passed` separately from
`release_gate_passed`, which stays false. This command makes no public changes
and does not unlock the private workflow. Official signatures, public module
downloads, immutable release checks and asset/registry readbacks are later
requirements.

Direct protected publisher phases require `--qualification` with the original
component configuration, `--review-policy`, and the protected environment's
`CPRA_RELEASE_REVIEW_POLICY_SHA256`. They recheck the readers and approval before
remote operations, bind the selected distribution, OCI assembly and dependency
cache to those inputs, and recheck retained hashes before the public phase.
Packaging evidence or a precomputed decision alone cannot admit publication.
Private-to-public workflow integration, adequate independent review, frozen
execution and real signature/readback qualification remain open.

## Source and build identity

The canonical module is `github.com/ziad-hsn/cpra`. Its `go.mod` retains `go 1.25.0`; official binaries use **Go 1.27.1**. A separate minimum-compiler job tests Go 1.25.0. Source compatibility does not extend Go's upstream security-support period. Compiler upgrades require review of the recipe, recorded archive checksums and compatibility results. See the [Go release policy](https://go.dev/doc/devel/release#policy).

`scripts/release/recipe.json` is the build contract and `.goreleaser.yaml` matches it. The release recipe uses:

- Normal optimization, `-trimpath`, `-mod=readonly`, `-pgo=off`, `-buildvcs=false` and bounded build parallelism (`-p=2`).
- `CGO_ENABLED=0`, `GOAMD64=v1` and `GOARM64=v8.0`.
- Symbols and DWARF retained: no `-s`, `-w` or executable compression.
- `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local`; ambient `GOFLAGS`, experiments, compiler and module-policy overrides are discarded.
- All optional drivers, with Linux-only systemd capability excluded on other operating systems by build constraints.

Race/coverage instrumentation belongs to verification builds. Neither `netgo`, `osusergo`, external-linker flags nor an unrepresentative PGO profile is a general release optimization. Refer to [Go build flags](https://pkg.go.dev/cmd/go), [linker options](https://pkg.go.dev/cmd/link), [toolchain selection](https://go.dev/doc/toolchain) and [PGO guidance](https://go.dev/doc/pgo).

Release preparation requires a clean committed tree. It generates `RELEASE.json` from that commit, recording the application version, full source commit, source UTC timestamp and epoch, target matrix, compiler archive hashes, build recipe, dashboard digest, storage-format version, chart version, package version/revision and source-file inventory. Source inputs are checked again before building. The recipe injects CPRa's actual `internal/version` variables with the canonical module path. The displayed date is labeled **source date**, not compilation time.

Qualification reports carry a `release_identity` computed from that frozen
manifest. It fingerprints the complete metadata, source inventory, compiler
archive pins and build recipe using sorted JSON, independently of JSON whitespace
or key ordering. Both SDK modules must appear in the source inventory; changing
their code or dependency files changes the identity even if a report still names
the same commit. The native, package, source-install, reproducibility, service,
container, chart and documentation producers record this identity. Documentation
uses metadata from the same private build job, rather than constructing a new
release identity from the checkout. The retained packaging evidence reader
rejects missing or mismatched identities.

Development builds made with `--candidate`, dirty inputs and incomplete source
inventories cannot issue this qualification identity. A clean private RC uses its
frozen version and ordinary release recipe. The identity is an integrity binding,
not a signature or proof that a test ran. Actual executable/package digests,
platforms, test outcomes and coverage still require their own checks. This change
does not complete the aggregate management, worker, provider or comparative-performance gate;
publication remains locked. Existing reports without the binding remain scoped
historical evidence and cannot be relabeled as final-candidate results.

Official builds deliberately omit automatic `vcs.*` fields. The explicit identity and signed provenance bind their source. Ordinary `go build` and `go install` use Go's `runtime/debug.ReadBuildInfo` fallback: a module version alone does not invent a commit.

## Packaging report validation

The packaging reader requires all 34 reports for the recorded target matrix.
Each report must be a JSON object of at most 16 MiB, without duplicate fields or
non-finite numbers. Its release identity must match the frozen manifest; any
provided application version and embedded deployment source must also agree.
Failed steps, cleanup errors and pending requirements reject the report even when
its top-level status says `pass`.

The reader checks the observations emitted by the qualification harnesses:

- Native restarts must recover ready, single-node Raft storage with the same node
  identity and a nondecreasing committed position. Service reports must include
  every lifecycle step, repeated permission observations, the matching user/system
  scope, restored identity and the deliberate-stop observation period. They also
  require a changed authentication epoch, a newly committed and observed operator,
  verified TLS, and HTTP 401 rejection of the pre-restore named token and original
  startup token. A restored service that merely answers readiness cannot pass.
- DEB and RPM reports require their distinct removal scenarios, all five package
  version/revision fixtures and installed executable hashes matching the native
  archives. Source installation requires both commands with default and full
  driver tags, version output, executable hashes and the actual native platform.
- Compose and Helm reports require the named storage, restart, upgrade, token,
  shutdown and probe scenarios. Helm additionally records the CSI driver, an
  observed RWOP scheduling rejection, unchanged calibrated provider-call counts,
  and the separate large manifest volume. Deployed executable hashes must match
  the corresponding native archive; the current chart campaign exercises amd64.
- Both source rebuilds must list all 23 unsigned executable/distribution payloads
  and match the final files, source archive and recorded recipe. Image rebuilds
  retain the saved runtime-reference image and both independent OCI transports.
  Recorded sizes and hashes must match those files. The reader verifies two
  identical OCI inventories from different input timestamps, each descriptor's
  content-addressed blob and size, the configuration's platform, source labels,
  non-root identity, working directory and entrypoint. It checks every layer's
  uncompressed digest against `rootfs.diff_ids`, following the
  [OCI configuration contract](https://github.com/opencontainers/image-spec/blob/v1.1.1/config.md#layer-diffid).
- Documentation reports require the documentation checkout's complete commit ID.

The report checks have small Python regression fixtures. They do not run the
native executables, package managers, Docker or Kubernetes, establish that a report
is authentic, or replace the actual campaigns. Documentation rendering, final
OCI/chart artifact binding, signed provenance and the aggregate management,
worker, provider and comparative-performance gate remain separately required. The publisher
stays locked. A passing report-validator test is not release qualification.

The image reader accepts the release recipe's self-contained, single-platform
Linux layout with SHA-256 blobs and ordinary or gzip layers. It does not fetch
external blobs or extract filesystem entries. Tar transport bytes and declared
payloads are limited to 2 GiB, as is total layer expansion; each JSON document is
limited to 16 MiB. Physical tar headers are checked before library decoding,
with a separate 16 MiB total allowance for extension metadata and rejection of
sparse outer files and global tar metadata. The reader also bounds the outer transport to 4,096 physical
headers and members,
the descriptor chain to eight nested indexes and the image to 128 layers. It
rejects duplicate or noncanonical paths, links, special files, privileged outer
member modes, ambiguous JSON, unsupported media types and inconsistent hashes.
These restrictions are CPRa's recorded recipe; the
[OCI layout specification](https://github.com/opencontainers/image-spec/blob/v1.1.1/image-layout.md)
permits broader layouts.

Retained files sit beside each report as `image-repro-ARCH-reference.docker.tar`,
`image-repro-ARCH-build-0.oci.tar` and `image-repro-ARCH-build-1.oci.tar`.
Copies are exclusive, have mode `0600` and are flushed before recording their
hashes. Malformed outputs are retained before validation, and CI uploads the
container evidence on failure. Small archive fixtures and controlled Docker
replies exercise these checks; actual Docker execution, final registry binding,
authenticated evidence and independent review remain release requirements.

## Installable source and embedded dashboard

Use the same published version for both programs:

```sh
go install github.com/ziad-hsn/cpra@VERSION
go install github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
```

Replace `VERSION` with an actual published tag, such as `vMAJOR.MINOR.PATCH`. Plain installation retains the default driver subset. To match prebuilt driver coverage:

```sh
go install -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' github.com/ziad-hsn/cpra@VERSION
go install -tags 'redis postgres mysql mongo rabbitmq kafka kubernetes aws systemd teams twilio' github.com/ziad-hsn/cpra/cmd/cpractl@VERSION
cpra -capabilities
cpra -validate -yaml /absolute/path/monitors.yaml
```

The dashboard, starter configuration and local service templates are embedded.
End users need no checkout, Node, pnpm or generation step.
`scripts/release/go_install.py` verifies the source archive against its recorded
inventory and stages the application and its pinned SDK version as separate
modules. Generated `RELEASE.json` stays outside the module payload. Installation
runs outside a checkout with only Go on `PATH`, fresh caches, `GOWORK=off`, and
VCS fetching disabled. This follows the
[versioned go install contract](https://go.dev/ref/mod#go-install).

Both installation and SDK verification use the build-only `tools/modulezip`
module, pinned to `golang.org/x/mod` v0.38.0. Upstream module ZIP selection excludes
nested modules, VCS directories and ineligible vendor files; valid committed files
are retained regardless of extension. The tool records actual module-content,
`go.mod` and ZIP hashes. Staging rejects changed or mismatched `go.mod` bytes and
refuses to replace an existing version. The helper is outside the application,
SDK and worker dependency graphs. See the
[Go module ZIP rules](https://go.dev/ref/mod#module-zip-files) and
[upstream archive implementation](https://pkg.go.dev/golang.org/x/mod/zip).

The archive helper compiles and passes its filesystem/command tests on both
supported compilers. Actual versioned candidate installations remain pending.
Private archive qualification and public-proxy retrieval are
separate gates. See the [SDK checkpoint](implementation/go-sdk-status.md#module-archive-checkpoint).

SDK consumer reports now retain the source-derived and downloaded `.zip`, `.mod`
and `.info` payloads. `scripts/release/sdk_evidence.py` inspects both compiler
reports against the frozen release inventory and private dependency preparation;
it rejects stale source, missing files, wrong versions/checksums, incomplete
documentation coverage and changed inputs. See the
[retained-consumer procedure](sdk/publishing.md) for the offline command and
remaining qualification boundaries. The private workflow keeps the six private
dependency payloads and both consumer campaigns before running the reader.
The reader's `consistent_pending_review` result does not open the aggregate
publication gate or replace actual platform, server, provider and comparative-performance runs.

The retained browser reader, `scripts/release/browser_evidence.py`, inspects the
four compiler/tag campaigns against frozen source and their actual raw Go
results, source inventories, log bytes and screenshots. It also compares the
HTTP fixture's served dashboard index and compiled runtime observation. It
rejects development workspaces for frozen-source coverage and never turns a
consistent component record into publication authorization. The
[browser procedure](validation.md#inspect-retained-browser-evidence) documents
its command, evidence limits and remaining review requirements.

Frontend development uses the exact Node **24.21.0** and pnpm **11.22.0** recorded in the recipe and the frozen lockfile. `dashboard_build.py --check` refuses local dotenv files, discards ambient frontend build variables, builds and tests, then requires generated assets and license notices to match committed files. Provider credentials never become frontend inputs. [Vite environment handling](https://v6.vite.dev/guide/env-and-mode) explains why values exposed at build time can become public browser content.

The frontend helpers preserve explicitly supplied `GOCACHE`, `GOMODCACHE`
and `GOPATH`; the dashboard helper also preserves `npm_config_cache` and
`npm_config_store_dir`. Set these paths to an owned temporary directory when
bounding local build storage. Ambient execution overrides and credentials remain
excluded, and the freshness gate still requires the current recorded inputs.

## Prepare private dependencies

First use the [module preparation procedure](sdk/publishing.md#prepare-dependency-changes-before-freezing-source)
to review Go-generated module files, including checksums for the full dependency
graph. If an unpublished SDK candidate changes, its README and checksum files
also change its module content identity. Refresh dependent checksums through the
explicit preparation option, review the proposed files and commit them before
creating the frozen source archive. Frozen cache preparation never repairs a
checksum mismatch or changes the source archive.

Before the SDK tags exist, prepare dependencies in a new directory outside the
checkout. Select the worker version explicitly; the application and worker must
already require the same reviewed SDK version. Source archive creation is
available before executable builds:

```sh
make release-prepare VERSION=vMAJOR.MINOR.PATCH
python3 scripts/release/release.py source
python3 scripts/release/dependencies.py \
  --source dist/release/cpra-vMAJOR.MINOR.PATCH-source.tar.gz \
  --go /absolute/go/bin/go --worker-version vWORKER_MAJOR.WORKER_MINOR.WORKER_PATCH \
  --out /absolute/new/cpra-dependencies
make release-build-goreleaser RELEASE_DEPENDENCIES=/absolute/new/cpra-dependencies
```

The preparation command validates source bytes and archived executable modes, creates private SDK and
worker module archives, and downloads them into its own module cache. Only these
two unpublished module paths receive a bootstrap checksum-database exception.
Public dependencies retain the recorded public proxy/checksum policy. The command
then removes the bootstrap exceptions, verifies cached modules and reads each
module's dependency graph under the release settings. It records source, compiler,
archive, module and cache-file identities in `dependencies.json`. A later build
accepts that directory only when its report and private cache files still match
the frozen release inputs. Go also performs its normal module verification; see
[Go dependency authentication](https://go.dev/ref/mod#authenticating) and
[cache verification](https://go.dev/ref/mod#go-mod-verify).

If Go changes `go.mod`, `go.sum` or another frozen input, preparation fails. On
Unix, changing a source file's executable permissions also fails verification. The
extracted source and logs remain in the private directory for review. Review and
commit the genuinely generated dependency changes before preparing a new source
archive and a new cache directory. The command never edits the checkout, adds a
local replacement, or treats changed input as a passing release. It refuses to
overwrite an existing dependency directory.
Once the output directory is created, failures produce `dependencies.json`,
including a missing or invalid source archive; a failed report cannot select a
cache for a release build.

Pass `--dependencies /absolute/new/cpra-dependencies` to direct release builds
and source-archive rebuilds. Each source rebuild uses a separate compiler cache.
Benchmark preparation can select `/absolute/new/cpra-dependencies/modules` with
`--module-cache`; retain the dependency report with its campaign evidence.

Private release build, native-platform and compiler-compatibility jobs each prepare
their cache from the frozen source archive. Compatibility preparation uses the
release compiler before selecting Go 1.25 or the release compiler for application,
SDK and worker tests. Jobs export only the cache paths; the public proxy/checksum
settings and `GOWORK=off` remain in effect. Preparation reports and proposed
module-file changes are retained privately even when preparation fails.

The current implementation has Python orchestration and workflow-policy tests.
Actual Go preparation, source installation, native builds and public retrieval
remain unexecuted for this change. The [SDK status](implementation/go-sdk-status.md#private-dependency-cache-checkpoint)
records that boundary. Preparing a cache does not publish an SDK or qualify the
application release.

## Reproduce unsigned distributions

The release builder needs Python 3.12+, the selected Go distribution, and the checksum-pinned GoReleaser OSS/nFPM tools. Node/pnpm are needed only to verify the committed dashboard, not to rebuild a shipped source archive.

```sh
python3 scripts/release/install_go.py --version go1.27.1 --out /absolute/new/go
export PATH=/absolute/new/go/bin:$PATH
python3 scripts/release/install_tools.py
python3 scripts/release/dashboard_build.py --check
make release-check check test-all-drivers
make release-prepare VERSION=vMAJOR.MINOR.PATCH
make release-build-goreleaser
make release-package
python3 scripts/release/sbom.py --syft bin/release-tools/syft
python3 scripts/release/release.py verify
```

Official artifacts are staged in `dist/release/OS_ARCH/`. Container images consume those exact staged executables. `scripts/release/release.py build --go /absolute/go/bin/go` provides the equivalent direct Go recipe for source reconstruction.

Source archives come from `git archive` of the approved commit plus explicitly generated `RELEASE.json`. They preserve executable modes and include build scripts, schemas, examples, notices, embedded templates and dashboard assets. Untracked workstation files are not selected by extension. Changes to `go.mod` or `go.sum` during a release build fail the build.

The full reproducibility gate compares the checkout's unsigned executables, native archives and DEB/RPM files with **two separate extractions** of the distributed source archive:

```sh
python3 scripts/release/source_rebuild.py \
  --source dist/release/cpra-vMAJOR.MINOR.PATCH-source.tar.gz \
  --stage dist/release --go /absolute/go/bin/go \
  --nfpm /absolute/path/bin/release-tools/nfpm \
  --out dist/release/reproducibility.json
```

All six targets are compared by default. A selective local run must list its targets and cannot satisfy the complete release gate. Signing timestamps/certificates are verified separately and are not part of unsigned-byte reproducibility. The chart packager also normalizes its tar headers to source time after staging the tested immutable image digest.

For an intentional dirty-tree experiment, `make release-prepare RELEASE_CANDIDATE=--candidate VERSION=v0.0.0-local.1` creates visibly marked candidate metadata. Candidate outputs cannot produce an official source archive or pass publication.

## Native packages and services

The output matrix is Linux/macOS tar archives and Windows ZIP archives for `amd64`/`arm64`; DEB and RPM are built for both Linux architectures. MSI, PKG, Homebrew, Scoop, WinGet and hosted apt/yum repositories are not provided by this release machinery. Sigstore signatures do not imply Apple notarization or Windows Authenticode.

Read [native installation](native-installation.md) for platform paths, installer ownership, service administration and stopped backup/restore. Linux system packages install administrator-owned binaries under `/usr/bin`, configuration under `/etc/cpra`, and state under `/var/lib/cpra`. A Linux user service uses absolute XDG locations with home-directory fallbacks; a system service does not put state in the installing user's home. macOS and Windows use their platform application-data locations. Explicit data-directory flags and runtime settings retain precedence.

The packaged systemd unit uses `Type=notify`, a dedicated account, state mode `0700`, `KillMode=mixed`, bounded restart rate, a 45-second application shutdown budget and a 60-second stop allowance. Installation leaves the service stopped. Host recovery permissions, credential files, SDK profiles and socket access require deliberate operator configuration under the actual service identity. Package hooks never grant Docker or systemd recovery authority or copy the installing user's credentials.

DEB and RPM share file definitions but have separate lifecycle scripts. Upgrades stop a running old process before file replacement and restart only if it was running. A stopped service remains stopped; upgrades preserve enabled state. Existing `cpra` accounts must have a non-root UID and the matching non-root primary group; package hooks fail rather than changing an incompatible identity. RPM's removal of the old package during an upgrade does not disable the replacement. SemVer prereleases map to a `~` prefix followed by an order-preserving identifier encoding; package revision is recorded separately. For example, tag `v1.2.3-rc.1` maps to package version `1.2.3~bfkevaa1a`. The original tag stays in archive filenames, application identity and `RELEASE.json`. This encoding also preserves arbitrary SemVer cases such as `a.1 < a-1`, where copying the raw suffix would invert Debian/RPM ordering. The exact package-manager version is recorded in `RELEASE.json`.

Each installation attempt discards old restart markers and records the current
owner state. An unreadable service state or failed stop prevents replacement.
RPM resumes an originally running service only after its configuration phase
completes successfully; a failed scriptlet does not authorize `posttrans` to
start it. Managed configuration files must be regular files, including before
token generation, and existing contents are preserved while ownership and
`0640` access are repaired. Debian declares the account utilities through
`Pre-Depends`, as required for their use before unpacking. The pinned nFPM
supports Debian's [pre-dependencies](https://nfpm.goreleaser.com/docs/configuration/);
its RPM configuration retains explicit utility dependencies without an unsupported
`requires.pre` field. Actual package-manager lifecycle tests remain required.

Edited package configuration is preserved through conffile/noreplace semantics. Debian scripts never rewrite a declared conffile. Removal retains durable state and the service identity. Before Debian removal, an administrator-private configuration recovery copy is kept in `/var/backups/cpra/package-config`; it survives a later purge. This credential-bearing copy is **outside** the Raft state directory and must be handled as secret configuration. Debian purge still applies Debian's normal conffile deletion; use the explicit recovery copy if reinstalling after purge. RPM retains edited configuration as `.rpmsave`. Neither removal nor purge recursively deletes durable state. Data deletion is a separate operator action. See [Debian conffile policy](https://www.debian.org/doc/debian-policy/ch-files.html#configuration-files) and [RPM scriptlet semantics](https://github.com/rpm-software-management/rpm/blob/master/docs/man/rpm-scriptlets.7.scd).

The lifecycle harness uses actual package managers and booted systemd only on explicitly isolated test hosts. Before any package or service command, it requires a matching native Linux architecture and refuses existing CPRa binaries, service definitions, configuration, state or recovery backups, including dangling links. It covers fresh installation, edited configuration, ready service, running/stopped updates between distinct versions, prerelease ordering and prerelease-to-final installation, enabled/disabled preservation, remove, reinstall and purge/recovery copies.

Qualification requires installed executable hashes and filesystem ownership,
permissions and file types after fresh installation, initial startup, running
revision upgrade, prerelease-to-final upgrade and reinstall. Running observations
also retain the actual service PID and start identity, executable image hash,
service UID/GID and recovered Raft node/commit state. Three running phases must
have distinct process identities and preserve the Raft identity and committed
position. The fixture has no monitors; observed monitor and worker-task counters
must remain zero. Observation requests use authenticated loopback reads with
redirects and ambient proxies disabled. A later observation failure is recorded
with its pending phase and the completed observations; a partial or failed report
cannot qualify the package.

The package-version fixtures reuse the candidate executable to test package-manager ordering and hooks; they do not establish compatibility between different storage-writing binary versions. A mocked `systemctl` test is a script contract test and cannot be reported as native service evidence.

## Container, chart and operational gates

Read [container and Helm operations](container-helm.md). Production images use the tested release executables, UID/GID `1001:1001`, certificates, notices and `cpractl`; developer builds have a separate Dockerfile. Compose uses a stable volume, read-only configuration, loopback exposure and bounded logs. No fixture tools or host-control privileges are part of the universal image. The deterministic `cpra-vMAJOR.MINOR.PATCH-compose.tar.gz` bundle contains Compose configuration, runtime settings, a versioned image example, identity and operating instructions. Its README commands run directly from the extracted bundle; the default image can be replaced with the verified release digest from `OCI.json`. Credentials and monitor manifests are supplied separately by the operator.

Helm creates one StatefulSet owner with retained storage; maintenance selects zero owners. Large manifests come from a pre-populated read-only configuration volume, not Helm values or oversized Kubernetes objects. API token rotation requires stopped local administration of the committed verifier before selecting a new immutable Secret and resuming one owner. Both deployment reports require the committed revision and HTTP 401 for the old token; a rollout alone is insufficient. The chart has no automatic backup hooks, live-copy guarantee, HPA, fake reload operation or multi-node failover claim.

Both Helm 3 and 4, including a 3-to-4 upgrade, are exercised. The release gate requires real RWOP contention evidence on the selected CSI fixture, in addition to chart rendering. An RWO local-path test remains useful but cannot pass the default RWOP gate. A test CSI hostpath driver is not production storage certification; record and retest the actual filesystem/storage class/CSI stack before claiming it.

Before replacement: validate the candidate, stop admission and the owner, take a complete stopped backup, replace the artifact, start against the same directory, and verify recovery. The backup includes node identity, Raft database, snapshots, history catalog and retained segments. Keep matching configuration/artifact identity and credentials under the separate documented backup procedure. Rollback requires storage-format compatibility and does not undo committed data or external interventions.

Default builds support application storage formats **1–14, 31, 33, 35 and 38**. The
private `externaljobs` build additionally supports worker formats **15–30, 32,
34, 36, 37 and 39**. [Agent actions](implementation/a2a-jobs.md) use common format
31, recovery-failure notifications use 33, and retained agent connections use
35. Combining those features with optional worker state requires formats 32,
34 and 36 respectively; committed-order worker assignment uses 37. State-change
notification admission uses common format 38 and combined worker format 39;
historical envelopes retain their original notification semantics. The higher
common formats do not make worker formats readable by default builds. The
release metadata must record the
reader/writer capability of the actual build. A default binary cannot open
tagged worker state, and an older binary cannot read records requiring a newer
format. Snapshot and process-restart checks remain separate from the final
platform, upgrade and release qualification gates.

## Publication and verification

The release workflow is manually dispatched with a candidate version and the documentation ref whose source metadata matches the exact source commit. Its initial policy job permits only a private repository with `publish=false`; every build/qualification job depends on that decision. The publication job requires `publish=true`, so it is unreachable under this policy. Direct `scripts/release/publish.py` public phases require the complete original private component configuration and an operator-pinned independent-review policy. There is no force or summary-report override. Private-to-public workflow integration remains pending independent review and complete frozen qualification. Existing packaging evidence is insufficient to unlock publication.

CI report uploads and the dedicated live-campaign workflow likewise require explicit private visibility. Ordinary CI can still test already-public source; these guards cannot prevent a person from pushing a candidate branch or tag. Repository/registry access settings and external branch/tag protections have not been verified by these local checks. Keep candidate source and SDK tags local or in access-controlled private storage. GitHub permits signed-in repository readers to download workflow artifacts, so an artifact upload is not private merely because it is not attached to a release. [GitHub artifact access](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/download-workflow-artifacts).

After the complete gate exists and passes, publication must additionally require reviewed `main` and the protected `release` environment. Configure required reviewers, release/tag restrictions and the narrowly scoped `RELEASE_ADMIN_READ_TOKEN` secret used to read the repository's immutable-release setting. The ordinary job token handles contents/packages/attestations; pull-request jobs have no publication credentials. [GitHub's workflow security guidance](https://docs.github.com/en/actions/how-tos/security-for-github-actions/security-guides/security-hardening-for-github-actions) covers these boundaries.

Do not manually pre-create a stable tag. It becomes installable through Go independently of whether a GitHub Release is still a draft. The currently locked publisher retains its draft/readback implementation for the later ticket-16 integration; it is not an enabled shipping procedure. That integration must put the aggregate gate before image pushes, public signing or attestation, drafts, and all tags. After authorization, verify checksums, signer identity, issuer, source commit and provenance against the frozen candidate. Immutable releases must be enabled; published bytes/tags are never overwritten. Corrections use a new version. Image aliases and the chart are promoted from the exact tested payloads. The chart version is a separate immutable version: bump `charts/cpra/Chart.yaml` before releasing any changed chart or image binding, including an application prerelease-to-final transition. Authenticated registry checks reject an unrelated existing application or chart version before creating the stable Git tag. A matching version may continue only with its original source-bound journal and complete byte readback; network or authorization uncertainty fails the gate. A partially failed publication remains a failed release operation; do not hide it by overwriting artifacts.

A release includes source/native/package/Compose payloads, `RELEASE.json`, checksums, target-specific dependency inventories and SPDX SBOMs, executed evidence, signature/provenance bundles and a trusted-root snapshot. These establish different properties: inventory, integrity, builder/source provenance and observed runtime behavior.

The native archive reader checks the actual `cpra` and `cpractl` bytes against
`DEPENDENCIES.json`, requires its target and exact two-program inventory to match
the selected platform, and compares embedded `RELEASE.json` with the candidate.
Metadata must be an unambiguous JSON object; duplicate fields and non-finite
numbers, including numeric overflow, fail validation. Every member has a unique,
canonical relative path and must be a regular file. Links, special files,
privileged modes and Windows case aliases are rejected. Unix executables must
have executable permissions.

The reader limits an archive and its declared file payloads to 1 GiB, individual
executables to 512 MiB, metadata files to 16 MiB and member counts to 4,096. The
release recipe's single-disk ZIP layout has no comments or ZIP64 records; its
central directory is checked against a 4 MiB limit before constructing the member
table. Executables are hashed in chunks, and invalid ZIP checksums or compressed
streams fail explicitly. These reader checks do not authenticate report authors,
exercise native binaries or replace the remaining aggregate release gate.

Verify downloaded assets before execution:

```sh
sha256sum --check SHA256SUMS
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/ziad-hsn/cpra/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS

gh attestation verify cpra-vMAJOR.MINOR.PATCH-linux-amd64.tar.gz \
  --repo ziad-hsn/cpra --signer-workflow ziad-hsn/cpra/.github/workflows/release.yml \
  --source-digest FULL_SOURCE_COMMIT --source-ref refs/heads/main \
  --bundle provenance.sigstore.json
```

For offline verification, bring the asset, bundle, GitHub CLI and independently trusted roots into the offline environment. Obtain current trusted roots on a trusted connected machine with `gh attestation trusted-root > trusted_root.jsonl`, then add `--custom-trusted-root trusted_root.jsonl` to the verification command. A root file shipped beside an unverified artifact is not an independent trust anchor. See [GitHub's offline verification procedure](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline).

Published support claims must link to the completed evidence for the same source commit. Cross-builds, native execution, mocked protocols, provider accounts and long-duration performance runs are separate evidence classes; missing evidence remains an outstanding gate.

## Mainstream reference implementations

The implementation applies upstream contracts to CPRa's ownership/storage model. [Caddy's GoReleaser configuration](https://github.com/caddyserver/caddy/blob/master/.goreleaser.yml) informs explicit recipes and native packaging; [Prometheus's build configuration](https://github.com/prometheus/prometheus/blob/main/.promu.yml) illustrates source identity and artifact inventories; [Tailscale's Windows service integration](https://github.com/tailscale/tailscale/blob/main/cmd/tailscaled/tailscaled_windows.go) illustrates native SCM lifecycle; and [etcd's released-asset verification](https://github.com/etcd-io/etcd/blob/main/.github/workflows/verify-released-assets.yaml) demonstrates verification of distributed executables. These projects are references, not claims that CPRa inherits their deployment or durability guarantees.
