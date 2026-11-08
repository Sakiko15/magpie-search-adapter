package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"magpie-search-adapter/internal/config"
	"magpie-search-adapter/internal/httpserver"
	"magpie-search-adapter/internal/magpie"
	"magpie-search-adapter/internal/resilience"
)

func main() {
	selfcheck := flag.Bool("selfcheck", false, "check the Magpie key and a web search, then exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	client, err := magpie.NewClient(magpie.Options{
		BaseURL:        cfg.MagpieBaseURL,
		APIKey:         cfg.MagpieAPIKey,
		MCPPath:        cfg.MCPPath,
		SearchMode:     cfg.SearchMode,
		AlphaModel:     cfg.AlphaModel,
		FetchMode:      cfg.FetchMode,
		InitTimeout:    cfg.InitTimeout,
		RequestTimeout: cfg.RequestTimeout,
		FetchTimeout:   cfg.FetchTimeout,
		MaxCount:       cfg.MaxCount,
		Logger:         logger,
	})
	if err != nil {
		logger.Error("magpie client configuration error", "error", err)
		os.Exit(2)
	}
	defer client.Close()

	if *selfcheck {
		os.Exit(runSelfCheck(client, cfg.RequestTimeout, logger))
	}

	srv, err := httpserver.New(cfg, resilience.Wrap(client, resilience.Config{
		Retries:   cfg.UpstreamRetries,
		BaseDelay: cfg.RetryBaseDelay,
		MaxDelay:  cfg.RetryMaxDelay,
		Budget:    cfg.SearchBudget,
		CacheTTL:  cfg.SearchCacheTTL,
		CacheSize: cfg.SearchCacheSize,
		Coalesce:  true,
	}, logger), client.Ready, logger)
	if err != nil {
		logger.Error("server configuration error", "error", err)
		os.Exit(2)
	}
	logger.Info("magpie-search-adapter starting",
		"listen", cfg.ListenAddr,
		"magpie", cfg.MagpieBaseURL,
		"search_mode", cfg.SearchMode,
		"fetch_mode", cfg.FetchMode,
		"max_count", cfg.MaxCount,
		"upstream_retries", cfg.UpstreamRetries,
		"search_cache_ttl", cfg.SearchCacheTTL,
	)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}

func runSelfCheck(client *magpie.Client, requestTimeout time.Duration, logger *slog.Logger) int {
	// Ready may run a probe search in alpha mode; allow one full timeout for
	// it and one for the selfcheck search, with margin.
	timeout := 2*requestTimeout + 30*time.Second
	if timeout < 2*time.Minute {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := client.Ready(ctx); err != nil {
		logger.Error("selfcheck: magpie is not ready", "error", err)
		return 1
	}
	logger.Info("selfcheck: magpie ready", "mode", client.Mode())
	results, err := client.Search(ctx, "magpie search adapter selfcheck", 1, nil)
	if err != nil {
		logger.Error("selfcheck: web search failed", "error", err)
		return 1
	}
	logger.Info("selfcheck: web search ok", "results", len(results))
	if len(results) == 0 {
		logger.Warn("selfcheck: search returned no results; check Magpie's Web search settings")
	}
	return 0
}
