// Package config loads booth-logging's runtime configuration from environment variables.
// Every value maps 1:1 to a Helm chart value/env var, mirroring the sibling modules'
// internal/config — there is no config file format of our own to version.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/projectbooth/booth-logging/internal/auth"
)

// DefaultRetention matches the chart's loki.retentionDays default (14). The chart always
// sets BOOTH_LOGGING_RETENTION from that same value, so this only matters for a local run.
const DefaultRetention = 14 * 24 * time.Hour

// Config is booth-logging's full runtime configuration.
type Config struct {
	HTTPAddr string

	// OIDC is the identity-provider configuration used to independently re-verify a
	// forwarded bearer token (core-platform-api.md's defense-in-depth requirement).
	OIDC auth.OIDCConfig

	// LokiURL is the in-release Loki the viewer queries.
	LokiURL string

	// Retention is Loki's retention period. Loki enforces it; this service only reports it
	// and uses it as the default query cap.
	Retention time.Duration

	// MaxQueryRange caps one query's time span; 0 means "same as Retention".
	MaxQueryRange time.Duration

	// AccessWorkspaces limits log access to owners of these workspaces; empty means an
	// owner of any workspace (docs/decisions/0002).
	AccessWorkspaces []string
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:         getEnv("BOOTH_HTTP_ADDR", ":8080"),
		LokiURL:          os.Getenv("BOOTH_LOKI_URL"),
		AccessWorkspaces: splitNonEmpty(os.Getenv("BOOTH_LOGGING_ACCESS_WORKSPACES")),
		OIDC: auth.OIDCConfig{
			IssuerURL:         os.Getenv("BOOTH_OIDC_ISSUER_URL"),
			ClientID:          os.Getenv("BOOTH_OIDC_CLIENT_ID"),
			RequireAudience:   os.Getenv("BOOTH_OIDC_REQUIRE_AUDIENCE") == "true",
			GroupsClaim:       getEnv("BOOTH_OIDC_GROUPS_CLAIM", auth.DefaultGroupsClaim),
			WorkloadIssuerURL: os.Getenv("BOOTH_WORKLOAD_ISSUER_URL"),
		},
	}

	var err error
	if cfg.Retention, err = durationEnv("BOOTH_LOGGING_RETENTION", DefaultRetention); err != nil {
		return Config{}, err
	}
	if cfg.MaxQueryRange, err = durationEnv("BOOTH_LOGGING_MAX_QUERY_RANGE", 0); err != nil {
		return Config{}, err
	}
	if cfg.Retention <= 0 {
		return Config{}, fmt.Errorf("BOOTH_LOGGING_RETENTION must be positive")
	}

	if cfg.OIDC.IssuerURL == "" {
		return Config{}, fmt.Errorf("BOOTH_OIDC_ISSUER_URL is required")
	}
	if cfg.OIDC.ClientID == "" {
		return Config{}, fmt.Errorf("BOOTH_OIDC_CLIENT_ID is required")
	}
	if cfg.LokiURL == "" {
		return Config{}, fmt.Errorf("BOOTH_LOKI_URL is required")
	}
	return cfg, nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s must be a non-negative Go duration such as 336h, got %q", key, v)
	}
	return d, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitNonEmpty(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
