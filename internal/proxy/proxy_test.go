package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrey/shorttok/internal/anthropic"
	"github.com/andrey/shorttok/internal/metrics"
	"github.com/andrey/shorttok/internal/pipeline"
)

const sse = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":42,"cache_read_input_tokens":7,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"output_tokens":15}}` + "\n\n"

func TestStreamingPassthroughAndUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "client-key" {
			t.Errorf("api key not forwarded")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer upstream.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.NewProxy(metrics.NewRegistry())
	h := &Handler{
		Client:   &anthropic.Client{BaseURL: upstream.URL, HTTP: upstream.Client()},
		Pipeline: pipeline.New(log),
		Metrics:  m,
		Log:      log,
		MaxBody:  1 << 20,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"m","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "client-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Body.String() != sse {
		t.Fatalf("stream altered:\n%s", rec.Body.String())
	}
	if m.Input.Value() != 42 || m.Output.Value() != 15 || m.CacheRead.Value() != 7 {
		t.Fatalf("usage: in=%d out=%d cache=%d", m.Input.Value(), m.Output.Value(), m.CacheRead.Value())
	}
	if rec.Header().Get("X-Shorttok-Tokens-Before") == "" {
		t.Fatal("missing savings header")
	}
}
