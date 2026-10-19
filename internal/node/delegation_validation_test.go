package node

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gitchain/gitchain/internal/protocol"
	"testing"
	"time"
)

func TestRejectInactiveDelegation(t *testing.T) {
	key, _ := crypto.GenerateKey()
	wallet := crypto.PubkeyToAddress(key.PublicKey)
	room := common.HexToHash("0xaabb")
	dom := protocol.NewDomain(31337, common.HexToAddress("0x1234"))
	now := uint64(time.Now().Unix())
	for _, tc := range []struct {
		name        string
		from, until uint64
	}{
		{"future", now + 600, now + 1200}, {"expired", now - 1200, now - 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &Node{cfg: Config{Wallet: wallet, Room: room}, domain: dom}
			d := protocol.Delegation{Room: room, Wallet: wallet, Delegate: common.HexToAddress("0x5678"), ValidFrom: tc.from, ValidUntil: tc.until, Kinds: protocol.DelegableKinds}
			sig, _ := protocol.SignDigest(d.ID(dom), key)
			sd := protocol.SignedDelegation{Delegation: d, WalletSig: sig}
			if _, err := n.SetDelegation(sd); err == nil {
				t.Fatal("inactive delegation accepted")
			}
			n.delegation = &sd
			if n.activeDelegation() != nil {
				t.Fatal("inactive key usable")
			}
		})
	}
}
