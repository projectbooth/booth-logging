package config

import (
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Setenv("BOOTH_OIDC_ISSUER_URL", "https://idp.example/realms/booth")
	t.Setenv("BOOTH_OIDC_CLIENT_ID", "booth-logging")
	t.Setenv("BOOTH_LOKI_URL", "http://loki:3100")
}

func TestLoad_Defaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention != 14*24*time.Hour {
		t.Errorf("Retention = %v, want 336h (the documented v0 default)", cfg.Retention)
	}
	if cfg.MaxQueryRange != 0 || cfg.HTTPAddr != ":8080" || cfg.OIDC.GroupsClaim != "groups" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoad_Overrides(t *testing.T) {
	setRequired(t)
	t.Setenv("BOOTH_LOGGING_RETENTION", "168h")
	t.Setenv("BOOTH_LOGGING_MAX_QUERY_RANGE", "24h")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention != 168*time.Hour || cfg.MaxQueryRange != 24*time.Hour {
		t.Errorf("durations = %v / %v", cfg.Retention, cfg.MaxQueryRange)
	}
}

func TestLoad_Errors(t *testing.T) {
	for name, env := range map[string][2]string{
		"missing loki url":   {"BOOTH_LOKI_URL", ""},
		"missing issuer":     {"BOOTH_OIDC_ISSUER_URL", ""},
		"bad retention":      {"BOOTH_LOGGING_RETENTION", "14 days"},
		"zero retention":     {"BOOTH_LOGGING_RETENTION", "0s"},
		"negative max range": {"BOOTH_LOGGING_MAX_QUERY_RANGE", "-1h"},
	} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(env[0], env[1])
			if _, err := Load(); err == nil {
				t.Errorf("Load succeeded with %s=%q", env[0], env[1])
			}
		})
	}
}
