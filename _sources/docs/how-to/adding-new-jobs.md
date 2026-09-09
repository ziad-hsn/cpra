---
title: "Add a check or action"
description: "Extend CPRa through schema, execution and verification while respecting contexts and incident ownership."
---

# Add a check or action

Start with an existing integration whose protocol and lifecycle are similar. The integration contract spans configuration, execution, and result handling.

1. Add the concrete configuration fields and copy behavior in `internal/loader/schema`.
2. Register YAML and JSON decoding and semantic validation. Exercise strict unknown-field behavior for polymorphic configuration.
3. Implement execution in `internal/jobs` and connect the dispatch path.
4. For optional SDK dependencies, add a build-tagged implementation and an explicit unavailable stub.
5. Verify result classification, deadline and cancellation behavior, and resource cleanup.
6. Update the driver documentation and add a focused regression or protocol fixture.

## Checks

Return the target result to the controller. Keep driver I/O bounded by the check context and close owned connections. A warning that does not mean a failed check must preserve that distinction through the result path.

## Recovery actions

The controller owns admission and verification. Do not create an independent retry loop that silently duplicates side effects. A deadline or lost response can leave the external action outcome uncertain.

## Notifications

Test provider-required fields and response classification. Distinguish retryable HTTP rejection from accepted delivery. Do not equate an HTTP acceptance with a human acknowledging the alert.

## Verification scope

Run the affected tests and `make test-all-drivers` for tagged code. Positive fixtures matter as well as timeout cases: rejecting every input is not a valid fix for a deadline bug.

Use the [source layout](../developer-guide.md) to locate current interfaces. Older template files and archived documentation may describe contracts that are no longer present.
