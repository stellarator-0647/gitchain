package protocol

import (
	"crypto/rand"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
)

// TypedData is the eth_signTypedData_v4 payload the browser hands to the
// wallet. Integers are strings so that JavaScript never rounds them.
type TypedData struct {
	Types       map[string][]TypedField `json:"types"`
	PrimaryType string                  `json:"primaryType"`
	Domain      map[string]any          `json:"domain"`
	Message     map[string]any          `json:"message"`
}

// TypedField is one EIP-712 member declaration.
type TypedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var eventFields = []TypedField{
	{"room", "bytes32"}, {"author", "address"}, {"seq", "uint64"},
	{"authorPrev", "bytes32"}, {"seen", "bytes32"}, {"ts", "uint64"},
	{"kind", "uint8"}, {"target", "bytes32"}, {"data1", "bytes32"}, {"data2", "bytes32"},
}

var domainFields = []TypedField{
	{"name", "string"}, {"version", "string"}, {"chainId", "uint256"}, {"verifyingContract", "address"},
}

// TypedData returns the wallet payload for e under d.
func (e Event) TypedData(d Domain) TypedData {
	return TypedData{
		Types:       map[string][]TypedField{"EIP712Domain": domainFields, "Event": eventFields},
		PrimaryType: "Event",
		Domain: map[string]any{
			"name":              d.Name,
			"version":           d.Version,
			"chainId":           d.ChainID.String(),
			"verifyingContract": d.VerifyingContract.Hex(),
		},
		Message: map[string]any{
			"room":       e.Room.Hex(),
			"author":     e.Author.Hex(),
			"seq":        strconv.FormatUint(e.Seq, 10),
			"authorPrev": e.AuthorPrev.Hex(),
			"seen":       e.Seen.Hex(),
			"ts":         strconv.FormatUint(e.Ts, 10),
			"kind":       strconv.FormatUint(uint64(e.Kind), 10),
			"target":     e.Target.Hex(),
			"data1":      e.Data1.Hex(),
			"data2":      e.Data2.Hex(),
		},
	}
}

// Blob is a message body stored on the content branch.
type Blob struct {
	Salt common.Hash `json:"salt"`
	Body string      `json:"body"`
}

// MaxBodyBytes bounds a single message body.
const MaxBodyBytes = 16 * 1024

// NewBlob draws a fresh 32-byte salt for body.
func NewBlob(body string) (Blob, error) {
	if !utf8.ValidString(body) {
		return Blob{}, errors.New("body must be valid UTF-8")
	}
	if len(body) == 0 || len(body) > MaxBodyBytes {
		return Blob{}, errors.New("body must be 1..16384 bytes")
	}
	var salt common.Hash
	if _, err := rand.Read(salt[:]); err != nil {
		return Blob{}, err
	}
	return Blob{Salt: salt, Body: body}, nil
}

// Commit returns the content commitment of the blob.
func (b Blob) Commit() common.Hash { return ContentCommit(b.Salt, b.Body) }
