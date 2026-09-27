// Package anthropic contains the minimal subset of the Anthropic Messages API
// that the proxy needs. Everything it does not understand is kept as raw JSON,
// so new API parameters pass through untouched.
package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Request is a /v1/messages request body.
type Request struct {
	Model    string
	System   json.RawMessage // string or []Block; nil if absent
	Messages []Message
	Stream   bool

	// Extra holds all other top-level fields (max_tokens, tools, metadata...) verbatim.
	Extra map[string]json.RawMessage
}

var knownFields = [...]string{"model", "system", "messages", "stream"}

func (r *Request) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if v, ok := m["model"]; ok {
		if err := json.Unmarshal(v, &r.Model); err != nil {
			return fmt.Errorf("model: %w", err)
		}
	}
	if v, ok := m["messages"]; ok {
		if err := json.Unmarshal(v, &r.Messages); err != nil {
			return fmt.Errorf("messages: %w", err)
		}
	}
	if v, ok := m["stream"]; ok {
		if err := json.Unmarshal(v, &r.Stream); err != nil {
			return fmt.Errorf("stream: %w", err)
		}
	}
	r.System = m["system"]
	for _, k := range knownFields {
		delete(m, k)
	}
	r.Extra = m
	return nil
}

func (r Request) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(r.Extra)+4)
	for k, v := range r.Extra {
		m[k] = v
	}
	m["model"] = r.Model
	m["messages"] = r.Messages
	if len(r.System) > 0 {
		m["system"] = r.System
	}
	if r.Stream {
		m["stream"] = true
	}
	return json.Marshal(m)
}

// Clone returns a deep copy of the request.
func (r *Request) Clone() (*Request, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var c Request
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// AppendSystem adds a block to the end of the system prompt,
// converting a string system prompt into block form if needed.
func (r *Request) AppendSystem(b Block) error {
	blocks, err := ParseBlocks(r.System)
	if err != nil {
		return fmt.Errorf("system: %w", err)
	}
	r.System = EncodeBlocks(append(blocks, b))
	return nil
}

// Message is a single conversation turn.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Blocks returns the message content as blocks, whatever form it was sent in.
func (m Message) Blocks() ([]Block, error) { return ParseBlocks(m.Content) }

// Block is a content block kept as raw JSON fields, so unknown block types
// and fields survive a parse/encode round trip.
type Block map[string]json.RawMessage

// TextBlock builds a {"type":"text"} block.
func TextBlock(text string) Block {
	return Block{"type": mustJSON("text"), "text": mustJSON(text)}
}

// Str returns a string field of the block, or "" if absent or not a string.
func (b Block) Str(key string) string {
	var s string
	_ = json.Unmarshal(b[key], &s)
	return s
}

func (b Block) Type() string { return b.Str("type") }
func (b Block) Text() string { return b.Str("text") }

// ParseBlocks accepts either a JSON string or an array of content blocks.
func ParseBlocks(raw json.RawMessage) ([]Block, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return []Block{TextBlock(s)}, nil
	}
	var blocks []Block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// EncodeBlocks serializes blocks back to JSON.
func EncodeBlocks(blocks []Block) json.RawMessage {
	data, err := json.Marshal(blocks)
	if err != nil {
		panic(err) // cannot happen: blocks hold already-valid JSON
	}
	return data
}

// Usage is the token accounting returned by the API.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
