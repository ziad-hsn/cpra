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
cpractl version
cpractl local init --management
~~~

Make selects the repository's local SDK workspace while its module candidates
are unpublished. Initialization creates an empty manifest, named operator token,
token hash policy, wrapping key and localhost TLS material in the platform's
standard locations. It prints paths and exact server/client commands, never the
token value. It refuses existing configuration/state and starts no service.

If 8060 is occupied, initialize with `cpractl local init --management --port 19060`
and use the matching address printed by that command. The selected port is saved
in `runtime.yaml`; services read it when they start.

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

If port 8080 is already occupied, choose another free port without stopping the
existing service. Copy `examples/monitors.yaml` to a private working file, change
its check URL to the chosen port, and use that file in the validation and apply
commands below. Keep the example's `id: example-service` for the describe command.

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

Keep token environment variables scoped to CLI commands. The server also reads
CPRA_AUTH_TOKEN and CPRA_AUTH_TOKEN_FILE as observation-mode bootstrap inputs;
passing its named management operator token through those inputs is rejected.
`cpra -validate` ignores these incidental client authentication environment values
and checks the supplied file without applying it to the committed catalog.
Normal server startup still checks its explicit and environment authentication
inputs; use the printed server command in a separate terminal.

For the dashboard, manually trust the generated local CA in your browser, open
https://localhost:8060, and sign in using the protected operator token read
locally from its file. Initialization does not install trust roots. The
[management startup guide](../management-startup.md) explains trust and rotation.

## 4. Apply and inspect a monitor

From the checkout, with the connection arguments or variables configured:

Validate the example with the `-runtime-config` path from the printed server
command. That runtime setting supplies the root for log notifications:

~~~sh
cpra -validate -yaml examples/monitors.yaml -runtime-config /absolute/path/from/init/runtime.yaml
~~~

Replace that path with the initializer's actual runtime path before running it.
Validation opens no store and contacts no target. Then apply through the
configured authenticated connection:

~~~sh
cpractl apply -f examples/monitors.yaml --wait --timeout 90s
cpractl get monitors
cpractl describe monitor/example-service
cpractl get queues
cpractl get slo
~~~

The bounded wait keeps the original operation handle and waits for its retained
application result before the describe request. If it times out, inspect or wait
on that operation; do not submit a replacement merely because the wait ended.

The example checks the local target every 30 seconds and writes incident
notifications to `alerts.jsonl` inside the process-configured notification log directory. It has no recovery action.
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

Use Ctrl+C to stop CPRa, then repeat the same server command without rerunning
initialization. `cpractl describe monitor/example-service` must still resolve the
committed monitor; restart does not import subsequent bootstrap-file edits.
SIGHUP logs an unsupported-reload notice and keeps running. Stop CPRa and the
target with Ctrl+C when finished. Keep the state directory for restart
recovery. Unknown action outcomes are held for investigation and are not replayed
automatically. Follow [persistence and recovery](../durability.md) for complete
stopped backup/restore and [deployment](../how-to/deploy-to-production.md) for
native services and container storage.
