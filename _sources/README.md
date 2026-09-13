# CPRa documentation sources

Canonical pages and assets are in `docs/` on main. This branch mirrors them in
`_sources/docs/` and owns MkDocs navigation, templates, validation and generated
site output. Use `scripts/docs/sync.py --site /path/to/gh-pages` from main, then
run `python _sources/rebuild.py` with the locked requirements here. Commit sources
and generated output together. See docs/maintaining-docs.md for full instructions.
