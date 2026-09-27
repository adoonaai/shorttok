// Package pricing converts token usage into money.
//
// Prices are USD per million tokens, matched by the longest model-name
// prefix. Built-in defaults cover known models; newer models are added with
// a JSON file (SHORTTOK_PRICING_FILE). Costs are returned in micro-USD, which
// conveniently equals tokens * price-per-million.
package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/adoonaai/shorttok/internal/anthropic"
)

// Multipliers applied to the base input price (5-minute cache TTL), unless a
// model sets its own cache read price.
const (
	CacheReadMultiplier  = 0.1
	CacheWriteMultiplier = 1.25
)

type Price struct {
	Input     float64 `json:"input"`                // USD per 1M input tokens
	Output    float64 `json:"output"`               // USD per 1M output tokens
	CacheRead float64 `json:"cache_read,omitempty"` // USD per 1M cache hits; 0 = Input * CacheReadMultiplier
}

// InputMicro is the input-side cost of usage in micro-USD.
func (p Price) InputMicro(u anthropic.Usage) int {
	cacheRead := p.CacheRead
	if cacheRead == 0 {
		cacheRead = CacheReadMultiplier * p.Input
	}
	t := p.Input*float64(u.InputTokens) +
		cacheRead*float64(u.CacheReadInputTokens) +
		CacheWriteMultiplier*p.Input*float64(u.CacheCreationInputTokens)
	return int(math.Round(t))
}

// OutputMicro is the output cost of usage in micro-USD.
func (p Price) OutputMicro(u anthropic.Usage) int {
	return int(math.Round(float64(u.OutputTokens) * p.Output))
}

type Table struct{ prices map[string]Price }

// Default returns prices known at the time of writing.
// Check https://www.anthropic.com/pricing and extend via a pricing file.
func Default() *Table {
	return &Table{prices: map[string]Price{
		"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25},
		"claude-fable-5":    {Input: 10, Output: 50},
		"claude-mythos-5-1": {Input: 10, Output: 50},
		"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20},
		"claude-opus-5":     {Input: 5, Output: 25},
		"claude-opus-4-8":   {Input: 5, Output: 25},
		"claude-sonnet-5":   {Input: 2, Output: 10},
		"claude-sonnet-4-6": {Input: 3, Output: 15},
		"claude-haiku-4-5":  {Input: 1, Output: 5},
		"claude-sonnet-4":   {Input: 3, Output: 15},
		"claude-opus-4-5":   {Input: 5, Output: 25},
		"claude-opus-4-6":   {Input: 5, Output: 25},
		"claude-opus-4-7":   {Input: 5, Output: 25},
		"claude-opus-4-1":   {Input: 15, Output: 75},
		"claude-opus-4-2":   {Input: 15, Output: 75}, // claude-opus-4-2025xxxx
		"claude-3-5-haiku":  {Input: 0.8, Output: 4},
		"claude-3-haiku":    {Input: 0.25, Output: 1.25},
		"claude-3-7-sonnet": {Input: 3, Output: 15},
	}}
}

// LoadFile merges {"<model prefix>": {"input": x, "output": y}, ...} over the table.
func (t *Table) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]Price
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range m {
		t.prices[k] = v
	}
	return nil
}

// Lookup finds the price for a model by the longest matching prefix.
func (t *Table) Lookup(model string) (Price, bool) {
	best, found := "", false
	for prefix := range t.prices {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best, found = prefix, true
		}
	}
	return t.prices[best], found
}
