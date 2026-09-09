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

The rebuild command writes only generated paths recorded in `_sources/generated-files.txt`. It uses a temporary build directory, validates the build, then replaces the generated output. Keep sources and regenerated output in the same commit.

## Preview

~~~sh
python -m mkdocs serve -f _sources/mkdocs.yml
~~~

Open the local address printed by MkDocs. The preview respects the `/cpra/` project prefix.

## Publish

Push the reviewed commit to `gh-pages`. The Pages workflow rebuilds from the locked requirements, validates links and metadata, uploads the generated site, and deploys it through GitHub Pages.

Retain redirects when consolidating older URLs. Verify the public homepage and a deep link after deployment. The code verification workflow on `main` is separate from the documentation deployment.
