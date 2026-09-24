// Package api defines CPRa resources, observations, and typed driver settings.
// It contains wire data and local validation without HTTP clients, controller
// internals, or provider execution libraries.
//
// Use [Driver] to construct a validated driver envelope from a configuration such
// as [PulseHTTPConfig]. Build a [Monitor] with that envelope, then validate it
// with [ValidateResourceValue] or send it through the public SDK. Shape validation
// does not contact a target or prove the server has compiled the requested driver.
//
// # Optional fields and identity
//
// Pointer fields distinguish omission from explicit false or zero. [Pointer]
// supplies a pointer to a value. [MergePatch] preserves JSON null when a caller
// needs to remove an optional field; arrays in a merge patch replace arrays.
// Durations use Go-style strings such as "60s".
//
// [Metadata] distinguishes a stable resource ID, an incarnation UID, and a
// configuration resource version. Incident revisions and execution identities
// are separate protocol fields. Preserve the identity required by each operation
// when reconciling resources after a restart or lost response.
//
// # Reading and writing
//
// [DecodeResource] and [StrictDecode] reject invalid desired configuration.
// [DecodeResponse] allows additive observation fields from newer servers while
// checking required fields. Unknown driver variants may be inspected as data;
// [ValidateResource] rejects unsupported variants before replacement. Do not
// turn a partial or unsupported observation into a replacement resource.
//
// The v2 schema remains a candidate until its server is implemented and
// qualified. Custom-job definitions are compiled only with externaljobs;
// ordinary builds contain the base schema and built-in driver configurations.
package api
