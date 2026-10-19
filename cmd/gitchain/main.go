// Command gitchain runs one member's local node: the room repository, the
// verifier, the sync loop and the loopback HTTP API for the browser.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gitchain/gitchain/internal/api"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/deploy"
	"github.com/gitchain/gitchain/internal/gitstore"
	"github.com/gitchain/gitchain/internal/node"
)

func main() {
	var (
		dataDir     = flag.String("data", "", "node data directory (required)")
		listen      = flag.String("listen", "127.0.0.1:8081", "loopback address for the browser API")
		deployFile  = flag.String("deploy", "deployments/31337.json", "trusted deployment manifest")
		roomName    = flag.String("room", "", "room name (the id is derived from it) or 0x room id")
		wallet      = flag.String("wallet", "", "member wallet address this node writes for")
		remote      = flag.String("remote", "", "room Git remote URL")
		initRepo    = flag.Bool("init", false, "create the room repository (genesis) and push it to -remote")
		anchorEnv   = flag.String("anchor-key-env", "", "env var with the anchor writer key; set only on the designated anchor node")
		devEnv      = flag.String("dev-key-env", "", "env var with a TEST key enabling POST /api/dev/send")
		origins     = flag.String("origins", "http://localhost:5173,http://127.0.0.1:5173,http://127.0.0.1:5174", "allowed browser origins")
		token       = flag.String("token", "", "API token (default: random, written to <data>/state/api-token)")
		syncEvery   = flag.Duration("sync-every", 5*time.Second, "sync interval")
		anchorN     = flag.Int("anchor-n", 20, "anchor after this many new events")
		anchorEvery = flag.Duration("anchor-every", 10*time.Minute, "anchor at least this often when there are new events")
	)
	flag.Parse()
	if *dataDir == "" || *roomName == "" || *wallet == "" {
		flag.Usage()
		os.Exit(2)
	}
	if host, _, err := net.SplitHostPort(*listen); err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatalf("-listen must be a loopback address, got %q", *listen)
	}
	if !common.IsHexAddress(*wallet) {
		log.Fatalf("-wallet is not an address")
	}
	man, err := deploy.Load(*deployFile)
	if err != nil {
		log.Fatal(err)
	}
	room := deploy.RoomID(*roomName)
	if strings.HasPrefix(*roomName, "0x") && len(*roomName) == 66 {
		room = common.HexToHash(*roomName)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	eth, err := chain.DialEth(ctx, man.RPC, man.ChainID, chain.Addresses{Registry: man.Registry, AnchorLog: man.AnchorLog, Governance: man.Governance}, man.Confirmations)
	if err != nil {
		log.Fatalf("chain: %v", err)
	}
	if _, err := eth.Room(ctx, room); err != nil {
		log.Fatalf("room %s: %v (create it on chain first)", room.Hex(), err)
	}

	repo, err := openRepo(filepath.Join(*dataDir, "repo"), *remote, *initRepo, room, man)
	if err != nil {
		log.Fatal(err)
	}

	var anchor chain.AnchorWriter
	if *anchorEnv != "" {
		k, err := deploy.KeyFromEnv(*anchorEnv)
		if err != nil {
			log.Fatal(err)
		}
		anchor = chain.EthAnchor{Eth: eth, Key: k}
		log.Printf("anchor node: %s", anchor.Address().Hex())
	}
	opts := api.Options{Origins: splitList(*origins), Token: *token}
	if *devEnv != "" {
		k, err := deploy.KeyFromEnv(*devEnv)
		if err != nil {
			log.Fatal(err)
		}
		if crypto.PubkeyToAddress(k.PublicKey) != common.HexToAddress(*wallet) {
			log.Fatal("dev key does not belong to -wallet")
		}
		opts.DevKey = k
		opts.DevGov = chain.EthGovernor{Eth: eth, Key: k}
		log.Printf("WARNING: dev signer enabled; %s signs with a local TEST key", "/api/dev/send")
	}
	if opts.Token == "" {
		opts.Token = randomToken()
	}
	if err := writeToken(*dataDir, opts.Token); err != nil {
		log.Fatal(err)
	}

	hub := api.NewHub()
	go hub.Run()
	cfg := node.Config{
		DataDir: *dataDir, Room: room, Wallet: common.HexToAddress(*wallet), ChainID: man.ChainID, RPC: man.RPC,
		Contracts: node.Contracts{Registry: man.Registry, AnchorLog: man.AnchorLog, Governance: man.Governance},
		SyncEvery: *syncEvery, AnchorN: *anchorN, AnchorEvery: *anchorEvery,
	}
	n, err := node.New(cfg, repo, eth, anchor, hub.Notify)
	if err != nil {
		log.Fatal(err)
	}
	n.DevSigner = opts.DevKey != nil
	go n.Run(ctx)

	srv := api.New(n, hub, opts)
	log.Printf("room %s, wallet %s, API on http://%s (token in %s)", room.Hex(), cfg.Wallet.Hex(), *listen, filepath.Join(*dataDir, "state", "api-token"))
	go func() {
		if err := srv.Run(*listen); err != nil {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
}

func openRepo(dir, remote string, init bool, room common.Hash, man deploy.Manifest) (*gitstore.Repo, error) {
	if r, err := gitstore.Open(dir); err == nil {
		return r, nil
	}
	if remote == "" {
		return nil, fmt.Errorf("%s has no repository; pass -remote (and -init for the first member)", dir)
	}
	if !init {
		return gitstore.Clone(remote, dir)
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"room": room, "chainId": man.ChainID, "registry": man.Registry, "anchorLog": man.AnchorLog, "governance": man.Governance,
		"note": "location hints only; verifiers use their trusted deployment manifest",
	}, "", "  ")
	r, err := gitstore.Init(dir, meta)
	if err != nil {
		return nil, err
	}
	if err := r.AddRemote(remote); err != nil {
		return nil, err
	}
	return r, r.Push()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func randomToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeToken(dataDir, token string) error {
	dir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// On Windows 0600 is advisory; protect the data directory with an ACL.
	return os.WriteFile(filepath.Join(dir, "api-token"), []byte(token), 0o600)
}
