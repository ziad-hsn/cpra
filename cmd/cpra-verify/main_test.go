package main

import (
	"testing"

	"github.com/ziad-hsn/cpra/internal/drivertest"
)

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
