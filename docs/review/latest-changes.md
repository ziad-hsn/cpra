---
title: Latest-change documentation review
description: Source coverage, corrected documentation claims and verification boundaries for the complete main and gh-pages refresh.
cpra_scope: docs
---

# Latest-change documentation review

Reviewed on **13 September 2026**. This is a documentation and source-contract
review. The [availability matrix](../versions.md) identifies main `51a835a`,
release candidate `410fbfb`, and the content-identified unpublished SDK snapshot.
It is not an independent release certification or a completed provider campaign.

## Coverage and corrections

| Area reviewed | Authoritative source | Documentation result |
| --- | --- | --- |
| Current main | `main.go`, Makefile, controller, loader, queue, jobs, v1 server and CLI | Main quickstart/API/CLI remain aligned with the in-memory application; dated branding and 17-test evidence separated from the earlier runtime review |
| Durable lifecycle | Candidate `main.go`, controller owner loop, `internal/durable` and runtime configuration | Corrected shutdown order, persistence claims, unknown-action holds and store-directory precedence |
| Readiness and API | Candidate `internal/web/server/server.go`, `durable.go`, readiness tests and CLI | Explicitly empty instances can be ready; controller progress/storage are authoritative; projection freshness is reported separately |
| Configuration and drivers | Both loader schemas, validation, driver dispatch and compiled capabilities | Separate main and candidate fields; all 33 driver tables compared to schema fields for each source, including six missing candidate endpoint/test fields; complete runtime-default and constraint reference added |
| Installation | Candidate `internal/platformpath`, `internal/localadmin`, preflight and native templates | Candidate checkout pinned; native operations, stopped backups and system/user scope retained; main does not claim those commands |
| Containers and releases | Candidate Dockerfiles, Compose, chart schema, release recipe and workflow | Production exact-binary images distinguished from source builds; platform and publication gates kept explicit |
| SDK | Local `api/openapi`, `sdk/go`, worker/collection code, reference generator and lesson sources | All three generated references and four lessons included with unpublished-source notices and exact source downloads |
| Management plan | Approved ten-ticket plan and SDK status records | Preflight-before-activation, separate versions, encrypted staging, controls and worker start authority preserved as planned requirements |
| Evidence | Provider runner/report definitions, implementation records and SDK verification | Historical results retained as dated evidence; old free-space and publication statements corrected; fixtures remain distinct from account effects |
| Documentation and branding | Published palette, logo/template assets and build tooling | Canonical Markdown and assets on main; synchronized gh-pages copy; source labels, navigation, redirects, search and theme preferences retained |

The inherited candidate API page had prose inside its YAML metadata header; it
was moved into the article. Other corrections removed claims that the candidate
has no persistent incident state, shuts HTTP down before draining, requires a
nonempty dashboard snapshot for readiness, or defaults implicitly to `./cpra-data`.
Its quickstart now checks out the candidate it describes. Both lifecycle guides explain recovery-before-red-incident ordering. Candidate group delivery is described per endpoint rather than using the older aggregate success rule. Private workstation
links were replaced with pinned source links or explicit historical references.

## Source identity

The [input inventory](source-inventory.json) records SHA-256 values for the reviewed
SDK working-tree files, including documentation, schemas and examples. It records
file content identity; it is not a signed release attestation. The local SDK
parent commit alone cannot identify that uncommitted work. Source downloads under
`sdk/source/` are exact reference copies and do not form a complete release.

The eight original durable tickets and their review record remain under
[implementation status](../implementation/STATUS.md). Their original incomplete
acceptance criteria are not checked off by publishing these docs. The approved
[management plan](../implementation/api-management-plan.md) remains a separate
future server implementation.

## Verification scope

Documentation verification checks source links and metadata, main/candidate
server flags and API routes, SDK reference generation, source-copy identity,
branch parity, strict MkDocs output, rendered pages, search, responsive layout,
themes and the deployed source identity. Runtime command examples use local
fixtures and temporary state. They do not exercise provider accounts or install
services on the user's machine.

Historical SDK reports describe their original executed runs. This refresh
rechecks the documentation generators and selected runtime contracts; it does
not convert historical reports into a new full SDK or release qualification.

## Remaining application work

The newer durability/release code has not been merged into main. Its dashboard
also needs the approved palette changes reconciled with its durable views when
that code is integrated. SDK modules have not been committed to a public branch
or published as versions. V2 server writes, encrypted configuration and the
external-worker dispatcher remain planned. Provider, platform, performance and
endurance release gates are documented in their respective candidate guides.
