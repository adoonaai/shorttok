package eval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adoonaai/shorttok/internal/pricing"
)

func TestRecall(t *testing.T) {
	if got := Recall("Servers in FRANKFURT, db is PostgreSQL 16", []string{"Frankfurt", "PostgreSQL 16", "$95"}); got < 0.66 || got > 0.67 {
		t.Fatalf("recall %.2f", got)
	}
	if Recall("x", nil) != -1 {
		t.Fatal("no expectations must give -1")
	}
}

func TestParseJudge(t *testing.T) {
	s, reason, err := parseJudge("```json\n{\"score\": 4, \"reason\": \"minor omission\"}\n```")
	if err != nil || s != 4 || reason != "minor omission" {
		t.Fatalf("%d %q %v", s, reason, err)
	}
	if _, _, err := parseJudge(`{"score": 9}`); err == nil {
		t.Fatal("out of range score must fail")
	}
}

func TestGeneratedConversationsAreValid(t *testing.T) {
	for _, it := range Generate(5, 8, "m", 1) {
		msgs := it.Messages
		if msgs[0].Role != "user" || msgs[len(msgs)-1].Role != "user" {
			t.Fatalf("%s: must start and end with user", it.ID)
		}
		for i := 1; i < len(msgs); i++ {
			if msgs[i].Role == msgs[i-1].Role {
				t.Fatalf("%s: roles must alternate at %d", it.ID, i)
			}
		}
		if len(it.Expect) != 5 {
			t.Fatalf("%s: expected 5 facts", it.ID)
		}
	}
}

// Fake proxy: the optimized path "forgets" the database and bills fewer tokens.
func TestRunnerComparesSides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		bypass := r.Header.Get(bypassHeader) == "1"
		answer, input := "Frankfurt, PostgreSQL 16, $95, June 2, Irina", 1000
		switch {
		case req.Model == "judge":
			answer = `{"score": 3, "reason": "database missing"}`
		case !bypass:
			answer, input = "Frankfurt, $95, June 2, Irina", 400
			w.Header().Set("X-Shorttok-Aux-Cost-Microusd", "100")
		}
		out, _ := json.Marshal(map[string]any{
			"content": []map[string]string{{"type": "text", "text": answer}},
			"usage":   map[string]int{"input_tokens": input, "output_tokens": 10},
		})
		_, _ = io.WriteString(w, string(out))
	}))
	defer srv.Close()

	it := Generate(1, 2, "claude-haiku-4-5-20251001", 1)[0]
	it.Expect = []string{"Frankfurt", "PostgreSQL 16", "$95", "June 2", "Irina"}
	r := &Runner{ProxyURL: srv.URL, HTTP: srv.Client(), Pricing: pricing.Default(), JudgeModel: "judge", Replay: true, Concurrency: 2}
	res := r.Run(context.Background(), []Item{it})[0]
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Base.Calls != 6 { // 5 user turns replayed + final answer = 6 calls
		t.Fatalf("calls %d", res.Base.Calls)
	}
	if res.Base.Recall != 1 || res.Opt.Recall != 0.8 || res.Score != 3 {
		t.Fatalf("recall %.2f/%.2f score %d", res.Base.Recall, res.Opt.Recall, res.Score)
	}
	s := Summarize([]Result{res})
	if s.OptMicro >= s.BaseMicro || s.AuxMicro != 600 {
		t.Fatalf("cost base=%d opt=%d aux=%d", s.BaseMicro, s.OptMicro, s.AuxMicro)
	}
	var sb strings.Builder
	WriteMarkdown(&sb, []Result{res}, s)
	if !strings.Contains(sb.String(), "Saved:") {
		t.Fatal("report missing summary")
	}
}
