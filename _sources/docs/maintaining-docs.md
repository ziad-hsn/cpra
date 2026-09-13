---
title: Maintain these docs
description: Keep canonical main documentation, gh-pages sources, generated output, version labels and CPRa brand assets synchronized.
cpra_scope: docs
---

# Maintain these docs

Edit documentation in **`docs/` on `main`**. The `gh-pages` branch holds the exact
copy in `_sources/docs/`, the MkDocs configuration and templates, and the generated
website. The application and documentation branches use separate Git worktrees.

## Choose the source scope

Read [versions and availability](versions.md) before changing a behavior claim.
Main guides describe the reviewed main runtime. `candidate/` describes the pinned
release commit. `sdk/` describes the identified unpublished SDK snapshot.
`implementation/` records plans and historical qualification work. Keep the
scope in page notices, examples, source links and search descriptions.

Record source changes in `docs/source-version.json`. Uncommitted candidates need
a content inventory as well as a parent commit. A docs-only commit does not
change the runtime revision. Update the matching fields in
`_sources/source-version.json` and `extra` in `_sources/mkdocs.yml`; keep
`documentation_commit` separate from `source_commit`.

## Edit, synchronize and build

Use separate checkouts such as `/work/cpra-main` and `/work/cpra-pages`. From main:

```sh
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

Navigation, redirects, theme configuration and build validation live under
`_sources/` on gh-pages. The strict build checks links and anchors, titles,
descriptions, one primary heading per page, project-prefixed URLs and search.
It replaces only paths in `_sources/generated-files.txt`. Commit canonical
Markdown on main and synchronized sources plus generated output on gh-pages.
Do not hand-edit generated HTML or the search index.

`python _sources/rebuild.py --output /path/to/new-artifact` builds a separate
artifact without replacing the tracked site; the output path must be absent.
`python -m mkdocs serve -f _sources/mkdocs.yml` provides a local preview.

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

Push reviewed commits to main and gh-pages. The Pages workflow rebuilds from
locked dependencies and deploys the site. Check its exact commit, then inspect
the published homepage, main quickstart, candidate guide, SDK reference and a
retained redirect. Exercise search, deep links, mobile layout, both themes,
saved choices and System mode. Compare published `site-version.json` and selected
assets with reviewed files. Documentation publication does not merge candidate
code or publish SDK tags, application binaries or containers.
