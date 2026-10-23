# Manage configuration with cpractl

`cpractl` uses the public Go SDK for v2 Monitor, NotificationEndpoint, Recipient,
NotificationGroup and Credential resources. It can create, read, describe,
replace, merge-patch and delete one resource at a time. Configuration writes are
conditional and produce operation receipts. A saved configuration can precede
its application by the controller; inspect the receipt when that distinction
matters.

It also provides an ephemeral multi-file `diff`. This freezes and validates all
selected inputs before asking the server for proposed changes. It does not
create an apply operation or change active configuration.

Monitor reads use stable resource IDs and bounded cursor pagination:

```bash
cpractl get monitors --limit 100 -o json
cpractl get monitor service-api -o yaml
cpractl describe monitor/service-api
cpractl get incidents --monitor-id service-api --limit 100
cpractl get history service-api --limit 100
cpractl get queues --limit 100
cpractl get state
```

Continue a list with its returned `--cursor` and the original page limit. Monitor
lists accept `--selector`; they do not use numeric entity IDs or page numbers.
Human monitor lists include `OBSERVED HEALTH`: `up`, `down`, `degraded` or
`unknown`, based on the latest reported check. This is distinct from saved
configuration, incident controls and pending health verification. `describe`
shows configuration identity, check/recovery settings and observed timing;
absent measurements appear as `unavailable`, and terminal controls are escaped.
Use `-o json` or `-o yaml` for the canonical resource, including full configured
fields. Those machine formats retain the wire contract.
`get state` reports controller and storage health. Use `get actions --monitor-id
service-api` for a monitor's action state. `get overview` is not a current command.

Request failures retain their SDK error classification. Human output includes
the HTTP status, reviewed problem code, valid request/operation identities and
up to ten field names with validation codes. Raw server detail, submitted values,
provider bodies and filesystem paths are not printed. Use the operation ID to
inspect the original receipt before explicit continuation; confirmed rejection
before submission does not imply that an earlier operation was canceled.

The dashboard reports the same bounded metadata for resource and monitor-control
failures. Collection controls also identify the failed phase. Validation and
activation continue the original immutable collection only after explicit
operator action; displaying an error or rereading progress submits no write.

## Connection and authorization

Start an explicitly enabled management server with named identities and TLS.
The [local management example](reference/source/examples/management/README.md.txt) creates a
private example installation. Connect to it without putting a bearer token in
the command line:

```bash
export CPRA_SERVER=https://127.0.0.1:8060
export CPRA_AUTH_TOKEN_FILE="$HOME/cpra-management-example/private/operator.token"
export CPRA_CA_FILE="$HOME/cpra-management-example/private/server.pem"
cpractl get monitor-configurations
```

The equivalent flags are `--server`, `--token-file`, and `--ca-file`. The token
file is reread for each request. `--ca-file` adds trust roots to the API
HTTPS client; it does not alter operating-system trust. Each request defaults to a ten-second timeout; change it with
`--request-timeout`.

Keep bearer environment variables scoped to this CLI terminal or use explicit
connection flags. `cpra` also reads `CPRA_AUTH_TOKEN` and `CPRA_AUTH_TOKEN_FILE`
for observation bootstrap; a named operator token must not be passed to those
server inputs. The [quickstart](tutorials/quickstart.md) explains validation from
a shared terminal.

Authenticated v2 HTTP is rejected by default. `--allow-insecure-http` explicitly
permits it only for the configured origin and does not grant server permissions.
Use it only with an intentionally configured local development server; a server
can still require HTTPS. Neither redirects nor automatic mutation retries are
enabled. A reader identity can observe the resources the server permits. Writes
require a named operator identity; notification Recipients are contacts, not
login accounts.

## Local encryption setup

Local wrapping keys are administered on the CPRa host, independently of API
credentials and the server's write-only Credential resources:

```sh
cpractl local encryption init --key-file /absolute/config/keys/wrapping.key --data-dir /absolute/state
cpractl local encryption inspect --key-file /absolute/config/keys/wrapping.key --data-dir /absolute/state
```

The commands require explicit absolute paths, use no API connection, and print
only a non-secret `key_id` in JSON. Initialization never overwrites an existing
key. Inspection validates its file permissions and identity without changing
configuration or opening Raft. See [native key setup](native-installation.md#initialize-a-local-encryption-key)
for service reader permissions, interrupted initialization, and retaining keys
needed by old records and backups.

## Create contacts, destinations, and a monitor

The [endpoint](reference/source/examples/cpractl/endpoint.yaml.txt),
[recipient](reference/source/examples/cpractl/recipient.yaml.txt),
[group](reference/source/examples/cpractl/group.yaml.txt), and
[monitor](reference/source/examples/cpractl/monitor.yaml.txt) examples contain one resource per
file. Apply the dependencies in this order: credential, endpoint, recipient,
group, monitor.
These commands use the connection environment above.

A `Credential` holds a write-only protected value. In an interactive terminal,
the following optional Python helper prompts without echo and pipes one resource
directly into the CLI. It stores no intermediate plaintext file and places no
secret in command arguments. Supply the designated webhook URL for your own
test destination:

```bash
python3 - <<'PY' | cpractl create credential -f - -o json
import getpass
import json

value = getpass.getpass("Webhook URL: ")
if not value:
    raise SystemExit("A webhook URL is required")
print(json.dumps({
    "apiVersion": "cpra.io/v2",
    "kind": "Credential",
    "metadata": {"id": "oncall-webhook-url"},
    "spec": {"value": value},
}))
PY

cpractl create endpoint -f examples/cpractl/endpoint.yaml
cpractl create recipient -f examples/cpractl/recipient.yaml
cpractl create group -f examples/cpractl/group.yaml
cpractl create monitor -f examples/cpractl/monitor.yaml
```

The endpoint selects the `webhook` driver and refers to the encrypted credential
for its URL. The Recipient refers to that endpoint. The group refers to the
Recipient. The monitor's red notification rule selects `notifyType: webhook`
and the group. CPRa resolves that type against the group's destinations; it
does not choose a delivery method from a contact name.

The monitor example starts disabled and uses `example.invalid`. Replace the
target and review the configuration before enabling it. Reading or describing
these resources does not request a check or send a notification. No check-now
command exists.

For automation, a Credential can instead come from a protected local file or
stdin supplied by your secret-management process. Credential reads show
availability and metadata, never the stored value. All output formats remove
an accidental credential-value echo from the server. Omit `spec.value` when
editing only its description; supply a new value explicitly when rotating it.
Keep local input files protected and out of version control.

## Edit with the version you reviewed

Read the resource first and record its `metadata.resourceVersion`:

```bash
cpractl get monitor/service-api -o json > monitor.current.json
```

Review a copy, edit the desired configuration, and replace it using that same
observed version. `OBSERVED_VERSION` below is the exact opaque field from your
read, without HTTP quotes; it is not a new version fetched by the command:

```bash
cpractl replace monitor/service-api -f monitor.edited.json \
  --resource-version OBSERVED_VERSION -o json
```

If the file contains a resource version, it must match the flag. Creation must
omit server-assigned UID, resourceVersion and generation. Stable IDs identify
resources across display-name changes; delete/recreate produces a new
incarnation and version.

Resource, control, incident and action-review revisions are distinct opaque
preconditions. Their formats may differ; do not parse them as UUIDs, manufacture
a revision or substitute one kind for another. Read the exact resource/control
or action being changed and use the corresponding revision from that response.

Use a JSON merge-patch file for a targeted edit. The
[patch example](reference/source/examples/cpractl/disable.json.txt) disables the monitor by
explicitly setting `spec.enabled` to `false`:

```bash
cpractl patch monitor/service-api --patch-file examples/cpractl/disable.json \
  --resource-version OBSERVED_VERSION -o json
```

Merge patch preserves omitted fields and sends explicit `false`, zero and
`null` values unchanged for server validation. Only `--type=merge` is supported.
Patch values are accepted from a file or stdin, not an inline argument. A
resource-version conflict requires another read and review of your intended
change; cpractl never fetches a newer version and overwrites it automatically.

Delete also requires the version you reviewed:

```bash
cpractl delete monitor/service-api --resource-version OBSERVED_VERSION -o json
```

Referenced dependencies cannot be deleted while consumers still use them.
Delete or edit consumers first. Ordinary resource deletion is distinct from
deleting retained incident history or a local Raft directory.

## Observe results without collecting the fleet

The configuration aliases are:

| Resource | Collection read | Stable-ID read or mutation |
| --- | --- | --- |
| Monitor | `get monitor-configurations` | `monitor/service-api` |
| NotificationEndpoint | `get endpoints` | `endpoint/oncall-webhook` |
| Recipient | `get recipients` or `get contacts` | `recipient/service-oncall` |
| NotificationGroup | `get groups` | `group/service-team` |
| Credential | `get credentials` or `get secrets` | `secret/oncall-webhook-url` |

Collection reads fetch one page: 100 resources by default, 500 maximum. Continue
with the returned `nextCursor`, keeping the original limit and selector. The
CLI never follows all pages implicitly. Label selectors are sent to the server;
an unsupported selector receives an error rather than client-side fleet
filtering. Expired cursors require a fresh list.

The requested limit is an upper bound. Resource pages can end earlier to stay
within the 8 MiB response ceiling or the request's read budget. Cursor retries
retain the original snapshot and starting position; the number of returned
resources can vary with read cost. Follow `nextCursor` until it is empty to
complete the ordered traversal.

```bash
cpractl get recipients --limit 100 -o json
cpractl get recipients --limit 100 --cursor RETURNED_CURSOR -o json
cpractl get operation RETURNED_OPERATION_ID -o json
cpractl get operation RETURNED_OPERATION_ID --results --limit 100 -o json
cpractl get operation RETURNED_OPERATION_ID --results --limit 100 --cursor RETURNED_CURSOR -o yaml
cpractl get operations --limit 100 -o json
cpractl get operations --monitor-id service-api --limit 100 --cursor RETURNED_CURSOR -o yaml
```

Copy the returned operation ID exactly. New server-issued handles have the form
`op.<canonical UUID epoch>.<20-digit positive sequence>`; their identity is
separate from resource and control versions. The CLI also accepts canonical
legacy UUID operation IDs. It preserves the complete handle in success receipts,
uncertain-response messages and follow-up reads. Do not construct an operation ID
from a resource version or remove its leading zeroes.

`get operations [ID]` also accepts the existing singular `get operation ID`.
Without an ID it returns one bounded operation page; preserve the original
limit and optional monitor filter when using its cursor. The monitor filter
selects operations whose original target is that Monitor, not same-named
credentials or indirectly related shared resources. Selectors and automatic
page collection are unavailable. Reading one ID accepts `--limit` and `--cursor`
only when `--results` is selected; `--monitor-id` remains a list-only filter.

`get operation ID --results` reads exactly one execution-result page, with a
default limit of 100 and a maximum of 500. Continue with the returned cursor and
the same limit. The table shows result availability and aggregate counts, then
separate configuration-decision and controller-disposition columns. An accepted
configuration remains committed when its controller child fails. Pending or
expired results have no item rows; a canceled parent can still have pending
results. Each row distinguishes restore invalidation from ordinary supersession;
wide output includes the original restore ID. JSON/YAML preserve the typed response, including unknown observation
values and omitted availability. This read does not activate, cancel, wait for,
or automatically collect execution results.

Operation progress preserves availability: an omitted uploaded, committed,
applied or validated observation is shown as `unavailable` in tables and remains
omitted from JSON/YAML. Reported zero counts and false flags are retained. Null
progress values are rejected as invalid responses. The operation's state and
progress describe what the server reported; a missing count never proves that
an operation committed or that it made no changes.

Resource JSON and YAML stdout contain a standalone API response with canonical field
names. Mutation receipt notices go to stderr, so they do not corrupt redirected
JSON. Table output sends its continuation cursor to stderr. `describe` reports
the resource's identity, desired configuration and available status;
`describe -o json` or `-o yaml` returns the resource itself. An unavailable status
is not fabricated as a successful observation.

On a lost write response, cpractl reports an unconfirmed outcome and includes a
safe operation identity when one is available. Inspect the original operation
and current resource before deciding what to do next. Do not treat a transport
failure as proof that the server rejected the write. Authentication failures,
permission failures, conflicts and API errors produce a nonzero exit status;
server-supplied detail strings are not echoed as potentially sensitive input.

One explicit failure contract distinguishes allocation uncertainty from a target
mutation: a complete `operationAllocationUnconfirmed` problem, HTTP 503 and
`X-CPRa-Admission: not-submitted`, with no operation handle, guarantees that no
resource or action mutation was submitted. A reservation may still exist. The
CLI reports this distinction and does not retry. Missing, malformed, truncated or
contradictory responses remain unconfirmed mutation outcomes. SDK callers can
identify the validated case with `errors.Is(err, cpra.ErrNotAdmitted)`; it also
matches `cpra.ErrUnavailable` and remains an ordinary typed API error.

This CRUD path fully decodes one resource, at most 1 MiB, before sending a
mutation. A malformed final document or second resource causes no request.
Directory/URL import and resumable collection apply belong to the separate
collection workflow and are not implemented by these CRUD commands. Use the
[CLI control guide](cpractl-controls.md) to acknowledge, dismiss, reopen, snooze,
unsnooze, Ignore, Unignore, disable or enable an exact target. The corresponding server behavior
is documented in [management controls](management-controls.md).

## Preview a complete collection

The dashboard's file import reports local configuration warnings as it prepares
the selected resources, before the optional server preview or any upload. This
also works for collections larger than the 4 MiB server-preview limit. The
preview shows the total warning count and up to ten source-attributed details
per resource page. Warnings, including missing blue notification destinations,
are advisory; they do not invalidate an otherwise valid collection or replace
whole-collection validation.

Teams can keep shared contacts, endpoints and credentials in separate files from
each service's monitors. Select every relevant file for one validation:

```bash
cpractl diff -f shared/contacts.yaml -f services/payments.yaml \
  -f services/orders.yaml
cpractl diff -R -f config/ -o json
cpractl diff -f https://example.com/config/monitors.yaml
cat desired.yaml | cpractl diff -f -
```

Each `-f` accepts a regular file, a directory, an explicit HTTPS URL, or `-` for
stdin. Repeat the flag instead of passing a comma-separated list; commas in a
selected path or URL are preserved. Stdin may appear only once. Directory
selection includes `.yaml`, `.yml`, and `.json` files in lexical order, enters
subdirectories only with `-R`, and does not traverse directory symlinks.
Overlapping file selections are deduplicated; duplicate resource definitions
across distinct sources are rejected even when their values match.

The shared SDK loader accepts standalone resources, resource lists, multi-document
YAML/JSON, and existing CPRa manifest envelopes. A malformed final input prevents
the authenticated preflight request entirely. Empty files and comments produce a
local no-op. For nonempty inputs, the server requires a named operator and validates
the complete submitted graph together with authorized retained dependencies.
Updated resources include changed schema paths from the same captured catalog
versions. Secret values and arbitrary map keys are never shown; arrays and
private maps appear as whole fields. Very large diffs use `metadata` and `spec`
summaries. Omitted field details mean unavailable, not unchanged.

The result is an observation of the current catalog; it is not a reservation or
a guarantee that a later conditional write will succeed.

Table and wide output show resource identity, proposed `create`, `update` or
`unchanged` outcome, the original resource version when available, and the local
file/document/item. JSON and YAML return a CLI report containing `valid`,
`changed`, `items`, and optional source-attributed `errors`. This report
deliberately omits resource specifications, credential values, inventory
commitments and private identity keys. Server-controlled error detail is replaced
by reviewed messages. URL source labels omit query strings, credentials and
fragments.

Diff uses these exit statuses:

| Status | Meaning |
| --- | --- |
| `0` | Valid input with no changes, including an empty collection |
| `1` | Valid input with proposed changes; no error banner |
| `2` | Input, validation, permission, connection, or other failure |

Existing command failures retain status `1`. If using a shell with `set -e`,
handle the diff result explicitly rather than treating differences as a failed
validation:

```bash
status=0
cpractl diff -R -f config/ -o json > diff.json || status=$?
case "$status" in
  0) echo "No changes" ;;
  1) echo "Review diff.json for proposed changes" ;;
  *) echo "Diff failed; inspect the error and any validation report" >&2
     exit "$status" ;;
esac
```

Input URLs use a separate unauthenticated source client. API bearer tokens,
cookies, management trust configuration and provider credentials are not inherited.
HTTPS is required unless `--allow-http-sources` explicitly permits an HTTP input;
that flag is separate from `--allow-insecure-http` for the CPRa API. Source reads
have a 30-second timeout and bounded redirects. Avoid placing signed URLs in shared
shell history even though their query strings are omitted from the report.

Ephemeral diff is bounded to 10,000 resources, 1 MiB per resource, and one
4 MiB preflight request. A larger input fails before requesting active changes;
this command does not switch to durable staging. `--max-staging-bytes` controls
the private temporary spool quota; `0` selects the SDK default of 1 GiB.
`--max-source-bytes` independently bounds cumulative raw source bytes; `0` uses
the staging quota. Individual documents retain the SDK's 16 MiB bound.

Temporary client staging can contain plaintext credentials. It uses restrictive
file permissions, the platform temporary directory, and cleanup on completion,
validation failure, transport failure, or cancellation. Select an appropriate
protected temporary filesystem when processing secrets. A lost response is
reported without an automatic retry. No upload, activation, deletion, provider
operation, or check-now command is performed by `diff`.

On Unix, the native CLI handles Ctrl-C and SIGTERM by cancelling the request and abandoning
a blocked standard-input read so that it can remove its spool before exiting.
A second signal uses the operating system's normal forced-exit behavior. Abrupt process
termination, power loss or a cleanup filesystem failure can still leave temporary
files; cleanup is not a promise that plaintext bytes are securely erased.

## Apply and wait for a collection

```sh
cpractl apply -f monitors.yaml -f recipients.yaml
cpractl apply -f ./configuration -R --wait --timeout=5m
cpractl apply -f monitors.yaml -f recipients.yaml --dry-run=server
cpractl wait operation/OPERATION_ID --timeout=5m
cpractl get operation OPERATION_ID --results --limit=100
```

Replace `OPERATION_ID` with the exact server-issued handle. `apply` accepts the
same repeated file, directory, stdin and explicit URL inputs as `diff`. It
freezes and validates the complete input set before its first API request, then
prepares one admission, uploads inactive staging, waits for sealed validation,
and activates the original operation. Without `--wait`, it returns after
activation admission and prints the operation handle. `--dry-run=server` uses
ephemeral preflight and does not allocate, upload or activate an operation;
proposed differences do not make this dry-run command fail.

`--wait` and `wait operation/ID` await retained execution readiness and print the
first bounded result page. A canceled parent can still have accepted children
in progress. Default apply/wait output shows a compact summary, the original
operation handle and a command to inspect detailed results. Nonzero conflicts,
blocked dependencies and failed/superseded work remain explicit. Use `-o wide`
or `get operation ID --results` for the full counters; JSON/YAML retain the
complete response. Catalog acceptance and controller application remain separate
counts. Partial, failed, canceled or unsupported outcomes produce a nonzero
exit while preserving the returned observation. JSON and YAML contain the
canonical operation object. Use `get operation ID --results --cursor ...` for
later pages; waiting does not collect all pages.

`--timeout` bounds the wait, while `--request-timeout` bounds each API request.
Ctrl-C or a timeout stops the reader and does not cancel server work. An
uncertain activation reply triggers one read of the same content-bound handle;
the CLI never repeats a mutation automatically. Retain the printed handle to
inspect the original attempt before deciding what to do next.

## Recover an incomplete original upload

Select the versioned file profile when creating a collection that may need
upload recovery from another CLI process:

```sh
cpractl apply --file-profile cpra.file.base.v1 \
  -f shared/contacts.yaml -f services/payments.yaml -f config/empty.yaml
```

This still runs the normal apply workflow, including validation and activation.
The flag binds the shared file-normalization contract to the operation at
creation; it does not change plain `apply` or `diff`. Only the exact profile
`cpra.file.base.v1` is accepted. Unsupported profiles fail before opening inputs
or making API requests. Existing unprofiled or typed SDK collections cannot be
relabeled for this recovery path.

Profiled apply permits at most 1,000 expanded sources, 64 MiB of cumulative raw
input, 10,000 resources, 1 MiB per resource, 16 MiB per document, and 512 MiB of
private client staging. The source/staging flags can lower their limits.
With the profile, `--max-source-bytes=0` selects 64 MiB and
`--max-staging-bytes=0` selects 512 MiB. Plain apply retains its existing defaults.

If the original upload is incomplete, keep its exact returned operation ID and
reselect the original inputs:

```sh
cpractl resume-upload operation/OPERATION_ID \
  -f shared/contacts.yaml -f services/payments.yaml -f config/empty.yaml -o json
```

`resume-upload` reads the original operation first, freezes the required raw
sources into private bounded staging, verifies them using the server-held
original identity, and appends only the missing resource rows. Existing accepted
ciphertext is preserved. It never creates a replacement collection, validates
the configuration graph, activates configuration, cancels the operation, or
invokes providers. A successful `complete: true` report means the original upload
is full; it does not mean configuration is applied. Continue validation and
activation separately through the original operation's dashboard controls or SDK.
Running `apply` again creates new intent rather than continuing this handle.

Use the same explicit `-f` order and expanded source boundaries as the original
apply, including empty files. Directory traversal uses lexical order and requires
`-R` for nested files. Comments, whitespace and unused source text participate in
the original raw-source commitment. A browser-created collection uses its browser
file ordering, so reproduce that order explicitly when recovering it with the CLI.
Filenames themselves are not transmitted. Explicit URLs and stdin are supported;
URL content must still supply the required original bytes. Source fetching stays
separate from the authenticated CPRa client.

An interrupted call can return a disposable `attemptID`. Inspect it without
changing its state, then explicitly continue that same attempt:

```sh
cpractl get upload-attempt OPERATION_ID ATTEMPT_ID -o json
cpractl resume-upload operation/OPERATION_ID --attempt ATTEMPT_ID \
  -f shared/contacts.yaml -f services/payments.yaml -f config/empty.yaml -o json
```

The known attempt is read before source acquisition. Bytes already staged in
that attempt remain authoritative; newly selected sources supply only its missing
bytes. A same-length local edit to an already staged prefix is not compared with
the retained prefix. The server verifies the complete assembled raw sources and
normalized resources against the original identity before appending any missing
resource. Changed newly supplied bytes therefore cannot redefine the original
collection or be accepted merely because earlier bytes matched.

When a known attempt is `verifying`, `verified`, `transferring` or `completed`,
the source files are no longer needed and are not reopened. The command polls
existing work, requests upload transfer once verification succeeds, and reads
the original receipt to confirm completion. This invocation can omit `-f`:

```sh
cpractl resume-upload operation/OPERATION_ID --attempt ATTEMPT_ID -o json
```

If the first attempt read reports `failed` with `transfer_failed`, that explicit
invocation may send one Resume request without rereading sources. The server
decides whether its retained transfer can reconcile or continue. Other failed
attempts do not receive Resume. A failure encountered later in the same command
stops it; there is no automatic mutation retry. An already full original upload
returns completion immediately without consulting a potentially expired or
restart-discarded attempt.

Lost mutation replies produce a nonzero exit and preserve any known original
and attempt handles. Inspect the report before explicitly invoking recovery again.
If an attempt-creation response is lost before its ID arrives, the protocol
cannot rediscover it by operation ID. Read the original operation, then wait for
the unknown attempt to expire or for a server restart to discard it before
creating another; repeatedly calling the command is not an automatic recovery
mechanism. Server restart removes attempts, not the original committed upload.
Reads and retries do not extend either expiry.

Recovery JSON/YAML output is a bounded CLI report containing `operationID`,
optional `attemptID`, `complete`, an optional `operation` state/count summary,
and optional `attempt` progress. Tables show the same safe observations. Reports
remain available on partial failure and contain no resources, source names,
paths, credentials, private commitments or arbitrary server diagnostics. Attempt
reads require the original named operator and current collection-write
permissions; read-only credentials cannot inspect another operator's attempt.

Raw recovery staging has a 64 MiB ceiling. `--max-source-bytes` and
`--max-staging-bytes` can tighten it; a larger staging value does not increase
the raw-input ceiling. Temporary files can contain plaintext credentials and
use the same protected temporary-filesystem and cancellation precautions as
other collection inputs. Polling is read-only, at least five seconds apart;
`--request-timeout` still bounds each request. Ctrl-C stops the client and does
not cancel an admitted server transfer.

The SDK's `collection.Reselect` supplies this upload-only workflow. Its separate
`collection.Resume` method still requires the original open `Frozen` and can
continue through activation. Freezing identical files again does not recreate
the original private identity. See the [collection continuation](cpractl-management.md)
for server ownership, storage limits and qualification boundaries.

## Continue the original collection through activation

```sh
cpractl resume operation/OPERATION_ID \
  -f shared/contacts.yaml -f services/payments.yaml -f config/empty.yaml
cpractl resume operation/OPERATION_ID --attempt ATTEMPT_ID --wait
```

`resume` is an explicit continuation of the original operation. It verifies the
exact raw sources and ordered resource identities using the server-held encrypted
original identity, fills missing uploads, reconciles the current phase, then
requests whole-input validation and conditional activation. Complete uploads
still require verification. Ordinary SDK file inputs retain their empty profile;
they are not relabeled as `cpra.file.base.v1`. The same 1,000-source, 64 MiB raw
input and 10,000-resource verification limits apply.

Keep source order, boundaries and contents unchanged, including comments,
whitespace and empty files. An explicit known attempt uses its already staged
bytes as authoritative; newly selected files supply only missing source bytes.
Attempts are disposable and disappear on server restart. The original operation
handle and committed progress survive. A failed/lost response stops continuation
and retains the original handle and any known attempt handle. No write is retried
automatically, and no replacement operation is created.

Continuation depends on the original phase. Losing a client reply does not stop
the server's validation. A queued validation or a finalized original verdict can
survive an owner restart. If the server loses a claimed, unfinished validation,
it records `interrupted`; `resume` reports that disposition and sends no new
work. Inspect the original operation and retained evidence before explicitly
applying new intent. It cannot recompile against changed configuration under the
old handle. An operation already in execution is observed with
`cpractl wait operation/OPERATION_ID`, without another activation request.

`--wait` observes the original execution after activation. Canceling the client
wait never cancels that execution. `resume-upload` remains limited to missing
uploads and never validates or activates. SDK callers use `collection.ResumeFiles`
for file-based continuation, or retain the original open `Frozen` for
`collection.Resume`.

## First-import recovery and validation details

`cpractl local bootstrap inspect` authenticates a stopped loading stage; `discard`
requires its exact identity, an actor and a reason, and proof that no bootstrap or
catalog was admitted. Both require `--data-dir` and `--runtime-config`. Use
`--stage temporary` for interrupted new input loading and `--stage published` for
an older loading stage. Frozen stages resume and cannot be discarded. See
[management startup](management-startup.md) for the protected audit and failure
procedure.

A rejected `apply` keeps its original operation ID and reports `rejected` after a
sealed invalid verdict. It prints at most ten safe field details from the first
returned validation page, using original local source coordinates. Additional
results are explicitly paginated:

```sh
cpractl get operation OPERATION_ID --validation --limit 100
```

The CLI does not fetch the entire collection to display an error and never
activates a rejected collection. Fixing input creates a different content
identity; explicit continuation must retain the original operation and sources.
