// Package chain is the node's view of the three contracts. Reader is what the
// verifier needs; AnchorWriter is only configured on the designated anchor
// node. Memory implements both for tests and chain-less development; Eth talks
// to a JSON-RPC endpoint.
package chain

import (
	"context"
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
)

// GenesisPrev is the prevIndex of the first checkpoint of a room.
const GenesisPrev = math.MaxUint64

// RoomInfo mirrors RoomRegistry.roomInfo plus the member list.
type RoomInfo struct {
	Creator      common.Address   `json:"creator"`
	AnchorWriter common.Address   `json:"anchorWriter"`
	CreatedBlock uint64           `json:"createdBlock"`
	Members      []common.Address `json:"members"`
}

// IsMember reports membership in the fixed member set.
func (r RoomInfo) IsMember(a common.Address) bool {
	for _, m := range r.Members {
		if m == a {
			return true
		}
	}
	return false
}

// Checkpoint is one AnchorLog entry.
type Checkpoint struct {
	Index     uint64         `json:"index"`
	PrevIndex uint64         `json:"prevIndex"`
	GitCommit common.Hash    `json:"gitCommit"`
	Root      common.Hash    `json:"root"`
	Count     uint32         `json:"count"`
	By        common.Address `json:"by"`
	BlockNo   uint64         `json:"blockNo"`
	BlockTime uint64         `json:"blockTime"`
	TxHash    common.Hash    `json:"txHash"`
}

// Proposal mirrors Governance.proposal.
type Proposal struct {
	ID            uint64         `json:"id"`
	Room          common.Hash    `json:"room"`
	Target        common.Hash    `json:"target"`
	ReasonHash    common.Hash    `json:"reasonHash"`
	Proposer      common.Address `json:"proposer"`
	CreatedBlock  uint64         `json:"createdBlock"`
	DeadlineBlock uint64         `json:"deadlineBlock"`
	Yes           uint32         `json:"yes"`
	No            uint32         `json:"no"`
	Executed      bool           `json:"executed"`
}

// Order outcomes of an OrderProposal.
const (
	OrderOpen   uint8 = 0
	OrderAFirst uint8 = 1
	OrderBFirst uint8 = 2
)

// OrderProposal mirrors Governance.orderProposal: the members' vote on
// whether event A comes before event B.
type OrderProposal struct {
	ID            uint64         `json:"id"`
	Room          common.Hash    `json:"room"`
	A             common.Hash    `json:"a"`
	B             common.Hash    `json:"b"`
	Proposer      common.Address `json:"proposer"`
	CreatedBlock  uint64         `json:"createdBlock"`
	DeadlineBlock uint64         `json:"deadlineBlock"`
	ForA          uint32         `json:"forA"`
	ForB          uint32         `json:"forB"`
	Outcome       uint8          `json:"outcome"`
}

// Before returns the agreed (first, second) pair; ok is false while open.
func (o OrderProposal) Before() (first, second common.Hash, ok bool) {
	switch o.Outcome {
	case OrderAFirst:
		return o.A, o.B, true
	case OrderBFirst:
		return o.B, o.A, true
	}
	return common.Hash{}, common.Hash{}, false
}

// Redaction is an executed deletion authorisation.
type Redaction struct {
	ProposalID uint64      `json:"proposalId"`
	Target     common.Hash `json:"target"`
	BlockNo    uint64      `json:"blockNo"`
}

// ErrUnknownRoom is returned when the registry has no such room.
var ErrUnknownRoom = errors.New("chain: unknown room")

// Reader reads confirmed chain state.
type Reader interface {
	Room(ctx context.Context, room common.Hash) (RoomInfo, error)
	Checkpoints(ctx context.Context, room common.Hash) ([]Checkpoint, error)
	Proposal(ctx context.Context, id uint64) (Proposal, error)
	ExecutedRedactions(ctx context.Context, room common.Hash) ([]Redaction, error)
	Proposals(ctx context.Context, room common.Hash) ([]Proposal, error)
	OrderProposals(ctx context.Context, room common.Hash) ([]OrderProposal, error)
	ConfirmedHead(ctx context.Context) (uint64, error)
}

// Governor sends governance transactions for a member key. Browsers use the
// wallet instead; this exists for scripts and the dev signer.
type Governor interface {
	Propose(ctx context.Context, room, target, reasonHash common.Hash) (uint64, error)
	Vote(ctx context.Context, id uint64, support bool) error
	Execute(ctx context.Context, id uint64) error
	ProposeOrder(ctx context.Context, room, a, b common.Hash) (uint64, error)
	VoteOrder(ctx context.Context, id uint64, aFirst bool) error
}

// AnchorWriter submits checkpoints. Only the room's anchorWriter may call it
// successfully; the contract enforces that.
type AnchorWriter interface {
	Address() common.Address
	Anchor(ctx context.Context, room common.Hash, prev uint64, commit, root common.Hash, count uint32) (common.Hash, error)
}
