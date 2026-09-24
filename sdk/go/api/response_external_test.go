package api_test

import (
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// The same public type name does not make a consumer's fields schema-defined.
type Measurement struct {
	ConsumerField string `json:"consumerField"`
	Optional      string `json:"optional,omitempty"`
}

func TestConsumerModelNameDoesNotBypassRequiredFields(t *testing.T) {
	var value Measurement
	if err := api.DecodeResponse([]byte(`{}`), &value); err == nil {
		t.Fatal("foreign model-name collision bypassed required field")
	}
	if err := api.DecodeResponse([]byte(`{"consumerField":"present"}`), &value); err != nil {
		t.Fatal(err)
	}
}
