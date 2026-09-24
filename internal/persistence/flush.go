package persistence

import (
	"context"
	"errors"
	"time"
)

// Flush joins all submissions enqueued before this call, including admitted
// commands whose callers stopped waiting for a reply. Stop new management
// admission first; this ordering barrier does not stop subsequent submitters or
// resolve uncertain external operations. The marker follows the same bounded
// submission queue, Raft log and deterministic application path as other writes.
func (s *Store) Flush(ctx context.Context) error {
	results, err := s.Submit(ctx, []Command{{Kind: "barrier", At: time.Now().UTC()}})
	if err != nil {
		return err
	}
	if len(results) != 1 || !results[0].Allowed {
		return errors.New("durable barrier was not confirmed")
	}
	return results[0].Err
}
