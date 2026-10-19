// Command gitchain-dev performs the chain-side steps that the browser does
// with a wallet (create room, propose, vote, execute) using keys from
// environment variables. It is for scripts, tests and demos with TEST keys.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/deploy"
)

const usage = `usage: gitchain-dev <command> [flags]

commands:
  room-id       -room NAME
  write-deploy  -broadcast contracts/broadcast/Deploy.s.sol/31337/run-latest.json -rpc URL -chain-id N -out FILE
  create-room   -deploy FILE -key-env VAR -room NAME -members a,b,c -anchor-writer ADDR
  propose       -deploy FILE -key-env VAR -room NAME -target EVENTID [-reason TEXT]
  vote          -deploy FILE -key-env VAR -id N [-support=false]
  execute       -deploy FILE -key-env VAR -id N
  proposal      -deploy FILE -id N`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var (
		deployFile   = fs.String("deploy", "deployments/31337.json", "deployment manifest")
		keyEnv       = fs.String("key-env", "", "env var holding the sender's TEST key")
		room         = fs.String("room", "", "room name or 0x id")
		members      = fs.String("members", "", "comma separated member addresses")
		anchorWriter = fs.String("anchor-writer", "", "anchor writer address")
		target       = fs.String("target", "", "event id to delete")
		reason       = fs.String("reason", "", "non-sensitive reason; only its hash goes on chain")
		id           = fs.Uint64("id", 0, "proposal id")
		support      = fs.Bool("support", true, "vote in favour")
		broadcast    = fs.String("broadcast", "", "forge broadcast run-latest.json")
		rpc          = fs.String("rpc", "http://127.0.0.1:8545", "JSON-RPC endpoint")
		chainID      = fs.Int64("chain-id", 31337, "chain id")
		out          = fs.String("out", "", "output file")
	)
	_ = fs.Parse(args)
	ctx := context.Background()

	roomID := func() common.Hash {
		if strings.HasPrefix(*room, "0x") && len(*room) == 66 {
			return common.HexToHash(*room)
		}
		if *room == "" {
			log.Fatal("-room is required")
		}
		return deploy.RoomID(*room)
	}

	switch cmd {
	case "room-id":
		fmt.Println(roomID().Hex())
		return
	case "write-deploy":
		writeDeploy(*broadcast, *rpc, *chainID, *out)
		return
	}

	man, err := deploy.Load(*deployFile)
	if err != nil {
		log.Fatal(err)
	}
	eth, err := chain.DialEth(ctx, man.RPC, man.ChainID, chain.Addresses{Registry: man.Registry, AnchorLog: man.AnchorLog, Governance: man.Governance}, 0)
	if err != nil {
		log.Fatal(err)
	}
	if cmd == "proposal" {
		p, err := eth.Proposal(ctx, *id)
		if err != nil {
			log.Fatal(err)
		}
		printJSON(p)
		return
	}
	key, err := deploy.KeyFromEnv(*keyEnv)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sender %s", crypto.PubkeyToAddress(key.PublicKey).Hex())

	switch cmd {
	case "create-room":
		var ms []common.Address
		for _, m := range strings.Split(*members, ",") {
			if m = strings.TrimSpace(m); m != "" {
				ms = append(ms, common.HexToAddress(m))
			}
		}
		rc, err := eth.Transact(ctx, key, man.Registry, eth.RegistryABI(), "createRoom", roomID(), ms, common.HexToAddress(*anchorWriter))
		check(err)
		fmt.Printf("room %s created in tx %s\n", roomID().Hex(), rc.TxHash.Hex())
	case "propose":
		reasonHash := crypto.Keccak256Hash([]byte(*reason))
		rc, err := eth.Transact(ctx, key, man.Governance, eth.GovernanceABI(), "proposeRedaction", roomID(), common.HexToHash(*target), reasonHash)
		check(err)
		for _, l := range rc.Logs {
			if len(l.Topics) > 1 && l.Topics[0] == eth.GovernanceABI().Events["Proposed"].ID {
				fmt.Printf("proposal %d\n", new(big.Int).SetBytes(l.Topics[1][:]).Uint64())
			}
		}
	case "vote":
		rc, err := eth.Transact(ctx, key, man.Governance, eth.GovernanceABI(), "vote", new(big.Int).SetUint64(*id), *support)
		check(err)
		fmt.Printf("voted in tx %s\n", rc.TxHash.Hex())
	case "execute":
		rc, err := eth.Transact(ctx, key, man.Governance, eth.GovernanceABI(), "execute", new(big.Int).SetUint64(*id))
		check(err)
		fmt.Printf("executed in tx %s\n", rc.TxHash.Hex())
	default:
		fmt.Println(usage)
		os.Exit(2)
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// writeDeploy turns a forge broadcast record into the deployment manifest.
func writeDeploy(broadcast, rpc string, chainID int64, out string) {
	raw, err := os.ReadFile(broadcast)
	check(err)
	var rec struct {
		Transactions []struct {
			ContractName    string `json:"contractName"`
			ContractAddress string `json:"contractAddress"`
			TransactionType string `json:"transactionType"`
		} `json:"transactions"`
	}
	check(json.Unmarshal(raw, &rec))
	m := deploy.Manifest{ChainID: chainID, RPC: rpc}
	for _, t := range rec.Transactions {
		if t.TransactionType != "CREATE" {
			continue
		}
		switch t.ContractName {
		case "RoomRegistry":
			m.Registry = common.HexToAddress(t.ContractAddress)
		case "AnchorLog":
			m.AnchorLog = common.HexToAddress(t.ContractAddress)
		case "Governance":
			m.Governance = common.HexToAddress(t.ContractAddress)
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	check(os.WriteFile(out, append(b, '\n'), 0o644))
	fmt.Printf("wrote %s\n", out)
}
