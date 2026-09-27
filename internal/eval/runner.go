package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adoonaai/shorttok/internal/anthropic"
	"github.com/adoonaai/shorttok/internal/pricing"
)

const bypassHeader = "X-Shorttok-Bypass"

type Runner struct {
	ProxyURL    string
	APIKey      string
	HTTP        *http.Client
	Pricing     *pricing.Table
	JudgeModel  string
	Replay      bool // replay the conversation turn by turn, like a real chat
	Concurrency int
	Progress    func(done, total int, r Result)
}

// Side is what one variant (baseline or optimized) cost and answered.
type Side struct {
	Answer    string
	Usage     anthropic.Usage // summed over all calls of the conversation
	Calls     int
	CostMicro int // main model, input + output
	AuxMicro  int // optimizer side calls (summaries)
	Latency   time.Duration
	Recall    float64
}

func (s Side) TotalMicro() int { return s.CostMicro + s.AuxMicro }

type Result struct {
	ID          string
	Base, Opt   Side
	Score       int // judge: 1..5, 0 if unavailable
	JudgeReason string
	Err         error
}

// Run evaluates items concurrently; each conversation itself runs sequentially,
// because turn order matters for caching and incremental summaries.
func (r *Runner) Run(ctx context.Context, items []Item) []Result {
	results := make([]Result, len(items))
	jobs := make(chan int)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	for w := 0; w < max(1, r.Concurrency); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = r.runItem(ctx, items[i])
				if r.Progress != nil {
					mu.Lock()
					done++
					r.Progress(done, len(items), results[i])
					mu.Unlock()
				}
			}
		}()
	}
feed:
	for i := range items {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func (r *Runner) runItem(ctx context.Context, it Item) Result {
	res := Result{ID: it.ID}
	var err error
	if res.Base, err = r.side(ctx, it, true); err != nil {
		res.Err = fmt.Errorf("baseline: %w", err)
		return res
	}
	if res.Opt, err = r.side(ctx, it, false); err != nil {
		res.Err = fmt.Errorf("optimized: %w", err)
		return res
	}
	if r.JudgeModel != "" {
		res.Score, res.JudgeReason, err = r.judge(ctx, it, res.Base.Answer, res.Opt.Answer)
		if err != nil {
			res.JudgeReason = "judge failed: " + err.Error()
		}
	}
	return res
}

func (r *Runner) side(ctx context.Context, it Item, bypass bool) (Side, error) {
	var steps [][]anthropic.Message
	if r.Replay {
		for i, m := range it.Messages[:len(it.Messages)-1] {
			if m.Role == "user" {
				steps = append(steps, it.Messages[:i+1])
			}
		}
	}
	steps = append(steps, it.Messages)

	var s Side
	price, priced := r.Pricing.Lookup(it.Model)
	for i, msgs := range steps {
		final := i == len(steps)-1
		maxTokens := 1 // intermediate turns: we only pay for the input, same on both sides
		if final {
			maxTokens = it.MaxTokens
		}
		c, err := r.call(ctx, it.Model, it.System, msgs, maxTokens, bypass)
		if err != nil {
			return s, fmt.Errorf("turn %d: %w", i+1, err)
		}
		s.Calls++
		s.Usage.Add(c.usage)
		s.AuxMicro += c.auxMicro
		if priced {
			s.CostMicro += price.InputMicro(c.usage) + price.OutputMicro(c.usage)
		}
		if final {
			s.Answer, s.Latency = c.text, c.latency
		}
	}
	s.Recall = Recall(s.Answer, it.Expect)
	return s, nil
}

type callResult struct {
	text     string
	usage    anthropic.Usage
	auxMicro int
	latency  time.Duration
}

func (r *Runner) call(ctx context.Context, model, system string, msgs []anthropic.Message, maxTokens int, bypass bool) (callResult, error) {
	payload := map[string]any{"model": model, "max_tokens": maxTokens, "messages": msgs}
	if system != "" {
		payload["system"] = system
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return callResult{}, err
	}

	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		c, retry, err := r.do(ctx, body, bypass)
		if err == nil || !retry || attempt == 5 {
			return c, err
		}
		select {
		case <-time.After(backoff):
			backoff *= 2
		case <-ctx.Done():
			return callResult{}, ctx.Err()
		}
	}
}

func (r *Runner) do(ctx context.Context, body []byte, bypass bool) (c callResult, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.ProxyURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return c, false, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", anthropic.DefaultVersion)
	if r.APIKey != "" {
		req.Header.Set("x-api-key", r.APIKey)
	}
	if bypass {
		req.Header.Set(bypassHeader, "1")
	}

	start := time.Now()
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return c, !errors.Is(err, context.Canceled), err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return c, true, err
	}
	c.latency = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		retry = resp.StatusCode == 429 || resp.StatusCode >= 500
		return c, retry, fmt.Errorf("%s: %.300s", resp.Status, data)
	}

	var out struct {
		Content []anthropic.Block `json:"content"`
		Usage   anthropic.Usage   `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return c, false, err
	}
	for _, b := range out.Content {
		if b.Type() == "text" {
			c.text += b.Text()
		}
	}
	c.usage = out.Usage
	c.auxMicro, _ = strconv.Atoi(resp.Header.Get("X-Shorttok-Aux-Cost-Microusd"))
	return c, false, nil
}
