// Package eval measures what the proxy saves and what it costs in quality.
//
// Every conversation is sent twice through the same proxy: once with the
// bypass header (baseline) and once optimized. Quality is measured two ways:
// fact recall (deterministic, against known facts) and an LLM judge that
// compares the optimized answer with the baseline answer.
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/adoonaai/shorttok/internal/anthropic"
)

// Item is one line of the JSONL dataset.
type Item struct {
	ID        string              `json:"id"`
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []anthropic.Message `json:"messages"`
	// Expect lists facts the final answer must mention (case-insensitive).
	Expect []string `json:"expect,omitempty"`
}

func Load(path string) ([]Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var items []Item
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var it Item
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if len(it.Messages) == 0 || it.Messages[len(it.Messages)-1].Role != "user" {
			return nil, fmt.Errorf("%s:%d: conversation must end with a user message", path, n)
		}
		if it.MaxTokens == 0 {
			it.MaxTokens = 512
		}
		items = append(items, it)
	}
	return items, sc.Err()
}

func Save(path string, items []Item) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// Recall is the share of expected facts found in the answer.
// Returns -1 when the item has no expectations.
func Recall(answer string, expect []string) float64 {
	if len(expect) == 0 {
		return -1
	}
	a := strings.ToLower(answer)
	hit := 0
	for _, e := range expect {
		if strings.Contains(a, strings.ToLower(e)) {
			hit++
		}
	}
	return float64(hit) / float64(len(expect))
}
