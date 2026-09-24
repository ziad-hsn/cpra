//go:build externaljobs

package collection

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestFreezeProfileExcludesExternalVariants(t *testing.T) {
	external := `{"type":"external","config":{"jobTypeID":"custom","version":"v1"}}`
	builtIn := `{"driver":{"type":"http","config":{"url":"https://example.test"}},"interval":"60s","timeout":"5s"}`
	for _, input := range []string{
		`{"apiVersion":"cpra.io/v2","kind":"JobType","metadata":{"id":"custom"},"spec":{"kind":"check","handler":"handler","protocolVersion":"v1","version":"v1"}}`,
		`{"apiVersion":"cpra.io/v2","kind":"NotificationEndpoint","metadata":{"id":"custom"},"spec":` + external + `}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":{"driver":` + external + `,"interval":"60s","timeout":"5s"}}}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":` + builtIn + `,"recovery":{"driver":` + external + `}}}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":` + builtIn + `,"notifications":{"red":{"driver":` + external + `}}}}`,
		`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"custom"},"spec":{"check":` + builtIn + `,"notifications":{"red":{"notifyType":"external","groupRef":"team"}}}}`,
	} {
		parent := t.TempDir()
		if f, err := FreezeProfile(context.Background(), []Source{Reader("source", strings.NewReader(input))}, FileNormalizationProfile, Options{TempDir: parent}); err == nil || f != nil {
			t.Fatal("profile expanded under external build")
		}
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) != 0 {
			t.Fatal("rejected external resource left staging", err)
		}
		ordinary, err := Freeze(context.Background(), []Source{Reader("source", strings.NewReader(input))}, Options{})
		if err != nil {
			t.Fatal("ordinary tagged Freeze behavior changed", err)
		}
		if ordinary.NormalizationProfile() != "" {
			t.Fatal("ordinary tagged input acquired base label")
		}
		_ = ordinary.Close()
	}
}
