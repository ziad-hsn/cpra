# Security policy

## Report privately

Report a suspected vulnerability using [GitHub private vulnerability reporting](https://github.com/ziad-hsn/cpra/security/advisories/new).
Include the affected commit or release, build tags, operating system, a minimal
reproduction, expected and observed behavior, and the likely impact. Replace
tokens, provider credentials, private targets and personal data with fixtures.
Do not post exploit details or secrets in a public issue, discussion or pull
request.

The reporting form requires the repository's private vulnerability reporting
setting to be enabled. If the form is unavailable, open an issue asking only
for a private security contact; do not include vulnerability details. Maintainers
must enable that channel before public release. CPRa is maintained as a personal
project and does not promise a response deadline or a bug bounty.

## Supported versions

CPRa is preparing its first qualified `v0.1.0-rc.1` release. No private candidate
or development commit carries a production-support guarantee. Security repairs
are developed on the current source branch; the published release record will
identify qualified versions and upgrade compatibility. Never roll back persisted
state to a binary that does not support its storage format.

## Operator trust boundary

CPRa performs network checks, notifications and configured recovery actions
with operator-provided credentials. An operator authorized to configure targets
and use credentials is trusted to choose destinations and actions. CPRa does
not provide per-credential destination isolation or security between mutually
untrusted tenants. Keep management access, state, wrapping keys and backups
protected. Do not use broad cloud or host permissions where a designated test
resource or restricted identity is sufficient.

External Go handlers, when compiled with `externaljobs` and explicitly enabled,
run in a separate operator-owned process. That boundary is not a sandbox for
hostile code. Use separate worker identities, files, credentials and deployment
limits. Unknown external outcomes require operator review; restart must not
silently repeat them.

For the reporting workflow, see [GitHub's private-reporting documentation](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/report-privately).
