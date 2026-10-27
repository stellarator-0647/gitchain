// Package verify turns the raw repository state plus confirmed chain state
// into per-event verdicts. It never trusts the Git remote: every status is
// derived from signatures, the fixed member set, content commitments,
// checkpoints and executed redactions. The rules are docs/protocol.md §4–§6.
package verify

import (
	"bytes"
	"container/heap"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/gitstore"
	"github.com/gitchain/gitchain/internal/protocol"
)

// Evidence, content and redaction states are reported separately; "anchored"
// says nothing about whether the body is still available.
const (
	EvidencePending  = "pending"
	EvidenceAnchored = "anchored"
	EvidenceInvalid  = "invalid"

	ContentAvailable = "available"
	ContentPending   = "pending"
	ContentMissing   = "missing"
	ContentPurged    = "purged"

	RedactionNone       = "none"
	RedactionAuthorized = "authorized"
	RedactionApplied    = "applied"

	RoomHealthy           = "healthy"
	RoomCheckpointMissing = "checkpoint_missing"
	RoomCheckpointInvalid = "checkpoint_invalid"
	RoomRPCUnavailable    = "rpc_unavailable"
)

// GitView is the read-only repository access the verifier needs.
type GitView interface {
	LsTree(rev, prefix string) ([]gitstore.TreeEntry, error)
	CatBlob(sha string) ([]byte, error)
	HasCommit(sha string) bool
	IsAncestor(ancestor, descendant string) (bool, error)
}

// Input is everything a verification round needs.
type Input struct {
	Domain      protocol.Domain
	Room        common.Hash
	RoomInfo    chain.RoomInfo
	Head        string
	Checkpoints []chain.Checkpoint
	Redactions  []chain.Redaction
	// Orders are the room's ordering votes; settled ones order concurrent pairs.
	Orders []chain.OrderProposal
	// ContentRev is the content branch revision to read bodies from; when
	// empty, Blob is used instead.
	ContentRev string
	// Now is the local clock (Unix seconds); it only decides whether a pending
	// delegated event can still become valid. Anchored ones use block time.
	Now uint64
	// Blob returns the stored body file for an event id, if present.
	Blob func(eventID common.Hash) ([]byte, bool)
	Git  GitView
}

// Message is the verdict for one event, in display order.
type Message struct {
	EventID         common.Hash    `json:"eventId"`
	Author          common.Address `json:"author"`
	Seq             uint64         `json:"seq"`
	Ts              uint64         `json:"ts"`
	Kind            uint8          `json:"kind"`
	Target          *common.Hash   `json:"target,omitempty"`
	Body            *string        `json:"body,omitempty"`
	Evidence        string         `json:"evidence"`
	Content         string         `json:"content"`
	Redaction       string         `json:"redaction"`
	CheckpointIndex *uint64        `json:"checkpointIndex,omitempty"`
	AnchorBlock     *uint64        `json:"anchorBlock,omitempty"`
	ProposalID      *uint64        `json:"proposalId,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	Path            string         `json:"path"`
	// DelegatedUntil is set for events signed by a delegate key.
	DelegatedUntil *uint64 `json:"delegatedUntil,omitempty"`
	// DelegationExpired marks a delegated event that was not anchored before
	// its delegation ended. It is invalid evidence but, unlike structural
	// errors, it does not stop the room from being anchored.
	DelegationExpired bool             `json:"delegationExpired,omitempty"`
	Concurrent        []ConcurrentPeer `json:"concurrent,omitempty"`
}

// ConcurrentPeer is another author's valid event that neither side had seen
// when signing (each is outside the other's seen snapshot), so no signed
// causal order exists between them.
type ConcurrentPeer struct {
	EventID common.Hash `json:"eventId"`
	// Order is "before" or "after" (this message relative to the peer) once
	// the members' vote on chain has settled it; empty while undecided.
	Order string `json:"order,omitempty"`
	// OrderProposal is the latest ordering vote on this pair, open or settled.
	OrderProposal *uint64 `json:"orderProposal,omitempty"`
}

// Alert is a room-level finding.
type Alert struct {
	Level  string `json:"level"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Result of one verification round.
type Result struct {
	Head     string            `json:"head"`
	Room     string            `json:"room"`
	Messages []Message         `json:"messages"`
	Alerts   []Alert           `json:"alerts"`
	Health   string            `json:"health"`
	Latest   *chain.Checkpoint `json:"latestCheckpoint,omitempty"`
	byID     map[common.Hash]int
	accepted []protocol.IndexedEvent
}

// Get returns the verdict for id.
func (r *Result) Get(id common.Hash) (Message, bool) {
	i, ok := r.byID[id]
	if !ok {
		return Message{}, false
	}
	return r.Messages[i], true
}

// Accepted returns the events with valid evidence, i.e. the set an anchor
// snapshot must contain.
func (r *Result) Accepted() []protocol.IndexedEvent { return r.accepted }

// Verifier caches per-commit work across rounds; commits are immutable, so a
// cached answer never goes stale.
type Verifier struct {
	mu        sync.Mutex
	blobCache map[string][]byte
	treeCache map[string][]gitstore.TreeEntry
	commits   map[string]bool
}

func (v *Verifier) hasCommit(g GitView, sha string) bool {
	if v.commits[sha] {
		return true
	}
	ok := g.HasCommit(sha)
	if ok {
		v.commits[sha] = true
	}
	return ok
}

// New returns a verifier with empty caches.
func New() *Verifier {
	return &Verifier{blobCache: map[string][]byte{}, treeCache: map[string][]gitstore.TreeEntry{}, commits: map[string]bool{}}
}

func (v *Verifier) tree(g GitView, rev string) ([]gitstore.TreeEntry, error) {
	if t, ok := v.treeCache[rev]; ok {
		return t, nil
	}
	t, err := g.LsTree(rev, "events")
	if err != nil {
		return nil, err
	}
	if len(rev) == 40 {
		v.treeCache[rev] = t
	}
	return t, nil
}

func (v *Verifier) blob(g GitView, sha string) ([]byte, error) {
	if b, ok := v.blobCache[sha]; ok {
		return b, nil
	}
	b, err := g.CatBlob(sha)
	if err == nil {
		v.blobCache[sha] = b
	}
	return b, err
}

type parsed struct {
	path   string
	id     common.Hash
	se     protocol.SignedEvent
	reason string // non-empty = invalid
}

// parseFile checks one event file: JSON, canonical signature, author
// signature, path, shape and membership.
func parseFile(in *Input, path string, raw []byte) parsed {
	p := parsed{path: path}
	if err := json.Unmarshal(raw, &p.se); err != nil {
		p.reason = "malformed event file"
		return p
	}
	p.id = p.se.Event.ID(in.Domain)
	norm, err := protocol.NormalizeSig(p.se.Sig)
	if err != nil {
		p.reason = "non-canonical signature: " + err.Error()
		return p
	}
	if !bytes.Equal(norm, p.se.Sig) {
		p.reason = "signature not in canonical 27/28 form"
		return p
	}
	if p.se.Delegation != nil {
		dn, err := protocol.NormalizeSig(p.se.Delegation.WalletSig)
		if err != nil || !bytes.Equal(dn, p.se.Delegation.WalletSig) {
			p.reason = "delegation signature not canonical"
			return p
		}
	}
	if _, err := p.se.Verify(in.Domain); err != nil {
		p.reason = err.Error()
		return p
	}
	want := gitstore.EventPath(p.se.Event.Author.Hex(), p.se.Event.Seq, p.id.Hex())
	if path != want {
		p.reason = "event stored at the wrong path"
		return p
	}
	if err := p.se.Event.CheckShape(in.Room); err != nil {
		p.reason = err.Error()
		return p
	}
	if !in.RoomInfo.IsMember(p.se.Event.Author) {
		p.reason = "author is not a room member"
	}
	return p
}

// chainCheck applies the per-author sequence rules to events that passed
// parseFile: seq from 0 without gaps, authorPrev links, and isolation of
// everything from the first equivocation or gap onwards.
func chainCheck(evs []*parsed) (alerts []Alert) {
	byAuthor := map[common.Address][]*parsed{}
	for _, p := range evs {
		if p.reason == "" {
			byAuthor[p.se.Event.Author] = append(byAuthor[p.se.Event.Author], p)
		}
	}
	for author, list := range byAuthor {
		sort.Slice(list, func(i, j int) bool {
			if list[i].se.Event.Seq != list[j].se.Event.Seq {
				return list[i].se.Event.Seq < list[j].se.Event.Seq
			}
			return bytes.Compare(list[i].id[:], list[j].id[:]) < 0
		})
		broken := ""
		var prev common.Hash
		expect := uint64(0)
		for i, p := range list {
			if broken != "" {
				p.reason = broken
				continue
			}
			s := p.se.Event.Seq
			dup := (i+1 < len(list) && list[i+1].se.Event.Seq == s) || (i > 0 && list[i-1].se.Event.Seq == s)
			switch {
			case dup:
				broken = fmt.Sprintf("equivocation: two events with seq %d", s)
				alerts = append(alerts, Alert{"critical", "EQUIVOCATION", fmt.Sprintf("%s signed two events with seq %d", author.Hex(), s)})
			case s != expect:
				broken = fmt.Sprintf("sequence gap: seq %d missing", expect)
			case s > 0 && p.se.Event.AuthorPrev != prev:
				broken = fmt.Sprintf("authorPrev of seq %d does not link to seq %d", s, s-1)
			}
			if broken != "" {
				p.reason = broken
				continue
			}
			prev = p.id
			expect++
		}
	}
	return alerts
}

// Run performs one verification round.
func (v *Verifier) Run(in Input) (*Result, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	res := &Result{Head: in.Head, Room: in.Room.Hex(), Health: RoomHealthy, byID: map[common.Hash]int{},
		Messages: []Message{}, Alerts: []Alert{}}
	headTree, err := v.tree(in.Git, in.Head)
	if err != nil {
		return nil, err
	}
	evs := make([]*parsed, 0, len(headTree))
	for _, te := range headTree {
		raw, err := v.blob(in.Git, te.Blob)
		if err != nil {
			return nil, err
		}
		p := parseFile(&in, te.Path, raw)
		evs = append(evs, &p)
	}
	res.Alerts = append(res.Alerts, chainCheck(evs)...)

	redacted := map[common.Hash]chain.Redaction{}
	for _, r := range in.Redactions {
		redacted[r.Target] = r
	}
	known := map[common.Hash]*parsed{}
	for _, p := range evs {
		if p.reason == "" {
			known[p.id] = p
		}
	}
	// Tombstones count only when they cite an executed proposal for their target.
	applied := map[common.Hash]uint64{}
	for _, p := range evs {
		if p.reason != "" || p.se.Event.Kind != protocol.KindTombstone {
			continue
		}
		pid, ok := protocol.ProposalIDFromWord(p.se.Event.Data1)
		r, auth := redacted[p.se.Event.Target]
		switch {
		case !ok || !auth || r.ProposalID != pid:
			p.reason = "tombstone does not cite an executed proposal for its target"
		case known[p.se.Event.Target] == nil:
			p.reason = "tombstone target is not a known event"
		default:
			applied[p.se.Event.Target] = pid
		}
	}

	anchored := v.checkCheckpoints(&in, res)
	if in.ContentRev != "" {
		entries, err := in.Git.LsTree(in.ContentRev, "blobs")
		if err != nil {
			return nil, err
		}
		shas := map[string]string{}
		for _, te := range entries {
			shas[strings.ToLower(idFromPath(te.Path))] = te.Blob
		}
		in.Blob = func(id common.Hash) ([]byte, bool) {
			sha, ok := shas[strings.ToLower(id.Hex())]
			if !ok {
				return nil, false
			}
			b, err := v.blob(in.Git, sha)
			return b, err == nil
		}
	}

	order, conc := v.order(&in, evs)
	for _, p := range order {
		m := Message{EventID: p.id, Author: p.se.Event.Author, Seq: p.se.Event.Seq, Ts: p.se.Event.Ts,
			Kind: p.se.Event.Kind, Path: p.path, Redaction: RedactionNone, Content: ContentAvailable}
		if p.se.Event.Target != (common.Hash{}) {
			t := p.se.Event.Target
			m.Target = &t
		}
		if p.reason != "" {
			m.Evidence, m.Reason = EvidenceInvalid, p.reason
		} else if cp, ok := anchored[p.id]; ok {
			idx, blk := cp.Index, cp.BlockNo
			m.Evidence, m.CheckpointIndex, m.AnchorBlock = EvidenceAnchored, &idx, &blk
		} else {
			m.Evidence = EvidencePending
		}
		if d := p.se.Delegation; d != nil && p.reason == "" {
			until := d.Delegation.ValidUntil
			m.DelegatedUntil = &until
			cp, isAnchored := anchored[p.id]
			if (isAnchored && (cp.BlockTime < d.Delegation.ValidFrom || cp.BlockTime > until)) || (!isAnchored && in.Now > until) {
				m.Evidence, m.CheckpointIndex, m.AnchorBlock = EvidenceInvalid, nil, nil
				m.DelegationExpired = true
				m.Reason = "checkpoint time is outside the delegation validity window"
			}
		}
		if r, ok := redacted[p.id]; ok {
			pid := r.ProposalID
			m.ProposalID = &pid
			m.Redaction = RedactionAuthorized
			if _, ok := applied[p.id]; ok {
				m.Redaction = RedactionApplied
			}
		}
		if p.se.Event.Kind == protocol.KindMessage {
			v.content(&in, p, &m)
		}
		m.Concurrent = conc[p.id]
		res.byID[p.id] = len(res.Messages)
		res.Messages = append(res.Messages, m)
		if p.reason == "" {
			res.accepted = append(res.accepted, protocol.IndexedEvent{ID: p.id, Signed: p.se, Leaf: p.se.LeafHash(p.id, in.Domain)})
		}
	}
	return res, nil
}

func (v *Verifier) content(in *Input, p *parsed, m *Message) {
	if m.Redaction != RedactionNone {
		// Honest nodes hide the body as soon as deletion is authorised, even
		// before the tombstone or the purge has propagated.
		m.Content = ContentPurged
		return
	}
	raw, ok := in.Blob(p.id)
	if !ok {
		m.Content = ContentMissing
		if m.Reason == "" {
			m.Reason = "body not available and no authorised deletion"
		}
		return
	}
	var b protocol.Blob
	if err := json.Unmarshal(raw, &b); err != nil || b.Commit() != p.se.Event.Data1 {
		m.Content = ContentMissing
		m.Reason = "stored body does not match the signed commitment"
		return
	}
	body := b.Body
	m.Body = &body
}

// checkCheckpoints validates every confirmed checkpoint against local history
// and returns, for each event, the first checkpoint that covers it.
func (v *Verifier) checkCheckpoints(in *Input, res *Result) map[common.Hash]chain.Checkpoint {
	covered := map[common.Hash]chain.Checkpoint{}
	for i, cp := range in.Checkpoints {
		fail := func(health, detail string) {
			if res.Health == RoomHealthy || health == RoomCheckpointInvalid {
				res.Health = health
			}
			res.Alerts = append(res.Alerts, Alert{"critical", strings.ToUpper(health), fmt.Sprintf("checkpoint #%d: %s", cp.Index, detail)})
		}
		if cp.Index != uint64(i) || (i == 0 && cp.PrevIndex != chain.GenesisPrev) || (i > 0 && cp.PrevIndex != uint64(i-1)) {
			fail(RoomCheckpointInvalid, "checkpoint chain is broken")
			continue
		}
		if cp.By != in.RoomInfo.AnchorWriter {
			fail(RoomCheckpointInvalid, "submitted by an address that is not the anchor writer")
			continue
		}
		commit, ok := protocol.CommitFromSeen(cp.GitCommit)
		if !ok || !v.hasCommit(in.Git, commit) {
			fail(RoomCheckpointMissing, "anchored commit is not available locally")
			continue
		}
		if anc, err := in.Git.IsAncestor(commit, in.Head); err != nil || !anc {
			fail(RoomCheckpointMissing, "anchored commit is not part of local history")
			continue
		}
		if i > 0 {
			prev, _ := protocol.CommitFromSeen(in.Checkpoints[i-1].GitCommit)
			if anc, err := in.Git.IsAncestor(prev, commit); err != nil || !anc {
				fail(RoomCheckpointInvalid, "does not extend the previous checkpoint")
				continue
			}
		}
		snap, err := v.snapshot(in, commit)
		if err != nil {
			fail(RoomCheckpointInvalid, err.Error())
			continue
		}
		tree, _, err := protocol.SnapshotTree(snap)
		if err != nil || tree.Root() != cp.Root || uint32(tree.Count()) != cp.Count {
			fail(RoomCheckpointInvalid, "root or count does not match the anchored snapshot")
			continue
		}
		for _, e := range snap {
			if _, ok := covered[e.ID]; !ok {
				covered[e.ID] = cp
			}
		}
		c := cp
		res.Latest = &c
	}
	return covered
}

// snapshot returns the events at commit; every one must be valid, because the
// anchor node only anchors fully verified snapshots.
func (v *Verifier) snapshot(in *Input, commit string) ([]protocol.IndexedEvent, error) {
	entries, err := v.tree(in.Git, commit)
	if err != nil {
		return nil, err
	}
	var ps []*parsed
	for _, te := range entries {
		raw, err := v.blob(in.Git, te.Blob)
		if err != nil {
			return nil, err
		}
		p := parseFile(in, te.Path, raw)
		ps = append(ps, &p)
	}
	chainCheck(ps)
	out := make([]protocol.IndexedEvent, 0, len(ps))
	for _, p := range ps {
		if p.reason != "" {
			return nil, fmt.Errorf("snapshot contains an invalid event (%s)", p.reason)
		}
		out = append(out, protocol.IndexedEvent{ID: p.id, Signed: p.se, Leaf: p.se.LeafHash(p.id, in.Domain)})
	}
	return out, nil
}

// order is a deterministic topological sort. An event depends on its
// authorPrev and on every event present in the commit it declares as seen;
// among ready events the smallest (ts, author, seq, id) goes first.
// order sorts events topologically (authorPrev and seen snapshots, then
// settled ordering votes between concurrent pairs) and reports, per valid
// event, the other authors' valid events it is concurrent with.
func (v *Verifier) order(in *Input, evs []*parsed) ([]*parsed, map[common.Hash][]ConcurrentPeer) {
	idx := map[common.Hash]*parsed{}
	for _, p := range evs {
		idx[p.id] = p
	}
	deps := map[*parsed][]*parsed{}
	indeg := map[*parsed]int{}
	seen := map[*parsed]map[common.Hash]bool{} // ids in the signed seen snapshot
	for _, p := range evs {
		indeg[p] += 0
		seen[p] = map[common.Hash]bool{}
		add := func(d *parsed) {
			if d != nil && d != p {
				deps[d] = append(deps[d], p)
				indeg[p]++
			}
		}
		if p.se.Event.Seq > 0 {
			add(idx[p.se.Event.AuthorPrev])
		}
		if commit, ok := protocol.CommitFromSeen(p.se.Event.Seen); ok && v.hasCommit(in.Git, commit) {
			if entries, err := v.tree(in.Git, commit); err == nil {
				for _, te := range entries {
					id := common.HexToHash(idFromPath(te.Path))
					seen[p][id] = true
					add(idx[id])
				}
			}
		}
	}

	// A seen snapshot is a Git tree, which holds every event merged before
	// it, so "a is in b's snapshot" already covers indirect knowledge.
	conc := map[common.Hash][]ConcurrentPeer{}
	isConc := func(a, b *parsed) bool {
		return a.reason == "" && b.reason == "" && a.se.Event.Author != b.se.Event.Author &&
			!seen[a][b.id] && !seen[b][a.id]
	}
	for i, a := range evs {
		for _, b := range evs[i+1:] {
			if isConc(a, b) {
				conc[a.id] = append(conc[a.id], ConcurrentPeer{EventID: b.id})
				conc[b.id] = append(conc[b.id], ConcurrentPeer{EventID: a.id})
			}
		}
	}
	mark := func(x, y common.Hash, order string, id uint64) {
		for i := range conc[x] {
			if conc[x][i].EventID == y {
				pid := id
				conc[x][i].OrderProposal = &pid
				if order != "" {
					conc[x][i].Order = order
				}
			}
		}
	}
	reaches := func(from, to *parsed) bool { // path in the current graph?
		stack, visited := []*parsed{from}, map[*parsed]bool{from: true}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == to {
				return true
			}
			for _, q := range deps[n] {
				if !visited[q] {
					visited[q] = true
					stack = append(stack, q)
				}
			}
		}
		return false
	}
	// Proposal ids grow with time, so earlier settlements win any conflict;
	// a vote that would contradict the graph (a cycle) is not applied.
	orders := append([]chain.OrderProposal(nil), in.Orders...)
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID < orders[j].ID })
	for _, o := range orders {
		a, b := idx[o.A], idx[o.B]
		if a == nil || b == nil || o.Room != in.Room || !isConc(a, b) {
			continue // votes cannot override the signed causal order
		}
		first, second, settled := o.Before()
		if !settled {
			mark(o.A, o.B, "", o.ID)
			mark(o.B, o.A, "", o.ID)
			continue
		}
		f, s := idx[first], idx[second]
		if reaches(s, f) {
			continue
		}
		deps[f] = append(deps[f], s)
		indeg[s]++
		mark(first, second, "before", o.ID)
		mark(second, first, "after", o.ID)
	}
	h := &readyHeap{}
	for _, p := range evs {
		if indeg[p] == 0 {
			heap.Push(h, p)
		}
	}
	out := make([]*parsed, 0, len(evs))
	for h.Len() > 0 {
		p := heap.Pop(h).(*parsed)
		out = append(out, p)
		for _, q := range deps[p] {
			indeg[q]--
			if indeg[q] == 0 {
				heap.Push(h, q)
			}
		}
	}
	// A cycle can only come from forged seen values; append the rest in key order.
	if len(out) < len(evs) {
		var rest []*parsed
		for _, p := range evs {
			if indeg[p] > 0 {
				rest = append(rest, p)
			}
		}
		sort.Slice(rest, func(i, j int) bool { return less(rest[i], rest[j]) })
		out = append(out, rest...)
	}
	return out, conc
}

func idFromPath(path string) string {
	base := path[strings.LastIndexByte(path, '/')+1:]
	return strings.TrimSuffix(base, ".json")
}

func less(a, b *parsed) bool {
	ea, eb := a.se.Event, b.se.Event
	if ea.Ts != eb.Ts {
		return ea.Ts < eb.Ts
	}
	if c := bytes.Compare(ea.Author[:], eb.Author[:]); c != 0 {
		return c < 0
	}
	if ea.Seq != eb.Seq {
		return ea.Seq < eb.Seq
	}
	return bytes.Compare(a.id[:], b.id[:]) < 0
}

type readyHeap []*parsed

func (h readyHeap) Len() int           { return len(h) }
func (h readyHeap) Less(i, j int) bool { return less(h[i], h[j]) }
func (h readyHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x any)        { *h = append(*h, x.(*parsed)) }
func (h *readyHeap) Pop() any {
	old := *h
	p := old[len(old)-1]
	*h = old[:len(old)-1]
	return p
}

// Rejection names an event file that must not enter the valid main branch.
type Rejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// CheckTrees validates the union of the event trees at the given revisions
// (normally local main and origin/main) before they are merged. Any rejection
// means the merge must not happen and the remote objects are quarantined.
func (v *Verifier) CheckTrees(in Input, revs ...string) ([]Rejection, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	seen := map[string]bool{}
	var ps []*parsed
	for _, rev := range revs {
		entries, err := v.tree(in.Git, rev)
		if err != nil {
			return nil, err
		}
		for _, te := range entries {
			key := te.Path + "@" + te.Blob
			if seen[key] {
				continue
			}
			seen[key] = true
			raw, err := v.blob(in.Git, te.Blob)
			if err != nil {
				return nil, err
			}
			p := parseFile(&in, te.Path, raw)
			ps = append(ps, &p)
		}
	}
	chainCheck(ps)
	var out []Rejection
	for _, p := range ps {
		if p.reason != "" {
			out = append(out, Rejection{Path: p.path, Reason: p.reason})
		}
	}
	return out, nil
}

// Snapshot returns the validated events at commit (see checkpoint rules).
func (v *Verifier) Snapshot(in Input, commit string) ([]protocol.IndexedEvent, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.snapshot(&in, commit)
}

// ReadEvent parses the signed event stored at path in rev.
func (v *Verifier) ReadEvent(g GitView, rev, path string) (protocol.SignedEvent, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var se protocol.SignedEvent
	entries, err := v.tree(g, rev)
	if err != nil {
		return se, err
	}
	for _, te := range entries {
		if te.Path == path {
			raw, err := v.blob(g, te.Blob)
			if err != nil {
				return se, err
			}
			err = json.Unmarshal(raw, &se)
			return se, err
		}
	}
	return se, fmt.Errorf("no event at %s", path)
}
