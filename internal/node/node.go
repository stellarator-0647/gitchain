// Package node ties the room repository, the verifier and the chain together
// for one member wallet. All writes go through one mutex (the serial operation
// queue from docs/protocol.md §8.2); the sync loop, the anchor policy and the
// redaction cleanup run on a ticker.
package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/gitstore"
	"github.com/gitchain/gitchain/internal/protocol"
	"github.com/gitchain/gitchain/internal/verify"
)

// Contracts are the trusted deployment addresses.
type Contracts struct {
	Registry   common.Address `json:"registry"`
	AnchorLog  common.Address `json:"anchorLog"`
	Governance common.Address `json:"governance"`
}

// Config configures one node.
type Config struct {
	DataDir     string
	Room        common.Hash
	Wallet      common.Address
	ChainID     int64
	RPC         string // trusted JSON-RPC endpoint, offered to the wallet when it lacks the chain
	Contracts   Contracts
	SyncEvery   time.Duration
	AnchorN     int
	AnchorEvery time.Duration
}

// Draft is an event prepared by the node and waiting for the wallet signature.
type Draft struct {
	ID        string             `json:"draftId"`
	EventID   common.Hash        `json:"eventId"`
	Event     protocol.Event     `json:"event"`
	TypedData protocol.TypedData `json:"typedData"`
	blob      *protocol.Blob
	created   time.Time
}

// Notifier receives push updates for the WebSocket hub.
type Notifier func(kind string, payload any)

// Node is one member's local GitChain node.
type Node struct {
	cfg    Config
	domain protocol.Domain
	repo   *gitstore.Repo
	reader chain.Reader
	anchor chain.AnchorWriter
	notify Notifier

	writeMu sync.Mutex // serial queue for drafts, commits and content rewrites
	// anchorMu serialises anchoring: the background round and a manual
	// "anchor now" must not both submit the same head (the contract rejects
	// the second as CommitAlreadyAnchored).
	anchorMu sync.Mutex
	draft   *Draft

	stateMu    sync.RWMutex
	verifier   *verify.Verifier
	result     *verify.Result
	lastSync   gitstore.SyncReport
	quarantine []verify.Rejection
	chainErr   string
	roomInfo   *chain.RoomInfo
	lastAnchor time.Time
	checkpoints []chain.Checkpoint

	// syncPaused stops fetch/push so a member can work "offline" (demo of
	// divergent histories and the later merge); local writes continue.
	syncPaused atomic.Bool

	// DevSigner is reported to the UI when the API has a TEST signing key.
	DevSigner bool

	delMu      sync.Mutex // guards delegation; never held while taking another lock
	delegation *protocol.SignedDelegation
}

// New wires a node around an existing repository clone.
func New(cfg Config, repo *gitstore.Repo, reader chain.Reader, anchor chain.AnchorWriter, notify Notifier) (*Node, error) {
	if cfg.SyncEvery == 0 {
		cfg.SyncEvery = 5 * time.Second
	}
	if cfg.AnchorN == 0 {
		cfg.AnchorN = 20
	}
	if cfg.AnchorEvery == 0 {
		cfg.AnchorEvery = 10 * time.Minute
	}
	if notify == nil {
		notify = func(string, any) {}
	}
	n := &Node{
		cfg: cfg, domain: protocol.NewDomain(cfg.ChainID, cfg.Contracts.Registry),
		repo: repo, reader: reader, anchor: anchor, notify: notify, verifier: verify.New(),
	}
	if err := os.MkdirAll(n.stateDir("outbox"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(n.stateDir("quarantine"), 0o700); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Node) stateDir(sub string) string { return filepath.Join(n.cfg.DataDir, "state", sub) }

// Domain returns the trusted EIP-712 domain.
func (n *Node) Domain() protocol.Domain { return n.domain }

// Config returns the node configuration.
func (n *Node) Config() Config { return n.cfg }

// Run starts the background loop and blocks until ctx ends.
func (n *Node) Run(ctx context.Context) {
	n.loadDelegation()
	if err := n.recoverOutbox(); err != nil {
		log.Println("outbox recovery:", err)
	}
	n.Tick(ctx)
	t := time.NewTicker(n.cfg.SyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.Tick(ctx)
		}
	}
}

// SetSyncPaused turns the simulated offline mode on or off.
func (n *Node) SetSyncPaused(p bool) { n.syncPaused.Store(p) }

// Tick runs one sync, verification, cleanup and anchor round.
func (n *Node) Tick(ctx context.Context) {
	if !n.syncPaused.Load() {
		n.SyncOnce(ctx)
	}
	if err := n.Refresh(ctx); err != nil {
		log.Println("refresh:", err)
	}
	if err := n.applyRedactions(ctx); err != nil {
		log.Println("redaction cleanup:", err)
	}
	if err := n.maybeAnchor(ctx, false); err != nil {
		log.Println("anchor:", err)
	}
}

// SyncOnce fetches, checks the incoming events, merges and pushes.
func (n *Node) SyncOnce(ctx context.Context) gitstore.SyncReport {
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	rep := gitstore.SyncReport{}
	defer func() {
		rep.Unpushed = n.repo.Unpushed()
		n.stateMu.Lock()
		n.lastSync = rep
		n.stateMu.Unlock()
		n.notify("sync", rep)
	}()
	if err := n.repo.Fetch(); err != nil {
		rep.Error = "fetch: " + err.Error()
		return rep
	}
	rep.Fetched = true
	if n.repo.HasRef(gitstore.RemoteMain()) {
		in, err := n.baseInput(ctx)
		if err != nil {
			rep.Error = err.Error()
			return rep
		}
		rej, err := n.verifier.CheckTrees(in, "refs/heads/"+gitstore.MainBranch, gitstore.RemoteMain())
		if err != nil {
			rep.Error = err.Error()
			return rep
		}
		n.stateMu.Lock()
		n.quarantine = rej
		n.stateMu.Unlock()
		if len(rej) > 0 {
			n.saveQuarantine(rej)
			rep.Error = fmt.Sprintf("remote contains %d invalid event file(s); merge refused", len(rej))
			return rep
		}
	}
	merged, fwd, err := n.repo.MergeMain()
	rep.MainMerged, rep.MainForwarded = merged, fwd
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	if rep.ContentRebuilt, err = n.repo.MergeContent(); err != nil {
		rep.Error = err.Error()
		return rep
	}
	if err := n.repo.Push(); err != nil {
		rep.Error = "push: " + err.Error()
		return rep
	}
	rep.Pushed = true
	return rep
}

func (n *Node) saveQuarantine(rej []verify.Rejection) {
	raw, _ := json.MarshalIndent(rej, "", "  ")
	_ = os.WriteFile(filepath.Join(n.stateDir("quarantine"), "latest.json"), raw, 0o600)
}

func (n *Node) room(ctx context.Context) (chain.RoomInfo, error) {
	n.stateMu.RLock()
	cached := n.roomInfo
	n.stateMu.RUnlock()
	if cached != nil {
		return *cached, nil // membership is fixed at creation in the MVP
	}
	info, err := n.reader.Room(ctx, n.cfg.Room)
	if err != nil {
		return info, err
	}
	n.stateMu.Lock()
	n.roomInfo = &info
	n.stateMu.Unlock()
	return info, nil
}

func (n *Node) baseInput(ctx context.Context) (verify.Input, error) {
	info, err := n.room(ctx)
	if err != nil {
		return verify.Input{}, fmt.Errorf("room info: %w", err)
	}
	return verify.Input{Domain: n.domain, Room: n.cfg.Room, RoomInfo: info, Git: n.repo,
		Blob: func(id common.Hash) ([]byte, bool) {
			b, err := n.repo.ReadBlob(id.Hex())
			return b, err == nil
		}}, nil
}

// Refresh re-verifies the room. Chain read failures keep the last result and
// mark the room as rpc_unavailable instead of guessing.
func (n *Node) Refresh(ctx context.Context) error {
	in, err := n.baseInput(ctx)
	if err != nil {
		n.setChainErr(err)
		return err
	}
	head, err := n.repo.Head()
	if err != nil {
		return err
	}
	in.Head = head
	in.Now = uint64(time.Now().Unix())
	if ch, err := n.repo.ContentHead(); err == nil {
		in.ContentRev = ch
	}
	cps, err := n.reader.Checkpoints(ctx, n.cfg.Room)
	if err != nil {
		n.setChainErr(err)
		return err
	}
	reds, err := n.reader.ExecutedRedactions(ctx, n.cfg.Room)
	if err != nil {
		n.setChainErr(err)
		return err
	}
	// Ordering votes only refine the display order of concurrent events; a
	// deployment without them (older Governance) still verifies everything else.
	orders, err := n.reader.OrderProposals(ctx, n.cfg.Room)
	if err != nil {
		log.Println("order proposals unavailable:", err)
		orders = nil
	}
	n.setChainErr(nil)
	in.Checkpoints, in.Redactions, in.Orders = cps, reds, orders
	res, err := n.verifier.Run(in)
	if err != nil {
		return err
	}
	n.stateMu.Lock()
	n.result = res
	n.checkpoints = cps
	n.stateMu.Unlock()
	n.notify("state", map[string]any{"head": res.Head, "health": res.Health, "alerts": res.Alerts})
	return nil
}

func (n *Node) setChainErr(err error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if err == nil {
		n.chainErr = ""
		return
	}
	n.chainErr = err.Error()
}

// Result returns the latest verification result (nil before the first round).
func (n *Node) Result() *verify.Result {
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	return n.result
}

// Status summarises the node for the UI.
type Status struct {
	Room       common.Hash         `json:"room"`
	Wallet     common.Address      `json:"wallet"`
	ChainID    int64               `json:"chainId"`
	RPC        string              `json:"rpc"`
	Contracts  Contracts           `json:"contracts"`
	IsAnchor   bool                `json:"isAnchorNode"`
	Health     string              `json:"health"`
	ChainError string              `json:"chainError,omitempty"`
	Sync       gitstore.SyncReport `json:"sync"`
	Quarantine []verify.Rejection  `json:"quarantine,omitempty"`
	Alerts     []verify.Alert      `json:"alerts"`
	Latest     *chain.Checkpoint   `json:"latestCheckpoint,omitempty"`
	Pending    *Draft              `json:"pendingDraft,omitempty"`
	DevSigner  bool                `json:"devSigner"`
	Delegation *ActiveDelegation   `json:"delegation,omitempty"`
	Members    int                 `json:"members"`
	SyncPaused bool                `json:"syncPaused"`
}

// Status returns the current node status.
func (n *Node) Status() Status {
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	s := Status{Room: n.cfg.Room, Wallet: n.cfg.Wallet, ChainID: n.cfg.ChainID, RPC: n.cfg.RPC, Contracts: n.cfg.Contracts,
		IsAnchor: n.anchor != nil, Health: verify.RoomHealthy, ChainError: n.chainErr, Sync: n.lastSync,
		Quarantine: n.quarantine, Alerts: []verify.Alert{}}
	if n.result != nil {
		s.Health, s.Alerts, s.Latest = n.result.Health, n.result.Alerts, n.result.Latest
	}
	if n.chainErr != "" {
		s.Health = verify.RoomRPCUnavailable
	}
	s.Pending = n.draft
	s.DevSigner = n.DevSigner
	s.SyncPaused = n.syncPaused.Load()
	if n.roomInfo != nil {
		s.Members = len(n.roomInfo.Members)
	}
	if d := n.activeDelegation(); d != nil {
		s.Delegation = &ActiveDelegation{Delegate: d.Delegation.Delegate, ValidUntil: d.Delegation.ValidUntil, ID: d.Delegation.ID(n.domain)}
	}
	return s
}

// nextPosition returns seq and authorPrev for the node wallet from the
// committed tree. Paths carry seq and event id, so no file reads are needed.
func (n *Node) nextPosition(head string) (uint64, common.Hash, error) {
	entries, err := n.repo.LsTree(head, "events/"+strings.ToLower(n.cfg.Wallet.Hex()))
	if err != nil {
		return 0, common.Hash{}, err
	}
	var best int64 = -1
	var prev common.Hash
	for _, e := range entries {
		parts := strings.Split(e.Path, "/")
		if len(parts) != 4 {
			continue
		}
		seq, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			continue
		}
		if seq > best {
			best = seq
			prev = common.HexToHash(strings.TrimSuffix(parts[3], ".json"))
		}
	}
	return uint64(best + 1), prev, nil
}

func newDraftID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (n *Node) prepare(kind uint8, target, data1 common.Hash, blob *protocol.Blob) (*Draft, error) {
	head, err := n.repo.Head()
	if err != nil {
		return nil, err
	}
	seq, prev, err := n.nextPosition(head)
	if err != nil {
		return nil, err
	}
	seen, err := protocol.SeenFromCommit(head)
	if err != nil {
		return nil, err
	}
	e := protocol.Event{Room: n.cfg.Room, Author: n.cfg.Wallet, Seq: seq, AuthorPrev: prev, Seen: seen,
		Ts: uint64(time.Now().Unix()), Kind: kind, Target: target, Data1: data1}
	if err := e.CheckShape(n.cfg.Room); err != nil {
		return nil, err
	}
	d := &Draft{ID: newDraftID(), EventID: e.ID(n.domain), Event: e, TypedData: e.TypedData(n.domain), blob: blob, created: time.Now()}
	n.draft = d // one pending draft per author; a new draft releases the old one
	return d, nil
}

// PrepareMessage builds the typed data for a new message.
func (n *Node) PrepareMessage(body string) (*Draft, error) {
	blob, err := protocol.NewBlob(body)
	if err != nil {
		return nil, err
	}
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	return n.prepare(protocol.KindMessage, common.Hash{}, blob.Commit(), &blob)
}

// PrepareTombstone builds the tombstone for an executed redaction proposal.
func (n *Node) PrepareTombstone(ctx context.Context, proposalID uint64) (*Draft, error) {
	p, err := n.reader.Proposal(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if p.Room != n.cfg.Room || !p.Executed {
		return nil, errors.New("proposal is not an executed redaction of this room")
	}
	if res := n.Result(); res != nil {
		if _, ok := res.Get(p.Target); !ok {
			return nil, errors.New("redaction target is not a known event")
		}
		// A target has at most one executed proposal, so one valid tombstone for
		// it is enough; a second would add nothing but noise.
		for _, m := range res.Messages {
			if m.Kind == protocol.KindTombstone && m.Evidence != verify.EvidenceInvalid &&
				m.Target != nil && *m.Target == p.Target {
				return nil, fmt.Errorf("proposal %d already has a tombstone (signed by %s)", p.ID, m.Author.Hex())
			}
		}
	}
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	return n.prepare(protocol.KindTombstone, p.Target, protocol.ProposalIDWord(p.ID), nil)
}

// CancelDraft releases the pending draft without consuming a sequence number.
func (n *Node) CancelDraft(id string) bool {
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	if n.draft == nil || n.draft.ID != id {
		return false
	}
	n.draft = nil
	return true
}

type outboxRecord struct {
	Signed protocol.SignedEvent `json:"signed"`
	Blob   *protocol.Blob       `json:"blob,omitempty"`
}

// CommitSigned stores a wallet-signed draft: outbox first, then the body on
// the content branch, then the event on main. Pushing is left to the sync
// loop so a temporary network failure never loses the event.
func (n *Node) CommitSigned(draftID string, sig []byte) (common.Hash, error) {
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	d := n.draft
	if d == nil || d.ID != draftID {
		return common.Hash{}, errors.New("unknown or expired draft")
	}
	norm, err := protocol.NormalizeSig(sig)
	if err != nil {
		return common.Hash{}, err
	}
	se, err := n.attachSignature(d.Event, norm)
	if err != nil {
		return common.Hash{}, err
	}
	if _, err := se.Verify(n.domain); err != nil {
		return common.Hash{}, fmt.Errorf("signature: %w", err)
	}
	head, err := n.repo.Head()
	if err != nil {
		return common.Hash{}, err
	}
	if seq, _, _ := n.nextPosition(head); seq != d.Event.Seq {
		n.draft = nil
		return common.Hash{}, errors.New("draft is stale; prepare a new one")
	}
	rec := outboxRecord{Signed: se, Blob: d.blob}
	raw, _ := json.Marshal(rec)
	ob := filepath.Join(n.stateDir("outbox"), d.EventID.Hex()+".json")
	if err := os.WriteFile(ob, raw, 0o600); err != nil {
		return common.Hash{}, err
	}
	if err := n.persist(rec); err != nil {
		return common.Hash{}, err
	}
	_ = os.Remove(ob)
	n.draft = nil
	n.notify("message", map[string]any{"eventId": d.EventID})
	return d.EventID, nil
}

func (n *Node) persist(rec outboxRecord) error {
	id := rec.Signed.Event.ID(n.domain)
	if rec.Blob != nil {
		b, _ := json.Marshal(rec.Blob)
		if _, err := n.repo.ReadBlob(id.Hex()); err != nil {
			if err := n.repo.AddBlob(id.Hex(), b); err != nil {
				return err
			}
		}
	}
	data, _ := json.MarshalIndent(rec.Signed, "", "  ")
	path := gitstore.EventPath(rec.Signed.Event.Author.Hex(), rec.Signed.Event.Seq, id.Hex())
	_, err := n.repo.CommitEvent(path, data, id.Hex())
	return err
}

// recoverOutbox finishes events that were signed but not committed before a
// crash. Event ids are idempotent: an already committed event is skipped.
func (n *Node) recoverOutbox() error {
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	files, _ := filepath.Glob(filepath.Join(n.stateDir("outbox"), "*.json"))
	head, err := n.repo.Head()
	if err != nil {
		return err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec outboxRecord
		if json.Unmarshal(raw, &rec) != nil {
			continue
		}
		id := rec.Signed.Event.ID(n.domain)
		path := gitstore.EventPath(rec.Signed.Event.Author.Hex(), rec.Signed.Event.Seq, id.Hex())
		if _, err := n.repo.ReadFile(head, path); err != nil {
			if err := n.persist(rec); err != nil {
				return err
			}
		}
		_ = os.Remove(f)
	}
	return nil
}
