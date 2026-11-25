package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type object = map[string]any

type request struct {
	Prompt []uint32 `json:"prompt"`
	Stream bool     `json:"stream"`
	Tokens int      `json:"max_tokens"`
	Model  string   `json:"model"`
}

type traceRow struct {
	At     float64  `json:"at_ms"`
	Tokens []uint32 `json:"prompt"`
	Output int      `json:"max_tokens"`
	Stream bool     `json:"stream"`
}
type job struct {
	row traceRow
	due time.Time
}
type sample struct {
	lat, first, tpot, lag time.Duration
	err                   error
}

func send(client *http.Client, url string, j job) sample {
	start := time.Now()
	s := sample{lag: start.Sub(j.due)}
	b, e := json.Marshal(request{Prompt: j.row.Tokens, Tokens: j.row.Output, Stream: j.row.Stream, Model: "lab"})
	if e != nil {
		s.err = e
		return s
	}
	res, e := client.Post(url+"/v1/completions", "application/json", bytes.NewReader(b))
	if e != nil {
		s.err = e
		return s
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		s.err = fmt.Errorf("HTTP %s %s", res.Status, b)
		return s
	}
	if j.row.Stream {
		scan := bufio.NewScanner(res.Body)
		scan.Buffer(make([]byte, 4096), 2<<20)
		done := false
		chunks := 0
		var last time.Duration
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			v := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if v == "[DONE]" {
				done = true
				break
			}
			var d struct{ Choices []struct{ Text string } }
			if e = json.Unmarshal([]byte(v), &d); e != nil || len(d.Choices) == 0 {
				s.err = fmt.Errorf("invalid SSE choice")
				return s
			}
			if d.Choices[0].Text != "" {
				last = time.Since(start)
				chunks++
				if chunks == 1 {
					s.first = last
				}
			}
		}
		if !done || chunks == 0 {
			s.err = fmt.Errorf("incomplete SSE: %v", scan.Err())
			return s
		}
		if chunks > 1 {
			s.tpot = (last - s.first) / time.Duration(chunks-1)
		}
	} else {
		var d struct{ Choices []struct{ Text string } }
		e = json.NewDecoder(res.Body).Decode(&d)
		if e != nil || len(d.Choices) == 0 {
			s.err = fmt.Errorf("invalid completion: %v", e)
			return s
		}
	}
	s.lat = time.Since(start)
	return s
}
func summary(samples []time.Duration) map[string]float64 {
	if len(samples) == 0 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	m := map[string]float64{}
	for key, p := range map[string]int{"p50": 50, "p95": 95, "p99": 99} {
		m[key] = float64(samples[(len(samples)*p+99)/100-1]) / 1e6
	}
	return m
}

// Run parses the replay flags, sends the workload and prints a JSON summary.
func Run(args []string) error {
	f := flag.NewFlagSet("replay", flag.ContinueOnError)
	url := f.String("url", "http://127.0.0.1:30000", "Router URL")
	n := f.Int("n", 1000, "synthetic requests")
	cc := f.Int("concurrency", 64, "client limit")
	rate := f.Float64("rate", 0, "scheduled requests/s; 0 saturates")
	seed := f.Int64("seed", 42, "generator seed")
	file := f.String("trace", "", "ordered JSONL trace")
	stream := f.Bool("stream", false, "stream generated workload")
	out := f.Int("output", 1, "generated output length")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *cc < 1 || *n < 1 || *rate < 0 || *out < 1 {
		return fmt.Errorf("invalid replay parameters")
	}
	var reader *bufio.Scanner
	if *file != "" {
		fh, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer fh.Close()
		reader = bufio.NewScanner(fh)
		reader.Buffer(make([]byte, 65536), 4<<20)
	}
	client := &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{MaxIdleConns: *cc, MaxIdleConnsPerHost: *cc}}
	defer client.CloseIdleConnections()
	jobs := make(chan job, *cc)
	results := make(chan sample, *cc)
	var wg sync.WaitGroup
	for i := 0; i < *cc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results <- send(client, *url, j)
			}
		}()
	}
	start := time.Now()
	var inputErr error
	go func() {
		defer close(jobs)
		rng := rand.New(rand.NewSource(*seed))
		prev := 0.0
		for i := 0; ; i++ {
			var row traceRow
			if reader != nil {
				if !reader.Scan() {
					inputErr = reader.Err()
					return
				}
				if e := json.Unmarshal(reader.Bytes(), &row); e != nil {
					inputErr = fmt.Errorf("row %d: %w", i+1, e)
					return
				}
			} else {
				if i >= *n {
					return
				}
				if *rate > 0 {
					row.At = 1000 * float64(i) / *rate
				}
				prefix := rng.Intn(32)
				row.Tokens = make([]uint32, 33)
				for t := range row.Tokens {
					row.Tokens[t] = uint32(1 + prefix*32 + t)
				}
				row.Tokens[32] = uint32(i % 8192)
				row.Output = *out
				row.Stream = *stream
			}
			if row.At < prev || len(row.Tokens) == 0 || row.Output < 1 {
				inputErr = fmt.Errorf("invalid trace row %d", i+1)
				return
			}
			prev = row.At
			due := time.Now()
			if reader != nil || *rate > 0 {
				due = start.Add(time.Duration(row.At * 1e6))
				if delay := time.Until(due); delay > 0 {
					time.Sleep(delay)
				}
			}
			jobs <- job{row, due}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	var lat, first, tpot, lag []time.Duration
	errors := 0
	firstError := ""
	for s := range results {
		lag = append(lag, s.lag)
		if s.err != nil {
			errors++
			if firstError == "" {
				firstError = s.err.Error()
			}
			continue
		}
		lat = append(lat, s.lat)
		if s.first > 0 {
			first = append(first, s.first)
		}
		if s.tpot > 0 {
			tpot = append(tpot, s.tpot)
		}
	}
	elapsed := time.Since(start).Seconds()
	report := object{"requests": len(lag), "success": len(lat), "errors": errors, "first_error": firstError, "seconds": elapsed, "success_rps": float64(len(lat)) / elapsed, "e2e_ms": summary(lat), "ttft_ms": summary(first), "inter_chunk_ms": summary(tpot), "dispatch_lag_ms": summary(lag), "seed": *seed, "rate": *rate}
	if e := json.NewEncoder(os.Stdout).Encode(report); e != nil {
		return e
	}
	if inputErr != nil {
		return inputErr
	}
	if errors > 0 || len(lat) == 0 {
		return fmt.Errorf("replay incomplete")
	}
	return nil
}
