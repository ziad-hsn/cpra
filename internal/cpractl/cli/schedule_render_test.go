package cli

import (
	"bytes"
	"cpra/internal/web/snapshot"
	"strings"
	"testing"
	"time"
)

func TestFutureNextCheck(t *testing.T) {
	b := new(bytes.Buffer)
	m := &snapshot.MonitorSummary{NextCheck: time.Now().Add(2 * time.Hour)}
	err := writeMonitorDetail(&printer{out: b, format: formatTable}, m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "Next check:            <1s") || strings.Contains(b.String(), "<1s") {
		t.Errorf("next check two hours away rendered as <1s:\n%s", b.String())
	}
}
