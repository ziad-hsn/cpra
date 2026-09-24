package clientconfig

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestTokenFileIsRereadAndFailuresDoNotSendAnonymousRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("first-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true}`))
	}))
	defer server.Close()
	cfg, err := FromTokenFile(server.URL, path, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cpra.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	if _, err = c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("second-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Ready(context.Background()); err == nil {
		t.Fatal("missing token did not fail")
	}
	if len(tokens) != 2 || tokens[0] != "Bearer first-secret" || tokens[1] != "Bearer second-secret" {
		t.Fatalf("unexpected auth requests %#v", tokens)
	}
}

func TestTokenBoundsTypeAndErrorsAreSafe(t *testing.T) {
	for name, token := range map[string]string{
		"empty":     " \n",
		"multiline": "secret\nother",
		"nul":       "secret\x00other",
		"too-large": strings.Repeat("secret", 3000),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secret-token")
			if err := os.WriteFile(path, []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := FromTokenFile("https://cpra.example.test", path, false)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe validation error %v", err)
			}
		})
	}
	if _, err := FromTokenFile("https://cpra.example.test", t.TempDir(), false); err == nil {
		t.Fatal("directory used as token")
	}
	if _, err := FromTokenFile("https://cpra.example.test", "", false); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestCancellationAndHTTPSDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := FromTokenFile("http://cpra.example.test", path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cpra.New(cfg); err == nil {
		t.Fatal("authenticated HTTP enabled by default")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cfg.TokenSource(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
