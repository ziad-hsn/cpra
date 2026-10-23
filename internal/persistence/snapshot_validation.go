package persistence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/raft"
)

var errSnapshotEnvelope = errors.New("snapshot envelope is corrupt or incompatible")

// Raft's FileSnapshotStore.List skips malformed metadata. A completed directory
// must instead make the error visible before selection can discard newer state.
func (s *snapshotStore) List() ([]*raft.SnapshotMeta, error) {
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(64)
		if err != nil && err != io.EOF {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				continue
			}
			info, e := root.Lstat(entry.Name())
			if e != nil {
				return nil, e
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, errSnapshotEnvelope
			}
			name := filepath.Join(entry.Name(), "meta.json")
			before, e := root.Lstat(name)
			if e != nil {
				return nil, e
			}
			if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 64<<10 {
				return nil, errSnapshotEnvelope
			}
			file, e := root.Open(name)
			if e != nil {
				return nil, e
			}
			after, e := file.Stat()
			if e != nil || !os.SameFile(before, after) {
				file.Close()
				return nil, errSnapshotEnvelope
			}
			data, e := io.ReadAll(io.LimitReader(file, 64<<10+1))
			closeErr := file.Close()
			if e != nil || closeErr != nil {
				return nil, errors.Join(e, closeErr)
			}
			var meta struct {
				raft.SnapshotMeta
				CRC []byte
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&meta) != nil || meta.ID != entry.Name() || meta.Version < raft.SnapshotVersionMin || meta.Version > raft.SnapshotVersionMax || meta.Index == 0 || len(meta.CRC) != 8 || meta.Size < 1 {
				return nil, errSnapshotEnvelope
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				return nil, errSnapshotEnvelope
			}
			state, e := root.Lstat(filepath.Join(entry.Name(), "state.bin"))
			if e != nil {
				return nil, e
			}
			if !state.Mode().IsRegular() || state.Size() != meta.Size {
				return nil, errSnapshotEnvelope
			}
		}
		if err == io.EOF {
			break
		}
	}
	return s.FileSnapshotStore.List()
}

// Only the newest completed snapshot can restore the application. Keeping this
// restriction in the store passed to Raft also prevents a later restore error
// from silently selecting an older image with different committed effects.
type newestSnapshotStore struct{ raft.SnapshotStore }

func (s newestSnapshotStore) List() ([]*raft.SnapshotMeta, error) {
	list, err := s.SnapshotStore.List()
	if len(list) > 1 {
		list = list[:1]
	}
	return list, err
}

// FileSnapshotStore.Open has already streamed and checked the full-file CRC.
// Inspect the JSON envelope token by token rather than decoding a second fleet
// and materializing a second set of synchronous ledger transactions.
func validateSnapshotEnvelope(reader io.Reader) error {
	r := bufio.NewReader(reader)
	magic, version := "", 0
	prefix, _ := r.Peek(1)
	if len(prefix) == 1 && prefix[0] == 'C' {
		line, err := r.ReadSlice('\n')
		if err != nil || len(line) > 128 {
			return errSnapshotEnvelope
		}
		magic = string(line)
		const start = "CPRA-COLLECTION-SNAPSHOT-"
		if !strings.HasPrefix(magic, start) {
			return errSnapshotEnvelope
		}
		parsed, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(magic, start), "\n"))
		if err != nil || !collectionFormat(parsed) || magic != fmt.Sprintf("%s%d\n", start, parsed) {
			return errSnapshotEnvelope
		}
		version = parsed
	}
	bounded := &snapshotTokenReader{reader: r}
	decoder := json.NewDecoder(bounded)
	decoder.UseNumber()
	token := func() (json.Token, error) { bounded.remaining = maxCommitBytes; return decoder.Token() }
	first, err := token()
	if err != nil || first != json.Delim('{') {
		return errSnapshotEnvelope
	}
	depth := 1
	imageVersion := 0
	expectVersion := false
	seenVersion := false
	rootKey := true
	for depth > 0 {
		item, err := token()
		if err != nil {
			return errSnapshotEnvelope
		}
		if expectVersion {
			number, ok := item.(json.Number)
			if !ok || seenVersion {
				return errSnapshotEnvelope
			}
			v, err := strconv.Atoi(string(number))
			if err != nil {
				return errSnapshotEnvelope
			}
			imageVersion = v
			seenVersion = true
			expectVersion = false
		} else if depth == 1 && rootKey {
			if key, ok := item.(string); ok {
				expectVersion = key == "version"
				rootKey = false
				continue
			}
		}
		if d, ok := item.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			if depth > 128 {
				return errSnapshotEnvelope
			}
		}
		if depth == 1 {
			rootKey = true
		}
	}
	if !seenVersion || !supportedFormat(imageVersion) || version != 0 && imageVersion != version || version == 0 && collectionFormat(imageVersion) {
		return errSnapshotEnvelope
	}
	tail := bufio.NewReader(io.MultiReader(decoder.Buffered(), r))
	if magic != "" {
		delimiter, err := tail.ReadByte()
		if err != nil || delimiter != '\n' {
			return errSnapshotEnvelope
		}
		prefix, err := tail.Peek(len(collectionLedgerMagic))
		if err != nil || !bytes.Equal(prefix, collectionLedgerMagic) {
			return errSnapshotEnvelope
		}
	}
	if magic == "" {
		for {
			c, err := tail.ReadByte()
			if err == io.EOF {
				return nil
			}
			if err != nil || c != ' ' && c != '\n' && c != '\r' && c != '\t' {
				return errSnapshotEnvelope
			}
		}
	}
	return nil
}

type snapshotTokenReader struct {
	reader    io.Reader
	remaining int64
}

func (r *snapshotTokenReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errSnapshotEnvelope
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}
