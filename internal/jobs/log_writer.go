package jobs

import (
	"fmt"
	"os"
	"sync"
)

// LogEntry represents a single log message to be written to a file.
type LogEntry struct {
	Path string
	Data []byte
}

// LogManager handles asynchronous file logging.
type LogManager struct {
	logChan chan LogEntry
	wg      sync.WaitGroup
	once    sync.Once
}

var (
	globalLogManager *LogManager
	initOnce         sync.Once
)

// GetLogManager returns the singleton LogManager instance.
func GetLogManager() *LogManager {
	initOnce.Do(func() {
		fmt.Println("DEBUG: Initializing LogManager")
		globalLogManager = &LogManager{
			logChan: make(chan LogEntry, 4096), // Buffered channel for high throughput
		}
		globalLogManager.start()
	})
	return globalLogManager
}

// WriteLog queues a log entry for writing.
// It is non-blocking unless the buffer is full.
func (m *LogManager) WriteLog(path string, data []byte) {
	m.logChan <- LogEntry{Path: path, Data: data}
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
			f, ok := files[entry.Path]
			if !ok {
				fmt.Printf("DEBUG: Opening log file: %s\n", entry.Path)
				var err error
				// Open file with append mode, create if not exists
				f, err = os.OpenFile(entry.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err != nil {
					// In a real system, we might log this to stderr or a fallback
					fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v\n", entry.Path, err)
					continue
				}
				files[entry.Path] = f
			}

			if _, err := f.Write(entry.Data); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to write to log file %s: %v\n", entry.Path, err)
				// If write fails, maybe close and remove from map to retry open next time
				_ = f.Close()
				delete(files, entry.Path)
			}
			// We rely on OS buffering for performance, but could add explicit Flush logic here
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
