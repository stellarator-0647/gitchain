package node

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func position(n *Node, id common.Hash) int {
	for i, m := range n.Result().Messages {
		if m.EventID == id {
			return i
		}
	}
	return -1
}

// Two members send without having seen each other's message: every node marks
// the pair concurrent, a settled on-chain vote fixes their order on every
// node, and a vote cannot override an order the signatures already prove.
func TestConcurrentOrderVote(t *testing.T) {
	w := newWorld(t)
	first := w.send(0, "甲方：先发的通知")
	w.tick()
	b := w.send(1, "乙方：确认签署")
	c := w.send(2, "监理：同时要求撤销") // neither node has synced yet
	w.tick()
	w.assertConverged(3)

	for i, m := range w.m {
		res := m.node.Result()
		mb, _ := res.Get(b)
		if len(mb.Concurrent) != 1 || mb.Concurrent[0].EventID != c || mb.Concurrent[0].Order != "" {
			t.Fatalf("node%d: concurrent of b = %+v", i, mb.Concurrent)
		}
		if mf, _ := res.Get(first); len(mf.Concurrent) != 0 {
			t.Fatalf("node%d: causally earlier message marked concurrent: %+v", i, mf.Concurrent)
		}
	}

	// Vote for the opposite of the default tie-break, so the result is visible.
	n0 := w.m[0].node
	early, late := b, c
	if position(n0, c) < position(n0, b) {
		early, late = c, b
	}
	id, err := w.chain.ProposeOrder(w.m[0].addr, w.room, early, late)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.chain.ProposeOrder(w.m[1].addr, w.room, late, early); err == nil {
		t.Fatal("second open proposal on the same pair accepted")
	}
	w.tick()
	if mb, _ := n0.Result().Get(b); mb.Concurrent[0].OrderProposal == nil || *mb.Concurrent[0].OrderProposal != id || mb.Concurrent[0].Order != "" {
		t.Fatalf("open vote not reported: %+v", mb.Concurrent)
	}
	for _, voter := range []int{0, 2} {
		if err := w.chain.VoteOrder(w.m[voter].addr, id, false); err != nil { // false = "late" first
			t.Fatal(err)
		}
	}
	w.tick()
	w.assertConverged(3)
	for i, m := range w.m {
		if position(m.node, late) > position(m.node, early) {
			t.Fatalf("node%d: settled order not applied", i)
		}
		ml, _ := m.node.Result().Get(late)
		if ml.Concurrent[0].Order != "before" {
			t.Fatalf("node%d: late event order = %q", i, ml.Concurrent[0].Order)
		}
	}

	// A vote on a causally ordered pair is recorded on chain but ignored.
	id2, err := w.chain.ProposeOrder(w.m[1].addr, w.room, first, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, voter := range []int{1, 2} {
		if err := w.chain.VoteOrder(w.m[voter].addr, id2, false); err != nil { // claims b before first
			t.Fatal(err)
		}
	}
	w.tick()
	if position(n0, b) < position(n0, first) {
		t.Fatal("vote overrode the signed causal order")
	}
}
