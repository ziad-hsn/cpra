package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ziad-hsn/cpra/internal/drivertest"
)

func main() {
	config := flag.String("config", "examples/verification/live.yaml", "User-configured monitor and observer scenarios")
	live := flag.Bool("live", false, "Invoke configured drivers against designated local, mock, sandbox, or live targets")
	out := flag.String("out", "verification-report.json", "Redacted evidence report")
	requireConfiguredPass := flag.Bool("require-configured-pass", false, "Exit successfully when every enabled case passes (at least one required); does not certify all live providers")
	flag.Parse()
	cfg, err := drivertest.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	output, err := reserveOutput(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer output.Close()
	fingerprint, err := executableFingerprint()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot identify verification executable")
		os.Exit(1)
	}
	if err := os.Setenv("CPRA_VERIFY_EVIDENCE_DIR", *out+".evidence"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	report, err := drivertest.Run(ctx, cfg, *live)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report.BinarySHA256 = fingerprint
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		_, err = output.Write(append(data, '\n'))
	}
	if err == nil {
		err = output.Sync()
	}
	if err == nil {
		err = output.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, r := range report.Records {
		fmt.Printf("%s/%s: %s (%s, %s)\n", r.Kind, r.Name, r.Status, r.EvidenceType, r.ObservationBoundary)
	}
	if !successful(report, *requireConfiguredPass) {
		os.Exit(2)
	}
}

// reserveOutput establishes a fresh campaign before any observer or driver runs.
// Failed initialization retains the reserved file; choose a new path to retry.
func reserveOutput(path string) (*os.File, error) {
	if path == "" {
		return nil, fmt.Errorf("verification output must name a new report file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("cannot prepare verification output directory: %w", err)
	}
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot reserve verification report; select a new output path: %w", err)
	}
	if err := os.Mkdir(path+".evidence", 0700); err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("cannot reserve verification evidence; select a new output path: %w", err)
	}
	return output, nil
}

func executableFingerprint() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func successful(report drivertest.Report, configuredOnly bool) bool {
	if configuredOnly {
		return report.AllConfiguredPassed
	}
	return report.Complete
}
