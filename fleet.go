package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
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
type request struct {
	Prompt []uint32 `json:"prompt"`
	Stream bool     `json:"stream"`
	Tokens int      `json:"max_tokens"`
	Model  string   `json:"model"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
func (w *worker) ServeHTTP(out http.ResponseWriter, r *http.Request) {
	n := 0
	var err error
	if val := r.Header.Get("X-Data-Parallel-Rank"); val != "" {
		n, err = strconv.Atoi(val)
	}
	if err != nil || n < 0 || n >= len(w.ranks) {
		http.Error(out, "invalid rank", 400)
		return
	}
	k := w.ranks[n]
	switch r.URL.Path {
	case "/health", "/health_generate", "/ready":
		out.WriteHeader(200)
	case "/version":
		writeJSON(out, object{"version": w.cfg.profile.Version})
	case "/v1/models":
		writeJSON(out, object{"object": "list", "data": []object{{"id": "lab", "object": "model", "owned_by": "lab", "created": 0}}})
	case "/get_model_info", "/get_server_info", "/model_info":
		if w.cfg.version != "" || (w.cfg.metricsOnly && r.URL.Path == "/get_server_info") {
			http.NotFound(out, r)
			return
		}
		writeJSON(out, object{"model_id": "lab", "model_path": "lab", "served_model_name": "lab", "instance_id": w.id, "kv_block_size": w.cfg.size, "effective_attention_block_size": w.cfg.size})
	case "/server_info":
		if w.cfg.metricsOnly || w.cfg.version != "" {
			http.NotFound(out, r)
			return
		}
		writeJSON(out, object{"vllm_config": object{"model_config": object{"model": "lab", "served_model_name": []string{"lab"}}, "cache_config": object{"block_size": w.cfg.size, "enable_prefix_caching": true, "prefix_caching_hash_algo": "sha256_cbor"}}})
	case "/kv_event_sources", "/lab/event-sources":
		if r.URL.Path == "/kv_event_sources" && !w.cfg.profile.SourceDiscovery {
			http.NotFound(out, r)
			return
		}
		m := object{}
		for i, s := range w.ranks {
			if s.events != nil {
				m[strconv.Itoa(i)] = s.events.metadata()
			}
		}
		writeJSON(out, m)
	case "/lab/state":
		k.mu.Lock()
		defer k.mu.Unlock()
		m := object{"last_reported_algorithm": k.lastReportedAlgorithm, "instance": w.id, "rank": n, "active": k.active.Load(), "waiting": k.waiting.Load(), "received": k.requests.Load(), "metrics_reads": k.metricsReads.Load(), "hits": k.hits.Load(), "canceled": k.canceled.Load(), "broken": k.broken.Load()}
		if k.events != nil {
			k.events.mu.Lock()
			m["next_seq"] = k.events.seq
			m["replays"] = k.events.replays
			k.events.mu.Unlock()
		}
		writeJSON(out, m)
	case "/metrics":
		for i, s := range w.ranks {
			s.mu.Lock()
			s.metricsReads.Add(1)
			algo := s.algorithm
			s.lastReportedAlgorithm = algo
			used := s.cache.order.Len()
			s.mu.Unlock()
			label := ""
			if algo != "" {
				label = fmt.Sprintf(",prefix_caching_hash_algo=%q", algo)
			}
			fmt.Fprintf(out, "vllm:cache_config_info{engine=%q,block_size=%q,enable_prefix_caching=\"True\"%s} 1\n", strconv.Itoa(i), strconv.Itoa(w.cfg.size), label)
			for name, val := range map[string]int64{"vllm:prefix_cache_hits_total": s.hits.Load(), "vllm:num_requests_running": s.active.Load(), "vllm:num_requests_waiting": s.waiting.Load(), "lab_requests_total": s.requests.Load(), "lab_prompt_tokens_total": s.promptTokens.Load(), "lab_metrics_reads_total": s.metricsReads.Load(), "lab_canceled_total": s.canceled.Load(), "lab_broken_total": s.broken.Load()} {
				fmt.Fprintf(out, "%s{engine=%q} %d\n", name, strconv.Itoa(i), val)
			}
			fmt.Fprintf(out, "vllm:kv_cache_usage_perc{engine=%q} %g\n", strconv.Itoa(i), float64(used)/float64(w.cfg.blocks))
		}
	case "/lab/clear", "/lab/drop-next", "/lab/algorithm", "/lab/expire-replay", "/lab/freeze", "/lab/cut":
		if r.Method != "POST" {
			http.Error(out, "POST required", 405)
			return
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		switch r.URL.Path {
		case "/lab/clear":
			k.cache.clear()
			k.events.emit([]object{{"type": "AllBlocksCleared"}})
		case "/lab/freeze":
			k.frozen = r.URL.Query().Get("value") == "1"
		case "/lab/cut":
			i, e := strconv.Atoi(r.URL.Query().Get("value"))
			if e != nil || i < 1 {
				http.Error(out, "invalid chunk count", 400)
				return
			}
			k.cut.Store(int64(i))
		case "/lab/algorithm":
			k.algorithm = r.URL.Query().Get("value")
		default:
			if k.events == nil {
				http.Error(out, "events disabled", 409)
				return
			}
			k.events.mu.Lock()
			if r.URL.Path == "/lab/drop-next" {
				k.events.drop = true
			} else {
				k.events.history = nil
			}
			k.events.mu.Unlock()
		}
		out.WriteHeader(204)
	case "/v1/completions":
		w.complete(out, r, k, n)
	default:
		http.NotFound(out, r)
	}
}
func (w *worker) complete(out http.ResponseWriter, r *http.Request, k *rank, n int) {
	var q request
	if r.Method != "POST" || json.NewDecoder(http.MaxBytesReader(out, r.Body, 2<<20)).Decode(&q) != nil || len(q.Prompt) == 0 || q.Tokens < 1 || q.Tokens > 16384 {
		http.Error(out, "expected token-ID completion", 400)
		return
	}
	ctx := r.Context()
	k.waiting.Add(1)
	select {
	case k.slots <- struct{}{}:
	case <-ctx.Done():
		k.waiting.Add(-1)
		k.canceled.Add(1)
		return
	}
	k.waiting.Add(-1)
	k.active.Add(1)
	defer func() { <-k.slots; k.active.Add(-1) }()
	k.requests.Add(1)
	k.promptTokens.Add(int64(len(q.Prompt)))
	k.mu.Lock()
	hits := k.cache.prefix(q.Prompt, w.cfg.size)
	k.mu.Unlock()
	missing := len(q.Prompt) - hits
	if !k.batch.step(ctx, missing) || !wait(ctx, w.cfg.network+time.Duration(missing)*w.cfg.prefill) {
		k.canceled.Add(1)
		return
	}
	k.mu.Lock()
	if !k.frozen {
		_, events := k.cache.access(q.Prompt, w.cfg.size)
		k.events.emit(events)
	}
	k.hits.Add(int64(hits))
	k.mu.Unlock()
	nextToken := func() bool { return k.batch.step(ctx, 1) && wait(ctx, w.cfg.decode) }
	out.Header().Set("X-Lab-Worker", w.id)
	out.Header().Set("X-Lab-Rank", strconv.Itoa(n))
	choice := func(text string, finish any) object {
		return object{"index": 0, "text": text, "finish_reason": finish, "logprobs": nil}
	}
	body := func(c object) object {
		return object{"id": w.id, "object": "text_completion", "created": 0, "model": "lab", "choices": []object{c}}
	}
	if !q.Stream {
		for i := 1; i < q.Tokens; i++ {
			if !nextToken() {
				k.canceled.Add(1)
				return
			}
		}
		b := body(choice(repeatToken(q.Tokens), "length"))
		b["lab_rank"] = n
		b["usage"] = object{"prompt_tokens": len(q.Prompt), "completion_tokens": q.Tokens, "total_tokens": len(q.Prompt) + q.Tokens, "prompt_tokens_details": object{"cached_tokens": hits}}
		writeJSON(out, b)
		return
	}
	out.Header().Set("Content-Type", "text/event-stream")
	flusher := out.(http.Flusher)
	cut := int(k.cut.Swap(0))
	if cut == 0 {
		cut, _ = strconv.Atoi(r.Header.Get("X-Lab-Cut-After"))
	}
	for i := 1; i <= q.Tokens; i++ {
		if i > 1 && !nextToken() {
			k.canceled.Add(1)
			return
		}
		var finish any
		if i == q.Tokens {
			finish = "length"
		}
		b, _ := json.Marshal(body(choice("x", finish)))
		if _, e := fmt.Fprintf(out, "data: %s\n\n", b); e != nil {
			k.canceled.Add(1)
			return
		}
		flusher.Flush()
		if cut == i {
			k.broken.Add(1)
			panic(http.ErrAbortHandler)
		}
	}
	fmt.Fprint(out, "data: [DONE]\n\n")
	flusher.Flush()
}
func repeatToken(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
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
			k := &rank{batch: newBatcher(ctx, c.tick, c.budget), cache: cache{capacity: c.blocks, rootHash: hashRoot, intHashes: c.intHashes}, algorithm: "sha256_cbor", slots: make(chan struct{}, c.slots)}
			if c.events {
				feed, e := newFeed(ctx, fmt.Sprintf("kv/%s/%d", w.id, j), j, c.window)
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
	log.Printf("ready workers=%d ranks_per_worker=%d first=%s", c.nodes, c.ranks, endpoint(c.port))
	return <-errs
}
