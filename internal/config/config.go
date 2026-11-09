package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds all runtime configuration, loaded from environment variables.
type Config struct {
	Addr          string        // HTTP listen address
	DataDir       string        // directory holding the SQLite file
	AdminPassword string        // admin password (required unless AdminToken set)
	AdminToken    string        // optional static API token instead of password
	DefaultRefresh time.Duration // default feed refresh interval
	FetchTimeout   time.Duration // per-article fetch timeout
	FetchJitter    time.Duration // max jitter added between per-host fetches
	FetchSpacing   time.Duration // minimum spacing between fetches to the same host
	UserAgent      string
	DBPath        string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Load reads configuration from the environment, applying defaults from the spec.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:           env("FTRSS_ADDR", "0.0.0.0:8080"),
		DataDir:        env("FTRSS_DATA_DIR", "/data"),
		AdminPassword:  os.Getenv("FTRSS_ADMIN_PASSWORD"),
		AdminToken:     os.Getenv("FTRSS_ADMIN_TOKEN"),
		DefaultRefresh: envDuration("FTRSS_DEFAULT_REFRESH", 60*time.Minute),
		FetchTimeout:   envDuration("FTRSS_FETCH_TIMEOUT", 20*time.Second),
		FetchJitter:    envDuration("FTRSS_FETCH_JITTER", 3*time.Second),
		FetchSpacing:   envDuration("FTRSS_FETCH_SPACING", 2*time.Second),
		UserAgent:      env("FTRSS_USER_AGENT", "full-text-rss/1.0 (+self-hosted; respectful fetcher)"),
	}
	if cfg.AdminPassword == "" && cfg.AdminToken == "" {
		return nil, fmt.Errorf("FTRSS_ADMIN_PASSWORD or FTRSS_ADMIN_TOKEN must be set")
	}
	cfg.DBPath = cfg.DataDir + "/fulltextrss.db"
	return cfg, nil
}
