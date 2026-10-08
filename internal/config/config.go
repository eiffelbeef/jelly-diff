package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	JellyfinURL    string
	JellyfinAPIKey string
	SyncInterval    time.Duration
	MinSyncInterval time.Duration
	DBPath          string
	ListenAddr      string
	WebhookURL      string
	WebhookType     string
	LogLevel        string
}

// Load reads configuration from the environment, optionally seeded by a .env file.
func Load() (*Config, error) {
	loadDotEnv(".env")

	cfg := &Config{
		JellyfinURL:    os.Getenv("JELLYFIN_URL"),
		JellyfinAPIKey: os.Getenv("JELLYFIN_API_KEY"),
		DBPath:         envDefault("DB_PATH", "/data/jelly-diff.db"),
		ListenAddr:     envDefault("LISTEN_ADDR", ":6363"),
		WebhookURL:     os.Getenv("WEBHOOK_URL"),
		WebhookType:    envDefault("WEBHOOK_TYPE", "generic"),
		LogLevel:       envDefault("LOG_LEVEL", "info"),
	}

	d, err := time.ParseDuration(envDefault("SYNC_INTERVAL", "1h"))
	if err != nil {
		return nil, fmt.Errorf("invalid SYNC_INTERVAL: %w", err)
	}
	cfg.SyncInterval = d

	minInterval, err := time.ParseDuration(envDefault("MIN_SYNC_INTERVAL", "10m"))
	if err != nil {
		return nil, fmt.Errorf("invalid MIN_SYNC_INTERVAL: %w", err)
	}
	cfg.MinSyncInterval = minInterval

	if cfg.JellyfinURL == "" {
		return nil, fmt.Errorf("JELLYFIN_URL is required")
	}
	if cfg.JellyfinAPIKey == "" {
		return nil, fmt.Errorf("JELLYFIN_API_KEY is required")
	}
	return cfg, nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadDotEnv reads key=value pairs from path and sets them as env vars (only
// if not already set), ignoring missing files and malformed lines.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, val) //nolint:errcheck
		}
	}
}
