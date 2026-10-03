package persistence

import "crypto/sha256"

// collectionExecutionRetirementTree retains at most two nodes per tree level
// for the retired prefix. Covered subtrees are opaque; only the remaining
// suffix may request proofs or accept leaves, even if a retired leaf was empty.
type collectionExecutionRetirementTree struct {
	binding CollectionExecutionBinding
	retired uint64
	tree    *collectionExecutionTerminalTree
}

func (c CollectionExecutionRetirementCheckpoint) terminalTree() (*collectionExecutionRetirementTree, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	frontier, err := c.frontierHashes()
	if err != nil {
		return nil, err
	}
	empty := collectionExecutionTerminalEmptyHashes()
	return &collectionExecutionRetirementTree{binding: c.Progress.Binding, retired: c.Progress.Processed, tree: c.prefixTree(frontier, &empty)}, nil
}

func (c CollectionExecutionRetirementCheckpoint) prefixTree(frontier [collectionExecutionTerminalDepth + 1][sha256.Size]byte, empty *[collectionExecutionTerminalDepth + 1][sha256.Size]byte) *collectionExecutionTerminalTree {
	var build func(int, uint64) *collectionExecutionTerminalNodeValue
	build = func(depth int, count uint64) *collectionExecutionTerminalNodeValue {
		if count == 0 {
			return nil
		}
		if count == 1<<uint(depth) {
			return &collectionExecutionTerminalNodeValue{sum: frontier[depth]}
		}
		n := &collectionExecutionTerminalNodeValue{}
		half := uint64(1 << uint(depth-1))
		if count >= half {
			n.left = &collectionExecutionTerminalNodeValue{sum: frontier[depth-1]}
			n.right = build(depth-1, count-half)
		} else {
			n.left = build(depth-1, count)
		}
		left, right := empty[depth-1], empty[depth-1]
		if n.left != nil {
			left = n.left.sum
		}
		if n.right != nil {
			right = n.right.sum
		}
		n.sum = collectionExecutionTerminalNode(left, right)
		return n
	}
	return &collectionExecutionTerminalTree{itemCount: c.Progress.ItemCount, root: build(collectionExecutionTerminalDepth, c.Progress.Processed)}
}

func (t *collectionExecutionRetirementTree) Clone() *collectionExecutionRetirementTree {
	if t == nil {
		return nil
	}
	n := *t
	n.tree = t.tree.Clone()
	return &n
}

func (t *collectionExecutionRetirementTree) Root() string {
	if t == nil {
		return ""
	}
	return t.tree.Root()
}

func (t *collectionExecutionRetirementTree) Proof(ordinal uint64) (collectionExecutionTerminalProof, error) {
	if t == nil || t.tree == nil || ordinal <= t.retired {
		return nil, ErrCollectionInvalid
	}
	return t.tree.Proof(ordinal)
}

func (t *collectionExecutionRetirementTree) Insert(terminal CollectionChildObservation) error {
	if t == nil || t.tree == nil || terminal.Ordinal <= t.retired || terminal.Binding != t.binding {
		return ErrCollectionInvalid
	}
	return t.tree.Insert(terminal)
}
