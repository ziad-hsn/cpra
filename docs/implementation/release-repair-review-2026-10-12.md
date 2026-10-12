# Independent repair checkpoint review — 2026-10-12

Scope: `2ba7b73f0c9d398bfee2fbcf3896f70ccc1fb37c` through
`b59d5b8ba60ed82f0a5b6b57857be27a2e380725` on `codex/release-repairs`.
Reviewer: a separate read-only reviewer agent, authorized by the user.
This is source review of a development checkpoint, not final release qualification.

The reviewer traced notification transitions and format migration, local and
tagged worker admission, PagerDuty/Opsgenie correlation, collection grouping and
continuation, access initialization and guards, static driver validation, and
Linux generation ownership and deletion. It ran no tests or provider operations
and excluded concurrent licensing/material changes.

| Finding | Confirmed trigger | Required repair |
| --- | --- | --- |
| R2-1: queued maintenance alerts | The controller prefilter postpones the entire monitor during maintenance. A window between health observations leaves queued notifications intact for delivery afterward. | Commit cancellation before the prefilter can defer code work; test the actual controller with no pulse during the window, including restart. |
| R2-2: warning alert correlation | A delivered warning receives a warning identity. Escalating to an outage replaces it with an incident identity, so eventual recovery closes only the latter provider alert. | Preserve provider correlation across severity escalation while notification episodes remain distinct; test warning, outage and confirmed recovery through provider dispatch. |
| R2-3: old envelope with new fields | Minimum-format selection checks notification version/deadline but misses endpoint policy IDs and other new identity fields. An old configure envelope can enter state that its snapshot reader rejects. | Check every nonzero delivery-format field at the command boundary; test rejection of old envelopes and a current-format snapshot round trip. |

The reviewer found no additional concrete deletion or pin-lifetime defect in the
inspected Linux generation path. Retirement catalog publication precedes deletion
intent removal, and native ledger closure waits for frozen readers. Its source
trace also confirmed that Opsgenie status-read failure after acceptance remains
unknown rather than repeating the accepted POST.

Native macOS/Windows retirement and growth, service permissions, full crash,
browser, compiler, provider, performance and distribution matrices remain open.
Those exclusions are not passing evidence. All three findings require repair and
review before the related work can be signed off.
