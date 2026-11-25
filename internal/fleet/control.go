package fleet

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
		m["next_seq"], m["replays"] = k.events.Stats()
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
		k.events.Emit(k.cache.Clear())
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
		if r.URL.Path == "/lab/drop-next" {
			k.events.DropNext()
		} else {
			k.events.ExpireReplay()
		}
	}
	out.WriteHeader(204)
}
