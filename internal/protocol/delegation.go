package protocol

import (
	"errors"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Delegation lets a wallet authorise a short-lived browser key (the
// delegate) to sign events of selected kinds on its behalf. The wallet signs
// the delegation once with EIP-712; the delegate then signs each event. The
// event author stays the wallet.
//
// Time validity is not judged from self-reported clocks: an event signed by a
// delegate is valid only if a checkpoint whose block time is <= ValidUntil
// covers it (docs/protocol.md §11).
type Delegation struct {
	Room       common.Hash    `json:"room"`
	Wallet     common.Address `json:"wallet"`
	Delegate   common.Address `json:"delegate"`
	ValidFrom  uint64         `json:"validFrom"`
	ValidUntil uint64         `json:"validUntil"`
	Kinds      uint64         `json:"kinds"` // bit k set = kind k allowed
}

// SignedDelegation is a delegation with the wallet's signature.
type SignedDelegation struct {
	Delegation Delegation   `json:"delegation"`
	WalletSig  SignatureHex `json:"walletSig"`
}

const (
	// MaxDelegationSeconds bounds a delegation's lifetime.
	MaxDelegationSeconds = 2 * 60 * 60
	// DelegableKinds are the kinds a delegate may sign: messages and receipts.
	// Tombstones and agreement events always need the wallet itself.
	DelegableKinds = 1<<KindMessage | 1<<KindReceipt

	delegationType = "Delegation(bytes32 room,address wallet,address delegate,uint64 validFrom,uint64 validUntil,uint256 kinds)"
)

var delegationTypeHash = crypto.Keccak256Hash([]byte(delegationType))

// StructHash returns hashStruct(Delegation).
func (d Delegation) StructHash() common.Hash {
	return crypto.Keccak256Hash(
		delegationTypeHash[:],
		d.Room[:],
		word(d.Wallet[:]),
		word(d.Delegate[:]),
		uintWord(d.ValidFrom),
		uintWord(d.ValidUntil),
		uintWord(d.Kinds),
	)
}

// ID returns the EIP-712 digest the wallet signs.
func (d Delegation) ID(dom Domain) common.Hash {
	sep := dom.Separator()
	sh := d.StructHash()
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, sep[:], sh[:])
}

// Allows reports whether kind is delegable under d.
func (d Delegation) Allows(kind uint8) bool { return kind < 64 && d.Kinds&(1<<kind) != 0 }

// Check enforces the static rules: lifetime, kinds and non-zero parties.
func (d Delegation) Check() error {
	switch {
	case d.Wallet == (common.Address{}) || d.Delegate == (common.Address{}):
		return errors.New("delegation: zero wallet or delegate")
	case d.Wallet == d.Delegate:
		return errors.New("delegation: delegate must differ from wallet")
	case d.ValidUntil <= d.ValidFrom:
		return errors.New("delegation: empty validity window")
	case d.ValidUntil-d.ValidFrom > MaxDelegationSeconds:
		return errors.New("delegation: lifetime exceeds 2 hours")
	case d.Kinds == 0 || d.Kinds&^DelegableKinds != 0:
		return errors.New("delegation: only message and receipt kinds may be delegated")
	}
	return nil
}

// Verify checks the wallet signature and static rules and returns the id.
func (sd SignedDelegation) Verify(dom Domain) (common.Hash, error) {
	id := sd.Delegation.ID(dom)
	if err := sd.Delegation.Check(); err != nil {
		return id, err
	}
	who, err := RecoverSigner(id, sd.WalletSig)
	if err != nil {
		return id, err
	}
	if who != sd.Delegation.Wallet {
		return id, errors.New("delegation: signature does not recover the wallet")
	}
	return id, nil
}

// TypedData returns the wallet payload for the delegation.
func (d Delegation) TypedData(dom Domain) TypedData {
	return TypedData{
		Types: map[string][]TypedField{"EIP712Domain": domainFields, "Delegation": {
			{"room", "bytes32"}, {"wallet", "address"}, {"delegate", "address"},
			{"validFrom", "uint64"}, {"validUntil", "uint64"}, {"kinds", "uint256"},
		}},
		PrimaryType: "Delegation",
		Domain: map[string]any{
			"name": dom.Name, "version": dom.Version, "chainId": dom.ChainID.String(),
			"verifyingContract": dom.VerifyingContract.Hex(),
		},
		Message: map[string]any{
			"room":       d.Room.Hex(),
			"wallet":     d.Wallet.Hex(),
			"delegate":   d.Delegate.Hex(),
			"validFrom":  strconv.FormatUint(d.ValidFrom, 10),
			"validUntil": strconv.FormatUint(d.ValidUntil, 10),
			"kinds":      new(big.Int).SetUint64(d.Kinds).String(),
		},
	}
}
