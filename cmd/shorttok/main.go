package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrey/shorttok/internal/anthropic"
	"github.com/andrey/shorttok/internal/config"
	"github.com/andrey/shorttok/internal/metrics"
	"github.com/andrey/shorttok/internal/optimizer/autocache"
	"github.com/andrey/shorttok/internal/optimizer/history"
	"github.com/andrey/shorttok/internal/pipeline"
	"github.com/andrey/shorttok/internal/pricing"
	"github.com/andrey/shorttok/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("bad config", "err", err)
		os.Exit(1)
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		log.Error("bad upstream url", "err", err)
		os.Exit(1)
	}

	client := &anthropic.Client{
		BaseURL: cfg.UpstreamURL,
		APIKey:  cfg.APIKey,
		HTTP:    &http.Client{Timeout: 10 * time.Minute}, // long generations
	}

	// Order matters: shrink first, then place cache breakpoints on the result.
	var opts []pipeline.Optimizer
	if cfg.HistoryEnabled {
		store := history.NewMemoryStore(cfg.HistoryCacheSize, cfg.HistoryCacheTTL)
		opts = append(opts, history.New(cfg.History, client, store))
	}
	if cfg.AutoCache {
		opts = append(opts, autocache.New())
	}

	prices := pricing.Default()
	if cfg.PricingFile != "" {
		if err := prices.LoadFile(cfg.PricingFile); err != nil {
			log.Error("cannot load pricing", "err", err)
			os.Exit(1)
		}
	}

	reg := metrics.NewRegistry()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/messages", &proxy.Handler{
		Client:   client,
		Pipeline: pipeline.New(log, opts...),
		Metrics:  metrics.NewProxy(reg),
		Log:      log,
		MaxBody:  cfg.MaxBodyBytes,
		Pricing:  prices,
		AuxModel: cfg.History.Model,
	})
	mux.Handle("GET /metrics", reg)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	// Everything else (count_tokens, models, batches...) is passed through as is.
	mux.Handle("/", &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			h := pr.Out.Header
			if cfg.APIKey != "" && h.Get("x-api-key") == "" && h.Get("authorization") == "" {
				h.Set("x-api-key", cfg.APIKey)
			}
		},
		FlushInterval: -1,
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: streamed responses can legitimately take minutes.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("shorttok listening", "addr", cfg.Listen, "upstream", cfg.UpstreamURL,
		"history", cfg.HistoryEnabled, "autocache", cfg.AutoCache)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
