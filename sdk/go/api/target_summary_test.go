package api

import (
	"encoding/json"
	"testing"
)

func TestCheckTargetSummaryExcludesSecrets(t *testing.T) {
	for _, c := range []struct{ driver, config, want string }{
		{"http", `{"url":"https://user:private@api.example/secret-in-path?token=private#private"}`, "https://api.example"},
		{"http", `{"url":"http://bad%private"}`, ""},
		{"tcp", `{"host":"::1","port":443}`, "[::1]:443"},
		{"mongo", `{"uri":"mongodb://private@host/private"}`, ""},
	} {
		d := DriverConfig{Type: c.driver, Config: json.RawMessage(c.config)}
		if got := CheckTargetSummary(d); got != c.want {
			t.Fatalf("%s summary = %q; want %q", c.driver, got, c.want)
		}
		d.CredentialRefs = Pointer(map[string]string{"url": "private-source", "host": "private-source"})
		if got := CheckTargetSummary(d); got != "" {
			t.Fatalf("credential-backed target leaked: %q", got)
		}
	}
}
