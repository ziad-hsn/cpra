//go:build !externaljobs

package cprafixture

import (
	"net/http"
	"testing"
)

func TestDefaultFixtureExcludesCustomJobRoutes(t *testing.T) {
	s := New()
	defer s.Close()
	r, _ := http.NewRequest(http.MethodGet, s.URL+"/api/v2/job-types", nil)
	r.Header.Set("Authorization", "Bearer "+Token)
	resp, err := s.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("custom routes present: %d", resp.StatusCode)
	}
}
