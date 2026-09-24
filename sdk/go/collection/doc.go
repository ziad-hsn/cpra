// Package collection loads, freezes, validates, and applies CPRa resource sets.
// It accepts files, directories, readers, explicitly requested URLs, and typed
// resource streams without depending on the CPRa application module.
//
// # Freeze before applying
//
// Pass [Source] values to [Freeze], or pass a [ResourceSource] to
// [FreezeResources]. [Slice] adapts a small in-memory resource set. Freezing reads
// the input once, validates resource shapes, rejects duplicate identities, and
// retains source locations locally. Each Freeze creates a random private key and
// commits to raw sources, exact serialized objects, their order and count. A later
// input edit cannot change that [Frozen] collection. Always call [Frozen.Close] to remove private plaintext
// staging. Callers retain ownership of supplied readers.
//
// Use [ValidateReferences] for local dependency checks. [Apply] creates inactive
// server staging, uploads bounded chunks, requests complete preflight, and only
// then requests activation. Activation uses per-resource conditions; there is no
// implicit pruning or whole-collection rollback. Server v2 implementation remains
// a prerequisite for remote application.
//
// # Progress and retry
//
// Retain [Result.OperationID] even when an operation returns an error. [Resume]
// checks the original operation and frozen content identity before continuing.
// Resume needs the original open Frozen instance. Refreezing identical files also
// creates a different identity. Every remote
// receipt must identify the same format, content and count before continuing.
// Changed input needs a different operation. Cancelling a waiting context does
// not cancel a server operation; cancellation is an explicit client method.
//
// [FreezeProfile] opts file inputs into [FileNormalizationProfile] at creation.
// After a client restart, [Reselect] can complete that original upload using
// selected raw sources and the identity retained by the server. It does not
// validate or activate configuration. Known attempts retain their already-staged
// bytes; selected files supply the missing suffix. An uncertain mutation reply
// stops the helper, leaving its operation and known attempt handles available.
//
// # Input boundaries
//
// Directory order is deterministic and recursion is explicit in [Options]. URL
// sources use a separate unauthenticated client and never inherit CPRa tokens.
// Input, resource, and staging limits fail explicitly. Staging can contain
// credentials from desired configuration; keep it private and exclude it from
// logs and backups. Freezing and validation execute no provider operations.
package collection
