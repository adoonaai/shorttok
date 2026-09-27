package pricing

import (
	"testing"

	"github.com/andrey/shorttok/internal/anthropic"
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
