---
title: Maintain these docs
description: Keep canonical main documentation, gh-pages sources, generated output, version labels and CPRa brand assets synchronized.
cpra_scope: docs
---

# Maintain these docs

Edit documentation in **`docs/` on `main`**. The `gh-pages` branch holds the exact
public subset in `_sources/docs/`, the MkDocs configuration and templates, and the generated
website. The application and documentation branches use separate Git worktrees.
`scripts/docs/navigation.json` on main owns the page navigation; synchronization
updates that section of the site configuration while preserving its other settings.
It also copies `docs/source-version.json`, updates the associated version fields,
and applies `scripts/docs/announcement.html.j2` to the announcement block. Other
template blocks, theme settings and redirects remain owned by gh-pages.

## Choose the source scope

Read [versions and availability](versions.md) before changing a behavior claim.
Current guides describe the identified development source and retain explicit
qualification limits. `candidate/` preserves pinned historical candidate guides.
`sdk/` describes the identified unpublished modules. `implementation/` and
`review/` preserve plans and dated engineering records. Keep the
scope in page notices, examples, source links and search descriptions.

Record source changes in `docs/source-version.json`. Uncommitted candidates need
a content inventory as well as a parent commit. A docs-only commit does not
change the runtime revision. Synchronization updates the matching fields in
`_sources/source-version.json` and `extra` in `_sources/mkdocs.yml`; keep
`documentation_commit` separate from `source_commit`.

For a frozen release, supply its generated `RELEASE.json` to both synchronization
and the copy check:

```sh
python3 scripts/docs/sync.py --site /work/cpra-pages --release /work/private-release/RELEASE.json
python3 scripts/docs/sync.py --site /work/cpra-pages --release /work/private-release/RELEASE.json --check
```

Use the source checkout or extracted archive that matches that manifest. The
command rejects changed recorded inputs, unrecorded documentation, and dirty or
experimental release metadata before writing the site. It derives the site's
source commit, source date, version and release identity from the frozen metadata.
The committed checkpoint metadata remains untouched; the generated site's two
`source-version.json` copies agree. This establishes which source is documented,
not whether qualification or publication has passed. The release workflow checks
these copies and the canonical content before its strict site build.

## Edit, synchronize and build

Use separate checkouts such as `/work/cpra-main` and `/work/cpra-pages`. From main:

```sh
python3 scripts/docs/references.py
python3 scripts/docs/check.py
python3 scripts/docs/sync.py --site /work/cpra-pages
python3 scripts/docs/sync.py --site /work/cpra-pages --check
```

From gh-pages, use Python 3.12 with the locked dependencies:

```sh
python3 -m venv .venv-docs
. .venv-docs/bin/activate
python -m pip install -r _sources/requirements.txt
python _sources/rebuild.py
python _sources/verify_site.py .
```

Add each user-facing Markdown page to `scripts/docs/navigation.json`. The source
check requires every non-archived page exactly once, and rejects duplicates,
missing pages and paths outside the docs. Dated `candidate/`, `implementation/`
and `review/` records remain in the repository but are not copied to the public
site. Synchronization also supplies MkDocs'
[`exclude_docs` patterns](https://www.mkdocs.org/user-guide/configuration/#exclude_docs)
for all three directories. `not_in_nav` alone still renders and indexes pages,
so it is insufficient. Public guides cannot link to those excluded records.
Missing-link and anchor validation remain enabled.
Redirects, theme configuration and build validation live
under `_sources/` on gh-pages. The strict build checks links and anchors, titles,
descriptions, one primary heading per page, project-prefixed URLs and search.
It replaces only paths in `_sources/generated-files.txt`. Commit canonical
Markdown on main and synchronized sources plus generated output on gh-pages.
Do not hand-edit generated HTML or the search index.

`python _sources/rebuild.py --output /path/to/new-artifact` builds a separate
artifact without replacing the tracked site; the output path must be absent.
`python -m mkdocs serve -f _sources/mkdocs.yml` provides a local preview.

## Implementation references and private records

Run `scripts/docs/references.py` after editing repository files referenced by
implementation records. It copies selected UTF-8 source files byte for byte into
`review/source/` for internal records and `reference/source/` for explicitly
linked public examples/API sources, with `.txt` suffixes. It rewrites repository
links and records their hashes in the repository-only
`review/reference-data.json`. Internal source copies and the index are excluded
from the public site. These selected downloads are not a complete source release
or verification evidence; public copies are included only when a public guide
references them.

Links from internal records to ignored `bin/verification/` artifacts lead to
the repository-only `review/local-evidence.md` index. That index records each path,
availability at preparation, and a file hash when available. It never copies the
artifact contents and never verifies a result. Missing records remain explicit.
`--check` verifies the prepared index without requiring private artifacts on CI;
it does not reinterpret their recorded availability as current evidence.

`make docs-references` refreshes these references. `make docs-check` verifies
their freshness and navigation as well as the source contracts. Preparation
rejects symlinks, paths outside the repository, credential files, binary inputs
and oversized downloads before rewriting documents. Code fences are preserved.
The [MkDocs file-layout rules](https://www.mkdocs.org/user-guide/writing-your-docs/#file-layout)
explain why download assets belong inside the documentation directory.

## Theme and artwork

`brand/palette.json` on main owns the neutral light and graphite dark roles.
After changing it, run `python3 scripts/brand/generate.py`; add `--images` to
regenerate opaque icons and the social preview using CairoSVG 2.8.2. Copy the
outputs to the corresponding canonical `docs/` paths before synchronization.
Preserve the transparent logo artwork and `brand/BRANDING.md` license terms.

`docs/assets/stylesheets/extra.css` uses shared roles for reading areas,
navigation, search, code, tables and callouts. The heading font and license are
in `docs/assets/fonts`. The header cycles System, Light, Dark. Keep the saved-theme
migration loaded before Material initializes its palette. Use the MkDocs `url`
filter for assets so the `/cpra/` prefix works on deep pages.

## SDK references

In a complete SDK candidate checkout, run `scripts/sdk/reference.py` and
`scripts/sdk/sync_guides.py` to regenerate references and lesson source assets.
Their `--check` modes verify the outputs. Import `docs/sdk` into canonical docs
with the candidate notices retained. Reconcile reviewed wording before replacing
pages. Source downloads keep exact bytes and `.txt` suffixes for Go/Markdown.

Update the snapshot inventory when candidate inputs change. An unchanged parent
commit cannot identify new uncommitted content. SDK fixtures do not establish
v2 server availability, public-module downloads or provider delivery.

## Publish and verify

Keep candidate sources, logs and rendered artifacts private while release gates
are open. After the required private qualification and source review, follow the
approved publication sequence for main and gh-pages. The Pages workflow rebuilds from
locked dependencies and deploys the site. Check its exact commit, then inspect
the published homepage, main quickstart, SDK reference and a
retained redirect. Exercise search, deep links, mobile layout, both themes,
saved choices and System mode. Compare published `site-version.json` and selected
assets with reviewed files. Documentation publication does not merge candidate
code or publish SDK tags, application binaries or containers.
