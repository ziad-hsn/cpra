#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.
if [ "${1:-0}" -eq 0 ]; then

if command -v systemctl >/dev/null 2>&1; then
    if [ -d /run/systemd/system ]; then systemctl stop cpra.service; fi
    systemctl disable cpra.service || :
fi
rm -f /run/cpra-package/restart
fi
