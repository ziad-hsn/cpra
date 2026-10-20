package persistence

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/hashicorp/raft"
)

// snapshotStore retains Raft's flush, rename, checksum and retention contract.
// Its root is private before Raft creates any files; each new sink is protected
// before an FSM can write state into it.
type snapshotStore struct {
	*raft.FileSnapshotStore
	directory string
	readOnly  bool
}

func newSnapshotStore(base string, retain int, administrative bool) (*snapshotStore, error) {
	dir := filepath.Join(base, "snapshots")
	_, statErr := os.Lstat(dir)
	if administrative && statErr == nil {
		// Stopped administration must not replace the runtime service's ACL or
		// ownership with the administrator's identity. It cannot create sinks.
		if err := checkSnapshotDirectory(dir); err != nil {
			return nil, err
		}
	} else {
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		if err := createSnapshotDirectory(dir); err != nil {
			return nil, err
		}
		if err := protectSnapshotPath(dir, true); err != nil {
			return nil, err
		}
	}
	store, err := raft.NewFileSnapshotStore(base, retain, io.Discard)
	if err != nil {
		return nil, err
	}
	return &snapshotStore{FileSnapshotStore: store, directory: dir, readOnly: administrative}, nil
}

func (s *snapshotStore) Create(version raft.SnapshotVersion, index, term uint64, configuration raft.Configuration, configurationIndex uint64, transport raft.Transport) (raft.SnapshotSink, error) {
	if s.readOnly {
		return nil, errors.New("stopped administrative storage cannot create snapshots")
	}
	sink, err := s.FileSnapshotStore.Create(version, index, term, configuration, configurationIndex, transport)
	if err != nil {
		return nil, err
	}
	// These names are part of the pinned Raft FileSnapshotStore implementation.
	// The integration test checks both the temporary and completed layouts.
	dir := filepath.Join(s.directory, sink.ID()+".tmp")
	for _, entry := range []struct {
		path string
		dir  bool
	}{{dir, true}, {filepath.Join(dir, "meta.json"), false}, {filepath.Join(dir, "state.bin"), false}} {
		if err := protectSnapshotPath(entry.path, entry.dir); err != nil {
			return nil, errors.Join(err, sink.Cancel())
		}
	}
	return sink, nil
}
