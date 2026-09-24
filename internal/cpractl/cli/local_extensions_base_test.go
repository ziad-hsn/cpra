//go:build !externaljobs

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestLocalExtensionsExcludeWorkerAuthentication(t *testing.T) {
	root := NewRootCommand()
	command, _, err := root.Find([]string{"local", "worker-auth"})
	if err == nil && command != nil && command.Name() == "worker-auth" {
		t.Fatal("default build registered worker administration")
	}
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"local", "--help"})
	if err := root.ExecuteContext(t.Context()); err != nil || strings.Contains(output.String(), "worker-auth") {
		t.Fatal("default local help exposed worker administration", err)
	}
}
