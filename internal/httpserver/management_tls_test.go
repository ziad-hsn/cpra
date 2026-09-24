package httpserver

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/fleetview"
)

func TestManagementServerTLSListenerAndConfiguration(t *testing.T) {
	previousLogger := controller.SystemLogger
	controller.SystemLogger = controller.NewLogger("web-test", false)
	t.Cleanup(func() { controller.SystemLogger = previousLogger })
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := fixture.TLS.Certificates[0]
	fixture.Close()
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "server.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(ServerConfig{Addr: "127.0.0.1:0", TLSCertFile: certFile, TLSKeyFile: keyFile, AuthToken: "tls-listener-fixture"}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+s.srv.Addr+"/api/v1/healthz", nil)
	req.Header.Set("Authorization", "Bearer tls-listener-fixture")
	response, err := fixture.Client().Do(req)
	if err != nil {
		t.Fatal("native listener failed TLS handshake", err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	for _, cfg := range []ServerConfig{{Addr: "127.0.0.1:0", TLSCertFile: certFile}, {Addr: "127.0.0.1:0", TLSCertFile: certFile, TLSKeyFile: filepath.Join(dir, "not-a-secret-key")}} {
		invalid := New(cfg, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
		if err := invalid.Start(); err == nil {
			invalid.Stop()
			t.Fatal("invalid TLS configuration bound listener")
		}
		if invalid.srv != nil {
			t.Fatal("configuration rejected after service startup")
		}
	}
}
