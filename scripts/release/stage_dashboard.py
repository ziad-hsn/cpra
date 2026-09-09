#!/usr/bin/env python3
"""Replace the Go embedding directory with an already successful Vite build."""
from pathlib import Path
import shutil

from package import dashboard_inventory

root = Path(__file__).resolve().parents[2]
source = root / 'dashboard/dist'
if not (source / 'index.html').is_file():
    raise SystemExit('Build the dashboard before staging assets')
_, notices = dashboard_inventory()
target = root / 'internal/web/server/assets'
# This directory contains generated assets only. Do not touch dashboard sources.
shutil.rmtree(target)
shutil.copytree(source, target)

license_dir = root / 'LICENSES'
license_dir.mkdir(exist_ok=True)
text = 'Licenses for dependencies in the embedded dashboard.\n'
for name, content in sorted(notices.items()):
    text += '\n' + '=' * 72 + '\n' + name + '\n' + '=' * 72 + '\n'
    text += content.decode('utf-8') + '\n'
(license_dir / 'dashboard.txt').write_text(text)
