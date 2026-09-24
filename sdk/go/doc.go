// Package cpra provides typed clients for CPRa management and observation APIs.
//
// This unpublished candidate has local server integration for resource management,
// controls, action review, operation reads, observations and bounded ephemeral
// collection preflight. Resumable collections
// and external-worker server integration remain unfinished. Discover the connected
// server's capabilities; a client method alone does not establish server support.
//
// # Getting started
//
// Construct a [Client] with [New], then call a resource service such as
// [MonitorsService]. Methods accept a context and return a [Response] containing
// typed data and request, resource-version, and operation metadata. Clients may
// be shared between goroutines after construction; treat their service fields
// and caller-supplied transports as immutable.
//
// [github.com/ziad-hsn/cpra/sdk/go/api] contains resource types and local driver
// validation. [github.com/ziad-hsn/cpra/sdk/go/collection] loads and freezes
// multi-file input before uploading and activating a collection. Neither package
// loads CPRa's controller or invokes provider drivers.
//
// # Conditional changes and errors
//
// Replacement, patching, deletion, and relevant controls require the version
// observed by the caller. Configuration versions and incident/control revisions
// have different meanings; use the version required by the method. A conflict
// requires a caller decision; the client does not fetch a newer version and
// overwrite it automatically.
//
// Use [errors.Is] with [ErrConflict], [ErrAmbiguous], or other sentinel errors,
// and [errors.As] to inspect an [Error] or [AmbiguousError]. An uncertain mutation
// may have committed. Retain returned operation metadata and inspect the
// original operation before attempting another change.
//
// # Request policy
//
// Authenticated connections require HTTPS unless [Config.AllowInsecureHTTP] is
// explicitly set for the configured origin. Redirects and automatic mutation
// retries are disabled. Ordinary requests default to ten seconds and bounded
// decoded responses; caller contexts can impose shorter deadlines. [TokenSource]
// supports credentials supplied at request time without a background poller.
//
// Optional custom-job APIs require the externaljobs build tag. The server also
// requires that tag, runtime enablement, and scoped authorization. The separate
// worker module is never a dependency of a normal SDK consumer.
package cpra
