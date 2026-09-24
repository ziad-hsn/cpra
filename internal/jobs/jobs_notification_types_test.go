package jobs

import (
	"testing"

	"github.com/ziad-hsn/cpra/internal/manifest"

	"github.com/mlange-42/ark/ecs"
)

func TestCreateCodeJobNewTypes(t *testing.T) {
	w := ecs.NewWorld()
	ent := w.NewEntity()

	cases := []struct {
		name   string
		config manifest.CodeConfig
	}{
		{"telegram", manifest.CodeConfig{Notify: "telegram", Config: &manifest.CodeNotificationTelegram{BotToken: "t", ChatID: "c"}}},
		{"discord", manifest.CodeConfig{Notify: "discord", Config: &manifest.CodeNotificationDiscord{WebhookURL: "https://x"}}},
		{"opsgenie", manifest.CodeConfig{Notify: "opsgenie", Config: &manifest.CodeNotificationOpsgenie{APIKey: "k"}}},
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
