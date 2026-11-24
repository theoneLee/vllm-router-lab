package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReplayRequiresTextAndDone(t *testing.T) {
	const chunk = "data: {\"choices\":[{\"text\":\"x\"}]}\n\n"
	const done = "data: [DONE]\n\n"
	for _, tc := range []struct {
		name, stream string
		wantSuccess  bool
	}{
		{"complete", chunk + chunk + done, true},
		{"truncated after text", chunk, false},
		{"empty stream", done, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.stream)
			}))
			defer server.Close()
			result := send(server.Client(), server.URL, job{row: traceRow{Tokens: []uint32{1}, Output: 2, Stream: true}, due: time.Now()})
			if (result.err == nil) != tc.wantSuccess {
				t.Fatalf("success=%t, error=%v", result.err == nil, result.err)
			}
		})
	}
}
