package schema

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExplicitIdentitySurvivesRenameAndDecoding(t *testing.T) {
	for _, input := range []string{`{"id":"my-monitor","name":"first"}`, "id: my-monitor\nname: first"} {
		var m Monitor
		var err error
		if input[0] == '{' {
			err = json.Unmarshal([]byte(input), &m)
		} else {
			err = yaml.Unmarshal([]byte(input), &m)
		}
		if err != nil {
			t.Fatal(err)
		}
		id, err := m.EffectiveID()
		if err != nil || id != "my-monitor" {
			t.Fatal(id, err)
		}
		before, _ := ConfigurationRevision(m, nil, nil)
		m.Name = "renamed"
		after, _ := ConfigurationRevision(m, nil, nil)
		if before != after {
			t.Fatal("rename changed target revision")
		}
		m.ID = ""
		fallback, _ := m.EffectiveID()
		if fallback == id || fallback == "" {
			t.Fatal("missing deterministic fallback")
		}
	}
}
