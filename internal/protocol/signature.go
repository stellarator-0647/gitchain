package protocol

import (
	"crypto/ecdsa"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// SignatureHex is a 65-byte r||s||v signature, JSON encoded as 0x-hex.
type SignatureHex = hexutil.Bytes

var secp256k1HalfN = new(big.Int).Rsh(crypto.S256().Params().N, 1)

// NormalizeSig returns the canonical form: 65 bytes, low s, v in {27, 28}.
// Signatures with high s are rejected rather than flipped, so that one event
// has exactly one valid signature encoding.
func NormalizeSig(sig []byte) ([]byte, error) {
	if len(sig) != 65 {
		return nil, errors.New("signature must be 65 bytes")
	}
	out := append([]byte(nil), sig...)
	switch out[64] {
	case 0, 1:
		out[64] += 27
	case 27, 28:
	default:
		return nil, errors.New("signature v must be 0, 1, 27 or 28")
	}
	s := new(big.Int).SetBytes(out[32:64])
	if s.Sign() == 0 || s.Cmp(secp256k1HalfN) > 0 {
		return nil, errors.New("signature s is not in the lower half order")
	}
	return out, nil
}

// RecoverSigner recovers the address that signed digest.
func RecoverSigner(digest common.Hash, sig []byte) (common.Address, error) {
	norm, err := NormalizeSig(sig)
	if err != nil {
		return common.Address{}, err
	}
	raw := append([]byte(nil), norm...)
	raw[64] -= 27
	pub, err := crypto.SigToPub(digest[:], raw)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// Verify checks the event signature and returns the event id. Without a
// delegation the signature must recover the author. With one, the delegation
// must be a valid wallet signature by the author for this room and kind, and
// the event signature must recover its delegate. Time validity of a delegated
// event is decided later against checkpoint block times.
func (se SignedEvent) Verify(d Domain) (common.Hash, error) {
	id := se.Event.ID(d)
	addr, err := RecoverSigner(id, se.Sig)
	if err != nil {
		return id, err
	}
	if se.Delegation == nil {
		if addr != se.Event.Author {
			return id, errors.New("signature does not recover the author")
		}
		return id, nil
	}
	del := se.Delegation.Delegation
	if _, err := se.Delegation.Verify(d); err != nil {
		return id, err
	}
	switch {
	case del.Wallet != se.Event.Author:
		return id, errors.New("delegation was not issued by the event author")
	case del.Room != se.Event.Room:
		return id, errors.New("delegation belongs to another room")
	case !del.Allows(se.Event.Kind):
		return id, errors.New("delegation does not allow this event kind")
	case addr != del.Delegate:
		return id, errors.New("signature does not recover the delegate")
	}
	return id, nil
}

// LeafHash returns the checkpoint leaf of the event. A delegated event's leaf
// also commits to the delegation, so a longer-lived delegation for the same
// key cannot be swapped in after anchoring.
func (se SignedEvent) LeafHash(id common.Hash, d Domain) common.Hash {
	if se.Delegation == nil {
		return Leaf(id, se.Sig)
	}
	return DelegatedLeaf(id, se.Sig, se.Delegation.Delegation.ID(d))
}

// SignDigest signs with a local key. It is used by tests, the vector
// generator and the explicitly labelled dev signer; browsers sign through the
// wallet instead.
func SignDigest(digest common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}
