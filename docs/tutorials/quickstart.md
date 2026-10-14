---
title: Quickstart
description: Initialize protected management access, run a local target and apply your first monitor.
cpra_scope: development
---

# Run your first monitor

Use the source checkout or qualified archive that matches these documentation
sources. [Versions and availability](../versions.md) distinguishes the current
implementation from published artifacts. Public module and release versions
remain gated by qualification and publication.

You need Go 1.26 or later, Make and Python 3. The built dashboard is included;
Node is needed only when changing its sources.

## 1. Build and initialize

From the CPRa checkout:

~~~sh
make
export PATH="$PWD/bin:$PATH"
cpra -version
cpractl local init --management
~~~

Make selects the repository's local SDK workspace while its module candidates
are unpublished. Initialization creates an empty manifest, named operator token,
token hash policy, wrapping key and localhost TLS material in the platform's
standard locations. It prints paths and exact server/client commands, never the
token value. It refuses existing configuration/state and starts no service.

This walkthrough is a fresh user installation. An existing installation needs
the documented stopped upgrade/rotation procedure; initialization does not
convert its authority or erase its data.

## 2. Start a local target

In another terminal:

~~~sh
mkdir -p /tmp/cpra-demo
printf 'ok\n' > /tmp/cpra-demo/health
python3 -m http.server 8080 --bind 127.0.0.1 --directory /tmp/cpra-demo
~~~

This serves http://127.0.0.1:8080/health. Keep it running.

## 3. Start CPRa and connect

Run the exact **server command** printed by initialization. It selects the
generated runtime settings, empty bootstrap manifest and -allow-empty.
Keep that process in the foreground in its own terminal.

For CLI requests, reuse the initializer's --server, --ca-file and --token-file
arguments. The following commands are the operation portion of that printed
client command:

~~~sh
cpractl ready
cpractl get state
~~~

Include those connection arguments with each command, or configure the matching
CPRA_SERVER, CPRA_CA_FILE and CPRA_AUTH_TOKEN_FILE variables using the printed
paths. Do not put a bearer in a URL or command argument.

For the dashboard, manually trust the generated local CA in your browser, open
https://localhost:8060, and sign in using the protected operator token read
locally from its file. Initialization does not install trust roots. The
[management startup guide](../management-startup.md) explains trust and rotation.

## 4. Apply and inspect a monitor

From the checkout, with the connection arguments or variables configured:

~~~sh
cpractl apply -f examples/monitors.yaml
cpractl get monitors
cpractl describe monitor/example-service
cpractl get queues
cpractl get slo
~~~

The example checks the local target every 30 seconds and writes incident
notifications to the configured alerts.jsonl file. It has no recovery action.
Management imports its bootstrap manifest once. Later file edits require an
explicit apply; editing the original file does not reload active configuration.
The dashboard can also create and edit monitors, receivers, groups and secrets.
Notification receivers are contacts, separate from login identities.

Before the first result, observed health is unknown. Wait for a check interval
and projection refresh. A committed resource may still await controller
application; inspect the original operation receipt if application is pending.

## 5. Observe an incident and stop

Stop the target server. After the configured failure threshold, inspect the
incident, action outcomes and monitor timeline. Restart the target and wait for
the healthy threshold. Intervention completion means verification is pending;
restored-health wording follows confirmed checks. Maintenance keeps checks
running while suppressing notifications/recovery, with no deferred alert burst.

Use Ctrl+C to stop CPRa and the target. Keep the state directory for restart
recovery. Unknown action outcomes are held for investigation and are not replayed
automatically. Follow [persistence and recovery](../durability.md) for complete
stopped backup/restore and [deployment](../how-to/deploy-to-production.md) for
native services and container storage.
