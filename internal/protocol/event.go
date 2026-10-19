// Package protocol defines the signed event format shared by every GitChain
// component: the EIP-712 Event struct, its identifier, content commitments and
// the Merkle tree used by checkpoints. docs/protocol.md is the normative spec;
// testdata/vectors.json pins the byte-level results.
package protocol

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Event kinds. Kinds 2, 4 and 6 belong to optional extensions; the core node
// accepts them structurally but only acts on 1 and 3.
const (
	KindMessage      uint8 = 1
	KindReceipt      uint8 = 2
	KindTombstone    uint8 = 3
	KindAgreementRef uint8 = 4
	KindRevision     uint8 = 6
)

// Event is the only signed record type. Every field is fixed width so that the
// EIP-712 encoding is identical in Go and TypeScript.
type Event struct {
	Room       common.Hash    `json:"room"`
	Author     common.Address `json:"author"`
	Seq        uint64         `json:"seq"`
	AuthorPrev common.Hash    `json:"authorPrev"`
	Seen       common.Hash    `json:"seen"`
	Ts         uint64         `json:"ts"`
	Kind       uint8          `json:"kind"`
	Target     common.Hash    `json:"target"`
	Data1      common.Hash    `json:"data1"`
	Data2      common.Hash    `json:"data2"`
}

// SignedEvent is what is stored under events/ on the main branch. Sig is
// either the author's own signature, or a delegate's signature together with
// the wallet-signed Delegation that authorises it.
type SignedEvent struct {
	Event      Event             `json:"event"`
	Sig        SignatureHex      `json:"sig"`
	Delegation *SignedDelegation `json:"delegation,omitempty"`
}

// Domain is the EIP-712 domain. ChainID and VerifyingContract must come from
// trusted configuration, never from repository files or proof bundles.
type Domain struct {
	Name              string
	Version           string
	ChainID           *big.Int
	VerifyingContract common.Address
}

const (
	DomainName    = "GitChain"
	DomainVersion = "2"
)

// NewDomain returns the protocol domain for a deployment.
func NewDomain(chainID int64, registry common.Address) Domain {
	return Domain{Name: DomainName, Version: DomainVersion, ChainID: big.NewInt(chainID), VerifyingContract: registry}
}

const (
	domainType = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	eventType  = "Event(bytes32 room,address author,uint64 seq,bytes32 authorPrev,bytes32 seen,uint64 ts,uint8 kind,bytes32 target,bytes32 data1,bytes32 data2)"
)

var (
	domainTypeHash = crypto.Keccak256Hash([]byte(domainType))
	eventTypeHash  = crypto.Keccak256Hash([]byte(eventType))
)

func word(b []byte) []byte { return common.LeftPadBytes(b, 32) }

func uintWord(v uint64) []byte { return word(new(big.Int).SetUint64(v).Bytes()) }

// Separator returns the EIP-712 domain separator.
func (d Domain) Separator() common.Hash {
	return crypto.Keccak256Hash(
		domainTypeHash[:],
		crypto.Keccak256([]byte(d.Name)),
		crypto.Keccak256([]byte(d.Version)),
		word(d.ChainID.Bytes()),
		word(d.VerifyingContract[:]),
	)
}

// StructHash returns hashStruct(Event).
func (e Event) StructHash() common.Hash {
	return crypto.Keccak256Hash(
		eventTypeHash[:],
		e.Room[:],
		word(e.Author[:]),
		uintWord(e.Seq),
		e.AuthorPrev[:],
		e.Seen[:],
		uintWord(e.Ts),
		uintWord(uint64(e.Kind)),
		e.Target[:],
		e.Data1[:],
		e.Data2[:],
	)
}

// ID returns the event identifier: the full EIP-712 digest that the wallet
// signs, keccak256(0x1901 || domainSeparator || hashStruct(event)).
func (e Event) ID(d Domain) common.Hash {
	sep := d.Separator()
	sh := e.StructHash()
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, sep[:], sh[:])
}

// ContentCommit returns keccak256(salt || utf8(body)), stored in data1 of a
// message event.
func ContentCommit(salt common.Hash, body string) common.Hash {
	return crypto.Keccak256Hash(salt[:], []byte(body))
}

// SeenFromCommit converts a 40-hex Git SHA-1 into the bytes32 seen field
// (left aligned, zero padded on the right).
func SeenFromCommit(sha string) (common.Hash, error) {
	sha = strings.TrimSpace(sha)
	if len(sha) != 40 {
		return common.Hash{}, fmt.Errorf("seen: want 40 hex chars, got %d", len(sha))
	}
	raw := common.FromHex(sha)
	if len(raw) != 20 {
		return common.Hash{}, errors.New("seen: invalid hex")
	}
	var h common.Hash
	copy(h[:20], raw)
	return h, nil
}

// CommitFromSeen is the inverse of SeenFromCommit.
func CommitFromSeen(h common.Hash) (string, bool) {
	for _, b := range h[20:] {
		if b != 0 {
			return "", false
		}
	}
	if h == (common.Hash{}) {
		return "", false
	}
	return strings.TrimPrefix(common.Bytes2Hex(h[:20]), "0x"), true
}

// CheckShape enforces the per-kind field rules from docs/protocol.md.
func (e Event) CheckShape(room common.Hash) error {
	zero := common.Hash{}
	if e.Room != room {
		return errors.New("room mismatch")
	}
	if e.Author == (common.Address{}) {
		return errors.New("zero author")
	}
	if e.Seq == 0 && e.AuthorPrev != zero {
		return errors.New("seq 0 must have zero authorPrev")
	}
	if e.Seq > 0 && e.AuthorPrev == zero {
		return errors.New("missing authorPrev")
	}
	switch e.Kind {
	case KindMessage:
		if e.Target != zero || e.Data1 == zero || e.Data2 != zero {
			return errors.New("message: want target=0, data1=commit, data2=0")
		}
	case KindReceipt:
		if e.Target == zero || e.Data1 == zero || e.Data2 != zero {
			return errors.New("receipt: want target=eventId, data1=content commit, data2=0")
		}
	case KindTombstone:
		if e.Target == zero || e.Data1 == zero || e.Data2 != zero {
			return errors.New("tombstone: want target=eventId, data1=proposalId, data2=0")
		}
	case KindAgreementRef, KindRevision:
		if e.Target == zero {
			return errors.New("agreement events need target=agreementId")
		}
	default:
		return fmt.Errorf("unknown kind %d", e.Kind)
	}
	return nil
}

// ProposalIDWord encodes a proposal id as data1 of a tombstone.
func ProposalIDWord(id uint64) common.Hash { return common.BytesToHash(uintWord(id)) }

// ProposalIDFromWord decodes data1 of a tombstone.
func ProposalIDFromWord(h common.Hash) (uint64, bool) {
	v := new(big.Int).SetBytes(h[:])
	if !v.IsUint64() {
		return 0, false
	}
	return v.Uint64(), true
}
