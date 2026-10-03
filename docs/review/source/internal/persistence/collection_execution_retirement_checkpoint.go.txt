package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/bits"
	"time"
)

const collectionExecutionRetirementCheckpointVersion = 1
const collectionExecutionRetirementCheckpointMaxBytes = 8 << 10

// CollectionExecutionRetirementCheckpoint commits to a contiguous retired
// decision prefix. Progress contains cumulative retired counts and bytes, not
// the live ledger's remaining quota. An accepted outcome advances this prefix
// only together with its matching terminal, including the 4096-byte charge.
// Prepared records are outside this checkpoint and must be accounted separately.
//
// TerminalFrontier stores one complete subtree hash per set bit of Processed,
// ordered from the lowest level upward. Nonaccepted decisions append empty
// leaves. At most 14 hashes represent the prefix; no retired record is retained.
// This codec grants no deletion authority. The committing transition must bind
// it to the immutable final result, and recovery must verify the remaining
// suffix reconstructs that result before trusting either checkpoint or rows.
type CollectionExecutionRetirementCheckpoint struct {
	Version          int                         `json:"version"`
	Progress         CollectionExecutionProgress `json:"progress"`
	TerminalFrontier []string                    `json:"terminal_frontier"`
}

// NewCollectionExecutionRetirementCheckpoint starts an empty retired prefix
// using the original execution binding, item count and activation time.
func NewCollectionExecutionRetirementCheckpoint(binding CollectionExecutionBinding, itemCount uint64, at time.Time) (CollectionExecutionRetirementCheckpoint, error) {
	p, err := NewCollectionExecutionProgress(binding, itemCount, at)
	if err != nil {
		return CollectionExecutionRetirementCheckpoint{}, err
	}
	c := CollectionExecutionRetirementCheckpoint{Version: collectionExecutionRetirementCheckpointVersion, Progress: p, TerminalFrontier: []string{}}
	return c, c.validate()
}

func (c CollectionExecutionRetirementCheckpoint) Clone() CollectionExecutionRetirementCheckpoint {
	c.Progress = c.Progress.Clone()
	if c.TerminalFrontier != nil {
		c.TerminalFrontier = append([]string{}, c.TerminalFrontier...)
	}
	return c
}

func (c CollectionExecutionRetirementCheckpoint) validate() error {
	p := c.Progress
	if c.Version != collectionExecutionRetirementCheckpointVersion || p.validate() != nil || p.Prepared != nil ||
		p.Accepted != p.ChildTerminals || c.TerminalFrontier == nil || len(c.TerminalFrontier) != bits.OnesCount64(p.Processed) ||
		len(c.TerminalFrontier) > collectionExecutionTerminalDepth {
		return ErrCollectionInvalid
	}
	frontier, err := c.frontierHashes()
	if err != nil {
		return err
	}
	empty := collectionExecutionTerminalEmptyHashes()
	if p.Accepted == 0 {
		for level := 0; level < collectionExecutionTerminalDepth; level++ {
			if p.Processed&(1<<uint(level)) != 0 && frontier[level] != empty[level] {
				return ErrCollectionInvalid
			}
		}
	}
	tree := c.prefixTree(frontier, &empty)
	if tree.Root() != p.TerminalRoot || p.Processed == 0 && !p.LastAt.Equal(p.StartedAt) {
		return ErrCollectionInvalid
	}
	return nil
}

func (c CollectionExecutionRetirementCheckpoint) frontierHashes() ([collectionExecutionTerminalDepth + 1][sha256.Size]byte, error) {
	var hashes [collectionExecutionTerminalDepth + 1][sha256.Size]byte
	if c.Progress.Processed > CollectionValidationMaxItems || len(c.TerminalFrontier) != bits.OnesCount64(c.Progress.Processed) {
		return hashes, ErrCollectionInvalid
	}
	index := 0
	for level := 0; level < collectionExecutionTerminalDepth; level++ {
		if c.Progress.Processed&(1<<uint(level)) == 0 {
			continue
		}
		if !bootstrapHash(c.TerminalFrontier[index]) {
			return hashes, ErrCollectionInvalid
		}
		raw, _ := hex.DecodeString(c.TerminalFrontier[index])
		copy(hashes[level][:], raw)
		index++
	}
	return hashes, nil
}

// append returns detached cumulative commitments for exactly the next decision.
// Missing accepted terminals and terminals on nonaccepted rows both fail before
// any byte/charge is counted. Callers pair these records in the atomic deletion.
func (c CollectionExecutionRetirementCheckpoint) append(outcome CollectionItemOutcome, terminal *CollectionChildObservation) (CollectionExecutionRetirementCheckpoint, error) {
	emptyResult := CollectionExecutionRetirementCheckpoint{}
	if c.validate() != nil || outcome.validate() != nil || outcome.Binding != c.Progress.Binding ||
		outcome.Ordinal != c.Progress.Processed+1 || outcome.Ordinal > c.Progress.ItemCount ||
		outcome.InputOrdinal > c.Progress.ItemCount || outcome.At.Before(c.Progress.StartedAt) ||
		(outcome.Decision == "accepted") != (terminal != nil) {
		return emptyResult, ErrCollectionInvalid
	}
	outcomeRaw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Outcome: &outcome})
	if err != nil {
		return emptyResult, err
	}
	empty := collectionExecutionTerminalEmptyHashes()
	leaf := empty[0]
	var terminalRaw []byte
	if terminal != nil {
		if !terminal.matches(outcome) {
			return emptyResult, ErrCollectionInvalid
		}
		terminalRaw, err = collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Terminal: terminal})
		if err != nil {
			return emptyResult, err
		}
		leaf = collectionExecutionTerminalLeaf(outcome.Ordinal, terminalRaw)
	}
	frontier, err := c.frontierHashes()
	if err != nil {
		return emptyResult, err
	}
	level := 0
	for c.Progress.Processed&(1<<uint(level)) != 0 {
		leaf = collectionExecutionTerminalNode(frontier[level], leaf)
		frontier[level] = [sha256.Size]byte{}
		level++
	}
	frontier[level] = leaf
	next := c.Clone()
	p := &next.Progress
	p.Processed++
	switch outcome.Decision {
	case "accepted":
		p.Accepted++
	case "unchanged":
		p.Unchanged++
	case "conflict":
		p.Conflicts++
	case "dependencyBlocked":
		p.DependencyBlocked++
	}
	p.OutcomeBytes += int64(len(outcomeRaw))
	p.TerminalBytes += int64(len(terminalRaw))
	p.EncodedBytes += int64(len(outcomeRaw) + len(terminalRaw))
	p.ChargedBytes += collectionExecutionCharge(collectionExecutionRecord{Outcome: &outcome}, outcomeRaw)
	p.OutcomeDigest = collectionExecutionOutcomeNextDigest(p.OutcomeDigest, outcomeRaw)
	if outcome.At.After(p.LastAt) {
		p.LastAt = outcome.At
	}
	if terminal != nil {
		p.ChildTerminals++
		switch {
		case terminal.InvalidatedByRestore != "":
			p.ChildInvalidated++
		case terminal.State == "completed":
			p.ChildApplied++
		case terminal.State == "failed":
			p.ChildFailed++
		default:
			p.ChildSuperseded++
		}
		if terminal.UpdatedAt.After(p.LastAt) {
			p.LastAt = terminal.UpdatedAt
		}
	}
	next.TerminalFrontier = make([]string, 0, bits.OnesCount64(p.Processed))
	for level := 0; level < collectionExecutionTerminalDepth; level++ {
		if p.Processed&(1<<uint(level)) != 0 {
			next.TerminalFrontier = append(next.TerminalFrontier, hex.EncodeToString(frontier[level][:]))
		}
	}
	p.TerminalRoot = next.prefixTree(frontier, &empty).Root()
	if err := next.validate(); err != nil {
		return emptyResult, err
	}
	return next, nil
}

// matchesFinal verifies a completely reconstructed decided prefix against the
// immutable execution progress. An abandoned prepared slot can advance LastAt
// and contribute bytes; its physical presence/removal still needs a separate
// ledger check. No prepared contents are copied into the retired checkpoint.
func (c CollectionExecutionRetirementCheckpoint) matchesFinal(final CollectionExecutionProgress) bool {
	if c.validate() != nil || final.validate() != nil || final.Accepted != final.ChildTerminals {
		return false
	}
	actual := c.Progress.Clone()
	if q := final.Prepared; q != nil {
		prepared := *q
		actual.Prepared = &prepared
		actual.EncodedBytes += q.EncodedBytes
		actual.ChargedBytes += q.EncodedBytes
		if q.At.After(actual.LastAt) {
			actual.LastAt = q.At
		}
	}
	return collectionExecutionProgressEqual(actual, final)
}

func collectionExecutionRetirementCheckpointEncoding(c CollectionExecutionRetirementCheckpoint) ([]byte, error) {
	if c.validate() != nil {
		return nil, ErrCollectionInvalid
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > collectionExecutionRetirementCheckpointMaxBytes {
		return nil, ErrCollectionInvalid
	}
	return raw, nil
}

func decodeCollectionExecutionRetirementCheckpoint(raw []byte) (CollectionExecutionRetirementCheckpoint, error) {
	empty := CollectionExecutionRetirementCheckpoint{}
	if len(raw) == 0 || len(raw) > collectionExecutionRetirementCheckpointMaxBytes {
		return empty, ErrCollectionInvalid
	}
	var c CollectionExecutionRetirementCheckpoint
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return empty, ErrCollectionInvalid
	}
	canonical, err := collectionExecutionRetirementCheckpointEncoding(c)
	if err != nil || !bytes.Equal(canonical, raw) {
		return empty, ErrCollectionInvalid
	}
	return c, nil
}
