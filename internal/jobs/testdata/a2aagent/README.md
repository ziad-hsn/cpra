# A2A interoperability fixture

This separate-process fixture uses the official
[`a2a-go/v2` server at v2.6.0](https://github.com/a2aproject/a2a-go/releases/tag/v2.6.0)
to check CPRa's HTTP+JSON transport and both action roles against another
implementation. The SDK
requires Go 1.26 or later. It is a dependency of this test fixture only; CPRa
uses the shared Go 1.26 minimum and imports no A2A SDK execution libraries.

The fixture binds an ephemeral loopback TLS listener. Its certificate and URL
are passed to the parent over stdout; the private key remains in the child.
The parent verifies TLS with that certificate and uses a fixed local test token.
Closing stdin stops the fixture. No provider account, remote agent, Docker
daemon, or model service is used.

The server's production `a2asrv.NewRESTHandler` handles every A2A request. Its
executor emits one of seven fixed scenarios: a standalone message, completed
task, failed task, rejected task, canceled task, input required, or authorization
required. A separate local observation endpoint counts executor invocations and
cancellations and records the received message identity. Tests require exactly
one invocation and zero cancellation calls for each scenario. The seven cases
run through the transport, intervention job and notification job separately.
Notification acceptance is not a task-completion assertion.

From the repository root, with Go 1.27.1 selected:

```sh
mkdir -p bin/verification/a2a
(cd internal/jobs/testdata/a2aagent && GOWORK=off go mod verify)
(cd internal/jobs/testdata/a2aagent && GOWORK=off CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -o ../../../../bin/verification/a2a/agent .)
python3 scripts/sdk/workspace.py --output "$PWD/bin/verification/a2a/go.work" --no-github-env
GOWORK="$PWD/bin/verification/a2a/go.work" CPRA_A2A_AGENT="$PWD/bin/verification/a2a/agent" go test -race -count=1 -tags=a2ainterop -run '^TestA2A' ./internal/jobs ./internal/controller/systems
```

The build destination in the fixture directory resolves to the repository's
`bin/verification/a2a/agent`. The same compiled fixture can be used while running
the final command with Go 1.26. The `a2ainterop` tag selects tests only: a missing
`CPRA_A2A_AGENT` fails the test instead of skipping it. The ordinary jobs tests
exercise local wire/error fixtures without this additional module.

This checks local protocol interoperability. It does not prove a remote agent's
authorization enforcement, investigation quality, successful recovery, or
public manifest/credential integration. The controller tests separately use
Raft, local network targets and forced process termination; the upstream fixture
does not stand in for a configured provider account.
