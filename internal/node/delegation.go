package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gitchain/gitchain/internal/protocol"
)

// DelegationDraft is the delegation the wallet is asked to sign.
type DelegationDraft struct {
	Delegation protocol.Delegation `json:"delegation"`
	ID         common.Hash         `json:"id"`
	TypedData  protocol.TypedData  `json:"typedData"`
}

// ActiveDelegation is what the UI shows about the current delegation.
type ActiveDelegation struct {
	Delegate   common.Address `json:"delegate"`
	ValidUntil uint64         `json:"validUntil"`
	ID         common.Hash    `json:"id"`
}

func (n *Node) delegationFile() string { return filepath.Join(n.stateDir(""), "delegation.json") }

// PrepareDelegation builds a delegation for delegate lasting seconds
// (capped at two hours) starting now.
func (n *Node) PrepareDelegation(delegate common.Address, seconds uint64) (*DelegationDraft, error) {
	if seconds == 0 || seconds > protocol.MaxDelegationSeconds {
		seconds = protocol.MaxDelegationSeconds
	}
	now := uint64(time.Now().Unix())
	d := protocol.Delegation{Room: n.cfg.Room, Wallet: n.cfg.Wallet, Delegate: delegate,
		ValidFrom: now, ValidUntil: now + seconds, Kinds: protocol.DelegableKinds}
	if err := d.Check(); err != nil {
		return nil, err
	}
	return &DelegationDraft{Delegation: d, ID: d.ID(n.domain), TypedData: d.TypedData(n.domain)}, nil
}

// SetDelegation installs a wallet-signed delegation for this node's wallet.
func (n *Node) SetDelegation(sd protocol.SignedDelegation) (*ActiveDelegation, error) {
	norm, err := protocol.NormalizeSig(sd.WalletSig)
	if err != nil {
		return nil, err
	}
	sd.WalletSig = norm
	id, err := sd.Verify(n.domain)
	if err != nil {
		return nil, err
	}
	d := sd.Delegation
	if d.Wallet != n.cfg.Wallet || d.Room != n.cfg.Room {
		return nil, errors.New("delegation is not for this node's wallet and room")
	}
	if now := uint64(time.Now().Unix()); now < d.ValidFrom || now >= d.ValidUntil {
		return nil, errors.New("delegation is not currently valid")
	}
	raw, _ := json.Marshal(sd)
	if err := os.WriteFile(n.delegationFile(), raw, 0o600); err != nil {
		return nil, err
	}
	n.delMu.Lock()
	n.delegation = &sd
	n.delMu.Unlock()
	return &ActiveDelegation{Delegate: d.Delegate, ValidUntil: d.ValidUntil, ID: id}, nil
}

// ClearDelegation forgets the delegation locally. Already signed events stay
// valid under their own delegation; this only stops the node accepting new
// delegate signatures. There is no on-chain revocation (docs/protocol.md §11).
func (n *Node) ClearDelegation() {
	n.delMu.Lock()
	defer n.delMu.Unlock()
	n.delegation = nil
	_ = os.Remove(n.delegationFile())
}

func (n *Node) loadDelegation() {
	raw, err := os.ReadFile(n.delegationFile())
	if err != nil {
		return
	}
	var sd protocol.SignedDelegation
	if json.Unmarshal(raw, &sd) == nil {
		if _, err := sd.Verify(n.domain); err == nil && uint64(time.Now().Unix()) < sd.Delegation.ValidUntil {
			n.delMu.Lock()
			n.delegation = &sd
			n.delMu.Unlock()
		}
	}
}

// activeDelegation returns the delegation if it is still usable.
func (n *Node) activeDelegation() *protocol.SignedDelegation {
	n.delMu.Lock()
	defer n.delMu.Unlock()
	if n.delegation == nil || uint64(time.Now().Unix()) < n.delegation.Delegation.ValidFrom || uint64(time.Now().Unix()) >= n.delegation.Delegation.ValidUntil {
		return nil
	}
	return n.delegation
}

// attachSignature decides whether sig is the wallet's own or the active
// delegate's, and returns the signed event to store.
func (n *Node) attachSignature(e protocol.Event, sig []byte) (protocol.SignedEvent, error) {
	se := protocol.SignedEvent{Event: e, Sig: sig}
	who, err := protocol.RecoverSigner(e.ID(n.domain), sig)
	if err != nil {
		return se, err
	}
	if who == n.cfg.Wallet {
		return se, nil
	}
	d := n.activeDelegation()
	if d == nil || who != d.Delegation.Delegate {
		return se, errors.New("signature is neither the wallet's nor the active delegate's")
	}
	if !d.Delegation.Allows(e.Kind) {
		return se, errors.New("this event kind needs the wallet's own signature")
	}
	se.Delegation = d
	return se, nil
}
