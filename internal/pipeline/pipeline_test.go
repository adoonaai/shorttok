package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/adoonaai/shorttok/internal/anthropic"
)

type breaker struct{}

func (breaker) Name() string { return "breaker" }
func (breaker) Apply(_ context.Context, rc *Context) error {
	rc.Req.Messages = nil // half-done change
	return errors.New("boom")
}

func TestFailedOptimizerIsRolledBack(t *testing.T) {
	rc := &Context{Req: &anthropic.Request{Model: "m", Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}}}}
	New(slog.New(slog.NewTextHandler(io.Discard, nil)), breaker{}).Run(context.Background(), rc)
	if len(rc.Req.Messages) != 1 {
		t.Fatal("change was not rolled back")
	}
	if rc.Report.Steps[0].Err == nil {
		t.Fatal("error not reported")
	}
}
