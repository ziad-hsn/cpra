package collection

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

type failedSourceReader struct{ err error }

func (r failedSourceReader) Read([]byte) (int, error) { return 0, r.err }

func TestFreezePreservesOnlyKnownLocalReadCauses(t *testing.T) {
	cause := errors.New("private injected read failure")
	for _, profile := range []string{"", FileNormalizationProfile} {
		for _, prefix := range []string{"", "# prefix\n", "["} {
			for _, local := range []bool{false, true} {
				var reader io.Reader = failedSourceReader{&os.PathError{Op: "read", Path: "private-input", Err: cause}}
				reader = io.MultiReader(strings.NewReader(prefix), reader)
				if local {
					reader = localSourceReader{io.NopCloser(reader)}
				}
				sources := []Source{Reader("input", reader)}
				var frozen *Frozen
				var err error
				if profile == "" {
					frozen, err = Freeze(t.Context(), sources, Options{})
				} else {
					frozen, err = FreezeProfile(t.Context(), sources, profile, Options{})
				}
				if frozen != nil {
					frozen.Close()
				}
				var access *SourceAccessError
				if err == nil || errors.As(err, &access) != local || errors.Is(err, cause) != local {
					t.Fatalf("profile %q local %v lost or fabricated source cause: %v", profile, local, err)
				}
				if strings.Contains(err.Error(), "private") {
					t.Fatal("source error exposed private cause", err)
				}
			}
		}
	}
}
