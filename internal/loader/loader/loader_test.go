package loader

import (
	"fmt"
	"testing"
)

func BenchmarkPrimeNumbers(b *testing.B) {
	for i := 0; i < b.N; i++ {
		l := NewYamlLoader("test.yaml")
		l.Load()
		m := l.GetManifest()
		if testing.Verbose() {
			fmt.Printf("loading %d monitors from %s\n", len(m.Monitors), "test.yaml")
		}
	}
}
