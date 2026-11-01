# CPRa: error-dependent intervention rules

Approved implementation addendum, 23 October 2026.

## Scope and decisions

Add monitor-level rules selecting one intervention from a typed check failure. Preserve incident progression, recovery verification, original execution identities/deadlines, and unknown-outcome safeguards. Continue on codex/release-repairs from inspected commit 967f9cf8b5733a9f93c56ceaf8712a748cdf5e08, preserving pending A2A work, prior repairs, evidence, worktrees, and the separate documentation checkout.

- Rules belong to each monitor; identical immutable definitions may share compiled state internally. No reusable policy resource.
- Support all 14 built-in check drivers and tagged external-worker checks.
- External check authors may declare up to 32 custom failure codes in an immutable JobType version.
- Guarded manual recovery names the currently qualified rule and cannot override its conditions.
- No escalation, action sequence, or fallback to a different intervention after execution.
- Keep Go 1.26.10 across modules and Go 1.27.3 as the release compiler. No publication is authorized by this addendum.

## Rule and public-interface contract

Extend file manifests through intervention.rules and management resources through spec.recovery.rules. Preserve existing single-action configuration; reject combining it with rules. Each rule has a stable ID, typed matcher, and one existing intervention configuration or an explicit no-intervention decision. Evaluate at most 32 ordered rules, stopping at the first match; the separate default is no intervention.

Match registered codes with optional phase, HTTP status, gRPC transport-status and gRPC health-status restrictions. Conditions combine with AND; values within a condition combine with OR. Accept exact codes and terminal dot-delimited prefixes such as tls.cert.*. Exclude arbitrary globs, regular expressions, raw-message matching, expressions, and templates. Reject unknown codes, duplicate IDs, incompatible fields, unavailable driver capabilities, and provably unreachable rules; warn about partial overlaps. Validate every branch through existing pure validators and credential-reference rules before activation. Validation opens no storage, reads no CA files, constructs no operational clients and contacts no providers. Existing resource/collection quotas cover the complete configuration.

Maintain one versioned registry of codes, canonical safe messages, compatible phases/details and applicable drivers. Preserve historical meanings. Classify through typed errors, errors.Is/errors.As and explicit protocol observations; do not infer a root cause from a symptom, SERVFAIL from a temporary DNS error, or incorrect configuration from authentication rejection. A failed observation differs from an unavailable observation. Generic unclassified failures remain visible but cannot authorize rule-based intervention; neither can local preparation/resource failures, cancellation, or missing observations. Retain only bounded allowlisted diagnostics, with no provider bodies or raw-error rings.

Rule configurations pin a taxonomy version. Expand prefixes against that version; a binary upgrade cannot widen authorization. Taxonomy changes require explicit apply and a visible diff.

Use the existing unhealthy threshold for rule qualification. Track bounded consecutive eligible observations matching the same rule. Different codes may count for that rule; different rules cannot combine. Incident health progression remains separate. Success, unavailable observations, configuration replacement and relevant suppression transitions reset qualification. Require fresh evidence after suppression.

Recheck rule, controls, configuration, target and deadline before execution. Cancel obsolete unsent intents; replacements require fresh qualification and remaining incident attempt/deadline allowance. Cancellation never refunds or renews those allowances. The first committed start pins rule, driver and target for the incident; permitted retries retain that selection. Verified recovery clears selection for a later incident but does not release historical unknown-action holds.

Update canonical schemas, generated base/tagged types, validators, operation inventory and documentation together. Expose failure, candidate/selected rule, qualification progress, decision revision and admission reason through current observations. Guarded recovery adds rule ID and decision/configuration preconditions while preserving control revision, actor and reason. Extend discovery with taxonomy, match fields and driver mappings. Add offline cpractl rules test using the shared loader/matcher, with typed observations and expected-rule assertions; no storage, credentials or provider invocation. Add dashboard editing, ordering, conditional diffs, errors and explanations without mutation retries or authentication downgrade. File-mode reads remain read-only; no check-now command.

## Execution, persistence and workers

Check classification does not confer retry permission. The controller remains the built-in retry owner. Preserve attempt limits, cooldowns, original deadlines, target/configuration bindings, fencing and commit-before-start. Retry only proven transient no-effect failures. Generic HTTP 500, partial writes, lost responses and interrupted execution remain uncertain absent affirmative driver evidence. Missing HTTP trace callbacks never prove no effect; diagnostic tracing must handle concurrency and callbacks after completion. Preserve bounded response handling on both compilers; add no unbounded draining or transport replay.

Continue checks and incident progression through unknown interventions. Health recovery is not evidence of an intervention outcome. Preserve maintenance, Ignore, acknowledge/dismiss/snooze/disable, blue review alerts, gray control notices, green eligibility and cyan verification-pending wording. No external configuration-failure alert, automatic unknown replay or notification-driven recovery loop.

Reserve storage formats 48/49 after 46/47. Persist bounded classification, taxonomy, qualification and selection bindings. Bind intent to incarnation, incident, observation identity, rule/configuration revision, original target/credential dependencies and execution budget. The authoritative committed transition validates selection; worker-proposed rule/permission fields cannot grant authority. Matching is deterministic over committed versioned inputs, with no replay I/O, current-clock reads or historical reclassification. Update command validation, digests, snapshots, migration and retention together. Preserve older supported records, deadlines and attempts; activation requires fresh observations. Complete stopped authenticated backups precede upgrade; older binaries reject newer state and rollback requires a compatible backup.

Use compact existing ECS components and immutable interned match policies with bounded accounting and ownership. No archetype per error, per-monitor table or diagnostic ring. Index candidates where useful and bound remaining matching work. Prepare only the selected intervention's runtime job. Credentials/executable objects stay outside decision records. Existing encryption, dependency guards, worker limits and destination fairness apply to every rule; configured alternatives do not reserve simultaneous execution.

Use worker protocol 2 for structured check failures and retain protocol 1 for existing contracts/executions. Custom codes are scoped to immutable JobType identity/version and cannot redefine built-ins. Validate bounded reports against the original contract, authenticated execution, grant and result identity; public messages are server-owned. Preserve failed observation versus noData/unavailability. Require explicit protocol-2 capability for external check rules, with no silent downgrade or field stripping. Check failure codes remain separate from recovery/notification rejection and retry authority. Result digests, receipt replay and encrypted journals preserve identical retransmission.

Introduce worker journal format 3 with stopped, exclusively locked, bounded conversion from format 2. Preserve identity, pending outcomes, unknown actions and evidence; validate replacement before publication and retain the original on interruption. Conversion invokes no handlers. All worker-only schemas/methods/registrations/dependencies remain behind externaljobs. Operator-owned handlers are trusted code: validated vocabulary and authority do not independently prove their observations.

## Checkpoints and acceptance

1. Contract/classifiers: registry, schemas, pure validators, all built-in adapters and shared corpus.
2. Rule execution/persistence: qualification, selection, bindings, lazy runtime preparation and formats 48/49.
3. External workers: declarations, protocol negotiation, reports, journal conversion and replay.
4. Interfaces: API/SDK/CLI/dashboard, guarded recovery and offline tests.
5. Documentation/final verification: examples, migration, generated artifacts, independent review and frozen-candidate evidence.

Every checkpoint requires focused passing tests, documentation and a separate reviewer agent. Required coverage includes first-match/default/overlap/prefix/version behavior; same/different-rule thresholds; unavailable observations and suppression; target/configuration changes; deadlines and retry exhaustion; unknown holds; real transport invocation counts around refusal/TLS rejection/partial writes/HTTP 500/lost responses; crashes around intent/start/effect/result; old-store migration, snapshots, backup and downgrade refusal; worker declarations/scopes/protocols/receipt loss/interruption/journal conversion; file/API/collection parity and malformed-final-input atomic validation; hidden credentials; desktop/mobile browser behavior; bounded shared/unique-policy allocations and no historical scans per check.

Run affected application, SDK and worker checks on Go 1.26.10 and 1.27.3 for default, built-in, externaljobs and combined variants, with race/vulnerability checks, reproducible generation, frontend checks and required real-server browser campaigns. Missing required tooling is a failure. Performance checks run sequentially for at most 15 minutes each. Preserve the 6 GiB task scratch limit and 64 MiB reserve; clean only identified task-owned disposable files.

Completion means rules can be configured through files/API/SDK/dashboard, explanations agree, and restart/uncertainty/configuration changes/workers cannot bypass policy. Document refused-connection recovery, status-specific A2A, explicit no-intervention and custom worker examples. Existing repair, native-platform, provider-account, packaging and larger qualification gates remain separate. No million-monitor or 24-hour campaign, package-name overhaul, new notification type, expression engine, arbitrary script/template, or release publication.

## Primary references

- [Kubernetes ordered failure policies](https://kubernetes.io/docs/concepts/workloads/controllers/job/#pod-failure-policy)
- [RFC 9110 retry semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2)
- [Go HTTP trace callbacks](https://pkg.go.dev/net/http/httptrace#ClientTrace)
- [Go HTTP connection reuse](https://go.dev/blog/http-connection-reuse)
- [Raft deterministic FSM/snapshot contract](https://pkg.go.dev/github.com/hashicorp/raft@v1.7.3#FSM)
