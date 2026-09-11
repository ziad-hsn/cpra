package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"cpra/internal/verification"
)

func main() {
	config := flag.String("config", "examples/verification/live.yaml", "User-configured monitor and observer scenarios")
	live := flag.Bool("live", false, "Invoke configured drivers against designated local, mock, sandbox, or live targets")
	out := flag.String("out", "verification-report.json", "Redacted evidence report")
	requireConfiguredPass := flag.Bool("require-configured-pass", false, "Exit successfully when every enabled case passes (at least one required); does not certify all 33 live providers")
	flag.Parse()
	cfg, err := verification.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("CPRA_VERIFY_EVIDENCE_DIR", *out+".evidence"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	report, err := verification.Run(ctx, cfg, *live)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		err = os.WriteFile(*out, append(data, '\n'), 0600)
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

func successful(report verification.Report, configuredOnly bool) bool {
	if configuredOnly {
		return report.AllConfiguredPassed
	}
	return report.Complete
}
