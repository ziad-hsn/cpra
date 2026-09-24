#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.
# A package must not take over an independently installed service.
if [ -f /etc/cpra/install.json ] && ! grep -Eq '"kind"[[:space:]]*:[[:space:]]*"package"' /etc/cpra/install.json; then
    echo "CPRa is managed by the local installer; uninstall that service before installing a package. Configuration and data must be preserved." >&2
    exit 1
fi

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

if command -v systemctl >/dev/null 2>&1; then
    case "$(systemctl show --property=ActiveState --value cpra.service 2>/dev/null || :)" in
        active|activating|reloading)
            install -d -m 0700 /run/cpra-package
            : > /run/cpra-package/restart
            systemctl stop cpra.service
            ;;
        deactivating) systemctl stop cpra.service ;;
    esac
fi
fi
