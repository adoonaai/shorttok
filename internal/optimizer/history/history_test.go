package history

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/adoonaai/shorttok/internal/anthropic"
	"github.com/adoonaai/shorttok/internal/pipeline"
)

type fakeSum struct{ prompts []string }

func (f *fakeSum) Complete(_ context.Context, _ http.Header, _, _, prompt string, _ int) (string, anthropic.Usage, error) {
	f.prompts = append(f.prompts, prompt)
	return fmt.Sprintf("summary#%d", len(f.prompts)), anthropic.Usage{InputTokens: 100, OutputTokens: 20}, nil
}

func text(role, s string) anthropic.Message {
	c, _ := json.Marshal(s)
	return anthropic.Message{Role: role, Content: c}
}

func blocks(role string, raw string) anthropic.Message {
	return anthropic.Message{Role: role, Content: json.RawMessage(raw)}
}

func conv(n int) []anthropic.Message {
	msgs := make([]anthropic.Message, n)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = text(role, fmt.Sprintf("message %d: %s", i, strings.Repeat("lorem ipsum ", 50)))
	}
	return msgs
}

func run(t *testing.T, o *Optimizer, msgs []anthropic.Message) *pipeline.Context {
	t.Helper()
	rc := &pipeline.Context{
		Req:     &anthropic.Request{Model: "m", System: json.RawMessage(`"You are helpful."`), Messages: msgs},
		Headers: http.Header{"X-Api-Key": {"k"}},
	}
	pipeline.New(slog.New(slog.NewTextHandler(io.Discard, nil)), o).Run(context.Background(), rc)
	if err := rc.Report.Steps[0].Err; err != nil {
		t.Fatalf("optimizer error: %v", err)
	}
	return rc
}

func TestIncrementalSummaries(t *testing.T) {
	sum := &fakeSum{}
	o := New(Config{Model: "haiku", KeepLast: 4, Chunk: 4, MinMessages: 8}, sum, NewMemoryStore(100, time.Hour))

	// 9 messages: cut at 4, first summary.
	rc := run(t, o, conv(9))
	if len(sum.prompts) != 1 || len(rc.Req.Messages) != 5 {
		t.Fatalf("calls=%d kept=%d", len(sum.prompts), len(rc.Req.Messages))
	}
	if !strings.Contains(string(rc.Req.System), "summary#1") || !strings.Contains(string(rc.Req.System), "You are helpful.") {
		t.Fatalf("system not rewritten: %s", rc.Req.System)
	}

	// 11 messages: cut still at 4, summary comes from cache.
	run(t, o, conv(11))
	if len(sum.prompts) != 1 {
		t.Fatalf("expected cache hit, got %d calls", len(sum.prompts))
	}

	// 13 messages: cut moves to 8, only messages 4..7 are summarized, on top of summary#1.
	rc = run(t, o, conv(13))
	if len(sum.prompts) != 2 {
		t.Fatalf("expected second call, got %d", len(sum.prompts))
	}
	p := sum.prompts[1]
	if !strings.Contains(p, "summary#1") || !strings.Contains(p, "message 4:") || strings.Contains(p, "message 3:") {
		t.Fatalf("not incremental: %s", p[:200])
	}
	if rc.Report.AuxUsage.InputTokens != 100 {
		t.Fatalf("aux usage not reported: %+v", rc.Report.AuxUsage)
	}
}

func TestSplitPointRespectsToolPairs(t *testing.T) {
	msgs := []anthropic.Message{
		text("user", "q"),
		text("assistant", "a"),
		text("user", "q2"),
		blocks("assistant", `[{"type":"tool_use","id":"t1","name":"f","input":{}}]`),
		blocks("user", `[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]`),
		text("assistant", "done"),
		text("user", "q3"),
	}
	// keepLast=3 -> k=4 is a tool_result, must move back to 2.
	if k := SplitPoint(msgs, 3, 1); k != 2 {
		t.Fatalf("k=%d, want 2", k)
	}
}

func TestShortConversationUntouched(t *testing.T) {
	sum := &fakeSum{}
	o := New(Config{Model: "haiku", KeepLast: 4, Chunk: 4, MinMessages: 8}, sum, NewMemoryStore(10, time.Hour))
	rc := run(t, o, conv(5))
	if len(sum.prompts) != 0 || len(rc.Req.Messages) != 5 {
		t.Fatal("short conversation must not be touched")
	}
}

func TestTenantsDoNotShareSummaries(t *testing.T) {
	a := prefixHashes("seed-a", conv(3))
	b := prefixHashes("seed-b", conv(3))
	if a[3] == b[3] {
		t.Fatal("different seeds must give different keys")
	}
}
