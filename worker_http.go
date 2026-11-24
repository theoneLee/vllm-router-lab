package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type object = map[string]any

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
		k.writeState(out, n, w.id)
	case "/metrics":
		w.writeMetrics(out)
	case "/lab/clear", "/lab/drop-next", "/lab/algorithm", "/lab/expire-replay", "/lab/freeze", "/lab/cut":
		k.control(out, r)
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
		events := k.cache.store(q.Prompt, w.cfg.size)
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
