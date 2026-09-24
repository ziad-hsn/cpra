//go:build externaljobs

package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeFileBaseDoesNotExpandInExternalBuild(t *testing.T) {
	external := `{"type":"external","config":{"jobTypeID":"custom","version":"v1"}}`
	builtInCheck := `{"driver":{"type":"http","config":{"url":"https://example.test"}},"interval":"60s","timeout":"5s"}`
	for _, input := range []string{
		`{"apiVersion":"cpra.io/v2","kind":"JobType","metadata":{"id":"custom"},"spec":{"kind":"check","handler":"handler","protocolVersion":"v1","version":"v1"}}`,
		`{"apiVersion":"cpra.io/v2","kind":"NotificationEndpoint","metadata":{"id":"custom"},"spec":` + external + `}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":{"driver":` + external + `,"interval":"60s","timeout":"5s"}}}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":` + builtInCheck + `,"recovery":{"driver":` + external + `}}}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":` + builtInCheck + `,"notifications":{"red":{"driver":` + external + `}}}}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":` + builtInCheck + `,"notifications":{"red":{"notifyType":"external","groupRef":"team"}}}}`,
	} {
		visits := 0
		if err := Decode(context.Background(), strings.NewReader(input), DecodeOptions{}, func(item Item) error {
			visits++
			before, _ := json.Marshal(item.Resource)
			if err := ValidateFileProfileResource(FileNormalizationProfile, item.Resource); err == nil {
				t.Fatal("public base helper accepted tagged resource")
			}
			after, _ := json.Marshal(item.Resource)
			if !bytes.Equal(before, after) {
				t.Fatal("profile check modified resource")
			}
			return nil
		}); err != nil || visits != 1 {
			t.Fatal("ordinary tagged Decode no longer supports the external fixture", err, visits)
		}
		err := NormalizeFile(context.Background(), strings.NewReader(input), FileNormalizationProfile, DecodeOptions{}, func(NormalizedItem) error { t.Fatal("base profile emitted an external resource"); return nil })
		if err == nil {
			t.Fatal("base profile silently broadened in an external build")
		}
	}
}
