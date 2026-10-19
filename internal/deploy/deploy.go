// Package deploy loads the trusted deployment manifest
// (deployments/<chainId>.json). Nodes and the verifier take chain id and
// contract addresses only from this file, never from repository contents.
package deploy

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Manifest describes one deployment.
type Manifest struct {
	ChainID       int64          `json:"chainId"`
	RPC           string         `json:"rpc"`
	Confirmations uint64         `json:"confirmations"`
	Registry      common.Address `json:"registry"`
	AnchorLog     common.Address `json:"anchorLog"`
	Governance    common.Address `json:"governance"`
}

// Load reads and checks a manifest.
func Load(path string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	if m.ChainID == 0 || m.RPC == "" || m.Registry == (common.Address{}) || m.AnchorLog == (common.Address{}) || m.Governance == (common.Address{}) {
		return m, fmt.Errorf("%s: chainId, rpc and all three contract addresses are required", path)
	}
	return m, nil
}

// KeyFromEnv reads a hex private key from the named environment variable.
// Keys are never accepted on the command line, so they stay out of shell
// history and process listings.
func KeyFromEnv(name string) (*ecdsa.PrivateKey, error) {
	v := strings.TrimPrefix(strings.TrimSpace(os.Getenv(name)), "0x")
	if v == "" {
		return nil, fmt.Errorf("environment variable %s is empty", name)
	}
	return crypto.HexToECDSA(v)
}

// RoomID derives a room id from a human-readable name.
func RoomID(name string) common.Hash { return crypto.Keccak256Hash([]byte("gitchain-room:" + name)) }
