package chain

import (
	"context"
	"crypto/ecdsa"
	"embed"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ABIs are exported from contracts/ with `forge inspect <C> abi --json`
// (see infra/scripts/export-abi.sh); rerun it after changing a contract.
//
//go:embed abi/*.json
var abiFS embed.FS

func loadABI(name string) (abi.ABI, error) {
	raw, err := abiFS.ReadFile("abi/" + name + ".json")
	if err != nil {
		return abi.ABI{}, err
	}
	return abi.JSON(strings.NewReader(string(raw)))
}

// Addresses are the deployed contract addresses.
type Addresses struct {
	Registry   common.Address
	AnchorLog  common.Address
	Governance common.Address
}

// Eth reads the contracts over JSON-RPC. All reads use the confirmed head
// (latest minus Confirmations) so one round sees one consistent block.
type Eth struct {
	Client        *ethclient.Client
	Addr          Addresses
	ChainID       *big.Int
	Confirmations uint64
	reg, log, gov abi.ABI
}

// DialEth connects and checks that the endpoint serves the expected chain.
func DialEth(ctx context.Context, rpc string, chainID int64, addr Addresses, confirmations uint64) (*Eth, error) {
	c, err := ethclient.DialContext(ctx, rpc)
	if err != nil {
		return nil, err
	}
	id, err := c.ChainID(ctx)
	if err != nil {
		return nil, err
	}
	if id.Int64() != chainID {
		return nil, fmt.Errorf("rpc serves chain %d, configured %d", id.Int64(), chainID)
	}
	e := &Eth{Client: c, Addr: addr, ChainID: id, Confirmations: confirmations}
	for name, dst := range map[string]*abi.ABI{"RoomRegistry": &e.reg, "AnchorLog": &e.log, "Governance": &e.gov} {
		if *dst, err = loadABI(name); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *Eth) ConfirmedHead(ctx context.Context) (uint64, error) {
	n, err := e.Client.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	if n < e.Confirmations {
		return 0, nil
	}
	return n - e.Confirmations, nil
}

func (e *Eth) call(ctx context.Context, block uint64, to common.Address, a abi.ABI, method string, args ...any) ([]any, error) {
	data, err := a.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	out, err := e.Client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, new(big.Int).SetUint64(block))
	if err != nil {
		return nil, err
	}
	return a.Unpack(method, out)
}

type roomTuple struct {
	Creator      common.Address
	AnchorWriter common.Address
	CreatedBlock uint64
	MemberCount  uint32
}

func (e *Eth) Room(ctx context.Context, room common.Hash) (RoomInfo, error) {
	head, err := e.ConfirmedHead(ctx)
	if err != nil {
		return RoomInfo{}, err
	}
	out, err := e.call(ctx, head, e.Addr.Registry, e.reg, "roomInfo", room)
	if err != nil {
		return RoomInfo{}, err
	}
	r := *abi.ConvertType(out[0], new(roomTuple)).(*roomTuple)
	if r.CreatedBlock == 0 {
		return RoomInfo{}, ErrUnknownRoom
	}
	mo, err := e.call(ctx, head, e.Addr.Registry, e.reg, "membersOf", room)
	if err != nil {
		return RoomInfo{}, err
	}
	return RoomInfo{Creator: r.Creator, AnchorWriter: r.AnchorWriter, CreatedBlock: r.CreatedBlock,
		Members: *abi.ConvertType(mo[0], new([]common.Address)).(*[]common.Address)}, nil
}

type checkpointTuple struct {
	GitCommit [32]byte
	Root      [32]byte
	Count     uint32
	By        common.Address
	BlockNo   uint64
	BlockTime uint64
	PrevIndex *big.Int
}

var maxUint256 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

func (e *Eth) Checkpoints(ctx context.Context, room common.Hash) ([]Checkpoint, error) {
	head, err := e.ConfirmedHead(ctx)
	if err != nil {
		return nil, err
	}
	out, err := e.call(ctx, head, e.Addr.AnchorLog, e.log, "checkpointCount", room)
	if err != nil {
		return nil, err
	}
	n := out[0].(*big.Int).Uint64()
	txs, err := e.anchorTxs(ctx, room, head)
	if err != nil {
		return nil, err
	}
	cps := make([]Checkpoint, 0, n)
	for i := uint64(0); i < n; i++ {
		o, err := e.call(ctx, head, e.Addr.AnchorLog, e.log, "checkpoint", room, new(big.Int).SetUint64(i))
		if err != nil {
			return nil, err
		}
		t := *abi.ConvertType(o[0], new(checkpointTuple)).(*checkpointTuple)
		prev := uint64(GenesisPrev)
		if t.PrevIndex.Cmp(maxUint256) != 0 {
			prev = t.PrevIndex.Uint64()
		}
		cps = append(cps, Checkpoint{Index: i, PrevIndex: prev, GitCommit: t.GitCommit, Root: t.Root, Count: t.Count,
			By: t.By, BlockNo: t.BlockNo, BlockTime: t.BlockTime, TxHash: txs[i]})
	}
	return cps, nil
}

func (e *Eth) logs(ctx context.Context, addr common.Address, ev abi.Event, room common.Hash, head uint64) ([]types.Log, error) {
	return e.Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(0), ToBlock: new(big.Int).SetUint64(head), Addresses: []common.Address{addr},
		Topics: [][]common.Hash{{ev.ID}, {room}},
	})
}

func (e *Eth) anchorTxs(ctx context.Context, room common.Hash, head uint64) (map[uint64]common.Hash, error) {
	ls, err := e.logs(ctx, e.Addr.AnchorLog, e.log.Events["Anchored"], room, head)
	if err != nil {
		return nil, err
	}
	out := map[uint64]common.Hash{}
	for _, l := range ls {
		if len(l.Topics) == 3 {
			out[new(big.Int).SetBytes(l.Topics[2][:]).Uint64()] = l.TxHash
		}
	}
	return out, nil
}

type proposalTuple struct {
	Room          [32]byte
	TargetEventId [32]byte
	ReasonHash    [32]byte
	Proposer      common.Address
	CreatedBlock  uint64
	DeadlineBlock uint64
	Yes           uint32
	No            uint32
	Executed      bool
}

func (e *Eth) Proposal(ctx context.Context, id uint64) (Proposal, error) {
	head, err := e.ConfirmedHead(ctx)
	if err != nil {
		return Proposal{}, err
	}
	out, err := e.call(ctx, head, e.Addr.Governance, e.gov, "proposal", new(big.Int).SetUint64(id))
	if err != nil {
		return Proposal{}, err
	}
	p := *abi.ConvertType(out[0], new(proposalTuple)).(*proposalTuple)
	return Proposal{ID: id, Room: p.Room, Target: p.TargetEventId, ReasonHash: p.ReasonHash, Proposer: p.Proposer,
		CreatedBlock: p.CreatedBlock, DeadlineBlock: p.DeadlineBlock, Yes: p.Yes, No: p.No, Executed: p.Executed}, nil
}

func (e *Eth) ExecutedRedactions(ctx context.Context, room common.Hash) ([]Redaction, error) {
	head, err := e.ConfirmedHead(ctx)
	if err != nil {
		return nil, err
	}
	ev := e.gov.Events["Executed"]
	ls, err := e.Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(0), ToBlock: new(big.Int).SetUint64(head), Addresses: []common.Address{e.Addr.Governance},
		Topics: [][]common.Hash{{ev.ID}, nil, {room}},
	})
	if err != nil {
		return nil, err
	}
	var out []Redaction
	for _, l := range ls {
		vals, err := ev.Inputs.NonIndexed().Unpack(l.Data)
		if err != nil || len(l.Topics) < 2 {
			continue
		}
		target := vals[0].([32]byte)
		out = append(out, Redaction{ProposalID: new(big.Int).SetBytes(l.Topics[1][:]).Uint64(), Target: target, BlockNo: l.BlockNumber})
	}
	return out, nil
}

// Proposals lists the room's proposals (from Proposed logs) with their
// current state.
func (e *Eth) Proposals(ctx context.Context, room common.Hash) ([]Proposal, error) {
	head, err := e.ConfirmedHead(ctx)
	if err != nil {
		return nil, err
	}
	ev := e.gov.Events["Proposed"]
	ls, err := e.Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(0), ToBlock: new(big.Int).SetUint64(head), Addresses: []common.Address{e.Addr.Governance},
		Topics: [][]common.Hash{{ev.ID}, nil, {room}},
	})
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, 0, len(ls))
	for _, l := range ls {
		if len(l.Topics) < 2 {
			continue
		}
		p, err := e.Proposal(ctx, new(big.Int).SetBytes(l.Topics[1][:]).Uint64())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// EthGovernor sends governance transactions with a member TEST key.
type EthGovernor struct {
	Eth *Eth
	Key *ecdsa.PrivateKey
}

func (g EthGovernor) Propose(ctx context.Context, room, target, reasonHash common.Hash) (uint64, error) {
	rc, err := g.Eth.Transact(ctx, g.Key, g.Eth.Addr.Governance, g.Eth.gov, "proposeRedaction", room, target, reasonHash)
	if err != nil {
		return 0, err
	}
	for _, l := range rc.Logs {
		if len(l.Topics) > 1 && l.Topics[0] == g.Eth.gov.Events["Proposed"].ID {
			return new(big.Int).SetBytes(l.Topics[1][:]).Uint64(), nil
		}
	}
	return 0, errors.New("no Proposed event in receipt")
}

func (g EthGovernor) Vote(ctx context.Context, id uint64, support bool) error {
	_, err := g.Eth.Transact(ctx, g.Key, g.Eth.Addr.Governance, g.Eth.gov, "vote", new(big.Int).SetUint64(id), support)
	return err
}

func (g EthGovernor) Execute(ctx context.Context, id uint64) error {
	_, err := g.Eth.Transact(ctx, g.Key, g.Eth.Addr.Governance, g.Eth.gov, "execute", new(big.Int).SetUint64(id))
	return err
}

func (g EthGovernor) ProposeOrder(ctx context.Context, room, a, b common.Hash) (uint64, error) {
	rc, err := g.Eth.Transact(ctx, g.Key, g.Eth.Addr.Governance, g.Eth.gov, "proposeOrder", room, a, b)
	if err != nil {
		return 0, err
	}
	for _, l := range rc.Logs {
		if len(l.Topics) > 1 && l.Topics[0] == g.Eth.gov.Events["OrderProposed"].ID {
			return new(big.Int).SetBytes(l.Topics[1][:]).Uint64(), nil
		}
	}
	return 0, errors.New("no OrderProposed event in receipt")
}

func (g EthGovernor) VoteOrder(ctx context.Context, id uint64, aFirst bool) error {
	_, err := g.Eth.Transact(ctx, g.Key, g.Eth.Addr.Governance, g.Eth.gov, "voteOrder", new(big.Int).SetUint64(id), aFirst)
	return err
}

type orderTuple struct {
	Room          [32]byte
	A             [32]byte
	B             [32]byte
	Proposer      common.Address
	CreatedBlock  uint64
	DeadlineBlock uint64
	ForA          uint32
	ForB          uint32
	Outcome       uint8
}

// OrderProposals lists the room's ordering proposals (from OrderProposed logs)
// with their current state.
func (e *Eth) OrderProposals(ctx context.Context, room common.Hash) ([]OrderProposal, error) {
	head, err := e.ConfirmedHead(ctx)
	if err != nil {
		return nil, err
	}
	ev := e.gov.Events["OrderProposed"]
	ls, err := e.Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(0), ToBlock: new(big.Int).SetUint64(head), Addresses: []common.Address{e.Addr.Governance},
		Topics: [][]common.Hash{{ev.ID}, nil, {room}},
	})
	if err != nil {
		return nil, err
	}
	out := make([]OrderProposal, 0, len(ls))
	for _, l := range ls {
		if len(l.Topics) < 2 {
			continue
		}
		id := new(big.Int).SetBytes(l.Topics[1][:])
		res, err := e.call(ctx, head, e.Addr.Governance, e.gov, "orderProposal", id)
		if err != nil {
			return nil, err
		}
		o := *abi.ConvertType(res[0], new(orderTuple)).(*orderTuple)
		out = append(out, OrderProposal{ID: id.Uint64(), Room: o.Room, A: o.A, B: o.B, Proposer: o.Proposer,
			CreatedBlock: o.CreatedBlock, DeadlineBlock: o.DeadlineBlock, ForA: o.ForA, ForB: o.ForB, Outcome: o.Outcome})
	}
	return out, nil
}

// Transact sends a contract call signed by key and waits for its receipt.
func (e *Eth) Transact(ctx context.Context, key *ecdsa.PrivateKey, to common.Address, a abi.ABI, method string, args ...any) (*types.Receipt, error) {
	data, err := a.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := e.Client.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, err
	}
	gas, err := e.Client.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Data: data})
	if err != nil {
		return nil, fmt.Errorf("%s would revert: %w", method, err)
	}
	tip, err := e.Client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, err
	}
	hdr, err := e.Client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, err
	}
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(hdr.BaseFee, big.NewInt(2)))
	tx, err := types.SignNewTx(key, types.LatestSignerForChainID(e.ChainID), &types.DynamicFeeTx{
		ChainID: e.ChainID, Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap, Gas: gas * 12 / 10, To: &to, Data: data,
	})
	if err != nil {
		return nil, err
	}
	if err := e.Client.SendTransaction(ctx, tx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		rc, err := e.Client.TransactionReceipt(ctx, tx.Hash())
		if err == nil {
			if rc.Status != types.ReceiptStatusSuccessful {
				return rc, fmt.Errorf("%s reverted in tx %s", method, tx.Hash().Hex())
			}
			return rc, nil
		}
		if !errors.Is(err, ethereum.NotFound) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("timed out waiting for %s", tx.Hash().Hex())
}

// RegistryABI, AnchorLogABI and GovernanceABI expose the parsed ABIs for
// scripts that send their own transactions.
func (e *Eth) RegistryABI() abi.ABI   { return e.reg }
func (e *Eth) AnchorLogABI() abi.ABI  { return e.log }
func (e *Eth) GovernanceABI() abi.ABI { return e.gov }

// EthAnchor is the anchor node's AnchorWriter.
type EthAnchor struct {
	Eth *Eth
	Key *ecdsa.PrivateKey
}

func (a EthAnchor) Address() common.Address { return crypto.PubkeyToAddress(a.Key.PublicKey) }

func (a EthAnchor) Anchor(ctx context.Context, room common.Hash, prev uint64, commit, root common.Hash, count uint32) (common.Hash, error) {
	p := new(big.Int).SetUint64(prev)
	if prev == GenesisPrev {
		p = maxUint256
	}
	rc, err := a.Eth.Transact(ctx, a.Key, a.Eth.Addr.AnchorLog, a.Eth.log, "anchor", room, p, commit, root, count)
	if err != nil {
		return common.Hash{}, err
	}
	return rc.TxHash, nil
}
