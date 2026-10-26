package chain

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Memory is an in-process stand-in for the contracts. It applies the same
// rules (anchor writer only, prevIndex chain, growing count, majority vote)
// so tests exercise real failure paths. Data built on it must be labelled as
// simulated; demos and experiments use a real chain.
type Memory struct {
	mu           sync.Mutex
	block        uint64
	rooms        map[common.Hash]RoomInfo
	checkpoints  map[common.Hash][]Checkpoint
	proposals    []Proposal
	votes        map[uint64]map[common.Address]bool
	executedAt   map[uint64]uint64
	orders       []OrderProposal
	orderVotes   map[uint64]map[common.Address]bool
	VotingBlocks uint64
	// TimeBase is the simulated timestamp of block 0; each block adds 12 s.
	TimeBase uint64
}

// NewMemory returns an empty simulated chain.
func NewMemory() *Memory {
	return &Memory{
		rooms:        map[common.Hash]RoomInfo{},
		checkpoints:  map[common.Hash][]Checkpoint{},
		votes:        map[uint64]map[common.Address]bool{},
		executedAt:   map[uint64]uint64{},
		orderVotes:   map[uint64]map[common.Address]bool{},
		VotingBlocks: 100,
		TimeBase:     1_700_000_000,
	}
}

func (m *Memory) tick() uint64 { m.block++; return m.block }

// CreateRoom mirrors RoomRegistry.createRoom.
func (m *Memory) CreateRoom(room common.Hash, sender common.Address, members []common.Address, anchorWriter common.Address) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rooms[room]; ok || room == (common.Hash{}) {
		return errors.New("room exists or zero id")
	}
	info := RoomInfo{Creator: sender, AnchorWriter: anchorWriter, Members: append([]common.Address(nil), members...)}
	if !info.IsMember(sender) || !info.IsMember(anchorWriter) {
		return errors.New("creator and anchor writer must be members")
	}
	seen := map[common.Address]bool{}
	for _, a := range members {
		if a == (common.Address{}) || seen[a] {
			return errors.New("zero or duplicate member")
		}
		seen[a] = true
	}
	info.CreatedBlock = m.tick()
	m.rooms[room] = info
	return nil
}

func (m *Memory) Room(_ context.Context, room common.Hash) (RoomInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[room]
	if !ok {
		return RoomInfo{}, ErrUnknownRoom
	}
	return r, nil
}

func (m *Memory) Checkpoints(_ context.Context, room common.Hash) ([]Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Checkpoint(nil), m.checkpoints[room]...), nil
}

func (m *Memory) ConfirmedHead(context.Context) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.block, nil
}

// MemoryAnchor binds a sender address to the simulated AnchorLog.
type MemoryAnchor struct {
	Chain  *Memory
	Sender common.Address
}

func (a MemoryAnchor) Address() common.Address { return a.Sender }

func (a MemoryAnchor) Anchor(_ context.Context, room common.Hash, prev uint64, commit, root common.Hash, count uint32) (common.Hash, error) {
	m := a.Chain
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[room]
	if !ok {
		return common.Hash{}, ErrUnknownRoom
	}
	if a.Sender != r.AnchorWriter {
		return common.Hash{}, errors.New("not anchor writer")
	}
	if commit == (common.Hash{}) || root == (common.Hash{}) || count == 0 {
		return common.Hash{}, errors.New("zero commit, root or count")
	}
	cps := m.checkpoints[room]
	if len(cps) == 0 {
		if prev != GenesisPrev {
			return common.Hash{}, errors.New("first checkpoint needs genesis prevIndex")
		}
	} else {
		last := cps[len(cps)-1]
		if prev != last.Index {
			return common.Hash{}, errors.New("prevIndex is not the latest checkpoint")
		}
		if count <= last.Count {
			return common.Hash{}, errors.New("count must grow")
		}
	}
	for _, c := range cps {
		if c.GitCommit == commit {
			return common.Hash{}, errors.New("commit already anchored")
		}
	}
	b := m.tick()
	tx := crypto.Keccak256Hash(room[:], commit[:], root[:], binary.BigEndian.AppendUint64(nil, b))
	m.checkpoints[room] = append(cps, Checkpoint{
		Index: uint64(len(cps)), PrevIndex: prev, GitCommit: commit, Root: root, Count: count,
		By: a.Sender, BlockNo: b, BlockTime: m.TimeBase + b*12, TxHash: tx,
	})
	return tx, nil
}

// ProposeRedaction mirrors Governance.proposeRedaction; ids start at 1.
func (m *Memory) ProposeRedaction(sender common.Address, room, target, reason common.Hash) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[room]
	if !ok || !r.IsMember(sender) {
		return 0, errors.New("not a member")
	}
	if target == (common.Hash{}) {
		return 0, errors.New("zero target")
	}
	for _, p := range m.proposals {
		if p.Room == room && p.Target == target && p.Executed {
			return 0, errors.New("already redacted")
		}
	}
	b := m.tick()
	id := uint64(len(m.proposals) + 1)
	m.proposals = append(m.proposals, Proposal{ID: id, Room: room, Target: target, ReasonHash: reason,
		Proposer: sender, CreatedBlock: b, DeadlineBlock: b + m.VotingBlocks})
	m.votes[id] = map[common.Address]bool{}
	return id, nil
}

// Vote mirrors Governance.vote.
func (m *Memory) Vote(sender common.Address, id uint64, support bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == 0 || id > uint64(len(m.proposals)) {
		return errors.New("no such proposal")
	}
	p := &m.proposals[id-1]
	if !m.rooms[p.Room].IsMember(sender) || m.votes[id][sender] || p.Executed || m.block+1 > p.DeadlineBlock {
		return errors.New("vote rejected")
	}
	m.tick()
	m.votes[id][sender] = true
	if support {
		p.Yes++
	} else {
		p.No++
	}
	return nil
}

// Execute mirrors Governance.execute.
func (m *Memory) Execute(id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == 0 || id > uint64(len(m.proposals)) {
		return errors.New("no such proposal")
	}
	p := &m.proposals[id-1]
	if p.Executed || m.block+1 > p.DeadlineBlock || int(p.Yes)*2 <= len(m.rooms[p.Room].Members) {
		return errors.New("execute rejected")
	}
	m.executedAt[id] = m.tick()
	p.Executed = true
	return nil
}

func (m *Memory) Proposal(_ context.Context, id uint64) (Proposal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == 0 || id > uint64(len(m.proposals)) {
		return Proposal{}, errors.New("no such proposal")
	}
	return m.proposals[id-1], nil
}

func (m *Memory) Proposals(_ context.Context, room common.Hash) ([]Proposal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Proposal
	for _, p := range m.proposals {
		if p.Room == room {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *Memory) ExecutedRedactions(_ context.Context, room common.Hash) ([]Redaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Redaction
	for _, p := range m.proposals {
		if p.Room == room && p.Executed {
			out = append(out, Redaction{ProposalID: p.ID, Target: p.Target, BlockNo: m.executedAt[p.ID]})
		}
	}
	return out, nil
}

// ProposeOrder mirrors Governance.proposeOrder: one live proposal per pair.
func (m *Memory) ProposeOrder(sender common.Address, room, a, b common.Hash) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.rooms[room].IsMember(sender) {
		return 0, errors.New("not a member")
	}
	if a == (common.Hash{}) || b == (common.Hash{}) || a == b {
		return 0, errors.New("bad pair")
	}
	for _, o := range m.orders {
		if o.Room == room && ((o.A == a && o.B == b) || (o.A == b && o.B == a)) &&
			(o.Outcome != OrderOpen || m.block+1 <= o.DeadlineBlock) {
			return 0, errors.New("pair already open or settled")
		}
	}
	blk := m.tick()
	id := uint64(len(m.orders) + 1)
	m.orders = append(m.orders, OrderProposal{ID: id, Room: room, A: a, B: b, Proposer: sender,
		CreatedBlock: blk, DeadlineBlock: blk + m.VotingBlocks})
	m.orderVotes[id] = map[common.Address]bool{}
	return id, nil
}

// VoteOrder mirrors Governance.voteOrder; a majority settles at once.
func (m *Memory) VoteOrder(sender common.Address, id uint64, aFirst bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == 0 || id > uint64(len(m.orders)) {
		return errors.New("no such order proposal")
	}
	o := &m.orders[id-1]
	members := len(m.rooms[o.Room].Members)
	if !m.rooms[o.Room].IsMember(sender) || m.orderVotes[id][sender] || o.Outcome != OrderOpen || m.block+1 > o.DeadlineBlock {
		return errors.New("order vote rejected")
	}
	m.tick()
	m.orderVotes[id][sender] = true
	count := &o.ForB
	if aFirst {
		count = &o.ForA
	}
	*count++
	if int(*count)*2 > members {
		o.Outcome = OrderBFirst
		if aFirst {
			o.Outcome = OrderAFirst
		}
	}
	return nil
}

func (m *Memory) OrderProposals(_ context.Context, room common.Hash) ([]OrderProposal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OrderProposal
	for _, o := range m.orders {
		if o.Room == room {
			out = append(out, o)
		}
	}
	return out, nil
}
