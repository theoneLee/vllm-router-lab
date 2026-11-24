package main

import (
	"fmt"
	"net/http"
	"strconv"
)

func (w *worker) writeMetrics(out http.ResponseWriter) {
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
		for name, val := range map[string]int64{
			"vllm:prefix_cache_hits_total": s.hits.Load(),
			"vllm:num_requests_running":    s.active.Load(),
			"vllm:num_requests_waiting":    s.waiting.Load(),
			"lab_requests_total":           s.requests.Load(),
			"lab_prompt_tokens_total":      s.promptTokens.Load(),
			"lab_metrics_reads_total":      s.metricsReads.Load(),
			"lab_canceled_total":           s.canceled.Load(),
			"lab_broken_total":             s.broken.Load(),
		} {
			fmt.Fprintf(out, "%s{engine=%q} %d\n", name, strconv.Itoa(i), val)
		}
		fmt.Fprintf(out, "vllm:kv_cache_usage_perc{engine=%q} %g\n", strconv.Itoa(i), float64(used)/float64(w.cfg.blocks))
	}
}
