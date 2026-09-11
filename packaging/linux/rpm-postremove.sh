#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.
if [ "${1:-0}" -eq 0 ]; then

rm -f /etc/cpra/install.json
if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload || :; fi
# auth.token, the cpra account, and /var/lib/cpra deliberately survive purge.
fi
