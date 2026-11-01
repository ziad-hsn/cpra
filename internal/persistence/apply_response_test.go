package persistence

import (
	"errors"
	"testing"
)

func TestMalformedStateResponseIsPermanent(t *testing.T) {
	for _, value := range []any{nil, "invalid", []any{}} {
		s := openCatalogMemory(t)
		_, err := s.appliedResponse(value)
		if !errors.Is(err, errStateResponse) || errors.Is(err, ErrCommitUnconfirmed) || s.Status().Ready {
			t.Fatal("malformed reply treated as retryable uncertainty", err)
		}
		if err := s.ControllerHealth(); err == nil {
			t.Fatal("malformed reply reopened admission")
		}
	}
}
