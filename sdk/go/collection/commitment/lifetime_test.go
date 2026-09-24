package commitment

import (
	"bytes"
	"testing"
)

// These white-box checks examine only directly owned buffers and references.
// They cannot establish erasure of compiler, runtime, or crypto-library copies.
func TestCloseAndFailureDropOwnedPrivateState(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyBytes)
	token, _ := SourceToken(1)
	for _, mode := range []string{"close", "finish", "quota", "early", "order"} {
		t.Run(mode, func(t *testing.T) {
			s, err := NewSourceAccumulator(key, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if mode == "early" {
				_, _ = s.Finish()
			} else if mode == "order" {
				_ = s.Begin("source.00000000000000000002")
			} else {
				if err = s.Begin(token); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "close":
					_ = s.Close()
				case "finish":
					_ = s.End()
					_, err = s.Finish()
					if err != nil {
						t.Fatal(err)
					}
				case "quota":
					_, _ = s.Write([]byte("too much"))
				}
			}
			if s.key != [KeyBytes]byte{} || s.mac != nil || s.source != nil || s.token != "" {
				t.Fatal("owned source state retained after completion/discard/failure")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Finish(); err == nil {
				t.Fatal("discarded source was reusable")
			}
		})
	}
	for _, mode := range []string{"close", "finish", "early", "invalid"} {
		a, err := NewAccumulator(key, 0, [MACBytes]byte{})
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "close":
			_ = a.Close()
		case "finish":
			_, err = a.Finish()
			if err != nil {
				t.Fatal(err)
			}
		case "early":
			a.count = 1
			_, _ = a.Finish()
		case "invalid":
			_ = a.Add(Position{}, [MACBytes]byte{})
		}
		if a.mac != nil {
			t.Fatal("MAC reference retained after completion/discard/failure")
		}
		if err = a.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = a.Finish(); err == nil {
			t.Fatal("discarded item accumulator was reusable")
		}
	}
	var s *SourceAccumulator
	var a *Accumulator
	if s.Close() != nil || a.Close() != nil {
		t.Fatal("nil Close must be safe for deferred cleanup")
	}
}
