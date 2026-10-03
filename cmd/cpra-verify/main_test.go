package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ziad-hsn/cpra/internal/drivertest"
)

func TestReportReservationPreservesExistingCampaign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	output, err := reserveOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.WriteString("original campaign"); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveOutput(path); err == nil {
		t.Fatal("an existing report must prevent a new campaign")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original campaign" {
		t.Fatalf("original report changed: %q, %v", data, err)
	}
}

func TestReportReservationRejectsExistingEvidenceAndLinks(t *testing.T) {
	for _, kind := range []string{"evidence", "link", "empty"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "report.json")
			switch kind {
			case "evidence":
				if err := os.Mkdir(path+".evidence", 0700); err != nil {
					t.Fatal(err)
				}
			case "link":
				target := filepath.Join(dir, "existing.json")
				if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("symlink creation is unavailable")
				}
			case "empty":
				path = ""
			}
			if _, err := reserveOutput(path); err == nil {
				t.Fatal("reused evidence, linked or empty output must be rejected")
			}
		})
	}
}

func TestExecutableFingerprintIdentifiesActualProcess(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(data)
	actual, err := executableFingerprint()
	if err != nil || actual != hex.EncodeToString(expected[:]) {
		t.Fatalf("fingerprint differs from actual executable: %q, %v", actual, err)
	}
}

func TestExitPolicyDoesNotPromoteConfiguredSuiteToCertification(t *testing.T) {
	local := drivertest.Report{AllConfiguredPassed: true}
	if successful(local, false) || !successful(local, true) {
		t.Fatal("configured-only success must be explicitly selected")
	}
	if successful(drivertest.Report{}, true) {
		t.Fatal("an empty or unconfigured suite must fail")
	}
	if !successful(drivertest.Report{Complete: true, AllConfiguredPassed: true}, false) {
		t.Fatal("full live verification must preserve the existing successful exit")
	}
}
