package interning

import (
	"fmt"
	"sync"
	"testing"
)

func TestIntern(t *testing.T) {
	// Test empty string
	if got := Intern(""); got != "" {
		t.Errorf("Intern(\"\") = %q, want \"\"", got)
	}

	// Test basic interning
	s1 := Intern("hello")
	s2 := Intern("hello")
	if s1 != s2 {
		t.Error("Intern should return same string for same input")
	}

	// Test different strings
	s3 := Intern("world")
	if s1 == s3 {
		t.Error("Intern should return different strings for different inputs")
	}
}

func TestInternConcurrent(t *testing.T) {
	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// All goroutines should get the same interned string
	results := make([]string, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				results[idx] = Intern("concurrent-test")
			}
		}(i)
	}

	wg.Wait()

	// Verify all got the same string
	first := results[0]
	for i, s := range results {
		if s != first {
			t.Errorf("goroutine %d got different string: %q vs %q", i, s, first)
		}
	}
}

// BenchmarkInternHit measures performance when string is already interned (common case).
func BenchmarkInternHit(b *testing.B) {
	// Pre-intern the string
	Intern("benchmark-hit")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = Intern("benchmark-hit")
		}
	})
}

// BenchmarkInternMiss measures performance when string is not interned.
func BenchmarkInternMiss(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Use unique strings to force misses
			_ = Intern(fmt.Sprintf("miss-%d", i))
			i++
		}
	})
}

// BenchmarkInternMixed simulates realistic workload with 90% hits.
func BenchmarkInternMixed(b *testing.B) {
	// Pre-intern common strings
	commonStrings := []string{"GET", "POST", "PUT", "DELETE", "pulse", "intervention", "code"}
	for _, s := range commonStrings {
		Intern(s)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				// 10% miss
				_ = Intern(fmt.Sprintf("rare-%d", i))
			} else {
				// 90% hit
				_ = Intern(commonStrings[i%len(commonStrings)])
			}
			i++
		}
	})
}

// BenchmarkInternHighContention tests performance under extreme contention.
func BenchmarkInternHighContention(b *testing.B) {
	// All goroutines compete for the same string
	Intern("contention")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = Intern("contention")
		}
	})
}

