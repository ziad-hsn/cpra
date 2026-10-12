---
title: Provider test options
description: Evidence boundaries for CPRa local services, protocol mocks, provider sandboxes and actual account effects.
cpra_scope: development
---

# Provider test options

The current inventory contains 35 built-in roles: 14 checks, six recovery roles
and 15 notification roles, including A2A recovery and notification. Use the
[verification guide](provider-testing.md) for supported configuration, current
report fields and execution commands. Missing configuration remains not verified.

Local integration exercises a real local service through the production driver.
A protocol mock checks request/response contracts. A provider sandbox exercises
its designated test boundary, which may suppress delivery. A live-account
observer confirms the actual designated effect. These are different evidence;
neither a successful HTTP response nor a mock proves completed recovery or
notification delivery.

The [dated provider research](review/provider-research-2026-09-10.md) preserves
the September investigation of test endpoints and the original 33 roles. Its
source-specific limitations and proposed changes are historical, including the
then TCP-only gRPC path and report fields. Current gRPC health mode, endpoint
overrides and evidence classification are documented in the current guides.
That research is not a current provider-account qualification matrix, and its
passing-case counts are not new verification results.
