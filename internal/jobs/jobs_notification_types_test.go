package jobs

import (
	"testing"

	"cpra/internal/loader/schema"

	"github.com/mlange-42/ark/ecs"
)

func TestCreateCodeJobNewTypes(t *testing.T) {
	w := ecs.NewWorld()
	ent := w.NewEntity()

	cases := []struct {
		name   string
		config schema.CodeConfig
	}{
		{"telegram", schema.CodeConfig{Notify: "telegram", Config: &schema.CodeNotificationTelegram{BotToken: "t", ChatID: "c"}}},
		{"discord", schema.CodeConfig{Notify: "discord", Config: &schema.CodeNotificationDiscord{WebhookURL: "https://x"}}},
		{"opsgenie", schema.CodeConfig{Notify: "opsgenie", Config: &schema.CodeNotificationOpsgenie{APIKey: "k"}}},
	}

	for _, tc := range cases {
		job, err := CreateCodeJob("mon", tc.config, ent, "red")
		if err != nil {
			t.Fatalf("%s: CreateCodeJob: %v", tc.name, err)
		}
		if job == nil || job.IsNil() {
			t.Fatalf("%s: nil job", tc.name)
		}
	}
}
