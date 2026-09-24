#!/usr/bin/env python3
"""Manual workflow entry point; arguments come from a private JSON config file."""
import json
import os
import pathlib
import subprocess
import sys

path=pathlib.Path(os.environ['CPRA_LIVE_CONFIG'])
if not path.is_absolute(): raise SystemExit('configuration must be an absolute user-owned path')
config=json.loads(path.read_text())
kind=os.environ['CPRA_LIVE_KIND']
if kind=='providers':
    command=[config['verification_binary'],'-live','-config',config['provider_configuration'],'-out',config['output']]
else:
    mode={'comparison':'compare','faults':'faults','soak':'soak'}[kind]
    command=[sys.executable,'scripts/benchmark/campaign.py','--mode',mode,'--candidate',config['candidate'],'--target',config['target'],'--builds',config['builds'],'--out',config['output']]
    if mode=='compare': command+=['--baseline',config['baseline']]
raise SystemExit(subprocess.call(command))
