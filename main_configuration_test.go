package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainRuntimeListenAddressAndExplicitOverride(t *testing.T) {
	for _, override := range []bool{false, true} {
		f := newMainManagementFixture(t)
		reservation, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := reservation.Addr().String()
		f.settings.Web.Address = address
		writeMainRuntime(t, f.options.runtimeFile, f.settings)
		explicit := ""
		if override {
			// Keep the configured socket occupied: only explicit precedence can
			// start successfully. Probes use the application's reported listener.
			defer reservation.Close()
			explicit = "127.0.0.1:0"
		} else if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
		client, stop := startMainManagementAt(t, f, explicit)
		if override == (f.options.webAddr == address) {
			t.Fatal("listener precedence changed", f.options.webAddr)
		}
		if _, err := client.Access(t.Context()); err != nil {
			t.Fatal("selected listener not usable", err)
		}
		stop()
	}
}

func TestMainRejectsProtectedLogRootBeforeStorage(t *testing.T) {
	for _, validate := range []bool{false, true} {
		f := newMainManagementFixture(t)
		f.settings.Notifications.LogDirectory = filepath.Join(f.settings.Storage.Directory, "notifications")
		writeMainRuntime(t, f.options.runtimeFile, f.settings)
		f.options.validate = validate
		err := runCPRa(t.Context(), f.options, func() { t.Error("unsafe configuration reached readiness") })
		if err == nil || !strings.Contains(err.Error(), "notifications.log_directory") || strings.Contains(err.Error(), f.settings.Storage.Directory) {
			t.Fatal("unsafe or unhelpful log-root error", err)
		}
		if _, err := os.Lstat(f.settings.Storage.Directory); !os.IsNotExist(err) {
			t.Fatal("rejected configuration opened storage", err)
		}
	}
}
