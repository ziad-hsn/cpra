package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// LogEntry represents a single log message to be written to a file.
type LogEntry struct {
	Path string
	Data []byte
}

// LogManager handles asynchronous file logging.
type LogManager struct {
	logChan     chan LogEntry
	wg          sync.WaitGroup
	once        sync.Once
	droppedLogs atomic.Int64 // Track dropped logs when buffer is full
}

var (
	globalLogManager *LogManager
	initOnce         sync.Once
)

// GetLogManager returns the singleton LogManager instance.
func GetLogManager() *LogManager {
	initOnce.Do(func() {
		globalLogManager = &LogManager{
			logChan: make(chan LogEntry, 4096), // Buffered channel for high throughput
		}
		globalLogManager.start()
	})
	return globalLogManager
}

// WriteLog queues a log entry for writing.
// It is non-blocking; if the buffer is full, the log is dropped and counted.
func (m *LogManager) WriteLog(path string, data []byte) {
	select {
	case m.logChan <- LogEntry{Path: path, Data: data}:
		// Successfully queued
	default:
		// Buffer full - drop log to prevent blocking job execution
		dropped := m.droppedLogs.Add(1)
		if dropped%1000 == 1 {
			fmt.Fprintf(os.Stderr, "WARNING: Log buffer full, %d logs dropped\n", dropped)
		}
	}
}

// DroppedLogs returns the count of logs dropped due to buffer overflow.
func (m *LogManager) DroppedLogs() int64 {
	return m.droppedLogs.Load()
}

// start launches the background writer goroutine.
func (m *LogManager) start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		files := make(map[string]*os.File)

		defer func() {
			// Close all open files on exit
			for _, f := range files {
				_ = f.Sync()
				_ = f.Close()
			}
		}()

		for entry := range m.logChan {
			// Skip empty paths - write to stdout as fallback
			if entry.Path == "" {
				fmt.Print(string(entry.Data))
				continue
			}

			f, ok := files[entry.Path]
			if !ok {
				// Ensure parent directory exists, create if needed
				dir := filepath.Dir(entry.Path)
				if err := os.MkdirAll(dir, 0755); err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: Failed to create log directory %s: %v\n", dir, err)
					continue
				}

				var err error
				// Open/create file with append mode (creates file if not exists)
				f, err = os.OpenFile(entry.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: Failed to open/create log file %s: %v\n", entry.Path, err)
					continue
				}
				files[entry.Path] = f
			}

			if _, err := f.Write(entry.Data); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: Failed to write to log file %s: %v\n", entry.Path, err)
				// Close and remove from map to retry open next time
				_ = f.Close()
				delete(files, entry.Path)
			}
		}
	}()
}

// Shutdown waits for all pending logs to be written and closes resources.
// Note: This should be called only once during application shutdown.
func (m *LogManager) Shutdown() {
	m.once.Do(func() {
		close(m.logChan)
		m.wg.Wait()
	})
}
