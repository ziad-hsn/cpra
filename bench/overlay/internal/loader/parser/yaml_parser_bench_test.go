package parser

import (
    "bytes"
    "fmt"
    "io"
    "testing"
)

func genYAMLMonitors(n int) []byte {
    var buf bytes.Buffer
    buf.WriteString("monitors:\n")
    for i := 0; i < n; i++ {
        fmt.Fprintf(&buf, "  - name: svc-%d\n", i)
        buf.WriteString("    enabled: true\n")
        buf.WriteString("    pulse_check:\n")
        buf.WriteString("      type: http\n")
        buf.WriteString("      interval: 1s\n")
        buf.WriteString("      timeout: 500ms\n")
        buf.WriteString("      healthy_threshold: 2\n")
        buf.WriteString("      unhealthy_threshold: 3\n")
        buf.WriteString("      config:\n")
        buf.WriteString("        url: http://example.com/health\n")
        buf.WriteString("        method: GET\n")
        buf.WriteString("    codes:\n")
        buf.WriteString("      green:\n")
        buf.WriteString("        dispatch: true\n")
        buf.WriteString("        notify: log\n")
        buf.WriteString("        config:\n")
        buf.WriteString("          file: /dev/null\n")
        buf.WriteString("      yellow:\n")
        buf.WriteString("        dispatch: true\n")
        buf.WriteString("        notify: log\n")
        buf.WriteString("        config:\n")
        buf.WriteString("          file: /dev/null\n")
    }
    return buf.Bytes()
}

func benchParseYAML(b *testing.B, n int) {
    data := genYAMLMonitors(n)
    rdr := bytes.NewReader(data)
    p := NewYamlParser()
    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Reset reader for each iteration
        rdr.Seek(0, io.SeekStart)
        if _, err := p.Parse(rdr); err != nil {
            b.Fatalf("parse error: %v", err)
        }
    }
}

func BenchmarkYAMLParser_1k(b *testing.B)   { benchParseYAML(b, 1000) }
func BenchmarkYAMLParser_5k(b *testing.B)   { benchParseYAML(b, 5000) }
func BenchmarkYAMLParser_10k(b *testing.B)  { benchParseYAML(b, 10000) }

