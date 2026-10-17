#!/bin/sh
set -eu
# Package lifecycle hooks never delete monitor configuration or durable state.

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
install -d -m 0700 -o cpra -g cpra /var/lib/cpra /var/log/cpra
install -d -m 0750 -o root -g cpra /etc/cpra
for cpra_config in /etc/cpra/auth.token /etc/cpra/runtime.yaml /etc/cpra/monitors.yaml /etc/cpra/install.json; do
    if [ -L "$cpra_config" ] || { [ -e "$cpra_config" ] && [ ! -f "$cpra_config" ]; }; then
        echo "CPRa configuration must be a regular file" >&2
        exit 1
    fi
done
# Config conffiles are owned by the package manager; never rewrite their content.
if [ ! -e /etc/cpra/auth.token ]; then
    umask 077
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > /etc/cpra/auth.token
    printf '\n' >> /etc/cpra/auth.token
    chown root:cpra /etc/cpra/auth.token
    chmod 0640 /etc/cpra/auth.token
fi
for cpra_config in /etc/cpra/auth.token /etc/cpra/runtime.yaml /etc/cpra/monitors.yaml; do
    if [ -f "$cpra_config" ]; then
        chown root:cpra "$cpra_config"
        chmod 0640 "$cpra_config"
    fi
done
printf '%s\n' '{"schema_version":1,"kind":"package"}' > /etc/cpra/install.json
chmod 0640 /etc/cpra/install.json
chown root:cpra /etc/cpra/install.json
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then systemctl daemon-reload; fi
# RPM runs posttrans even when another scriptlet failed. Only this completed
# configuration phase may authorize resumption of an originally running owner.
if [ -f /run/cpra-package/restart ]; then
    mv /run/cpra-package/restart /run/cpra-package/restart-ready
fi
