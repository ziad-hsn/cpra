package streaming

import (
    "os"
    "testing"
    "bytes"
    "fmt"
    "context"
)

func genJSONMonitors(n int) []byte {
    var buf bytes.Buffer
    buf.WriteString("{\n  \"monitors\": [\n")
    for i := 0; i < n; i++ {
        if i > 0 { buf.WriteString(",\n") }
        fmt.Fprintf(&buf, "    {\n      \"name\": \"svc-%d\",\n      \"enabled\": true,\n      \"pulse_check\": {\n        \"type\": \"http\",\n        \"interval\": \"1s\",\n        \"timeout\": \"500ms\",\n        \"healthy_threshold\": 2,\n        \"unhealthy_threshold\": 3,\n        \"config\": {\n          \"url\": \"http://example.com/health\",\n          \"method\": \"GET\"\n        }\n      },\n      \"codes\": {\n        \"green\": {\n          \"dispatch\": true,\n          \"notify\": \"log\",\n          \"config\": { \"file\": \"/dev/null\" }\n        },\n        \"yellow\": {\n          \"dispatch\": true,\n          \"notify\": \"log\",\n          \"config\": { \"file\": \"/dev/null\" }\n        }\n      }\n    }", i)
    }
    buf.WriteString("\n  ]\n}\n")
    return buf.Bytes()
}

func benchStreamingJSON(b *testing.B, n int) {
    data := genJSONMonitors(n)
    f, err := os.CreateTemp("", "bench_json_*.json")
    if err != nil { b.Fatal(err) }
    defer os.Remove(f.Name())
    if _, err := f.Write(data); err != nil { b.Fatal(err) }
    f.Close()

    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        parser, err := NewStreamingJsonParser(f.Name(), ParseConfig{BatchSize: 10000, BufferSize: 4*1024*1024})
        if err != nil { b.Fatal(err) }
        batchChan, errChan := parser.ParseBatches(context.Background(), nil)
        for range batchChan { /* drain */ }
        select {
        case e := <-errChan:
            if e != nil { b.Fatalf("streaming parse error: %v", e) }
        default:
        }
    }
}

func BenchmarkStreamingJSON_1k(b *testing.B)   { benchStreamingJSON(b, 1000) }
func BenchmarkStreamingJSON_5k(b *testing.B)   { benchStreamingJSON(b, 5000) }
func BenchmarkStreamingJSON_10k(b *testing.B)  { benchStreamingJSON(b, 10000) }

