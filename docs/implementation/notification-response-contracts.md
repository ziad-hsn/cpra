# Notification response contracts

Reviewed on 2026-10-12 for the private release-repair branch. This records
production-path classification and local regression coverage, not live-account
delivery certification. A transport failure, generic HTTP 5xx, HTTP 408,
redirect or unrecognized response leaves an effect unknown. Raw response bodies
are not exposed through action diagnostics.

| Provider | Implemented rejection evidence and delay policy | Primary contract |
| --- | --- | --- |
| Slack | Known webhook validation errors are permanent rejection. HTTP 429 requires a valid `Retry-After` to authorize retry. | [Webhook errors](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/), [rate limits](https://docs.slack.dev/apis/web-api/rate-limits/) |
| Discord | Selected validation/authentication codes are permanent rejection. HTTP 429 requires a bounded JSON `retry_after`; a supplied valid header may increase the delay. | [Rate limits](https://github.com/discord/discord-api-docs/blob/main/developers/topics/rate-limits.mdx), [error codes](https://github.com/discord/discord-api-docs/blob/main/developers/topics/opcodes-and-status-codes.mdx) |
| Telegram | A successful response requires `ok: true`. An unsuccessful acknowledgement with a matching client error is rejection; 429 needs `parameters.retry_after` before retry. | [Bot API responses](https://core.telegram.org/bots/api#making-requests), [response parameters](https://core.telegram.org/bots/api#responseparameters) |
| Pushover | A successful response requires `status: 1`. `status: 0` with a client-error or success HTTP status is permanent rejection, including exhausted monthly quota. | [API responses and quota](https://pushover.net/api) |
| Teams | The documented throttling marker in the response body is recognized even with HTTP 200. HTTP 429 is also rejection; CPRa applies bounded backoff. | [Connector throttling](https://learn.microsoft.com/en-us/microsoftteams/platform/webhooks-and-connectors/how-to/connectors-using) |
| Twilio | HTTP 429 is documented as unprocessed and permits retry. A matching client-error status and Twilio error code are permanent rejection. | [Error 20429](https://www.twilio.com/docs/api/errors/20429) |
| PagerDuty | HTTP 400 is rejection. HTTP 429 permits retry; a valid header is honored. Without one CPRa selects a two-minute delay, which cannot fit the ordinary 45-second budget. | [Events API overview](https://docs.pagerduty.com/developer/events-api-v2-overview) |
| Opsgenie | Documented client errors are rejection. HTTP 429 permits bounded backoff. A failed processing-status read after asynchronous acceptance remains unknown. | [Rate limiting](https://docs.opsgenie.com/docs/api-rate-limiting), [response contract](https://docs.opsgenie.com/docs/response) |
| Generic webhook, Datadog, Mattermost, VictorOps | Successful HTTP status establishes acceptance only. Other responses remain unknown; this repair does not infer retry permission from a generic 429. | Conservative CPRa policy; no sufficient replay contract established in this checkpoint. |

Body decoding is bounded to 64 KiB. Invalid, duplicate or contradictory JSON,
oversized responses and body-read failures do not authorize retry. Retry delays
are bounded to 24 hours; the existing overall deadline remains authoritative.
Malformed or duplicate delay headers cannot trigger an immediate retry.

The controller persists the provider delay in the format-40/41 result command,
then derives the next queued attempt deterministically. A longer provider delay
does not extend the deadline. The original delivery identity, cause and deadline
remain stable across retries. This policy may stop delivery rather than retry
within a provider's much longer rate-limit period.

Local production-driver tests cover acknowledgement bodies, fractional delays,
quota rejection, Teams' body-only throttling, malformed responses, response-read
timeouts, and HTTP 5xx ambiguity. A real Raft snapshot/restart test preserves
the queued delay, identity and deadline. Go 1.25 and focused race checks pass.
These checks do not establish receipt by a real destination or provider account.
