package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/config"
	"github.com/eiffelbeef/jelly-diff/internal/jellyfin"
	"github.com/eiffelbeef/jelly-diff/internal/notify"
	"github.com/eiffelbeef/jelly-diff/internal/server"
	"github.com/eiffelbeef/jelly-diff/internal/storage"
	syncworker "github.com/eiffelbeef/jelly-diff/internal/sync"
	webfs "github.com/eiffelbeef/jelly-diff/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	// Configure structured logger
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	// Open storage
	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		slog.Error("storage open failed", "err", err)
		os.Exit(1)
	}
	defer store.Close() //nolint:errcheck

	// Jellyfin client
	client := jellyfin.NewClient(cfg.JellyfinURL, cfg.JellyfinAPIKey)

	// Notifier
	var notifier notify.Notifier = notify.NoOp{}
	if cfg.WebhookURL != "" {
		notifier = notify.NewWebhook(cfg.WebhookURL, cfg.WebhookType)
	}

	// Sync worker
	w := syncworker.NewWorker(cfg, client, store, notifier)

	// HTTP server
	srv := server.New(cfg, store, w, client, webfs.FS)
	httpServer := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      srv,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start sync worker
	go w.Start(ctx)

	// Start HTTP server
	slog.Info("starting jelly-diff", "addr", cfg.ListenAddr)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := httpServer.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}
