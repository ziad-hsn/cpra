package main
import (
    "fmt"
    "os"
    "time"
)
func main() {
    // Test timezone name display
    os.Setenv("CPRA_TIMEZONE", "America/New_York")
    timezone, _ := time.LoadLocation("America/New_York")
    now := time.Now().In(timezone)
    timestamp := now.Format("2006-01-02T15:04:05.000Z07:00")
    timezoneName := timezone.String()
    
    // Test tracing format
    os.Setenv("CPRA_TRACING", "true")
    traceInfo := ""
    if os.Getenv("CPRA_TRACING") == "true" {
        traceInfo = " [TRACE:abc12345]"
    }
    
    fmt.Printf("NEW FORMAT: %s %s [INFO] [SYSTEM]%s Starting application\n", timestamp, timezoneName, traceInfo)
    fmt.Printf("CODE LOG:   %s %s [test-monitor]%s Test alert message\n", timestamp, timezoneName, traceInfo)
}
