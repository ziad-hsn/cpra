package persistence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

const collectionExecutionTerminalDepth = 14
const collectionExecutionTerminalLeaves = 1 << collectionExecutionTerminalDepth

// Proof siblings run from the leaf toward the root. Length is checked at the
// boundary; callers cannot truncate or extend it to change the tree geometry.
type collectionExecutionTerminalProof [][sha256.Size]byte

func collectionExecutionTerminalEmptyHashes() [collectionExecutionTerminalDepth + 1][sha256.Size]byte {
	var empty [collectionExecutionTerminalDepth + 1][sha256.Size]byte
	empty[0] = sha256.Sum256([]byte("cpra/collection/execution-terminal-empty/v1\x00"))
	for level := 1; level <= collectionExecutionTerminalDepth; level++ {
		empty[level] = collectionExecutionTerminalNode(empty[level-1], empty[level-1])
	}
	return empty
}

func collectionExecutionTerminalEmptyRoot() string {
	empty := collectionExecutionTerminalEmptyHashes()
	return hex.EncodeToString(empty[collectionExecutionTerminalDepth][:])
}

func collectionExecutionTerminalNode(left, right [sha256.Size]byte) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/collection/execution-terminal-node/v1\x00"))
	_, _ = h.Write(left[:])
	_, _ = h.Write(right[:])
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func collectionExecutionTerminalLeaf(ordinal uint64, raw []byte) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/collection/execution-terminal-leaf/v1\x00"))
	var position [8]byte
	binary.BigEndian.PutUint64(position[:], ordinal)
	_, _ = h.Write(position[:])
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(raw)
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func collectionExecutionTerminalPath(leaf [sha256.Size]byte, ordinal uint64, proof collectionExecutionTerminalProof) [sha256.Size]byte {
	position := ordinal - 1
	for _, sibling := range proof {
		if position&1 == 0 {
			leaf = collectionExecutionTerminalNode(leaf, sibling)
		} else {
			leaf = collectionExecutionTerminalNode(sibling, leaf)
		}
		position >>= 1
	}
	return leaf
}

// collectionExecutionTerminalInsert proves that the selected slot is empty in
// the authoritative prior root. It rejects even an identical repeated leaf;
// exact replay is reconciled against the immutable ledger before this helper.
func collectionExecutionTerminalInsert(root string, itemCount, ordinal uint64, raw []byte, proof collectionExecutionTerminalProof) (string, error) {
	if !bootstrapHash(root) || itemCount == 0 || itemCount > CollectionValidationMaxItems || ordinal == 0 || ordinal > itemCount || len(proof) != collectionExecutionTerminalDepth || len(raw) > collectionChildTerminalReserve {
		return "", ErrCollectionInvalid
	}
	r, err := decodeCollectionExecutionRecord(raw)
	if err != nil || r.Terminal == nil || r.Terminal.Ordinal != ordinal {
		return "", ErrCollectionInvalid
	}
	empty := collectionExecutionTerminalEmptyHashes()
	before := collectionExecutionTerminalPath(empty[0], ordinal, proof)
	if hex.EncodeToString(before[:]) != root {
		return "", ErrCollectionConflict
	}
	after := collectionExecutionTerminalPath(collectionExecutionTerminalLeaf(ordinal, raw), ordinal, proof)
	return hex.EncodeToString(after[:]), nil
}

// collectionExecutionTerminalTree is a disposable sparse cache, never durable
// authority. Immutable nodes make Clone constant-time and each insertion copies
// only its 15-node root-to-leaf path. At most 32,767 nodes are reachable from one
// root; old speculative paths become collectible when their roots are released.
// Callers compare Root to committed progress before using a proof and install
// speculative roots only after ledger commit. A tree handle has one owner.
type collectionExecutionTerminalTree struct {
	itemCount uint64
	root      *collectionExecutionTerminalNodeValue
}

type collectionExecutionTerminalNodeValue struct {
	sum         [sha256.Size]byte
	left, right *collectionExecutionTerminalNodeValue
}

func newCollectionExecutionTerminalTree(itemCount uint64) (*collectionExecutionTerminalTree, error) {
	if itemCount == 0 || itemCount > CollectionValidationMaxItems {
		return nil, ErrCollectionInvalid
	}
	return &collectionExecutionTerminalTree{itemCount: itemCount}, nil
}

func (t *collectionExecutionTerminalTree) Clone() *collectionExecutionTerminalTree {
	if t == nil {
		return nil
	}
	n := *t
	return &n
}

func (t *collectionExecutionTerminalTree) Root() string {
	if t == nil || t.itemCount == 0 || t.itemCount > CollectionValidationMaxItems {
		return ""
	}
	if t.root != nil {
		return hex.EncodeToString(t.root.sum[:])
	}
	return collectionExecutionTerminalEmptyRoot()
}

func (t *collectionExecutionTerminalTree) Proof(ordinal uint64) (collectionExecutionTerminalProof, error) {
	if t == nil || t.itemCount == 0 || t.itemCount > CollectionValidationMaxItems || ordinal == 0 || ordinal > t.itemCount {
		return nil, ErrCollectionInvalid
	}
	proof := make(collectionExecutionTerminalProof, collectionExecutionTerminalDepth)
	empty := collectionExecutionTerminalEmptyHashes()
	node := t.root
	position := ordinal - 1
	for depth := collectionExecutionTerminalDepth; depth > 0; depth-- {
		var selected, sibling *collectionExecutionTerminalNodeValue
		if node != nil {
			if position&(1<<uint(depth-1)) == 0 {
				selected, sibling = node.left, node.right
			} else {
				selected, sibling = node.right, node.left
			}
		}
		if sibling != nil {
			proof[depth-1] = sibling.sum
		} else {
			proof[depth-1] = empty[depth-1]
		}
		node = selected
	}
	return proof, nil
}

func collectionExecutionTerminalCopyPath(old *collectionExecutionTerminalNodeValue, depth int, position uint64, leaf [sha256.Size]byte, empty *[collectionExecutionTerminalDepth + 1][sha256.Size]byte) *collectionExecutionTerminalNodeValue {
	if depth == 0 {
		return &collectionExecutionTerminalNodeValue{sum: leaf}
	}
	next := new(collectionExecutionTerminalNodeValue)
	if old != nil {
		*next = *old
	}
	if position&(1<<uint(depth-1)) == 0 {
		next.left = collectionExecutionTerminalCopyPath(next.left, depth-1, position, leaf, empty)
	} else {
		next.right = collectionExecutionTerminalCopyPath(next.right, depth-1, position, leaf, empty)
	}
	left, right := empty[depth-1], empty[depth-1]
	if next.left != nil {
		left = next.left.sum
	}
	if next.right != nil {
		right = next.right.sum
	}
	next.sum = collectionExecutionTerminalNode(left, right)
	return next
}

func (t *collectionExecutionTerminalTree) Insert(terminal CollectionChildObservation) error {
	proof, err := t.Proof(terminal.Ordinal)
	if err != nil {
		return err
	}
	raw, err := collectionExecutionEncoding(collectionExecutionRecord{Version: collectionExecutionRecordVersion, Terminal: &terminal})
	if err != nil {
		return err
	}
	if _, err = collectionExecutionTerminalInsert(t.Root(), t.itemCount, terminal.Ordinal, raw, proof); err != nil {
		return err
	}
	empty := collectionExecutionTerminalEmptyHashes()
	t.root = collectionExecutionTerminalCopyPath(t.root, collectionExecutionTerminalDepth, terminal.Ordinal-1, collectionExecutionTerminalLeaf(terminal.Ordinal, raw), &empty)
	return nil
}
