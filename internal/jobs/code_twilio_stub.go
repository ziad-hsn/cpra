//go:build !twilio

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func newCodeTwilioJob(cfg *manifest.CodeNotificationTwilio, monitor, color, message string, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("twilio notification requires building with -tags=twilio")
}
