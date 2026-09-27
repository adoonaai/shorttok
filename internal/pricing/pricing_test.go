package pricing

import (
	"testing"

	"github.com/adoonaai/shorttok/internal/anthropic"
)

func TestLongestPrefixWins(t *testing.T) {
	tb := Default()
	if p, _ := tb.Lookup("claude-opus-4-1-20250805"); p.Input != 15 {
		t.Fatalf("opus 4.1: %+v", p)
	}
	if p, _ := tb.Lookup("claude-opus-4-5-20251101"); p.Input != 5 {
		t.Fatalf("opus 4.5: %+v", p)
	}
	if _, ok := tb.Lookup("gpt-4o"); ok {
		t.Fatal("unknown model must not match")
	}
}

func TestCacheMultipliers(t *testing.T) {
	p := Price{Input: 3, Output: 15}
	u := anthropic.Usage{InputTokens: 100, CacheReadInputTokens: 1000, CacheCreationInputTokens: 400, OutputTokens: 10}
	// (100 + 0.1*1000 + 1.25*400) * 3 = 2100
	if got := p.InputMicro(u); got != 2100 {
		t.Fatalf("input cost %d", got)
	}
	if got := p.OutputMicro(u); got != 150 {
		t.Fatalf("output cost %d", got)
	}
}

func TestCurrentModels(t *testing.T) {
	tb := Default()
	for model, want := range map[string]float64{
		"claude-fable-5-1": 10,
		"claude-opus-5-5":  4, // must not fall back to the claude-opus-5 prefix
		"claude-opus-5":    5,
		"claude-sonnet-5":  2,
	} {
		if p, ok := tb.Lookup(model); !ok || p.Input != want {
			t.Errorf("%s: %+v, %v", model, p, ok)
		}
	}
}

func TestCustomCacheReadPrice(t *testing.T) {
	p := Price{Input: 4, Output: 20, CacheRead: 0.2}
	u := anthropic.Usage{InputTokens: 100, CacheReadInputTokens: 1000}
	// 4*100 + 0.2*1000 = 600, not the default 4*100 + 0.4*1000 = 800
	if got := p.InputMicro(u); got != 600 {
		t.Fatalf("input cost %d", got)
	}
}
