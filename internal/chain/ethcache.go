package chain

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Public RPC endpoints limit eth_getLogs block ranges and request rates, and
// a node polls every few seconds. So Eth scans logs from the deployment block
// in fixed-size chunks, keeps what it has scanned and later asks only for new
// blocks; results that change with chain state are reused until the
// confirmed head moves; and the head itself is reused for a moment.

// DefaultLogChunk is the eth_getLogs block range used when the manifest sets none.
const DefaultLogChunk = 2000

// headTTL bounds how long a read of the confirmed head is reused.
const headTTL = 2 * time.Second

type logScan struct {
	next uint64 // first block not yet scanned
	logs []types.Log
}

type memo struct {
	head uint64
	v    any
}

// ethCache holds Eth's incremental log scans and per-head memos.
type ethCache struct {
	mu     sync.Mutex
	headAt time.Time
	head   uint64
	scans  map[string]*logScan
	memos  map[string]memo
	cps    map[common.Hash][]Checkpoint // checkpoints never change once written
}

func (e *Eth) chunk() uint64 {
	if e.LogChunk > 0 {
		return e.LogChunk
	}
	return DefaultLogChunk
}

// rawHead reads the confirmed head, reusing a read younger than headTTL.
func (e *Eth) rawHead(ctx context.Context) (uint64, error) {
	e.cache.mu.Lock()
	if !e.cache.headAt.IsZero() && time.Since(e.cache.headAt) < headTTL {
		h := e.cache.head
		e.cache.mu.Unlock()
		return h, nil
	}
	e.cache.mu.Unlock()
	n, err := e.Client.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	if n < e.Confirmations {
		n = 0
	} else {
		n -= e.Confirmations
	}
	e.cache.mu.Lock()
	e.cache.head, e.cache.headAt = n, time.Now()
	e.cache.mu.Unlock()
	return n, nil
}

// forgetHead makes the next read fetch the head again; called after our own
// transactions so their effects show up at once.
func (e *Eth) forgetHead() {
	e.cache.mu.Lock()
	e.cache.headAt = time.Time{}
	e.cache.mu.Unlock()
}

// filter returns all logs matching addr/topics from the deployment block up to
// head, fetching only blocks not scanned before, at most chunk() blocks per request.
func (e *Eth) filter(ctx context.Context, addr common.Address, topics [][]common.Hash, head uint64) ([]types.Log, error) {
	key := addr.Hex() + fmt.Sprint(topics)
	e.cache.mu.Lock()
	defer e.cache.mu.Unlock()
	if e.cache.scans == nil {
		e.cache.scans = map[string]*logScan{}
	}
	sc := e.cache.scans[key]
	if sc == nil {
		sc = &logScan{next: e.FromBlock}
		e.cache.scans[key] = sc
	}
	for sc.next <= head {
		to := sc.next + e.chunk() - 1
		if to > head {
			to = head
		}
		ls, err := e.Client.FilterLogs(ctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(sc.next), ToBlock: new(big.Int).SetUint64(to),
			Addresses: []common.Address{addr}, Topics: topics,
		})
		if err != nil {
			return nil, fmt.Errorf("logs %d-%d: %w", sc.next, to, err)
		}
		sc.logs = append(sc.logs, ls...)
		sc.next = to + 1
	}
	return append([]types.Log(nil), sc.logs...), nil
}

// memoized returns the value computed for key at head, computing it once per head.
func memoized[T any](e *Eth, key string, head uint64, compute func() (T, error)) (T, error) {
	e.cache.mu.Lock()
	if m, ok := e.cache.memos[key]; ok && m.head == head {
		e.cache.mu.Unlock()
		return m.v.(T), nil
	}
	e.cache.mu.Unlock()
	v, err := compute()
	if err != nil {
		return v, err
	}
	e.cache.mu.Lock()
	if e.cache.memos == nil {
		e.cache.memos = map[string]memo{}
	}
	e.cache.memos[key] = memo{head: head, v: v}
	e.cache.mu.Unlock()
	return v, nil
}
