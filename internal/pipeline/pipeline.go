// Package pipeline runs a chain of request optimizers.
package pipeline

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/andrey/shorttok/internal/anthropic"
	"github.com/andrey/shorttok/internal/tokens"
)

// Optimizer rewrites a request to make it cheaper.
// It may mutate rc.Req freely; on error the pipeline rolls the change back.
type Optimizer interface {
	Name() string
	Apply(ctx context.Context, rc *Context) error
}

// Context is what an optimizer sees for one request.
type Context struct {
	Req     *anthropic.Request
	Headers http.Header // upstream headers (auth, version) for side calls
	Report  Report
}

// Report describes what the pipeline did.
type Report struct {
	Steps []Step
	// AuxUsage counts tokens spent by optimizers themselves (e.g. summaries),
	// so savings can be reported honestly.
	AuxUsage anthropic.Usage
}

func (r *Report) AddAux(u anthropic.Usage) { r.AuxUsage.Add(u) }

// Changed reports whether the named optimizer modified the request.
func (r *Report) Changed(name string) bool {
	for _, s := range r.Steps {
		if s.Name == name && s.Err == nil && s.Before != s.After {
			return true
		}
	}
	return false
}

// CompressionRatio is how many times the shrinking steps reduced the
// request (>= 1). Steps that grow it slightly, like adding cache markers,
// are ignored: they change price, not size.
func (r *Report) CompressionRatio() float64 {
	ratio := 1.0
	for _, s := range r.Steps {
		if s.Err == nil && s.After > 0 && s.After < s.Before {
			ratio *= float64(s.Before) / float64(s.After)
		}
	}
	return ratio
}

// Step is the outcome of one optimizer.
type Step struct {
	Name          string
	Before, After int // estimated tokens
	Duration      time.Duration
	Err           error
}

type Pipeline struct {
	opts []Optimizer
	log  *slog.Logger
}

func New(log *slog.Logger, opts ...Optimizer) *Pipeline {
	return &Pipeline{opts: opts, log: log}
}

// Run applies optimizers in order. It is fail-open: if an optimizer fails,
// its changes are discarded and the request continues as it was before it.
// A broken optimizer must never break the user's request.
func (p *Pipeline) Run(ctx context.Context, rc *Context) {
	for _, o := range p.opts {
		snapshot, err := rc.Req.Clone()
		if err != nil {
			p.log.Error("cannot snapshot request, skipping optimizers", "err", err)
			return
		}
		st := Step{Name: o.Name(), Before: tokens.Estimate(rc.Req)}
		start := time.Now()
		err = o.Apply(ctx, rc)
		st.Duration = time.Since(start)
		if err != nil {
			*rc.Req = *snapshot
			st.Err = err
			p.log.Warn("optimizer failed, change rolled back", "optimizer", o.Name(), "err", err)
		}
		st.After = tokens.Estimate(rc.Req)
		rc.Report.Steps = append(rc.Report.Steps, st)
	}
}
