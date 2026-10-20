package persistence

import (
	"context"
	"errors"
	"time"
)

var errInstallationIdentity = errors.New("installation namespace conflicts with durable state")

func validateInstallationCommand(c Command) error {
	extra := c
	extra.Kind, extra.At, extra.InstallationID = "", time.Time{}, ""
	if extra != (Command{}) || !catalogIdentifier(c.InstallationID, 256) {
		return errInstallationIdentity
	}
	return nil
}

// The random node identity is proposed outside Apply. A complete restore keeps
// the committed installation identity even when it receives a new Raft node ID.
func (s *Store) ensureInstallation(ctx context.Context) error {
	for !s.installationMu.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer s.installationMu.Unlock()
	if s.installationReady {
		return nil
	}
	results, err := s.Submit(ctx, []Command{{Kind: "installation", InstallationID: s.installationID, At: time.Now().UTC()}})
	if err != nil {
		return err
	}
	if len(results) != 1 || !results[0].Allowed {
		return errInstallationIdentity
	}
	s.installationReady = true
	return nil
}

func (f *machine) applyInstallation(c Command) Result {
	if f.image.InstallationID != "" && f.image.InstallationID != c.InstallationID {
		return Result{Err: errInstallationIdentity}
	}
	if f.image.InstallationID == "" {
		f.image.InstallationID = c.InstallationID
		for id, m := range f.image.Monitors {
			// Existing action payloads, correlations, exposure and reviews are
			// unchanged. Only subsequently admitted identities use the namespace.
			m.InstallationID = c.InstallationID
			f.image.Monitors[id] = m
		}
	}
	f.image.Version = joinStorageFormat(f.image.Version, ActionFormatVersion)
	return Result{Allowed: true}
}

func (m Monitor) namespacedIdentity(value string) string {
	if m.InstallationID == "" {
		return value // Deterministic replay before namespace admission.
	}
	return identity("installation/" + m.InstallationID + "/" + value)
}
