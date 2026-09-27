// Package autocache places Anthropic prompt-cache breakpoints automatically:
// one at the end of the system prompt and one at the end of the last message.
// With the second one each turn reads the previous turn's prefix from cache.
//
// Cache writes cost more than normal input (1.25x for the 5-minute TTL),
// so this pays off for multi-turn chats and repeated prompts, not for
// one-shot requests. Hence it can be switched off in config.
package autocache

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/andrey/shorttok/internal/anthropic"
	"github.com/andrey/shorttok/internal/pipeline"
)

var ephemeral = json.RawMessage(`{"type":"ephemeral"}`)

type Optimizer struct{}

func New() *Optimizer { return &Optimizer{} }

func (*Optimizer) Name() string { return "autocache" }

func (*Optimizer) Apply(_ context.Context, rc *pipeline.Context) error {
	req := rc.Req
	if hasCacheControl(req) {
		return nil // the client manages caching itself
	}

	sys, err := anthropic.ParseBlocks(req.System)
	if err != nil {
		return err
	}
	if i := lastCacheable(sys); i >= 0 {
		sys[i]["cache_control"] = ephemeral
		req.System = anthropic.EncodeBlocks(sys)
	}

	if n := len(req.Messages); n > 0 {
		m := &req.Messages[n-1]
		blocks, err := m.Blocks()
		if err != nil {
			return err
		}
		if i := lastCacheable(blocks); i >= 0 {
			blocks[i]["cache_control"] = ephemeral
			m.Content = anthropic.EncodeBlocks(blocks)
		}
	}
	return nil
}

// A quoted "cache_control" can only appear in raw JSON as an object key:
// inside string values the quotes would be escaped.
var marker = []byte(`"cache_control"`)

func hasCacheControl(r *anthropic.Request) bool {
	if bytes.Contains(r.System, marker) {
		return true
	}
	if _, ok := r.Extra["cache_control"]; ok {
		return true
	}
	for _, m := range r.Messages {
		if bytes.Contains(m.Content, marker) {
			return true
		}
	}
	return false
}

func lastCacheable(blocks []anthropic.Block) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		switch blocks[i].Type() {
		case "thinking", "redacted_thinking":
			continue
		case "text":
			if blocks[i].Text() == "" {
				continue
			}
		}
		return i
	}
	return -1
}
