package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateDoesNotCreateState(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "monitors.yaml")
	if err := os.WriteFile(manifest, []byte("monitors: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "not-created")
	err := runCPRa(context.Background(), runOptions{manifest: manifest, dataDir: state, allowEmpty: true, validate: true, shutdownTimeout: time.Second}, func() { t.Fatal("validation reported running") })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("validation touched storage: %v", err)
	}
}

func TestInvalidManifestDoesNotCreateState(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "monitors.yaml")
	if err := os.WriteFile(manifest, []byte("monitors: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "not-created")
	if err := runCPRa(context.Background(), runOptions{manifest: manifest, dataDir: state, shutdownTimeout: time.Second}, func() {}); err == nil {
		t.Fatal("invalid empty manifest accepted")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("invalid manifest touched storage: %v", err)
	}
}
