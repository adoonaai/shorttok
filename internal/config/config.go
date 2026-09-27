// Package config loads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/adoonaai/shorttok/internal/optimizer/history"
)

type Config struct {
	Listen       string
	UpstreamURL  string
	APIKey       string // optional fallback when clients send no key
	MaxBodyBytes int64
	LogLevel     string
	PricingFile  string
	CountTimeout time.Duration // 0 disables exact token counting

	AutoCache bool

	HistoryEnabled   bool
	History          history.Config
	HistoryCacheSize int
	HistoryCacheTTL  time.Duration
}

func Load() (Config, error) {
	e := &env{}
	c := Config{
		Listen:       e.str("SHORTTOK_LISTEN", ":8080"),
		UpstreamURL:  e.str("SHORTTOK_UPSTREAM", "https://api.anthropic.com"),
		APIKey:       e.str("ANTHROPIC_API_KEY", ""),
		MaxBodyBytes: int64(e.int("SHORTTOK_MAX_BODY_MB", 32)) << 20,
		LogLevel:     e.str("SHORTTOK_LOG_LEVEL", "info"),
		PricingFile:  e.str("SHORTTOK_PRICING_FILE", ""),
		CountTimeout: e.dur("SHORTTOK_COUNT_TIMEOUT", 3*time.Second),

		AutoCache: e.bool("SHORTTOK_AUTOCACHE", true),

		HistoryEnabled: e.bool("SHORTTOK_HISTORY", true),
		History: history.Config{
			Model:            e.str("SHORTTOK_HISTORY_MODEL", "claude-haiku-4-5-20251001"),
			KeepLast:         e.int("SHORTTOK_HISTORY_KEEP_LAST", 6),
			Chunk:            e.int("SHORTTOK_HISTORY_CHUNK", 8),
			MinMessages:      e.int("SHORTTOK_HISTORY_MIN_MESSAGES", 12),
			MinTokens:        e.int("SHORTTOK_HISTORY_MIN_TOKENS", 2000),
			MaxSummaryTokens: e.int("SHORTTOK_HISTORY_MAX_SUMMARY_TOKENS", 1024),
		},
		HistoryCacheSize: e.int("SHORTTOK_HISTORY_CACHE_SIZE", 10000),
		HistoryCacheTTL:  e.dur("SHORTTOK_HISTORY_CACHE_TTL", 24*time.Hour),
	}
	if c.History.KeepLast < 2 {
		e.errs = append(e.errs, errors.New("SHORTTOK_HISTORY_KEEP_LAST must be >= 2"))
	}
	return c, errors.Join(e.errs...)
}

type env struct{ errs []error }

func (e *env) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func (e *env) int(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", key, err))
	}
	return n
}

func (e *env) bool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", key, err))
	}
	return b
}

func (e *env) dur(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", key, err))
	}
	return d
}
