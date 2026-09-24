package api_test

import (
	"encoding/json"
	"fmt"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Driver builds configuration data; it does not make an HTTP health check.
func ExampleDriver() {
	driver, err := api.Driver("check", "http", api.PulseHTTPConfig{
		URL: api.Pointer("https://payments.example.test/health"),
	})
	if err != nil {
		panic(err)
	}
	monitor := api.Monitor{
		APIVersion: api.APIVersion,
		Kind:       "Monitor",
		Metadata:   api.Metadata{ID: "payments-http"},
		Spec: api.MonitorSpec{Check: api.CheckSpec{
			Driver: driver, Interval: "60s", Timeout: "5s",
		}},
	}
	if err := api.ValidateResourceValue(monitor); err != nil {
		panic(err)
	}
	fmt.Println(monitor.Metadata.ID, monitor.Spec.Check.Interval)
	// Output: payments-http 60s
}

// Pointer preserves false instead of treating it as an omitted setting.
func ExamplePointer() {
	settings := struct {
		Enabled *bool `json:"enabled,omitempty"`
	}{}
	omitted, err := json.Marshal(settings)
	if err != nil {
		panic(err)
	}
	settings.Enabled = api.Pointer(false)
	explicit, err := json.Marshal(settings)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(omitted))
	fmt.Println(string(explicit))
	// Output:
	// {}
	// {"enabled":false}
}
