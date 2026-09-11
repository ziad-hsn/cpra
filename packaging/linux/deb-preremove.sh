#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.
if [ "${1:-}" = remove ]; then
# dpkg owns conffile purge semantics. Preserve a private, explicit recovery copy
# before removal so a later purge cannot erase the operator's configuration.
install -d -m 0700 -o root -g root /var/backups/cpra/package-config
for name in runtime.yaml monitors.yaml auth.token; do
    if [ -f "/etc/cpra/$name" ]; then
        cp -p "/etc/cpra/$name" "/var/backups/cpra/package-config/$name"
    fi
done

if command -v systemctl >/dev/null 2>&1; then
    if [ -d /run/systemd/system ]; then systemctl stop cpra.service; fi
    systemctl disable cpra.service || :
fi
rm -f /run/cpra-package/restart
fi
