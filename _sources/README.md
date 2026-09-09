# CPRa documentation sources

Application code lives on the main branch. This gh-pages branch contains the
editable Markdown under docs/ and generated site files at the repository root.

Use Python 3.12, install requirements.txt in a virtual environment, then run
python _sources/rebuild.py from the branch root. The build validates internal
links and metadata before replacing inventoried generated files. Commit sources
and generated output together. See docs/maintaining-docs.md for the full guide.
