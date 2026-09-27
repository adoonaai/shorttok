package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adoonaai/shorttok/internal/anthropic"
	"github.com/adoonaai/shorttok/internal/metrics"
	"github.com/adoonaai/shorttok/internal/pipeline"
	"github.com/adoonaai/shorttok/internal/pricing"
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

// shrinker pretends to halve the conversation.
type shrinker struct{}

func (shrinker) Name() string { return "shrinker" }
func (shrinker) Apply(_ context.Context, rc *pipeline.Context) error {
	rc.Req.Messages = rc.Req.Messages[len(rc.Req.Messages)/2:]
	return nil
}

func TestCostMetricsAndBypass(t *testing.T) {
	var gotMessages []int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropic.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMessages = append(gotMessages, len(req.Messages))
		if r.Header.Get(BypassHeader) != "" {
			t.Error("bypass header leaked upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":100}}`)
	}))
	defer upstream.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := metrics.NewRegistry()
	m := metrics.NewProxy(reg)
	h := &Handler{
		Client:   &anthropic.Client{BaseURL: upstream.URL, HTTP: upstream.Client()},
		Pipeline: pipeline.New(log, shrinker{}),
		Metrics:  m,
		Log:      log,
		MaxBody:  1 << 20,
		Pricing:  pricing.Default(),
	}
	body := `{"model":"claude-sonnet-4-6","max_tokens":10,"messages":[` +
		`{"role":"user","content":"aaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"role":"assistant","content":"bbbbbbbbbbbbbbbbbbbbbbbbbbbb"},` +
		`{"role":"user","content":"cccccccccccccccccccccccccccc"},{"role":"assistant","content":"dddddddddddddddddddddddddddd"}]}`

	send := func(bypass bool) {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		if bypass {
			req.Header.Set(BypassHeader, "1")
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	send(false)
	if gotMessages[0] != 2 {
		t.Fatalf("optimized request should have 2 messages, got %d", gotMessages[0])
	}
	input := m.Cost("input", "claude-sonnet-4-6").Value()
	baseline := m.Cost("baseline", "claude-sonnet-4-6").Value()
	if input != 3000 || baseline < 5000 { // 1000 tok * $3/M = 3000 micro-USD; ~2x before shrinking
		t.Fatalf("input=%d baseline=%d", input, baseline)
	}

	send(true)
	if gotMessages[1] != 4 {
		t.Fatalf("bypass must not optimize, got %d messages", gotMessages[1])
	}
	if d := m.Cost("baseline", "claude-sonnet-4-6").Value() - baseline; d != 3000 {
		t.Fatalf("bypass baseline must equal actual, got %d", d)
	}
}

func TestExactCounts(t *testing.T) {
	const body = `{"model":"claude-sonnet-5","max_tokens":10,"messages":[` +
		`{"role":"user","content":"a"},{"role":"assistant","content":"b"},` +
		`{"role":"user","content":"c"},{"role":"assistant","content":"d"}]}`

	run := func(t *testing.T, countStatus int) (*httptest.ResponseRecorder, *metrics.Proxy) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var m map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&m)
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/messages/count_tokens" {
				if _, ok := m["max_tokens"]; ok {
					t.Error("count_tokens must not receive max_tokens")
				}
				var msgs []json.RawMessage
				_ = json.Unmarshal(m["messages"], &msgs)
				w.WriteHeader(countStatus)
				_, _ = io.WriteString(w, `{"input_tokens":`+strconv.Itoa(len(msgs)*300)+`}`)
				return
			}
			_, _ = io.WriteString(w, `{"content":[],"usage":{"input_tokens":600,"output_tokens":10}}`)
		}))
		t.Cleanup(upstream.Close)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		m := metrics.NewProxy(metrics.NewRegistry())
		h := &Handler{
			Client:       &anthropic.Client{BaseURL: upstream.URL, HTTP: upstream.Client()},
			Pipeline:     pipeline.New(log, shrinker{}),
			Metrics:      m,
			Log:          log,
			MaxBody:      1 << 20,
			Pricing:      pricing.Default(),
			CountTimeout: time.Second,
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		return rec, m
	}

	t.Run("ok", func(t *testing.T) {
		rec, m := run(t, http.StatusOK)
		hd := rec.Header()
		if hd.Get("X-Shorttok-Tokens-Before") != "1200" || hd.Get("X-Shorttok-Tokens-After") != "600" ||
			hd.Get("X-Shorttok-Tokens-Exact") != "true" {
			t.Fatalf("headers: %v", hd)
		}
		// 600 billed tokens, exactly 2x shrunk.
		if got := m.Baseline.Value(); got != 1200 {
			t.Fatalf("baseline tokens %d", got)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		rec, m := run(t, http.StatusTooManyRequests)
		if rec.Header().Get("X-Shorttok-Tokens-Exact") != "false" || m.CountFallbacks.Value() != 1 {
			t.Fatalf("expected estimate fallback, headers %v", rec.Header())
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("count failure must not fail the request: %d", rec.Code)
		}
	})
}
