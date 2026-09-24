#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.

if [ -f /run/cpra-package/restart ]; then
    systemctl start cpra.service
    rm -f /run/cpra-package/restart
fi
