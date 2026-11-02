package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadReadsDotEnv(t *testing.T) {
	keys := []string{
		"DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD",
		"SCHEDULER_INTERVAL_SECONDS", "WORKER_CONCURRENCY", "JOB_TIMEOUT_SECONDS",
		"RUNNING_STALE_AFTER_SECONDS", "GIT_PUSH_RETRIES", "GIT_USER_NAME", "GIT_USER_EMAIL",
		"BITBUCKET_PROJECT_URL", "BITBUCKET_API_TOKEN_PATH", "DISCOVERY_INTERVAL", "DISCOVERY_INTERVAL_SECONDS",
	}
	type oldValue struct {
		value string
		set   bool
	}
	old := make(map[string]oldValue, len(keys))
	for _, key := range keys {
		value, set := os.LookupEnv(key)
		old[key] = oldValue{value: value, set: set}
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if value := old[key]; value.set {
				_ = os.Setenv(key, value.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	})

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	dir := t.TempDir()
	content := "DB_HOST=localhost\nDB_NAME=jobs\nDB_USER=sa\nDB_PASSWORD=secret\n" +
		"BITBUCKET_API_TOKEN_PATH=/tmp/token\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBHost != "localhost" || cfg.DBName != "jobs" || cfg.BitbucketAPITokenPath != "/tmp/token" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.DiscoveryInterval != 30*24*time.Hour {
		t.Fatalf("DiscoveryInterval = %s, want 30 days", cfg.DiscoveryInterval)
	}
}

func TestParseDiscoveryDuration(t *testing.T) {
	tests := map[string]time.Duration{
		"45s":        45 * time.Second,
		"30m":        30 * time.Minute,
		"12h":        12 * time.Hour,
		"30d":        30 * 24 * time.Hour,
		"1d12h30m5s": 36*time.Hour + 30*time.Minute + 5*time.Second,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := parseDuration("DISCOVERY_INTERVAL", input)
			if err != nil || got != want {
				t.Fatalf("parseDuration(%q) = %s, %v; want %s", input, got, err, want)
			}
		})
	}
}

func TestParseDiscoveryDurationRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "0s", "30", "1w", "1.5h", "-1h"} {
		t.Run(input, func(t *testing.T) {
			if _, err := parseDuration("DISCOVERY_INTERVAL", input); err == nil {
				t.Fatalf("parseDuration(%q) unexpectedly succeeded", input)
			}
		})
	}
}
