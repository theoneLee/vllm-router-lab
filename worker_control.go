package main

import (
	"net/http"
	"strconv"
)

func (k *rank) writeState(out http.ResponseWriter, n int, instance string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m := object{
		"last_reported_algorithm": k.lastReportedAlgorithm,
		"instance":                instance,
		"rank":                    n,
		"active":                  k.active.Load(),
		"waiting":                 k.waiting.Load(),
		"received":                k.requests.Load(),
		"metrics_reads":           k.metricsReads.Load(),
		"hits":                    k.hits.Load(),
		"canceled":                k.canceled.Load(),
		"broken":                  k.broken.Load(),
	}
	if k.events != nil {
		k.events.mu.Lock()
		m["next_seq"] = k.events.seq
		m["replays"] = k.events.replays
		k.events.mu.Unlock()
	}
	writeJSON(out, m)
}

func (k *rank) control(out http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(out, "POST required", 405)
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	switch r.URL.Path {
	case "/lab/clear":
		k.cache.clear()
		k.events.emit([]cacheEvent{allBlocksCleared{}})
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
}
