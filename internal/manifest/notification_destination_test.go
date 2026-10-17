package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func TestNotificationDestinationConflict(t *testing.T) {
	for _, decode := range []func([]byte, any) error{json.Unmarshal, yaml.Unmarshal} {
		for _, inline := range []string{`,"notify":"discord","config":{"privateTypo":"PRIVATE"}`, `,"config":{}`, `,"notify":"discord"`} {
			var codes Codes
			err := decode([]byte(`{"red":{"notify_group":"ops"`+inline+`}}`), &codes)
			if detail, ok := api.ValidationFieldError(err); !ok || detail.Field != "codes.red" || detail.Reason != "notificationDestinationConflict" || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("conflict not rejected safely: %v", err)
			}
		}
		var codes Codes
		if err := decode([]byte(`{"red":{"notify_group":"ops"}}`), &codes); err != nil {
			t.Fatal(err)
		}
		// The typed manifest's empty inline fields cannot create a destination.
		encoded, err := json.Marshal(codes)
		if err != nil {
			t.Fatal(err)
		}
		if err := decode(encoded, &codes); err != nil {
			t.Fatal("group round trip", err)
		}
	}
}
