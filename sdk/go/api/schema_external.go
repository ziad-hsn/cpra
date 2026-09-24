//go:build externaljobs

package api

import _ "embed"

//go:embed schema_external.json
var schemaBytes []byte

// Schema returns an independent copy of this build's OpenAPI contract.
func Schema() []byte { return append([]byte(nil), schemaBytes...) }
