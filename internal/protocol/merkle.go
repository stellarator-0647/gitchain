package protocol

import (
	"bytes"
	"errors"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Leaf commits to an event and its signature:
// keccak256(0x00 || eventId || keccak256(sig)). The 0x00 prefix separates
// leaves from inner nodes; binding the signature stops a different signature
// from being passed off as the anchored one.
func Leaf(eventID common.Hash, sig []byte) common.Hash {
	return crypto.Keccak256Hash([]byte{0x00}, eventID[:], crypto.Keccak256(sig))
}

// DelegatedLeaf is the leaf of a delegate-signed event:
// keccak256(0x01 || eventId || keccak256(sig) || delegationId).
func DelegatedLeaf(eventID common.Hash, sig []byte, delegationID common.Hash) common.Hash {
	return crypto.Keccak256Hash([]byte{0x01}, eventID[:], crypto.Keccak256(sig), delegationID[:])
}

func hashPair(a, b common.Hash) common.Hash {
	if bytes.Compare(a[:], b[:]) > 0 {
		a, b = b, a
	}
	return crypto.Keccak256Hash(a[:], b[:])
}

// Tree is a sorted-pair binary Merkle tree. On a level with an odd number of
// nodes the last node is promoted unchanged; a single leaf is its own root.
type Tree struct {
	levels [][]common.Hash
}

// ErrEmptyTree is returned for zero leaves; empty checkpoints are forbidden.
var ErrEmptyTree = errors.New("merkle: empty tree")

// BuildTree builds a tree over leaves in the given order.
func BuildTree(leaves []common.Hash) (*Tree, error) {
	if len(leaves) == 0 {
		return nil, ErrEmptyTree
	}
	level := append([]common.Hash(nil), leaves...)
	t := &Tree{levels: [][]common.Hash{level}}
	for len(level) > 1 {
		next := make([]common.Hash, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
			} else {
				next = append(next, hashPair(level[i], level[i+1]))
			}
		}
		t.levels = append(t.levels, next)
		level = next
	}
	return t, nil
}

// Root returns the tree root.
func (t *Tree) Root() common.Hash { return t.levels[len(t.levels)-1][0] }

// Count returns the number of leaves.
func (t *Tree) Count() int { return len(t.levels[0]) }

// Proof returns the sibling path for leaf i. Levels where the node was
// promoted contribute no sibling.
func (t *Tree) Proof(i int) []common.Hash {
	proof := []common.Hash{}
	for _, level := range t.levels[:len(t.levels)-1] {
		sib := i ^ 1
		if sib < len(level) {
			proof = append(proof, level[sib])
		}
		i /= 2
	}
	return proof
}

// VerifyProof checks a sorted-pair proof against root.
func VerifyProof(leaf common.Hash, proof []common.Hash, root common.Hash) bool {
	h := leaf
	for _, p := range proof {
		h = hashPair(h, p)
	}
	return h == root
}

// IndexedEvent is a signed event together with its id and checkpoint leaf.
type IndexedEvent struct {
	ID     common.Hash
	Signed SignedEvent
	Leaf   common.Hash
}

// Index computes id and leaf of a signed event under d.
func Index(se SignedEvent, d Domain) IndexedEvent {
	id := se.Event.ID(d)
	return IndexedEvent{ID: id, Signed: se, Leaf: se.LeafHash(id, d)}
}

// SortForTree orders events by author address bytes, then seq, then id.
func SortForTree(evs []IndexedEvent) {
	sort.Slice(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		if c := bytes.Compare(a.Signed.Event.Author[:], b.Signed.Event.Author[:]); c != 0 {
			return c < 0
		}
		if a.Signed.Event.Seq != b.Signed.Event.Seq {
			return a.Signed.Event.Seq < b.Signed.Event.Seq
		}
		return bytes.Compare(a.ID[:], b.ID[:]) < 0
	})
}

// SnapshotTree sorts events and builds the checkpoint tree over them.
// It returns the tree and the sorted events so callers can map leaf indexes.
func SnapshotTree(evs []IndexedEvent) (*Tree, []IndexedEvent, error) {
	sorted := append([]IndexedEvent(nil), evs...)
	SortForTree(sorted)
	leaves := make([]common.Hash, len(sorted))
	for i, e := range sorted {
		if e.Leaf == (common.Hash{}) {
			if e.Signed.Delegation != nil {
				return nil, nil, errors.New("merkle: delegated event without precomputed leaf")
			}
			e.Leaf = Leaf(e.ID, e.Signed.Sig)
		}
		leaves[i] = e.Leaf
	}
	t, err := BuildTree(leaves)
	return t, sorted, err
}
