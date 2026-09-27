// Package proxy serves an Anthropic-compatible POST /v1/messages endpoint
// that optimizes the request and forwards it upstream, streaming included.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/adoonaai/shorttok/internal/anthropic"
	"github.com/adoonaai/shorttok/internal/metrics"
	"github.com/adoonaai/shorttok/internal/pipeline"
	"github.com/adoonaai/shorttok/internal/pricing"
	"github.com/adoonaai/shorttok/internal/tokens"
)

// BypassHeader makes the proxy forward a request without optimizing it.
// Used by the eval harness to get a baseline through the very same path.
const BypassHeader = "X-Shorttok-Bypass"

type Handler struct {
	Client   *anthropic.Client
	Pipeline *pipeline.Pipeline
	Metrics  *metrics.Proxy
	Log      *slog.Logger
	MaxBody  int64
	Pricing  *pricing.Table // optional: enables cost metrics
	AuxModel string         // model used by optimizers for side calls

	// CountTimeout > 0 enables exact before/after token counts through
	// count_tokens for requests the pipeline shrank. The counts run alongside
	// the upstream call; on timeout or error the byte estimate is used.
	CountTimeout time.Duration
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.MaxBody))
	if err != nil {
		http.Error(w, "cannot read body: "+err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	hdr := h.Client.Headers(r.Header)

	// Optimize. Anything unexpected -> forward the original bytes (fail-open).
	body := raw
	var (
		req           anthropic.Request
		rep           pipeline.Report
		before, after int
	)
	if err := json.Unmarshal(raw, &req); err != nil {
		h.Log.Warn("unparseable request, passing through", "err", err)
	} else {
		before, after = tokens.Estimate(&req), tokens.Estimate(&req)
		if !isBypass(r.Header) {
			rc := &pipeline.Context{Req: &req, Headers: hdr}
			h.Pipeline.Run(r.Context(), rc)
			rep = rc.Report
			if out, err := json.Marshal(rc.Req); err == nil {
				body = out
				after = tokens.Estimate(rc.Req)
			} else {
				h.Log.Error("cannot encode optimized request, passing through", "err", err)
				rep = pipeline.Report{AuxUsage: rep.AuxUsage} // original is sent: no savings
			}
		}
	}
	auxMicro := h.auxCost(rep.AuxUsage)

	// Exact counts only matter when the request was shrunk: otherwise
	// before == after and the API's usage block is already exact.
	ratio := rep.CompressionRatio()
	var counted chan exactCount
	if h.CountTimeout > 0 && ratio > 1 {
		ctx, cancel := context.WithTimeout(r.Context(), h.CountTimeout)
		defer cancel()
		counted = make(chan exactCount, 1)
		go func() { counted <- h.countExact(ctx, hdr, raw, &req) }()
	}

	resp, err := h.Client.Send(r.Context(), "/v1/messages", body, hdr)
	if err != nil {
		h.Metrics.UpstreamErrors.Add(1)
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	exact := false
	if counted != nil {
		if c := <-counted; c.err == nil && c.after > 0 {
			before, after, exact = c.before, c.after, true
			ratio = float64(before) / float64(after)
		} else {
			h.Metrics.CountFallbacks.Add(1)
			h.Log.Warn("exact token count failed, using estimate", "err", c.err)
		}
	}

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Shorttok-Tokens-Before", strconv.Itoa(before))
	w.Header().Set("X-Shorttok-Tokens-After", strconv.Itoa(after))
	w.Header().Set("X-Shorttok-Tokens-Exact", strconv.FormatBool(exact))
	w.Header().Set("X-Shorttok-Aux-Input-Tokens", strconv.Itoa(rep.AuxUsage.InputTokens))
	w.Header().Set("X-Shorttok-Aux-Output-Tokens", strconv.Itoa(rep.AuxUsage.OutputTokens))
	w.Header().Set("X-Shorttok-Aux-Cost-Microusd", strconv.Itoa(auxMicro))
	w.WriteHeader(resp.StatusCode)

	var usage anthropic.Usage
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		err = copySSE(w, resp.Body, func(data []byte) { mergeEvent(&usage, data) })
	} else {
		var data []byte
		if data, err = io.ReadAll(resp.Body); err == nil {
			_, err = w.Write(data)
			var x struct {
				Usage anthropic.Usage `json:"usage"`
			}
			_ = json.Unmarshal(data, &x)
			usage = x.Usage
		}
	}
	if err != nil {
		h.Log.Warn("response copy interrupted", "err", err)
	}

	h.record(req.Model, resp.StatusCode, before, after, ratio, usage, rep, auxMicro)
	h.Log.Info("request",
		"model", req.Model, "stream", req.Stream, "status", resp.StatusCode,
		"tokens_before", before, "tokens_after", after, "exact", exact,
		"input", usage.InputTokens, "output", usage.OutputTokens,
		"cache_read", usage.CacheReadInputTokens, "cache_write", usage.CacheCreationInputTokens,
		"aux_input", rep.AuxUsage.InputTokens, "aux_output", rep.AuxUsage.OutputTokens,
		"duration", time.Since(start))
}

// ratio is how many times the request was shrunk: from exact counts when
// available, otherwise from the pipeline's estimates.
func (h *Handler) record(model string, code, before, after int, ratio float64, u anthropic.Usage, rep pipeline.Report, auxMicro int) {
	m := h.Metrics
	m.Request(code).Add(1)
	m.EstBefore.Add(before)
	m.EstAfter.Add(after)
	m.Input.Add(u.InputTokens)
	m.Output.Add(u.OutputTokens)
	m.CacheRead.Add(u.CacheReadInputTokens)
	m.CacheWrite.Add(u.CacheCreationInputTokens)
	m.AuxInput.Add(rep.AuxUsage.InputTokens)
	m.AuxOutput.Add(rep.AuxUsage.OutputTokens)
	for _, s := range rep.Steps {
		if s.Err != nil {
			m.OptimizerError(s.Name).Add(1)
		}
	}
	if auxMicro > 0 {
		m.Cost("aux", h.AuxModel).Add(auxMicro)
	}

	billed := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	if billed == 0 || model == "" {
		return // failed request: nothing was charged
	}
	m.Baseline.Add(int(math.Round(float64(billed) * ratio)))

	if h.Pricing == nil {
		return
	}
	p, ok := h.Pricing.Lookup(model)
	if !ok {
		m.Unpriced(model).Add(1)
		return
	}
	actual := p.InputMicro(u)
	var baseline int
	if rep.Changed("autocache") {
		// Caching was our doing: without the proxy every token is full price.
		baseline = p.InputMicro(anthropic.Usage{InputTokens: int(math.Round(float64(billed) * ratio))})
	} else {
		// The client's own caching pattern would have been the same: credit compression only.
		baseline = int(math.Round(float64(actual) * ratio))
	}
	m.Cost("baseline", model).Add(baseline)
	m.Cost("input", model).Add(actual)
	m.Cost("output", model).Add(p.OutputMicro(u))
}

type exactCount struct {
	before, after int
	err           error
}

// countExact counts the original request (re-parsed from raw, since the
// pipeline rewrote req in place) and the optimized one concurrently.
func (h *Handler) countExact(ctx context.Context, hdr http.Header, raw []byte, optimized *anthropic.Request) exactCount {
	var orig anthropic.Request
	if err := json.Unmarshal(raw, &orig); err != nil {
		return exactCount{err: err}
	}
	var (
		c        exactCount
		afterErr error
		done     = make(chan struct{})
	)
	go func() {
		defer close(done)
		c.after, afterErr = h.Client.CountTokens(ctx, hdr, optimized)
	}()
	c.before, c.err = h.Client.CountTokens(ctx, hdr, &orig)
	<-done
	c.err = errors.Join(c.err, afterErr)
	return c
}

func (h *Handler) auxCost(u anthropic.Usage) int {
	if h.Pricing == nil || u == (anthropic.Usage{}) {
		return 0
	}
	p, ok := h.Pricing.Lookup(h.AuxModel)
	if !ok {
		return 0
	}
	return p.InputMicro(u) + p.OutputMicro(u)
}

func isBypass(h http.Header) bool {
	v := strings.ToLower(h.Get(BypassHeader))
	return v == "1" || v == "true"
}

// copySSE copies a server-sent event stream line by line, flushing at every
// event boundary, and hands each data payload to onData.
func copySSE(w http.ResponseWriter, body io.Reader, onData func([]byte)) error {
	fl, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return werr // client went away
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				onData(bytes.TrimSpace(line[len("data:"):]))
			} else if len(bytes.TrimSpace(line)) == 0 && fl != nil {
				fl.Flush()
			}
		}
		if err == io.EOF {
			if fl != nil {
				fl.Flush()
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

var messageEvent = []byte(`"message_`)

// mergeEvent collects usage from message_start / message_delta events.
func mergeEvent(u *anthropic.Usage, data []byte) {
	if !bytes.Contains(data, messageEvent) {
		return // cheap skip for the many content_block_delta events
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Usage anthropic.Usage `json:"usage"`
		} `json:"message"`
		Usage anthropic.Usage `json:"usage"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		*u = ev.Message.Usage
	case "message_delta":
		u.OutputTokens = ev.Usage.OutputTokens // cumulative
		if ev.Usage.InputTokens > 0 {
			u.InputTokens = ev.Usage.InputTokens
		}
	}
}

var skipHeaders = map[string]bool{
	"Content-Length": true, "Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Upgrade": true, "Proxy-Connection": true, "Te": true, "Trailer": true,
}

func copyHeaders(dst, src http.Header) {
	for k, v := range src {
		if !skipHeaders[k] {
			dst[k] = append([]string(nil), v...)
		}
	}
}
