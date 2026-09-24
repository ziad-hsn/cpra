package persistence

import (
	"errors"

	"github.com/hashicorp/raft"
)

// IsLeadershipUnavailable classifies temporary Raft ownership changes. A write
// with this error may have committed; callers must reconcile before retrying it.
func IsLeadershipUnavailable(err error) bool {
	return errors.Is(err, raft.ErrNotLeader) || errors.Is(err, raft.ErrLeadershipLost) ||
		errors.Is(err, raft.ErrLeadershipTransferInProgress)
}
