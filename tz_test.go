package main
import (
    "fmt"
    "os"
    "time"
)

func getLoggingTimezone() *time.Location {
    if tz := os.Getenv("CPRA_TIMEZONE"); tz != "" {
        if loc, err := time.LoadLocation(tz); err == nil {
            return loc
        }
    }
    return time.Local
}

func main() {
    os.Setenv("CPRA_TIMEZONE", "America/New_York")
    
    timezone := getLoggingTimezone()
    now := time.Now().In(timezone)
    timestamp := now.Format("2006-01-02T15:04:05.000Z07:00")
    
    fmt.Printf("Enhanced timestamp format: %s\n", timestamp)
    fmt.Printf("Timezone: %s\n", timezone)
    fmt.Printf("Original format would be: %s\n", time.Now().UTC().Format(time.RFC3339))
}
