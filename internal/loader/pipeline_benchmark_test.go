package loader

import (
	"testing"

	"cpra/internal/loader/schema"
)

var sampleMonitorListItem = []byte(`  - name: "test-service"
    enabled: true
    pulse_check:
      type: http
      interval: 30s
      timeout: 5s
      config:
        method: GET
        url: http://example.com/health
    intervention:
      action: docker
      config:
        container: "svc"
        action: restart
    codes:
      red:
        dispatch: true
        notify: log
        config:
          file: "/tmp/alerts.log"
`)

func BenchmarkNormalizeMonitorYAML(b *testing.B) {
	dst := make([]byte, 0, len(sampleMonitorListItem))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		dst = normalizeMonitorYAML(dst[:0], sampleMonitorListItem)
		if len(dst) == 0 {
			b.Fatal("unexpected empty normalized yaml")
		}
	}
}

func BenchmarkParseMonitorFromBytes(b *testing.B) {
	p := &Pipeline{}
	var m schema.Monitor
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := p.parseMonitorFromBytes(sampleMonitorListItem, &m); err != nil {
			b.Fatal(err)
		}
	}
}

