package streaming

import (
    "os"
    "testing"
    "bytes"
    "fmt"
    "context"
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

func benchStreamingYAML(b *testing.B, n int) {
    data := genYAMLMonitors(n)
    f, err := os.CreateTemp("", "bench_yaml_*.yaml")
    if err != nil { b.Fatal(err) }
    defer os.Remove(f.Name())
    if _, err := f.Write(data); err != nil { b.Fatal(err) }
    f.Close()

    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        parser, err := NewStreamingYamlParser(f.Name(), ParseConfig{BatchSize: 10000, BufferSize: 4*1024*1024})
        if err != nil { b.Fatal(err) }
        batchChan, errChan := parser.ParseBatches(context.Background(), nil)
        // drain
        for range batchChan { /* no-op */ }
        select {
        case e := <-errChan:
            if e != nil { b.Fatalf("streaming parse error: %v", e) }
        default:
        }
    }
}

func BenchmarkStreamingYAML_1k(b *testing.B)   { benchStreamingYAML(b, 1000) }
func BenchmarkStreamingYAML_5k(b *testing.B)   { benchStreamingYAML(b, 5000) }
func BenchmarkStreamingYAML_10k(b *testing.B)  { benchStreamingYAML(b, 10000) }

