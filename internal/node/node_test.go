package node

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/gitstore"
	"github.com/gitchain/gitchain/internal/protocol"
	"github.com/gitchain/gitchain/internal/verify"
)

// Anvil's first three well-known test keys. Test identities only.
var testKeys = []string{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
}

type member struct {
	key  *ecdsa.PrivateKey
	addr common.Address
	node *Node
	repo *gitstore.Repo
}

type world struct {
	t      *testing.T
	ctx    context.Context
	chain  *chain.Memory
	room   common.Hash
	remote string
	m      []*member
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWorld(t *testing.T) *world {
	t.Helper()
	tmp := t.TempDir()
	w := &world{t: t, ctx: context.Background(), chain: chain.NewMemory(),
		room: crypto.Keccak256Hash([]byte("test-room")), remote: filepath.Join(tmp, "remote.git")}
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", w.remote).CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v %s", err, out)
	}
	var addrs []common.Address
	for _, k := range testKeys {
		key, _ := crypto.HexToECDSA(k)
		m := &member{key: key, addr: crypto.PubkeyToAddress(key.PublicKey)}
		w.m = append(w.m, m)
		addrs = append(addrs, m.addr)
	}
	if err := w.chain.CreateRoom(w.room, addrs[0], addrs, addrs[0]); err != nil {
		t.Fatal(err)
	}
	contracts := Contracts{Registry: common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa3")}
	for i, m := range w.m {
		dir := filepath.Join(tmp, fmt.Sprintf("node%d", i))
		var err error
		if i == 0 {
			meta, _ := json.Marshal(map[string]any{"room": w.room, "chainId": 31337})
			m.repo, err = gitstore.Init(filepath.Join(dir, "repo"), meta)
			if err == nil {
				err = m.repo.AddRemote(w.remote)
			}
			if err == nil {
				err = m.repo.Push()
			}
		} else {
			m.repo, err = gitstore.Clone(w.remote, filepath.Join(dir, "repo"))
		}
		if err != nil {
			t.Fatal(err)
		}
		var anchor chain.AnchorWriter
		if i == 0 {
			anchor = chain.MemoryAnchor{Chain: w.chain, Sender: m.addr}
		}
		cfg := Config{DataDir: dir, Room: w.room, Wallet: m.addr, ChainID: 31337, Contracts: contracts, AnchorN: 1000}
		if m.node, err = New(cfg, m.repo, w.chain, anchor, nil); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *world) send(i int, body string) common.Hash {
	w.t.Helper()
	id, err := w.m[i].node.DevSend(body, w.m[i].key)
	if err != nil {
		w.t.Fatalf("member %d send: %v", i, err)
	}
	return id
}

func (w *world) tick(idx ...int) {
	if len(idx) == 0 {
		idx = []int{0, 1, 2}
	}
	for round := 0; round < 2; round++ {
		for _, i := range idx {
			w.m[i].node.Tick(w.ctx)
		}
	}
}

func order(n *Node) []common.Hash {
	var out []common.Hash
	for _, m := range n.Result().Messages {
		out = append(out, m.EventID)
	}
	return out
}

func (w *world) assertConverged(want int) {
	w.t.Helper()
	base := order(w.m[0].node)
	if len(base) != want {
		w.t.Fatalf("node0 sees %d events, want %d", len(base), want)
	}
	for i, m := range w.m[1:] {
		got := order(m.node)
		if fmt.Sprint(got) != fmt.Sprint(base) {
			w.t.Fatalf("node%d order differs:\n%v\n%v", i+1, got, base)
		}
	}
	for i, m := range w.m {
		for _, msg := range m.node.Result().Messages {
			if msg.Evidence == verify.EvidenceInvalid {
				w.t.Fatalf("node%d: %s invalid: %s", i, msg.EventID.Hex(), msg.Reason)
			}
		}
	}
}

func TestEndToEnd(t *testing.T) {
	w := newWorld(t)

	// Three members talk; every node converges on the same order.
	a1 := w.send(0, "甲方：图纸今天发")
	w.tick()
	w.send(1, "乙方：收到")
	w.send(2, "监理：周五验收")
	w.tick()
	w.assertConverged(3)

	// Anchoring makes every event anchored on every node.
	if err := w.m[0].node.AnchorNow(w.ctx); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	w.tick()
	for i, m := range w.m {
		msg, _ := m.node.Result().Get(a1)
		if msg.Evidence != verify.EvidenceAnchored {
			t.Fatalf("node%d: evidence %s", i, msg.Evidence)
		}
	}

	// A proof exported by another member verifies against the chain root.
	pb, err := w.m[1].node.ExportProof(w.ctx, a1, true)
	if err != nil {
		t.Fatal(err)
	}
	cps, _ := w.chain.Checkpoints(w.ctx, w.room)
	leaf := protocol.Leaf(pb.EventID, pb.Event.Sig)
	if !protocol.VerifyProof(leaf, pb.Inclusion.Proof, cps[pb.Inclusion.CheckpointIndex].Root) {
		t.Fatal("exported inclusion proof does not verify")
	}
	if pb.Content == nil || pb.Content.Commit() != pb.Event.Event.Data1 {
		t.Fatal("exported content does not match its commitment")
	}

	// A member who is not the anchor writer cannot anchor.
	bad := chain.MemoryAnchor{Chain: w.chain, Sender: w.m[1].addr}
	if _, err := bad.Anchor(w.ctx, w.room, 0, common.HexToHash("0x01"), common.HexToHash("0x02"), 99); err == nil {
		t.Fatal("non-writer anchored")
	}

	t.Run("authorised deletion", func(t *testing.T) {
		pid, err := w.chain.ProposeRedaction(w.m[0].addr, w.room, a1, common.Hash{})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.chain.Execute(pid); err == nil {
			t.Fatal("executed without votes")
		}
		_ = w.chain.Vote(w.m[0].addr, pid, true)
		_ = w.chain.Vote(w.m[1].addr, pid, true)
		if err := w.chain.Execute(pid); err != nil {
			t.Fatal(err)
		}
		w.tick()
		for i, m := range w.m {
			msg, _ := m.node.Result().Get(a1)
			if msg.Redaction != verify.RedactionAuthorized || msg.Content != verify.ContentPurged || msg.Body != nil {
				t.Fatalf("node%d: redaction=%s content=%s", i, msg.Redaction, msg.Content)
			}
			if _, err := m.repo.ReadBlob(a1.Hex()); err == nil {
				t.Fatalf("node%d still serves the deleted body", i)
			}
		}
		d, err := w.m[1].node.PrepareTombstone(w.ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.m[1].node.DevSignDraft(d, w.m[1].key); err != nil {
			t.Fatal(err)
		}
		w.tick()
		if err := w.m[0].node.AnchorNow(w.ctx); err != nil {
			t.Fatal(err)
		}
		w.tick()
		msg, _ := w.m[2].node.Result().Get(a1)
		if msg.Redaction != verify.RedactionApplied {
			t.Fatalf("tombstone not applied: %s", msg.Redaction)
		}
		pb, err := w.m[2].node.ExportProof(w.ctx, a1, true)
		if err != nil {
			t.Fatal(err)
		}
		if pb.Content != nil || pb.Redaction == nil || pb.Redaction.TombstoneInclusion == nil {
			t.Fatal("deleted message proof must carry the tombstone and no content")
		}
	})

	t.Run("offline partition merge", func(t *testing.T) {
		before := len(order(w.m[0].node))
		for k := 0; k < 5; k++ {
			w.send(2, fmt.Sprintf("离线 %d", k)) // node2 never syncs meanwhile
			w.send(0, fmt.Sprintf("在线 %d", k))
			w.tick(0, 1)
		}
		w.tick()
		w.assertConverged(before + 10)
		if err := w.m[0].node.AnchorNow(w.ctx); err != nil {
			t.Fatal(err)
		}
		w.tick()
		for _, msg := range w.m[1].node.Result().Messages {
			if msg.Evidence != verify.EvidenceAnchored {
				t.Fatalf("%s not anchored after merge", msg.EventID.Hex())
			}
		}
	})

	t.Run("delegated signing", func(t *testing.T) {
		n1 := w.m[1].node
		temp, _ := crypto.GenerateKey()
		dd, err := n1.PrepareDelegation(crypto.PubkeyToAddress(temp.PublicKey), 3600)
		if err != nil {
			t.Fatal(err)
		}
		wsig, _ := protocol.SignDigest(dd.ID, w.m[1].key)
		if _, err := n1.SetDelegation(protocol.SignedDelegation{Delegation: dd.Delegation, WalletSig: wsig}); err != nil {
			t.Fatal(err)
		}
		sendTemp := func(body string) common.Hash {
			d, err := n1.PrepareMessage(body)
			if err != nil {
				t.Fatal(err)
			}
			id, err := n1.DevSignDraft(d, temp)
			if err != nil {
				t.Fatalf("delegated commit: %v", err)
			}
			return id
		}
		w.chain.TimeBase = dd.Delegation.ValidFrom
		ok := sendTemp("临时密钥签的消息")
		w.tick()
		if err := w.m[0].node.AnchorNow(w.ctx); err != nil {
			t.Fatal(err)
		}
		w.tick()
		msg, _ := w.m[2].node.Result().Get(ok)
		if msg.Evidence != verify.EvidenceAnchored || msg.DelegatedUntil == nil {
			t.Fatalf("delegated message: evidence=%s reason=%s", msg.Evidence, msg.Reason)
		}
		pb, err := w.m[2].node.ExportProof(w.ctx, ok, true)
		if err != nil {
			t.Fatal(err)
		}
		cps, _ := w.chain.Checkpoints(w.ctx, w.room)
		if pb.Event.Delegation == nil || !protocol.VerifyProof(pb.Event.LeafHash(ok, n1.Domain()), pb.Inclusion.Proof, cps[pb.Inclusion.CheckpointIndex].Root) {
			t.Fatal("delegated proof does not verify")
		}

		// The delegate cannot sign a tombstone.
		pid, _ := w.chain.ProposeRedaction(w.m[1].addr, w.room, ok, common.Hash{})
		_ = w.chain.Vote(w.m[0].addr, pid, true)
		_ = w.chain.Vote(w.m[1].addr, pid, true)
		_ = w.chain.Execute(pid)
		w.tick()
		td, err := n1.PrepareTombstone(w.ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := n1.DevSignDraft(td, temp); err == nil {
			t.Fatal("delegate key signed a tombstone")
		}
		n1.CancelDraft(td.ID)

		// Anchored only after the delegation ended: invalid, but anchoring goes on.
		late := sendTemp("授权到期后才被锚定")
		w.tick()
		w.chain.TimeBase = dd.Delegation.ValidUntil + 1000
		if err := w.m[0].node.AnchorNow(w.ctx); err != nil {
			t.Fatalf("expired delegated event blocked anchoring: %v", err)
		}
		w.tick()
		msg, _ = w.m[2].node.Result().Get(late)
		if msg.Evidence != verify.EvidenceInvalid || !msg.DelegationExpired {
			t.Fatalf("late delegated message: evidence=%s expired=%v", msg.Evidence, msg.DelegationExpired)
		}
		after := w.send(0, "之后的普通消息照常锚定")
		w.tick()
		if err := w.m[0].node.AnchorNow(w.ctx); err != nil {
			t.Fatal(err)
		}
		w.tick()
		if msg, _ := w.m[1].node.Result().Get(after); msg.Evidence != verify.EvidenceAnchored {
			t.Fatalf("message after expiry: %s %s", msg.Evidence, msg.Reason)
		}
		w.chain.TimeBase = 1_700_000_000
	})

	t.Run("forged event is quarantined", func(t *testing.T) {
		// Member 2 forges an event claiming member 1 as author and pushes it
		// with plain git, bypassing its node.
		res := w.m[2].node.Result()
		last := res.Messages[len(res.Messages)-1]
		e := protocol.Event{Room: w.room, Author: w.m[1].addr, Seq: 99, AuthorPrev: last.EventID,
			Ts: 1, Kind: protocol.KindMessage, Data1: common.HexToHash("0x01")}
		id := e.ID(w.m[2].node.Domain())
		sig, _ := protocol.SignDigest(id, w.m[2].key)
		data, _ := json.Marshal(protocol.SignedEvent{Event: e, Sig: sig})
		path := gitstore.EventPath(e.Author.Hex(), e.Seq, id.Hex())
		dir := w.m[2].repo.Dir
		full := filepath.Join(dir, filepath.FromSlash(path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, data, 0o644)
		git(t, dir, "add", path)
		git(t, dir, "commit", "-q", "-m", "forged")
		git(t, dir, "push", "-q", "origin", "main")

		rep := w.m[0].node.SyncOnce(w.ctx)
		if !strings.Contains(rep.Error, "invalid") || rep.MainMerged || rep.MainForwarded {
			t.Fatalf("forged event was merged: %+v", rep)
		}
		if len(w.m[0].node.Status().Quarantine) == 0 {
			t.Fatal("no quarantine entry")
		}
	})
}
