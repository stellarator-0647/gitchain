package protocol

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

func testKey(t *testing.T, hexKey string) (*SignedEvent, Domain) {
	t.Helper()
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDomain(31337, common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa3"))
	blob := Blob{Salt: common.HexToHash("0x01"), Body: "你好, GitChain"}
	e := Event{
		Room: common.HexToHash("0xaa"), Author: crypto.PubkeyToAddress(key.PublicKey),
		Seq: 0, Seen: mustSeen(t, "0123456789abcdef0123456789abcdef01234567"), Ts: 1760000000,
		Kind: KindMessage, Data1: blob.Commit(),
	}
	sig, err := SignDigest(e.ID(d), key)
	if err != nil {
		t.Fatal(err)
	}
	return &SignedEvent{Event: e, Sig: sig}, d
}

func mustSeen(t *testing.T, sha string) common.Hash {
	h, err := SeenFromCommit(sha)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The hand-written encoder must agree with go-ethereum's generic EIP-712
// implementation fed the same typed data the browser receives.
func TestEventIDMatchesGethTypedData(t *testing.T) {
	se, d := testKey(t, "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	raw, err := json.Marshal(se.Event.TypedData(d))
	if err != nil {
		t.Fatal(err)
	}
	var td apitypes.TypedData
	if err := json.Unmarshal(raw, &td); err != nil {
		t.Fatal(err)
	}
	digest, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		t.Fatal(err)
	}
	if got := se.Event.ID(d); !bytes.Equal(got[:], digest) {
		t.Fatalf("event id %x != geth digest %x", got, digest)
	}
}

func TestVerifySignature(t *testing.T) {
	se, d := testKey(t, "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if _, err := se.Verify(d); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	other := NewDomain(1, d.VerifyingContract)
	if _, err := se.Verify(other); err == nil {
		t.Fatal("signature accepted under a different chain id")
	}
	tampered := *se
	tampered.Event.Ts++
	if _, err := tampered.Verify(d); err == nil {
		t.Fatal("tampered event accepted")
	}
	// v as 0/1 is accepted and normalised.
	alt := *se
	alt.Sig = append([]byte(nil), se.Sig...)
	alt.Sig[64] -= 27
	if _, err := alt.Verify(d); err != nil {
		t.Fatalf("v=0/1 rejected: %v", err)
	}
}

func TestHighSRejected(t *testing.T) {
	se, d := testKey(t, "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	n := crypto.S256().Params().N
	s := new(big.Int).SetBytes(se.Sig[32:64])
	hi := new(big.Int).Sub(n, s)
	sig := append([]byte(nil), se.Sig...)
	copy(sig[32:64], common.LeftPadBytes(hi.Bytes(), 32))
	sig[64] = 55 - sig[64] // the high-s twin has the other recovery id (27 <-> 28)
	_, err := RecoverSigner(se.Event.ID(d), sig)
	if err == nil || !strings.Contains(err.Error(), "lower half") {
		t.Fatalf("high-s signature not rejected for its s value: %v", err)
	}
}

func TestShapeRules(t *testing.T) {
	se, _ := testKey(t, "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err := se.Event.CheckShape(se.Event.Room); err != nil {
		t.Fatal(err)
	}
	bad := se.Event
	bad.Data2 = common.HexToHash("0x01")
	if bad.CheckShape(bad.Room) == nil {
		t.Fatal("message with data2 accepted")
	}
	bad = se.Event
	bad.Seq = 3
	if bad.CheckShape(bad.Room) == nil {
		t.Fatal("seq>0 without authorPrev accepted")
	}
}

func TestSeenRoundTrip(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	h := mustSeen(t, sha)
	back, ok := CommitFromSeen(h)
	if !ok || back != sha {
		t.Fatalf("round trip gave %q %v", back, ok)
	}
}

func TestMerkleProofsAllSizes(t *testing.T) {
	for n := 1; n <= 9; n++ {
		leaves := make([]common.Hash, n)
		for i := range leaves {
			leaves[i] = crypto.Keccak256Hash([]byte{byte(i)})
		}
		tree, err := BuildTree(leaves)
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 && tree.Root() != leaves[0] {
			t.Fatal("single leaf must be its own root")
		}
		for i, l := range leaves {
			if !VerifyProof(l, tree.Proof(i), tree.Root()) {
				t.Fatalf("n=%d leaf %d: proof failed", n, i)
			}
		}
		wrong := crypto.Keccak256Hash([]byte("x"))
		if VerifyProof(wrong, tree.Proof(0), tree.Root()) {
			t.Fatalf("n=%d: forged leaf verified", n)
		}
	}
	if _, err := BuildTree(nil); err != ErrEmptyTree {
		t.Fatal("empty tree accepted")
	}
}

func TestLeafBindsSignature(t *testing.T) {
	id := crypto.Keccak256Hash([]byte("event"))
	if Leaf(id, []byte{1}) == Leaf(id, []byte{2}) {
		t.Fatal("leaf does not depend on the signature")
	}
}

func TestDelegation(t *testing.T) {
	wallet, _ := crypto.HexToECDSA("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	temp, _ := crypto.HexToECDSA("59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d")
	d := NewDomain(31337, common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa3"))
	room := common.HexToHash("0xaa")
	del := Delegation{Room: room, Wallet: crypto.PubkeyToAddress(wallet.PublicKey), Delegate: crypto.PubkeyToAddress(temp.PublicKey),
		ValidFrom: 1000, ValidUntil: 1000 + 3600, Kinds: DelegableKinds}
	wsig, _ := SignDigest(del.ID(d), wallet)
	sd := &SignedDelegation{Delegation: del, WalletSig: wsig}
	e := Event{Room: room, Author: del.Wallet, Ts: 1, Kind: KindMessage, Data1: common.HexToHash("0x01")}
	sig, _ := SignDigest(e.ID(d), temp)
	se := SignedEvent{Event: e, Sig: sig, Delegation: sd}
	if _, err := se.Verify(d); err != nil {
		t.Fatalf("delegated event rejected: %v", err)
	}
	if se.LeafHash(e.ID(d), d) == Leaf(e.ID(d), sig) {
		t.Fatal("delegated leaf must commit to the delegation")
	}
	tomb := e
	tomb.Kind, tomb.Target, tomb.Data1 = KindTombstone, common.HexToHash("0x02"), ProposalIDWord(1)
	tsig, _ := SignDigest(tomb.ID(d), temp)
	if _, err := (SignedEvent{Event: tomb, Sig: tsig, Delegation: sd}).Verify(d); err == nil {
		t.Fatal("delegate signed a tombstone")
	}
	other := e
	other.Author = common.HexToAddress("0x1234")
	osig, _ := SignDigest(other.ID(d), temp)
	if _, err := (SignedEvent{Event: other, Sig: osig, Delegation: sd}).Verify(d); err == nil {
		t.Fatal("delegation used for another author")
	}
	long := del
	long.ValidUntil = long.ValidFrom + MaxDelegationSeconds + 1
	if long.Check() == nil {
		t.Fatal("delegation longer than two hours accepted")
	}
	forged := *sd
	forged.WalletSig, _ = SignDigest(del.ID(d), temp)
	if _, err := (SignedEvent{Event: e, Sig: sig, Delegation: &forged}).Verify(d); err == nil {
		t.Fatal("delegation not signed by the wallet accepted")
	}
}
