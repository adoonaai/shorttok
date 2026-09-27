// Package metrics is a tiny dependency-free Prometheus exporter.
// It only supports counters, which is all the MVP needs.
// Swap for prometheus/client_golang once histograms are required.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type Counter struct {
	family, help, labels string
	v                    atomic.Int64
}

func (c *Counter) Add(n int) {
	if n > 0 {
		c.v.Add(int64(n))
	}
}

func (c *Counter) Value() int64 { return c.v.Load() }

type Registry struct {
	mu       sync.Mutex
	counters map[string]*Counter
}

func NewRegistry() *Registry { return &Registry{counters: map[string]*Counter{}} }

// Counter returns the counter for family+labels, creating it on first use.
// labels is in exposition format without braces, e.g. `code="200"`.
func (r *Registry) Counter(family, help, labels string) *Counter {
	key := family + "{" + labels + "}"
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[key]
	if !ok {
		c = &Counter{family: family, help: help, labels: labels}
		r.counters[key] = c
	}
	return c
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	list := make([]*Counter, 0, len(r.counters))
	for _, c := range r.counters {
		list = append(list, c)
	}
	r.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if list[i].family != list[j].family {
			return list[i].family < list[j].family
		}
		return list[i].labels < list[j].labels
	})

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	prev := ""
	for _, c := range list {
		if c.family != prev {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.family, c.help, c.family)
			prev = c.family
		}
		name := c.family
		if c.labels != "" {
			name += "{" + c.labels + "}"
		}
		fmt.Fprintf(w, "%s %d\n", name, c.Value())
	}
}

// Proxy groups the proxy's counters.
type Proxy struct {
	reg *Registry

	UpstreamErrors *Counter
	EstBefore      *Counter
	EstAfter       *Counter
	Input          *Counter
	Output         *Counter
	CacheRead      *Counter
	CacheWrite     *Counter
	AuxInput       *Counter
	AuxOutput      *Counter
	Baseline       *Counter
}

func NewProxy(reg *Registry) *Proxy {
	return &Proxy{
		reg:            reg,
		UpstreamErrors: reg.Counter("shorttok_upstream_errors_total", "Requests that failed to reach the upstream API.", ""),
		EstBefore:      reg.Counter("shorttok_estimated_tokens_before_total", "Estimated input tokens before optimization.", ""),
		EstAfter:       reg.Counter("shorttok_estimated_tokens_after_total", "Estimated input tokens after optimization.", ""),
		Input:          reg.Counter("shorttok_input_tokens_total", "Uncached input tokens reported by the API.", ""),
		Output:         reg.Counter("shorttok_output_tokens_total", "Output tokens reported by the API.", ""),
		CacheRead:      reg.Counter("shorttok_cache_read_tokens_total", "Input tokens served from the prompt cache.", ""),
		CacheWrite:     reg.Counter("shorttok_cache_write_tokens_total", "Input tokens written to the prompt cache.", ""),
		AuxInput:       reg.Counter("shorttok_aux_input_tokens_total", "Input tokens spent by optimizers (e.g. summaries).", ""),
		AuxOutput:      reg.Counter("shorttok_aux_output_tokens_total", "Output tokens spent by optimizers (e.g. summaries).", ""),
		Baseline:       reg.Counter("shorttok_baseline_input_tokens_total", "Estimated input tokens the requests would have used without the proxy.", ""),
	}
}

func (p *Proxy) Request(code int) *Counter {
	return p.reg.Counter("shorttok_requests_total", "Proxied /v1/messages requests by upstream status.", `code="`+strconv.Itoa(code)+`"`)
}

func (p *Proxy) OptimizerError(name string) *Counter {
	return p.reg.Counter("shorttok_optimizer_errors_total", "Optimizer failures (the change was rolled back).", `optimizer="`+escape(name)+`"`)
}

// Cost is spend in micro-USD. kind is one of:
// baseline (estimated input cost without the proxy), input, output, aux.
// Savings = baseline - input - aux; output is the same either way.
func (p *Proxy) Cost(kind, model string) *Counter {
	return p.reg.Counter("shorttok_cost_microusd_total", "Spend in micro-USD by kind and model.",
		`kind="`+escape(kind)+`",model="`+escape(model)+`"`)
}

func (p *Proxy) Unpriced(model string) *Counter {
	return p.reg.Counter("shorttok_unpriced_requests_total", "Requests for models missing from the pricing table.", `model="`+escape(model)+`"`)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escape(v string) string { return labelEscaper.Replace(v) }
