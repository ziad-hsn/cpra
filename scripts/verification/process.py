"""Explicit private paths for CPRa process fixtures; never select user defaults."""
import os
import pathlib
import stat


def private_process(root, *, state, configuration, credentials, logs):
    """Validate all fixture locations before returning an isolated environment.

    Callers still pass the state and configuration paths as explicit flags.
    This helper neither creates nor changes permissions on supplied paths.
    """
    root = pathlib.Path(root)
    if not root.is_absolute() or not root.is_dir() or root.is_symlink():
        raise ValueError('fixture root must be an existing private absolute directory')
    info = root.stat()
    if os.name != 'nt' and (info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) & 0o077):
        raise ValueError('fixture root must be owned by the caller with mode 0700')
    home = pathlib.Path.home()
    forbidden = [home / '.local/state/cpra', home / '.config/cpra', pathlib.Path('/var/lib/cpra'),
                 pathlib.Path('/etc/cpra'), home / 'Library/Application Support/CPRa',
                 pathlib.Path('/Library/Application Support/CPRa')]
    for key in ('XDG_STATE_HOME', 'XDG_CONFIG_HOME'):
        value = os.environ.get(key)
        if value and pathlib.Path(value).is_absolute():
            forbidden.append(pathlib.Path(value) / 'cpra')
    for key in ('LOCALAPPDATA', 'PROGRAMDATA'):
        if os.environ.get(key):
            forbidden.append(pathlib.Path(os.environ[key]) / 'CPRa')
    resolved = root.resolve()
    if any(resolved == item.resolve() or resolved.is_relative_to(item.resolve()) for item in forbidden):
        raise ValueError('fixture paths must not use normal CPRa directories')
    for supplied in (state, configuration, credentials, logs):
        path = pathlib.Path(supplied)
        if not path.is_absolute() or path == root or not path.is_relative_to(root):
            raise ValueError('state, configuration, credentials and logs require explicit fixture paths')
        if not path.resolve().is_relative_to(resolved):
            raise ValueError('fixture path escapes its private directory')
        for ancestor in (path, *path.parents):
            if ancestor == root:
                break
            if ancestor.is_symlink():
                raise ValueError('fixture paths must not contain symbolic links')
    environment = {key: value for key, value in os.environ.items()
                   if key in ('PATH', 'LANG', 'SYSTEMROOT', 'GOMAXPROCS')}
    environment.update(HOME=str(root / 'home'), XDG_CONFIG_HOME=str(root / 'user-config'),
                       XDG_STATE_HOME=str(root / 'user-state'), TZ='UTC')
    return environment
