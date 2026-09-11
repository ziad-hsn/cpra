---
title: "Maintain these docs"
description: "Edit Markdown on gh-pages, rebuild the static CPRa documentation and publish through the Pages workflow."
---

# Maintain these docs

The `main` branch contains the application. The `gh-pages` branch contains these documentation sources and the generated site. Keep the documentation's source commit in sync with the application behavior you have verified.

## Edit and build

In a checkout of `gh-pages`:

~~~sh
python3 -m venv .venv-docs
. .venv-docs/bin/activate
python -m pip install -r _sources/requirements.txt
python _sources/rebuild.py
python _sources/verify_site.py .
~~~

Edit Markdown under `_sources/docs`. Navigation, redirects, page metadata, and theme settings live in `_sources/mkdocs.yml`. Update `_sources/source-version.json` when documenting a newer application commit, and check version links in the pages.

Brand masters and usage terms live in `brand/` on `main`. Copy the published
light and dark SVG lockups and marks to `_sources/docs/images`, and keep
`images/social-preview.png` in sync with `brand/dist/png/social-preview.png`.
The favicon set belongs directly under `_sources/docs`; its relative manifest
paths and the theme's URL filter keep links working under `/cpra/` and on deep
pages. The locally served heading font and its license live in `assets/fonts`.

The rebuild command writes only generated paths recorded in `_sources/generated-files.txt`. It uses a temporary build directory, validates the build, then replaces the generated output. Keep sources and regenerated output in the same commit.

## Preview

~~~sh
python -m mkdocs serve -f _sources/mkdocs.yml
~~~

Open the local address printed by MkDocs. The preview respects the `/cpra/` project prefix.

## Publish

Push the reviewed commit to `gh-pages`. The Pages workflow rebuilds from the locked requirements, validates links and metadata, uploads the generated site, and deploys it through GitHub Pages.

Retain redirects when consolidating older URLs. Verify the public homepage and a deep link after deployment. The code verification workflow on `main` is separate from the documentation deployment.

## Shared theme colors

The documentation and dashboard use the neutral white and graphite palette in
[`brand/palette.json` on main](https://github.com/ziad-hsn/cpra/blob/main/brand/palette.json).
Update that source rather than maintaining separate color values here. From a
main checkout, run `python3 scripts/brand/generate.py --docs-root /path/to/gh-pages`
to copy the shared stylesheet and branding assets into this checkout, then run
`python _sources/rebuild.py` here. Add `--images` when regenerating the opaque
icons and social preview; that option requires CairoSVG 2.8.2.

Component rules live in `docs/assets/stylesheets/extra.css`. Keep backgrounds,
reading areas, navigation, code blocks, search, tables, and callouts tied to the
shared semantic roles. The header cycles through System, Light, and Dark; verify
both explicit choices and system changes before publishing.
