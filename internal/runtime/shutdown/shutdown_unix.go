//go:build !windows

package shutdown

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func listen(cancel func()) <-chan string {
	out := make(chan string, 1)
	var once sync.Once

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		s := <-sigChan
		once.Do(func() {
			out <- s.String()
			cancel()
		})
	}()

	return out
}
