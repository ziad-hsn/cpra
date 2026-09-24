// Package commitment computes bounded keyed commitments for collection uploads.
// It performs no HTTP requests, resource decoding, or file operations.
//
// Resource commitments cover the exact JSON object bytes transmitted by a client,
// not a re-encoding performed by a receiver. Callers must validate the decoded
// resource identity and schema separately. Accumulators enforce count and order;
// the staging layer must enforce uniqueness of resource identities.
//
// Callers own key generation, storage and lifetime. A key must contain 32 random
// bytes, shared only with authorized verification code and encrypted staging.
// This package never generates keys or opens files. It does not retain input
// resource/source buffers after a call returns. Its HMAC implementations retain
// internal key-derived state. Defer Close on every accumulator to discard owned
// buffers and references on cancellation or failure; neither Close nor Finish
// promises erasure of every copy from Go memory. Accumulators are
// single-owner values and must not be copied or used concurrently.
//
// See README.md and testdata/v1.json for the exact framing and interoperability
// vectors. Source labels are opaque ordered tokens, never filenames or URLs.
package commitment
