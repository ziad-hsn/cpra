package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommandDoesNotResolveCredentialsOrServer(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "/missing/token")
	t.Setenv("CPRA_SERVER", "https://invalid.invalid")
	for _, argument := range []string{"version", "--version"} {
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{argument})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "commit") || !strings.Contains(out.String(), "source date") {
			t.Fatal("missing identity", out.String())
		}
	}
}
