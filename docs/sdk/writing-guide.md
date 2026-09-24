---
title: SDK candidate · Maintain the SDK lessons and reference
description: SDK candidate · Maintain the SDK lessons and reference for the reviewed CPRa source; see the version and availability notice.
cpra_scope: sdk
---

> **Unpublished SDK candidate:** this guide follows the source in this checkout. Confirm the connected server’s capabilities and release qualification before using candidate APIs. See [availability and source](../versions.md#go-sdk-and-approved-management-plan).

# Maintain the SDK lessons and reference

Keep each lesson focused on a visible result. Begin with one command that works
without an account, show its output, and explain the small piece of code that
produced it. Put configured deployment instructions after the local exercise.
The reader should be able to stop the demo and know what state remains.

## Sources and decisions

| Source | Applied decision |
| --- | --- |
| [Diátaxis tutorials](https://diataxis.fr/tutorials/) | Give readers a concrete task and visible results before extended explanation |
| [Diátaxis reference](https://diataxis.fr/reference/) | Keep exact methods and fields in consistently generated reference pages |
| [Google procedure guidance](https://developers.google.com/style/procedures) | State prerequisites, use ordered actions when order matters, and show expected results |
| [Google voice and tone](https://developers.google.com/style/tone) | Use direct, conversational instructions with specific verbs and no promotional claims |
| [Google clear sentences](https://developers.google.com/tech-writing/one/clear-sentences) | Name the actor and action; split sentences that carry unrelated steps |
| [Go doc comments](https://go.dev/doc/comment) | Keep exported comments attached to the API they describe and use runnable Go examples |

These references guide the format. They do not certify this documentation or
identify whether a sentence was written by a person or an AI system.

## Editing passes

First check the reader's path: prerequisites, command, expected output, relevant
code, failure, and cleanup. Run the command from the directory printed in the
guide. Confirm flags against `-help`; use the output produced by the program.

Next check every behavior claim against the implementation. Specify whether an
observation came from a mock queue, HTTP fixture, encrypted local journal, real
cluster, or cloud account. Replace general assurances such as “handles failures”
with the actual rule: “leave the SQS message pending when registration state is
uncertain.”

Finally edit the prose. Remove empty introductions, promotional adjectives,
repeated conclusions, unnecessary contrast formulas, and words such as
“seamlessly” that hide a missing explanation. Prefer “the worker retries the
same recorded result” to “the worker provides robust recovery.” These are
editing prompts, not an AI-origin detector. Keep technical terms when they help
readers distinguish an incident, assignment, execution, receipt, or version.

## Review iterations for this change

The first pass separated finite local demos from configured modes, because the
current server has no v2 management routes. Each lesson now names that boundary
before its first command.

The second pass replaced vague coverage with observable checks: Kubernetes
creates DNS monitors for all Services and TCP monitors for usable TCP ports;
AWS verifies target absence before changing a mapped monitor; the DAO worker
reports gateway acceptance separately from handset delivery.

The verification pass runs the commands and checks duplicate delivery, stale
versions, lost replies, malformed inputs, and build exclusion. Independent
review corrections and executed results are recorded in [verification](verification.md).
Update that record when the behavior or commands change.
