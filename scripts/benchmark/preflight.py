#!/usr/bin/env python3
"""Check physical host storage before any large WSL campaign allocation."""
import json
import pathlib
import platform
import shutil
import subprocess

MINIMUM_HOST_FREE = 30 * 1024 ** 3
POWERSHELL = '/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe'


def inspect(required_fixture_bytes=0):
    result = {'required_host_free_bytes': MINIMUM_HOST_FREE,
              'required_fixture_headroom_bytes': required_fixture_bytes,
              'platform': platform.platform(), 'ready': False}
    if not pathlib.Path(POWERSHELL).exists():
        result['reason'] = 'The agreed Windows/WSL host could not be inspected.'
        return result
    script = r'$v=Get-ItemProperty "HKCU:\Software\Microsoft\Windows\CurrentVersion\Lxss\*" | Where-Object {$_.DistributionName -eq "Ubuntu"}; $d=Get-PSDrive -Name C; @{free=$d.Free;used=$d.Used;base=$v.BasePath} | ConvertTo-Json -Compress'
    try:
        completed = subprocess.run([POWERSHELL, '-NoProfile', '-NonInteractive', '-Command', script], capture_output=True, text=True, check=True, timeout=20)
        data = json.loads(completed.stdout)
        result.update(host_free_bytes=int(data['free']), host_used_bytes=int(data['used']), ubuntu_vhd_base=data['base'], guest_free_bytes=shutil.disk_usage('/').free)
        if not str(data['base']).lower().startswith('c:'):
            result['reason'] = 'Ubuntu backing drive differs from the agreed C: configuration; inspect it before continuing.'
        elif result['host_free_bytes'] < MINIMUM_HOST_FREE + required_fixture_bytes:
            result['reason'] = 'Physical C: free space is below the 30 GiB minimum plus fixture headroom.'
        else:
            result['ready'] = True
    except (OSError, subprocess.SubprocessError, ValueError, KeyError) as error:
        result['reason'] = 'Physical host disk inspection failed: ' + type(error).__name__
    return result
