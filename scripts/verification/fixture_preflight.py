"""Storage and memory admission for short, cached-image local fixtures.

This check cannot qualify a large benchmark. That campaign retains its separate
30 GiB host minimum in scripts/benchmark/preflight.py.
"""
import json
import pathlib
import shutil
import subprocess

GIB = 1024 ** 3
HOST_RESERVE_BYTES = 4 * GIB
MEMORY_RESERVE_BYTES = GIB
POWERSHELL = '/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe'
# Include allowance for container metadata, bounded logs and evidence, even
# though the database profiles put their disposable data on bounded tmpfs.
HEADROOM_BYTES = {'docker': GIB // 8, 'redis': GIB // 4, 'postgres': GIB,
                  'mysql': 2 * GIB, 'mongo': GIB, 'rabbitmq': GIB, 'kafka': GIB}
MEMORY_BYTES = {'docker': GIB // 8, 'redis': GIB // 4, 'postgres': GIB // 2,
                'mysql': GIB, 'mongo': GIB, 'rabbitmq': GIB, 'kafka': GIB}


def inspect(driver):
    if driver not in HEADROOM_BYTES:
        raise ValueError('Unknown local fixture driver')
    result = {'purpose': 'bounded_local_fixture', 'driver': driver, 'ready': False,
              'large_campaign_qualified': False, 'required_host_reserve_bytes': HOST_RESERVE_BYTES,
              'required_fixture_headroom_bytes': HEADROOM_BYTES[driver],
              'required_memory_reserve_bytes': MEMORY_RESERVE_BYTES,
              'required_fixture_memory_bytes': MEMORY_BYTES[driver]}
    if not pathlib.Path(POWERSHELL).exists():
        result['reason'] = 'The physical Windows/WSL host could not be inspected.'
        return result
    script = r'$v=Get-ItemProperty "HKCU:\Software\Microsoft\Windows\CurrentVersion\Lxss\*" | Where-Object {$_.DistributionName -eq "Ubuntu"}; $d=Get-PSDrive -Name C; @{free=$d.Free;base=$v.BasePath} | ConvertTo-Json -Compress'
    try:
        completed = subprocess.run([POWERSHELL, '-NoProfile', '-NonInteractive', '-Command', script],
                                   capture_output=True, text=True, check=True, timeout=20)
        data = json.loads(completed.stdout)
        memory = dict(line.split(':', 1) for line in pathlib.Path('/proc/meminfo').read_text().splitlines())
        result.update(host_free_bytes=int(data['free']), ubuntu_vhd_base=data['base'],
                      guest_free_bytes=shutil.disk_usage('/').free,
                      available_memory_bytes=int(memory['MemAvailable'].split()[0]) * 1024)
        if not str(data['base']).lower().startswith('c:'):
            result['reason'] = 'Ubuntu backing drive differs from the agreed C: configuration.'
        elif result['host_free_bytes'] < HOST_RESERVE_BYTES + HEADROOM_BYTES[driver]:
            result['reason'] = 'Physical C: space is below the local fixture budget plus 4 GiB reserve.'
        elif result['guest_free_bytes'] < HEADROOM_BYTES[driver]:
            result['reason'] = 'Guest space is below the local fixture budget.'
        elif result['available_memory_bytes'] < MEMORY_RESERVE_BYTES + MEMORY_BYTES[driver]:
            result['reason'] = 'Available memory is below the fixture limit plus 1 GiB reserve.'
        else:
            result['ready'] = True
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError, OverflowError) as error:
        result['reason'] = 'Local fixture inspection failed: ' + type(error).__name__
    return result


def output_path(name):
    path = pathlib.Path(name).absolute()
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    environment = pathlib.Path(str(path) + '.environment.json')
    if path.exists() or path.is_symlink() or environment.exists() or environment.is_symlink():
        raise ValueError('Choose a new output path; earlier fixture evidence is preserved.')
    return path


def write_new(path, value):
    with pathlib.Path(path).open('x') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')
