package shutdown

// Listen subscribes to OS-specific shutdown events and invokes cancel once.
// It returns a channel that receives a human-readable reason when cancellation is triggered.
func Listen(cancel func()) <-chan string {
	return listen(cancel)
}
