package main

import (
	"context"
	"crypto/rand"

	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"sync"
	"sync/atomic"
	"time"
)

type rank struct {
	frozen                           bool
	cut                              atomic.Int64
	batch                            *batcher
	metricsReads                     atomic.Int64
	mu                               sync.Mutex
	cache                            cache
	events                           *feed
	algorithm                        string
	slots                            chan struct{}
	hits, requests, canceled, broken atomic.Int64
	promptTokens                     atomic.Int64
	lastReportedAlgorithm            string
	active, waiting                  atomic.Int64
}
type fleetConfig struct {
	intHashes                                       bool
	profile                                         profile
	version, seed                                   string
	metricsOnly                                     bool
	tick                                            time.Duration
	budget                                          int
	nodes, ranks, port, slots, blocks, size, window int
	events                                          bool
	prefill, decode, network                        time.Duration
}
type worker struct {
	cfg   fleetConfig
	id    string
	ranks []*rank
}

func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func fleet(args []string) error {
	var c fleetConfig
	f := flag.NewFlagSet("fleet", flag.ContinueOnError)
	f.BoolVar(&c.intHashes, "int-hashes", false, "export low 64 bits as vLLM legacy integer event hashes")
	f.StringVar(&c.version, "vllm-version", "", "versioned vLLM profile (developer server_info disabled); see protocol/versions.json")
	f.StringVar(&c.seed, "hash-seed", "", "explicit numeric PYTHONHASHSEED for SHA-256 CBOR")
	f.BoolVar(&c.metricsOnly, "metrics-only", false, "discover cache metadata via per-rank metrics")
	f.DurationVar(&c.tick, "tick", 0, "continuous batching iteration period (0 disables)")
	f.IntVar(&c.budget, "batch-tokens", 128, "shared token budget per iteration")
	f.IntVar(&c.nodes, "workers", 2, "independent HTTP listeners")
	f.IntVar(&c.ranks, "ranks", 1, "ranks per endpoint")
	f.IntVar(&c.port, "port", 18000, "first worker port")
	f.IntVar(&c.slots, "slots", 16, "running sequences per rank")
	f.IntVar(&c.blocks, "blocks", 256, "prefix cache capacity per rank")
	f.IntVar(&c.size, "block-size", 16, "tokens per block")
	f.IntVar(&c.window, "replay-window", 128, "retained batches")
	f.BoolVar(&c.events, "events", true, "publish ZMQ events")
	f.DurationVar(&c.prefill, "prefill", 0, "cost per uncached token")
	f.DurationVar(&c.decode, "decode", 0, "inter-token delay")
	f.DurationVar(&c.network, "delay", 0, "fixed response delay")
	if e := f.Parse(args); e != nil {
		return e
	}
	if min(c.nodes, c.ranks, c.slots, c.blocks, c.size, c.port, c.window) < 1 || c.port+c.nodes > 65536 || min(c.tick, c.prefill, c.decode, c.network) < 0 || c.budget < 1 {
		return fmt.Errorf("invalid fleet parameters")
	}
	version := c.version
	if version == "" {
		version = "0.34.0"
	}
	var err error
	c.profile, err = selectProfile(version)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, c.nodes)
	for i := 0; i < c.nodes; i++ {
		var epoch [8]byte
		if _, e := rand.Read(epoch[:]); e != nil {
			return e
		}
		w := &worker{cfg: c, id: fmt.Sprintf("worker-%d-%x", c.port+i, epoch)}
		for j := 0; j < c.ranks; j++ {
			hashRoot, err := c.profile.hashRoot(c.seed)
			if err != nil {
				return err
			}
			k := &rank{batch: newBatcher(ctx, c.tick, c.budget), cache: cache{capacity: c.blocks, rootHash: hashRoot}, algorithm: "sha256_cbor", slots: make(chan struct{}, c.slots)}
			if c.events {
				feed, e := newFeed(ctx, fmt.Sprintf("kv/%s/%d", w.id, j), j, c.window, c.intHashes)
				if e != nil {
					return e
				}
				k.events = feed
			}
			w.ranks = append(w.ranks, k)
		}
		ln, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.port+i))
		if e != nil {
			return e
		}
		srv := &http.Server{Handler: w, ReadHeaderTimeout: 5 * time.Second}
		go func() { errs <- srv.Serve(ln) }()
	}
	log.Printf("ready workers=%d ranks_per_worker=%d first=http://127.0.0.1:%d", c.nodes, c.ranks, c.port)
	return <-errs
}
