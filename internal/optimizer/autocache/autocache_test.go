package autocache

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adoonaai/shorttok/internal/anthropic"
	"github.com/adoonaai/shorttok/internal/pipeline"
)

func TestAddsBreakpoints(t *testing.T) {
	req := &anthropic.Request{
		System: json.RawMessage(`"sys"`),
		Messages: []anthropic.Message{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	if err := New().Apply(context.Background(), &pipeline.Context{Req: req}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req.System), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("system: %s", req.System)
	}
	if !strings.Contains(string(req.Messages[0].Content), `"cache_control"`) {
		t.Fatalf("message: %s", req.Messages[0].Content)
	}
}

func TestRespectsClientCaching(t *testing.T) {
	sys := `[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}]`
	req := &anthropic.Request{
		System:   json.RawMessage(sys),
		Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	_ = New().Apply(context.Background(), &pipeline.Context{Req: req})
	if string(req.Messages[0].Content) != `"hi"` {
		t.Fatal("must not touch requests that already use cache_control")
	}
}

func TestTextMentioningCacheControlIsNotAMarker(t *testing.T) {
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"what is \"cache_control\"?"`)}},
	}
	if hasCacheControl(req) {
		t.Fatal("escaped text must not count as a cache_control key")
	}
}
