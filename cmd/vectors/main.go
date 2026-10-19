// Command vectors writes testdata/vectors.json (also runnable as
// `GITCHAIN_WRITE_VECTORS=1 go test ./cmd/vectors`): byte-exact expectations that
// the Go node and the TypeScript verifier must both reproduce. Keys are
// Anvil's public TEST keys; ECDSA signing is deterministic (RFC 6979), so the
// output is stable across runs.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gitchain/gitchain/internal/protocol"
)

var keys = []string{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
}

type eventVector struct {
	Name          string               `json:"name"`
	Signed        protocol.SignedEvent `json:"signed"`
	Blob          *protocol.Blob       `json:"blob,omitempty"`
	StructHash    common.Hash          `json:"structHash"`
	EventID       common.Hash          `json:"eventId"`
	Signer        common.Address       `json:"signer"`
	ContentCommit *common.Hash         `json:"contentCommit,omitempty"`
	Leaf          common.Hash          `json:"leaf"`
	DelegationID  *common.Hash         `json:"delegationId,omitempty"`
}

type treeVector struct {
	Leaves []common.Hash   `json:"leaves"`
	Root   common.Hash     `json:"root"`
	Proofs [][]common.Hash `json:"proofs"`
}

type negative struct {
	Name   string               `json:"name"`
	Signed protocol.SignedEvent `json:"signed"`
	Expect string               `json:"expect"`
}

func main() {
	out := "testdata/vectors.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	write(out)
}

// write generates the vectors into out.
func write(out string) {
	domain := protocol.NewDomain(31337, common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa3"))
	room := crypto.Keccak256Hash([]byte("gitchain-room:demo"))
	seen, _ := protocol.SeenFromCommit("0123456789abcdef0123456789abcdef01234567")

	var evs []eventVector
	prev := map[common.Address]common.Hash{}
	seq := map[common.Address]uint64{}
	add := func(name string, ki int, e protocol.Event, blob *protocol.Blob) {
		key, _ := crypto.HexToECDSA(keys[ki])
		addr := crypto.PubkeyToAddress(key.PublicKey)
		e.Room, e.Author, e.Seen, e.Seq, e.AuthorPrev = room, addr, seen, seq[addr], prev[addr]
		if blob != nil {
			e.Data1 = blob.Commit()
		}
		id := e.ID(domain)
		sig, err := protocol.SignDigest(id, key)
		if err != nil {
			log.Fatal(err)
		}
		v := eventVector{Name: name, Signed: protocol.SignedEvent{Event: e, Sig: sig}, Blob: blob,
			StructHash: e.StructHash(), EventID: id, Signer: addr, Leaf: protocol.Leaf(id, sig)}
		if blob != nil {
			c := blob.Commit()
			v.ContentCommit = &c
		}
		evs = append(evs, v)
		prev[addr], seq[addr] = id, seq[addr]+1
	}
	salt := func(b byte) common.Hash { return common.BytesToHash([]byte{b}) }
	add("ascii message", 0, protocol.Event{Ts: 1760000000, Kind: protocol.KindMessage}, &protocol.Blob{Salt: salt(1), Body: "drawings sent today"})
	add("unicode message", 1, protocol.Event{Ts: 1760000060, Kind: protocol.KindMessage}, &protocol.Blob{Salt: salt(2), Body: "乙方：收到，周三开工 ✅"})
	add("second message same author", 0, protocol.Event{Ts: 1760000120, Kind: protocol.KindMessage}, &protocol.Blob{Salt: salt(3), Body: "监理周五到场"})
	add("tombstone", 2, protocol.Event{Ts: 1760000180, Kind: protocol.KindTombstone, Target: evs[0].EventID, Data1: protocol.ProposalIDWord(1)}, nil)
	add("receipt", 1, protocol.Event{Ts: 1760000240, Kind: protocol.KindReceipt, Target: evs[2].EventID, Data1: *evs[2].ContentCommit}, nil)

	// A message by wallet 1 signed by a delegate key (key 2) under a one-hour delegation.
	{
		walletKey, _ := crypto.HexToECDSA(keys[1])
		tempKey, _ := crypto.HexToECDSA(keys[2])
		wallet := crypto.PubkeyToAddress(walletKey.PublicKey)
		del := protocol.Delegation{Room: room, Wallet: wallet, Delegate: crypto.PubkeyToAddress(tempKey.PublicKey),
			ValidFrom: 1760000000, ValidUntil: 1760003600, Kinds: protocol.DelegableKinds}
		did := del.ID(domain)
		wsig, _ := protocol.SignDigest(did, walletKey)
		blob := &protocol.Blob{Salt: salt(4), Body: "临时密钥代签：材料已到场"}
		e := protocol.Event{Room: room, Author: wallet, Seq: seq[wallet], AuthorPrev: prev[wallet], Seen: seen,
			Ts: 1760000300, Kind: protocol.KindMessage, Data1: blob.Commit()}
		id := e.ID(domain)
		sig, _ := protocol.SignDigest(id, tempKey)
		se := protocol.SignedEvent{Event: e, Sig: sig, Delegation: &protocol.SignedDelegation{Delegation: del, WalletSig: wsig}}
		c := blob.Commit()
		evs = append(evs, eventVector{Name: "delegated message", Signed: se, Blob: blob, StructHash: e.StructHash(), EventID: id,
			Signer: del.Delegate, ContentCommit: &c, Leaf: se.LeafHash(id, domain), DelegationID: &did})
	}

	trees := map[string]treeVector{}
	for _, n := range []int{1, 2, 3, 5, 6} {
		var idx []protocol.IndexedEvent
		for _, v := range evs[:n] {
			idx = append(idx, protocol.Index(v.Signed, domain))
		}
		t, sorted, err := protocol.SnapshotTree(idx)
		if err != nil {
			log.Fatal(err)
		}
		tv := treeVector{Root: t.Root()}
		for i, e := range sorted {
			tv.Leaves = append(tv.Leaves, e.Leaf)
			tv.Proofs = append(tv.Proofs, t.Proof(i))
		}
		trees[fmt.Sprint(n)] = tv
	}

	wrongChain := evs[0].Signed
	tampered := evs[1].Signed
	tampered.Event.Ts++
	highS := evs[0].Signed
	highS.Sig = append([]byte(nil), highS.Sig...)
	s := new(big.Int).SetBytes(highS.Sig[32:64])
	copy(highS.Sig[32:64], common.LeftPadBytes(new(big.Int).Sub(crypto.S256().Params().N, s).Bytes(), 32))
	highS.Sig[64] = 55 - highS.Sig[64] // 27 <-> 28: the high-s twin has the other recovery id
	negatives := []negative{
		{"field changed after signing", tampered, "signer mismatch"},
		{"high-s signature", highS, "non-canonical signature"},
	}

	doc := map[string]any{
		"domain": map[string]any{"name": domain.Name, "version": domain.Version, "chainId": 31337,
			"verifyingContract": domain.VerifyingContract, "separator": domain.Separator()},
		"room":                    room,
		"events":                  evs,
		"trees":                   trees,
		"negatives":               negatives,
		"wrongChainId":            1,
		"wrongChainEvent":         wrongChain,
		"wrongChainExpectedError": "signer mismatch",
	}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", out)
}
