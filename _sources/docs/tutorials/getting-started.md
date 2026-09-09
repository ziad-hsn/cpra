---
title: "Installation and builds"
description: "Build the CPRa server and CLI, select optional drivers, rebuild dashboard assets and package Linux distributions."
---

# Installation and builds

## Build from source

Use Go 1.25 or later and Make. The published code is on the repository's `main` branch:

~~~sh
git clone --branch main https://github.com/ziad-hsn/cpra.git
cd cpra
make
~~~

`make` builds the server and CLI with embedded dashboard assets. Run `./bin/cpra -version` or `./bin/cpractl --version` to inspect the build.

These docs correspond to [a370969](https://github.com/ziad-hsn/cpra/commit/a370969b041b399c0778318d8915ce059fd74294). Check out that commit to reproduce this documentation snapshot.

## Include optional drivers

~~~sh
make BUILD_TAGS='redis postgres kubernetes'
~~~

Optional checks: `redis postgres mysql mongo rabbitmq kafka`. Optional recovery: `kubernetes aws systemd`. Optional alerts: `teams twilio`.

A configuration can name an optional driver even when its implementation is absent from the binary. Build the required tags before trying that integration. [Driver reference](../reference/jobs-reference.md)

## Rebuild the dashboard

Use Node.js 24, pnpm 11.22.0, and Python 3:

~~~sh
make dashboard-build dashboard-check
~~~

The build installs dependencies from the lockfile, builds the frontend, and stages its assets for Go embedding. Dashboard checks run TypeScript, lint, and tests.

## Create distribution archives

~~~sh
make release VERSION=0.1.0
~~~

Choose a version for your own build. The example does not imply that a `0.1.0` GitHub Release exists. Packaging creates Linux amd64 and arm64 server/CLI archives, a source archive, dependency notices, and SHA-256 checksums under `dist/release`. It does not upload them.

## Build a container

~~~sh
docker build -f docker/Dockerfile -t cpra:local .
~~~

The container runs as UID 1001. It needs a readable manifest and write access to configured log destinations. See [deployment](../how-to/deploy-to-production.md#containers) before mounting host or Docker access.
