package node

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/gitstore"
	"github.com/gitchain/gitchain/internal/protocol"
	"github.com/gitchain/gitchain/internal/verify"
)

// applyRedactions hides and removes, from the content branch this node
// manages, every body whose deletion is authorised on chain. Only the
// current content snapshot is rewritten; older Git objects stay until a
// controlled maintenance window (docs/protocol.md §6.5).
func (n *Node) applyRedactions(ctx context.Context) error {
	res := n.Result()
	if res == nil {
		return nil
	}
	var targets []string
	for _, m := range res.Messages {
		if m.Redaction == verify.RedactionNone || m.Kind != protocol.KindMessage {
			continue
		}
		if _, err := n.repo.ReadBlob(m.EventID.Hex()); err == nil {
			targets = append(targets, strings.ToLower(m.EventID.Hex()))
		}
	}
	if len(targets) == 0 {
		return nil
	}
	n.writeMu.Lock()
	defer n.writeMu.Unlock()
	head, err := n.repo.ContentHead()
	if err != nil {
		return err
	}
	man, err := n.repo.ReadManifest(head)
	if err != nil {
		return err
	}
	blobs, err := n.repo.AllBlobs(head)
	if err != nil {
		return err
	}
	deleted := append(append([]string(nil), man.Deleted...), targets...)
	if err := n.repo.RebuildContent(blobs, deleted, man.Generation+1, []string{head}); err != nil {
		return err
	}
	n.notify("redaction", map[string]any{"purged": targets})
	return nil
}

// Reasons AnchorNowReport gives for not submitting a checkpoint.
const (
	AnchorNothingNew = "nothing_new" // every accepted event is already covered
	AnchorUnpushed   = "unpushed"    // waiting until other members can fetch the head
	AnchorPending    = "pending"     // our last checkpoint is not confirmed yet
)

// AnchorOutcome says what one anchoring round did.
type AnchorOutcome struct {
	Skipped string      `json:"skipped"` // "" when a checkpoint was submitted
	Index   uint64      `json:"index"`   // submitted (or still pending) checkpoint index
	Count   uint32      `json:"count"`
	Tx      common.Hash `json:"tx"`
}

// pendingAnchor is a checkpoint this node submitted that the confirmed chain
// view does not show yet. Until it does, the node must not build the next one
// on the stale view (it would cite the wrong prevIndex and revert).
type pendingAnchor struct {
	index uint64
	count uint32
	tx    common.Hash
}

// AnchorNow anchors immediately if this is the anchor node (demo entry).
func (n *Node) AnchorNow(ctx context.Context) error {
	_, err := n.anchorRound(ctx, true)
	return err
}

// AnchorNowReport is AnchorNow that also reports the checkpoint submitted or why none was.
func (n *Node) AnchorNowReport(ctx context.Context) (AnchorOutcome, error) {
	return n.anchorRound(ctx, true)
}

func (n *Node) maybeAnchor(ctx context.Context, force bool) error {
	_, err := n.anchorRound(ctx, force)
	return err
}

func (n *Node) anchorRound(ctx context.Context, force bool) (out AnchorOutcome, err error) {
	skip := func(why string) (AnchorOutcome, error) { return AnchorOutcome{Skipped: why}, nil }
	fail := func(e error) (AnchorOutcome, error) { return AnchorOutcome{}, e }
	n.anchorMu.Lock()
	defer n.anchorMu.Unlock()
	if n.anchor == nil {
		if force {
			return fail(errors.New("this node is not configured as the anchor node"))
		}
		return skip("")
	}
	info, err := n.room(ctx)
	if err != nil {
		return fail(err)
	}
	if info.AnchorWriter != n.anchor.Address() {
		return fail(errors.New("anchor key is not the room's anchor writer"))
	}
	res := n.Result()
	if res == nil {
		return skip(AnchorNothingNew)
	}
	n.stateMu.Lock()
	quarantined := len(n.quarantine) > 0
	last := n.lastAnchor
	if p := n.pending; p != nil && res.Latest != nil && res.Latest.Index >= p.index {
		n.pending = nil // the confirmed view has caught up
	}
	pending := n.pending
	n.stateMu.Unlock()
	if pending != nil {
		return AnchorOutcome{Skipped: AnchorPending, Index: pending.index, Count: pending.count, Tx: pending.tx}, nil
	}
	if quarantined || res.Health != verify.RoomHealthy {
		return fail(errors.New("room is not healthy; refusing to anchor"))
	}
	for _, m := range res.Messages {
		if m.Evidence == verify.EvidenceInvalid && !m.DelegationExpired {
			return fail(fmt.Errorf("head contains invalid event %s; refusing to anchor", m.EventID.Hex()))
		}
	}
	if n.repo.Unpushed() > 0 {
		return skip(AnchorUnpushed) // anchor only what other members can fetch
	}
	acc := res.Accepted()
	var prevCount uint32
	prev, next := uint64(chain.GenesisPrev), uint64(0)
	if res.Latest != nil {
		prevCount, prev, next = res.Latest.Count, res.Latest.Index, res.Latest.Index+1
	}
	newEvents := len(acc) - int(prevCount)
	if newEvents <= 0 {
		return skip(AnchorNothingNew)
	}
	due := force || newEvents >= n.cfg.AnchorN || time.Since(last) >= n.cfg.AnchorEvery
	if !due {
		return skip("")
	}
	tree, _, err := protocol.SnapshotTree(acc)
	if err != nil {
		return fail(err)
	}
	commit, err := protocol.SeenFromCommit(res.Head)
	if err != nil {
		return fail(err)
	}
	tx, err := n.anchor.Anchor(ctx, n.cfg.Room, prev, commit, tree.Root(), uint32(tree.Count()))
	if err != nil {
		return fail(err)
	}
	out = AnchorOutcome{Index: next, Count: uint32(tree.Count()), Tx: tx}
	n.stateMu.Lock()
	n.lastAnchor = time.Now()
	n.pending = &pendingAnchor{index: next, count: out.Count, tx: tx}
	n.stateMu.Unlock()
	n.notify("anchor", map[string]any{"tx": tx, "count": tree.Count(), "root": tree.Root()})
	return out, n.Refresh(ctx)
}

// Inclusion proves that an event is a leaf of a checkpoint.
type Inclusion struct {
	CheckpointIndex uint64        `json:"checkpointIndex"`
	Root            common.Hash   `json:"root"`
	Count           uint32        `json:"count"`
	LeafIndex       int           `json:"leafIndex"`
	Proof           []common.Hash `json:"proof"`
}

// RedactionProof carries the tombstone of a deleted message.
type RedactionProof struct {
	ProposalID         uint64               `json:"proposalId"`
	Tombstone          protocol.SignedEvent `json:"tombstone"`
	TombstoneInclusion *Inclusion           `json:"tombstoneInclusion"`
}

// ProofBundle is the self-contained evidence for one event
// (docs/protocol.md §6.6). It references a deployment; it never overrides
// the verifier's trusted configuration.
type ProofBundle struct {
	Version   int                  `json:"version"`
	ChainID   int64                `json:"chainId"`
	Room      common.Hash          `json:"room"`
	Contracts Contracts            `json:"contracts"`
	Event     protocol.SignedEvent `json:"event"`
	EventID   common.Hash          `json:"eventId"`
	Content   *protocol.Blob       `json:"content"`
	Inclusion *Inclusion           `json:"inclusion"`
	Redaction *RedactionProof      `json:"redaction"`
}

func (n *Node) inclusion(ctx context.Context, id common.Hash, cpIndex uint64) (*Inclusion, error) {
	cps, err := n.reader.Checkpoints(ctx, n.cfg.Room)
	if err != nil {
		return nil, err
	}
	if cpIndex >= uint64(len(cps)) {
		return nil, errors.New("checkpoint not found")
	}
	cp := cps[cpIndex]
	commit, _ := protocol.CommitFromSeen(cp.GitCommit)
	in, err := n.baseInput(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := n.verifier.Snapshot(in, commit)
	if err != nil {
		return nil, err
	}
	tree, sorted, err := protocol.SnapshotTree(snap)
	if err != nil {
		return nil, err
	}
	for i, e := range sorted {
		if e.ID == id {
			return &Inclusion{CheckpointIndex: cp.Index, Root: tree.Root(), Count: uint32(tree.Count()), LeafIndex: i, Proof: tree.Proof(i)}, nil
		}
	}
	return nil, errors.New("event not in checkpoint snapshot")
}

// ExportProof builds the proof bundle for eventID. includeContent controls
// whether body and salt are disclosed; deleted messages never include them.
func (n *Node) ExportProof(ctx context.Context, id common.Hash, includeContent bool) (*ProofBundle, error) {
	res := n.Result()
	if res == nil {
		return nil, errors.New("node has not verified the room yet")
	}
	m, ok := res.Get(id)
	if !ok {
		return nil, errors.New("unknown event")
	}
	if m.Evidence != verify.EvidenceAnchored || m.CheckpointIndex == nil {
		return nil, fmt.Errorf("event evidence is %q; only anchored events can be exported", m.Evidence)
	}
	se, err := n.verifier.ReadEvent(n.repo, res.Head, m.Path)
	if err != nil {
		return nil, err
	}
	inc, err := n.inclusion(ctx, id, *m.CheckpointIndex)
	if err != nil {
		return nil, err
	}
	pb := &ProofBundle{Version: 2, ChainID: n.cfg.ChainID, Room: n.cfg.Room, Contracts: n.cfg.Contracts,
		Event: se, EventID: id, Inclusion: inc}
	if m.Redaction == verify.RedactionNone && includeContent && m.Content == verify.ContentAvailable {
		raw, err := n.repo.ReadBlob(id.Hex())
		if err == nil {
			var b protocol.Blob
			if json.Unmarshal(raw, &b) == nil {
				pb.Content = &b
			}
		}
	}
	if m.Redaction == verify.RedactionApplied && m.ProposalID != nil {
		for _, t := range res.Messages {
			if t.Kind == protocol.KindTombstone && t.Target != nil && *t.Target == id && t.Evidence != verify.EvidenceInvalid {
				tse, err := n.verifier.ReadEvent(n.repo, res.Head, t.Path)
				if err != nil {
					return nil, err
				}
				rp := &RedactionProof{ProposalID: *m.ProposalID, Tombstone: tse}
				if t.CheckpointIndex != nil {
					if rp.TombstoneInclusion, err = n.inclusion(ctx, t.EventID, *t.CheckpointIndex); err != nil {
						return nil, err
					}
				}
				pb.Redaction = rp
				break
			}
		}
	}
	return pb, nil
}

// DevSend drafts, signs with a local test key and commits in one call. It
// exists for scripts and stress tests only and must use keys labelled as
// test identities.
func (n *Node) DevSend(body string, key *ecdsa.PrivateKey) (common.Hash, error) {
	d, err := n.PrepareMessage(body)
	if err != nil {
		return common.Hash{}, err
	}
	sig, err := protocol.SignDigest(d.EventID, key)
	if err != nil {
		return common.Hash{}, err
	}
	return n.CommitSigned(d.ID, sig)
}

// DevSignPending signs the pending draft with a local test key.
func (n *Node) DevSignPending(draftID string, key *ecdsa.PrivateKey) (common.Hash, error) {
	n.writeMu.Lock()
	d := n.draft
	n.writeMu.Unlock()
	if d == nil || d.ID != draftID {
		return common.Hash{}, errors.New("unknown or expired draft")
	}
	return n.DevSignDraft(d, key)
}

// DevSignDraft signs an already prepared draft with a local test key.
func (n *Node) DevSignDraft(d *Draft, key *ecdsa.PrivateKey) (common.Hash, error) {
	sig, err := protocol.SignDigest(d.EventID, key)
	if err != nil {
		return common.Hash{}, err
	}
	return n.CommitSigned(d.ID, sig)
}

// Proposals lists the room's deletion proposals with their chain state.
func (n *Node) Proposals(ctx context.Context) ([]chain.Proposal, error) {
	return n.reader.Proposals(ctx, n.cfg.Room)
}

// OrderProposals lists the room's votes on the order of concurrent events.
func (n *Node) OrderProposals(ctx context.Context) ([]chain.OrderProposal, error) {
	return n.reader.OrderProposals(ctx, n.cfg.Room)
}

// GitView describes the local room repository for the UI: where main and
// origin/main point, recent commits, and which commit each checkpoint anchored.
type GitView struct {
	Head              string            `json:"head"`
	RemoteHead        string            `json:"remoteHead"`
	Unpushed          int               `json:"unpushed"`
	SyncPaused        bool              `json:"syncPaused"`
	ContentHead       string            `json:"contentHead"`
	ContentGeneration uint64            `json:"contentGeneration"`
	ContentDeleted    int               `json:"contentDeleted"`
	Commits           []gitstore.Commit `json:"commits"`
	Checkpoints       []GitCheckpoint   `json:"checkpoints"`
}

// GitCheckpoint ties a checkpoint to the Git commit whose snapshot it anchored.
type GitCheckpoint struct {
	Index   uint64 `json:"index"`
	Commit  string `json:"commit"`
	Count   uint32 `json:"count"`
	BlockNo uint64 `json:"blockNo"`
}

// Git returns the repository view with up to n recent main commits.
func (n *Node) Git(limit int) (*GitView, error) {
	head, err := n.repo.Head()
	if err != nil {
		return nil, err
	}
	commits, err := n.repo.Log(limit)
	if err != nil {
		return nil, err
	}
	v := &GitView{Head: head, RemoteHead: n.repo.RemoteHead(), Unpushed: n.repo.Unpushed(),
		SyncPaused: n.syncPaused.Load(), Commits: commits, Checkpoints: []GitCheckpoint{}}
	if ch, err := n.repo.ContentHead(); err == nil {
		v.ContentHead = ch
		if m, err := n.repo.ReadManifest(ch); err == nil {
			v.ContentGeneration, v.ContentDeleted = m.Generation, len(m.Deleted)
		}
	}
	n.stateMu.RLock()
	cps := n.checkpoints
	n.stateMu.RUnlock()
	for _, cp := range cps {
		// gitCommit is the 20-byte SHA-1 left-aligned in 32 bytes.
		v.Checkpoints = append(v.Checkpoints, GitCheckpoint{Index: cp.Index, Commit: strings.ToLower(cp.GitCommit.Hex()[2:42]),
			Count: cp.Count, BlockNo: cp.BlockNo})
	}
	return v, nil
}
