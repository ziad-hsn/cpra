//go:build externaljobs

package cli

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRulesTestExternalExampleUsesOnlySuppliedContract(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "/missing/do-not-read")
	for _, code := range []string{"custom.cache.full", "custom.cache.undeclared"} {
		root := NewRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"rules", "test", "-f", filepath.Join("..", "..", "..", "examples", "intervention-rules", "external.yaml"), "--monitor-id", "cache", "--code", code, "--phase", "check", "--expect-rule", "inspect-full-cache"})
		err := root.ExecuteContext(t.Context())
		if (err == nil) != (code == "custom.cache.full") {
			t.Fatal(out.String(), err)
		}
	}
}
