package main

import (
	"testing"

	"cpra/internal/verification"
)

func TestExitPolicyDoesNotPromoteConfiguredSuiteToCertification(t *testing.T) {
	local := verification.Report{AllConfiguredPassed: true}
	if successful(local, false) || !successful(local, true) {
		t.Fatal("configured-only success must be explicitly selected")
	}
	if successful(verification.Report{}, true) {
		t.Fatal("an empty or unconfigured suite must fail")
	}
	if !successful(verification.Report{Complete: true, AllConfiguredPassed: true}, false) {
		t.Fatal("full live verification must preserve the existing successful exit")
	}
}
