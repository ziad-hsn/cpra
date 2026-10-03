#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.
# A package must not take over an independently installed service.
if [ -f /etc/cpra/install.json ] && ! grep -Eq '"kind"[[:space:]]*:[[:space:]]*"package"' /etc/cpra/install.json; then
    echo "CPRa is managed by the local installer; uninstall that service before installing a package. Configuration and data must be preserved." >&2
    exit 1
fi

# Restart intent belongs to this attempt, not a previous interrupted upgrade.
if [ -L /run/cpra-package ] || { [ -e /run/cpra-package ] && [ ! -d /run/cpra-package ]; }; then
    echo "CPRa package progress must be a regular directory" >&2
    exit 1
fi
for cpra_marker in /run/cpra-package/restart /run/cpra-package/restart-ready; do
    if [ -L "$cpra_marker" ] || { [ -e "$cpra_marker" ] && [ ! -f "$cpra_marker" ]; }; then
        echo "CPRa package progress must not contain links or special files" >&2
        exit 1
    fi
done
rm -f /run/cpra-package/restart /run/cpra-package/restart-ready

if ! getent group cpra >/dev/null; then groupadd --system cpra; fi
if ! getent passwd cpra >/dev/null; then
    useradd --system --gid cpra --home-dir /var/lib/cpra --shell /usr/sbin/nologin cpra
fi
# An existing alias must not turn the unprivileged service into root or use
# another primary group. Refuse the identity; never modify an existing account.
cpra_passwd=$(getent passwd cpra)
cpra_group=$(getent group cpra)
cpra_old_ifs=$IFS
IFS=:
read -r cpra_name cpra_password cpra_uid cpra_gid cpra_rest <<EOF
$cpra_passwd
EOF
read -r cpra_group_name cpra_group_password cpra_group_gid cpra_members <<EOF
$cpra_group
EOF
IFS=$cpra_old_ifs
case "$cpra_uid:$cpra_gid:$cpra_group_gid" in
    *[!0-9:]*|:*|*::*|*:) echo "CPRa account has invalid numeric identity" >&2; exit 1 ;;
esac
if [ "$cpra_uid" -eq 0 ] || [ "$cpra_group_gid" -eq 0 ] || [ "$cpra_gid" -ne "$cpra_group_gid" ]; then
    echo "CPRa requires a non-root cpra account with the cpra primary group; refusing existing identity" >&2
    exit 1
fi
if [ "${1:-}" = upgrade ]; then

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    if ! cpra_active_state=$(systemctl show --property=ActiveState --value cpra.service); then
        echo "CPRa service state could not be read; refusing upgrade before replacement" >&2
        exit 1
    fi
    case "$cpra_active_state" in
        active|activating|reloading)
            install -d -m 0700 -o root -g root /run/cpra-package
            umask 077
            : > /run/cpra-package/restart
            if ! systemctl stop cpra.service; then
                rm -f /run/cpra-package/restart
                echo "CPRa service could not be stopped; refusing upgrade before replacement" >&2
                exit 1
            fi
            ;;
        deactivating) systemctl stop cpra.service ;;
        inactive|failed) ;;
        *) echo "CPRa service state is unsupported; refusing upgrade before replacement" >&2; exit 1 ;;
    esac
fi
fi
