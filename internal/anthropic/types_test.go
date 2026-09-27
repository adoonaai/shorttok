package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoundTripKeepsUnknownFields(t *testing.T) {
	in := `{"model":"m","max_tokens":100,"metadata":{"user_id":"u"},"tools":[{"name":"x"}],"messages":[{"role":"user","content":"hi"}],"future_param":true}`
	var r Request
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"max_tokens":100`, `"user_id":"u"`, `"future_param":true`, `"tools"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("lost %s in %s", want, out)
		}
	}
}

func TestAppendSystemConvertsString(t *testing.T) {
	r := Request{System: json.RawMessage(`"base"`)}
	if err := r.AppendSystem(TextBlock("extra")); err != nil {
		t.Fatal(err)
	}
	blocks, _ := ParseBlocks(r.System)
	if len(blocks) != 2 || blocks[0].Text() != "base" || blocks[1].Text() != "extra" {
		t.Fatalf("got %s", r.System)
	}
}
