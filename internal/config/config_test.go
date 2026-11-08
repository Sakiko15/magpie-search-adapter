package config

import (
	"testing"
	"time"
)

func requiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ADAPTER_API_KEYS", "test-key")
	t.Setenv("MAGPIE_API_KEY", "sk-magpie-key-test")
	t.Setenv("MAGPIE_BASE_URL", "https://magpie.example.com")
}

func TestLoadRejectsMalformedInt(t *testing.T) {
	requiredEnv(t)
	t.Setenv("MAX_COUNT", "2O")
	if _, err := Load(); err == nil {
		t.Fatal("MAX_COUNT=2O must not silently fall back to the default")
	}
}

func TestLoadAllowsBudgetLargerThanRequestTimeout(t *testing.T) {
	requiredEnv(t)
	t.Setenv("REQUEST_TIMEOUT", "60s")
	t.Setenv("SEARCH_BUDGET", "180s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SearchBudget != 180*time.Second {
		t.Fatalf("budget = %s, want 180s", cfg.SearchBudget)
	}
}

func TestLoadDefaultsRequestTimeout(t *testing.T) {
	requiredEnv(t)
	t.Setenv("REQUEST_TIMEOUT", "")
	t.Setenv("SEARCH_BUDGET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestTimeout != 90*time.Second {
		t.Fatalf("request timeout = %s, want 90s", cfg.RequestTimeout)
	}
	if cfg.SearchBudget != 90*time.Second {
		t.Fatalf("budget = %s, want 90s", cfg.SearchBudget)
	}
}
