# Release repair implementation progress

Plan: [approved release repairs](release-repair-plan.md).
Baseline: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c`.
Branch: `codex/release-repairs`.
Updated: 2026-10-12. The candidate is not frozen or publicly qualified.

All checks below are private development evidence for the inspected working
source. They are not a replacement for the complete final-commit matrices or
independent review. Failed reproductions and intermediate failures are retained.

| Ticket | Implemented in this repair branch | Still required |
| --- | --- | --- |
| 1. Baseline | Approved plan saved; all three reviews mapped to F01-F56 with source and evidence dispositions; other checkouts and prior evidence preserved. | Final finding dispositions and independent review. |
| 2. Notifications | Formats 40/41; episode, logical delivery and stable per-target endpoint identities; obsolete queued cancellation; maintenance cancellation; verification-pending wording; unknown HTTP 5xx; persisted overall deadlines and retry identity. PagerDuty/Opsgenie receive committed incident/endpoint correlation; Opsgenie distinguishes acceptance from processing and closes the original alias. Focused lifecycle and real network receiver tests pass. | Safe failure diagnostics across interfaces; complete provider-specific rejection rules; remaining severity and maintenance edge cases; complete process-crash and restart campaigns; independent review. |
| 3. Collections | Immutable routine health publication without the FSM lock; health-read cancellation does not poison write recovery; contiguous uploads use one synchronous transaction per 64 resources/1 MiB group; exact pre-submission marker classification in API/SDK; explicit `resume` with server verification of original raw and ordered resource identities, including complete uploads; `resume-upload` retains upload-only behavior. | Real 35-50-monitor imports and interruptions across all phases; dashboard continuation controls; full contention and restart campaigns; independent review. |
| 4. Storage | Store-owned completed-generation catalog, atomic reader/snapshot pins and Linux native retirement are integrated. Current/previous completed generations remain protected; retirement catalog publication precedes removal of the durable deletion intent. Sampled path-free counts and cleanup conditions are exposed through state, SDK and dashboard. Windows initial mapping is 64 MiB. Linux retention, restart, process interruption, publication failure and focused race checks pass. Existing lazy allocation, authenticated stopped backup/restore and synchronous writes remain. | Native macOS/Windows retirement implementation and execution; complete allocated-size accounting and Windows growth/footprint evidence; full backup/restore and corruption matrix after these changes; independent review. |
| 5. Access | Go-based `local init --management`, exclusive protected files and localhost CA/leaf certificate; system token-output separation; mode-aware service arguments; minimum 32 printable token characters; explicit access discovery; ignored-bootstrap warning; loopback Host/origin guards; profiling cmdline removal; bounded failed-authentication throttling with 401/Retry-After; CSP/security headers. Local initialization, API/authentication and real application start/create/restart checks pass. Dashboard type checking, lint and tests pass across the full run and focused fixture repairs. | Embedded asset rebuild and required browser qualification; native system service and ACL tests; trusted-operator boundary documentation; independent security review. |
| 6. Drivers/configuration | File parsers always reject unknown fields and duplicate JSON keys. Shared static driver settings validate required destinations, ranges, gRPC mode and Docker budgets in manifests, management and SDK preflight/new apply, while original-input normalization stays byte-compatible. gRPC health mode uses verified TLS by default, the configured service and SERVING-only success. Docker has separate stop grace and operation budgets. ICMP selects supported privilege mode or rejects the platform. PagerDuty/Opsgenie correlation, closure and severity fixtures pass. Complete jobs/manifest/loader/SDK API/collection suites and focused management checks pass. | Native service-identity ICMP checks; complete driver/provider and process campaign; independent review. |
| 7. Interfaces/diagnostics | Version and canonical schema explanation routes implemented and authorized; SDK inventory and access discovery generated from canonical inputs; unavailable source time remains omitted. Dashboard primary headings, missing routes, empty/filtered/out-of-range page wording, compact health label and controller terminology repaired. CLI monitor lists show observed health; human describe renders safe summary fields and explicit unavailable times/counts. Terminal controls are escaped in table/key-value cells. | Failure diagnostics and safe problem detail; final frontend/browser verification and independent display/CLI review; unified logging and caller/color handling; dead-path reachability audit; measured idle profile and any warranted loop repair. |
| 8. Materials/qualification | README begins with protected management initialization. Private compiler/frontend tools installed from recorded checksums. CC BY 4.0 artwork license, Ziad Hassan attribution and separate name-use note added, with archive/package/image/dashboard notice inputs. Security reporting, contribution and conduct guidance added. | Remaining documentation consolidation, generated assets/notices and packaging verification, final source freeze and all release gates. |

## Recorded local checks

The release compiler used for new checks is Go 1.27.1, checksum-pinned by the
repository installer. Earlier Go 1.27.0 checks remain earlier development
evidence. Frontend qualification uses pinned Node v24.21.0 and pnpm 11.22.0.

- API/SDK admission classification and original-handle tests passed.
- SDK original-file continuation and mismatch/lost-reply tests passed.
- Management encrypted input proofs, collection upload grouping and CLI
  continuation tests passed.
- Complete jobs and controller-system suites passed on Go 1.27.1.
- The first broader persistence suite failed six cases. Its format/header/test
  publication assertions were repaired; the focused six regressions passed.
  A complete final-source persistence rerun is still required.
- Tagged notification/recovery, exact worker receipt/start and detached
  connection observation checks passed on Go 1.27.1.
- Complete HTTP server, authentication and local administration suites passed.
- The generated localhost installation authenticated, created a disabled monitor,
  stopped, removed its obsolete bootstrap policy and recovered the same committed
  monitor identity on restart. No provider operation was enabled by that fixture.
- SDK operation inventory and discovery consumer checks passed.
- Dashboard type checking and lint passed. The complete 521-test run passed 519;
  the remaining two fixture assertions were corrected and their 29-test suites
  passed on rerun. Tests cover explicit access discovery without write downgrade
  and exact original-operation non-admission evidence. Generated asset rebuilds
  and required real-server browser campaigns remain open.
- Static driver configuration and real gRPC/Docker production-path fixtures
  passed on Go 1.27.1. Frozen file-profile conformance remains unchanged. Existing
  SDK client checks passed; an obsolete non-existent `sdk/go/legacy` target was
  rejected by the test runner and is not reported as a passing module check.
- Generation ownership and Linux retirement tests passed, including five real
  Store restart/snapshot cycles, pinned reads, interrupted retirement and failed
  catalog replacement before intent removal. Focused generation race checks
  passed. Generation state API checks, dashboard diagnostics tests and current
  frontend type checking passed. Native macOS/Windows execution remains pending.
- Focused generation/restart checks passed on Go 1.25.0 as well as Go 1.27.1.
- Provider correlation and asynchronous Opsgenie processing fixtures passed;
  status-read failures after acceptance remain unknown. The mock-contract
  receiver and its twelve request-oracle tests were updated and pass. These
  fixtures are not provider-account delivery evidence.
- The independent checkpoint review identified three notification/replay
  findings, now reproduced and repaired. Controller maintenance cancellation
  survives a real Raft restart and prevents calls from already queued workers.
  Warning/outage/recovery dispatches keep PagerDuty/Opsgenie provider identity.
  Old envelopes reject all newly introduced identity fields and malformed
  notification versions. Focused replay/policy suites and complete controller
  system/job suites pass. See the [checkpoint review](release-repair-review-2026-10-12.md).
- Re-review closed the correlation and format findings and exposed a missed
  maintenance window across restart/stall. Durable original queue times now
  prevent that delayed delivery, including an already admitted worker and a
  retry due after the window. Current-time result processing also suppresses
  new notifications during maintenance. Focused default/tagged checks pass;
  source re-review of this extension remains open.
- Missed-window source review passed. A further batch-one cancellation
  regression was reproduced and repaired with command-count/encoded-byte
  prefixes and continuation on the next owner turn. Its restart/no-effect/
  storage-health regression passes; independent source re-review at `e9bc6c5`
  closed the finding without a further concrete defect.
- Artwork/release archive contracts, asset freshness contracts and frontend
  type checking passed after the licensing changes. The 702-test release-script
  run passed 700; its failures were an absent compiler on the check's PATH and
  an old discovery operation count. Archive and native event-reader regressions
  passed after correcting the environment/count. Documentation reference copies
  are stale after the source repairs and must be regenerated before strict
  documentation qualification.
- All 526 frontend tests, type checking and lint passed after the display work;
  the two subsequent review edge-case fixes passed in the eleven-test app suite.
  The reviewer confirmed the notices path without a pixel-fit/browser claim.
  Complete CLI tests pass with descriptive monitor output and terminal escaping.
- The complete pinned dashboard rebuild passed with 528 tests, type checking,
  lint, parser generation, embedded notices and asset freshness metadata. Source
  review closed the two dashboard edge cases. The remaining CLI snooze finding
  is repaired: pending or missing control observations render unavailable,
  rather than asserting that a monitor is not snoozed. The complete CLI suite
  passes with separate missing, pending, unsnoozed and snoozed regressions.

An independent reviewer agent reviewed checkpoint `b59d5b8` at the user's
request. Correlation and format repairs passed source re-review; a further
missed-window maintenance and bounded cancellation repairs passed source re-review;
this is not a review
of a final frozen release. No public branch, tag,
release, module, image, chart or documentation promotion has been performed.

## External gates

The user confirmed on 2026-10-12 that no native runner fleet, private Kubernetes
environment or designated external provider/agent accounts are configured.
Those qualification gates remain open; fixture passing is not account evidence.

The last physical C: measurement was 30,977,564,672 bytes, below the initial
35 GiB comparative-campaign prerequisite. The large campaign has not started.
Task-owned scratch remains subject to the 6 GiB cap and 64 MiB reserve.

The 24-hour endurance run remains cancelled. The release endpoint remains
`v0.1.0-rc.1`; stable publication requires a separate release decision.
