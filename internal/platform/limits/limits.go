package limits

// Limits describes detected CPU and memory limits (if any).
type Limits struct {
	Cores       *int
	MemoryBytes *int64
	Source      string
}

// Detect returns platform-specific limits without failing if undefined.
func Detect() Limits {
	return detect()
}
