//go:build windows

package shutdown

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

func listen(cancel func()) <-chan string {
	out := make(chan string, 1)
	var once sync.Once

	// Fallback to os.Interrupt to catch CTRL+C.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	// Console control handler for close/logoff/shutdown using WinAPI directly.
	callback := syscall.NewCallback(func(ctrlType uintptr) uintptr {
		switch ctrlType {
		case windows.CTRL_C_EVENT, windows.CTRL_BREAK_EVENT, windows.CTRL_CLOSE_EVENT, windows.CTRL_SHUTDOWN_EVENT, windows.CTRL_LOGOFF_EVENT:
			once.Do(func() {
				out <- consoleReason(uint32(ctrlType))
				cancel()
			})
			return 1 // handled
		default:
			return 0
		}
	})
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setConsoleCtrlHandler := kernel32.NewProc("SetConsoleCtrlHandler")
	_, _, _ = setConsoleCtrlHandler.Call(callback, uintptr(1))

	go func() {
		<-sigChan
		once.Do(func() {
			out <- "interrupt"
			cancel()
		})
	}()

	return out
}

func consoleReason(ctrlType uint32) string {
	switch ctrlType {
	case windows.CTRL_C_EVENT:
		return "CTRL_C_EVENT"
	case windows.CTRL_BREAK_EVENT:
		return "CTRL_BREAK_EVENT"
	case windows.CTRL_CLOSE_EVENT:
		return "CTRL_CLOSE_EVENT"
	case windows.CTRL_SHUTDOWN_EVENT:
		return "CTRL_SHUTDOWN_EVENT"
	case windows.CTRL_LOGOFF_EVENT:
		return "CTRL_LOGOFF_EVENT"
	default:
		return "console_event"
	}
}
