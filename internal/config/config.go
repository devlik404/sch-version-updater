package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	DBHost, DBPort, DBName, DBUser, DBPassword string
	SchedulerInterval, JobTimeout, StaleAfter  time.Duration
	WorkerConcurrency, PushRetries             int
	GitUserName, GitUserEmail                  string
	BitbucketProjectURL, BitbucketAPITokenPath string
	DiscoveryInterval                          time.Duration
}

func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	c := Config{
		DBHost: os.Getenv("DB_HOST"), DBPort: value("DB_PORT", "1433"), DBName: os.Getenv("DB_NAME"),
		DBUser: os.Getenv("DB_USER"), DBPassword: os.Getenv("DB_PASSWORD"),
		GitUserName: value("GIT_USER_NAME", "SCH Version Updater"), GitUserEmail: value("GIT_USER_EMAIL", "sch-version-updater@localhost"),
		BitbucketProjectURL: os.Getenv("BITBUCKET_PROJECT_URL"), BitbucketAPITokenPath: os.Getenv("BITBUCKET_API_TOKEN_PATH"),
	}
	var err error
	if c.SchedulerInterval, err = seconds("SCHEDULER_INTERVAL_SECONDS", 60); err != nil {
		return Config{}, err
	}
	if c.JobTimeout, err = seconds("JOB_TIMEOUT_SECONDS", 600); err != nil {
		return Config{}, err
	}
	if c.StaleAfter, err = seconds("RUNNING_STALE_AFTER_SECONDS", 1800); err != nil {
		return Config{}, err
	}
	if c.WorkerConcurrency, err = integer("WORKER_CONCURRENCY", 1); err != nil {
		return Config{}, err
	}
	if c.PushRetries, err = integer("GIT_PUSH_RETRIES", 3); err != nil {
		return Config{}, err
	}
	if c.DiscoveryInterval, err = discoveryInterval(); err != nil {
		return Config{}, err
	}
	if c.StaleAfter <= c.JobTimeout {
		return Config{}, fmt.Errorf("RUNNING_STALE_AFTER_SECONDS must be greater than JOB_TIMEOUT_SECONDS")
	}
	for name, v := range map[string]string{"DB_HOST": c.DBHost, "DB_NAME": c.DBName, "DB_USER": c.DBUser, "DB_PASSWORD": c.DBPassword, "BITBUCKET_API_TOKEN_PATH": c.BitbucketAPITokenPath} {
		if v == "" {
			return Config{}, fmt.Errorf("%s is required", name)
		}
	}
	return c, nil
}

func (c Config) DatabaseURL() string {
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(c.DBUser, c.DBPassword), Host: c.DBHost + ":" + c.DBPort}
	q := u.Query()
	q.Set("database", c.DBName)
	u.RawQuery = q.Encode()
	return u.String()
}

func value(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func integer(k string, fallback int) (int, error) {
	v, err := strconv.Atoi(value(k, strconv.Itoa(fallback)))
	if err != nil || v < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", k)
	}
	return v, nil
}
func seconds(k string, fallback int) (time.Duration, error) {
	v, err := integer(k, fallback)
	return time.Duration(v) * time.Second, err
}

func discoveryInterval() (time.Duration, error) {
	if raw := os.Getenv("DISCOVERY_INTERVAL"); raw != "" {
		return parseDuration("DISCOVERY_INTERVAL", raw)
	}
	if raw := os.Getenv("DISCOVERY_INTERVAL_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 {
			return 0, fmt.Errorf("DISCOVERY_INTERVAL_SECONDS must be a positive integer")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	return 30 * 24 * time.Hour, nil
}

// parseDuration accepts positive integer components using d, h, m, and s,
// for example 30d, 12h, or 1d12h30m.
func parseDuration(name, raw string) (time.Duration, error) {
	if raw == "" {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	var total time.Duration
	for pos := 0; pos < len(raw); {
		start := pos
		for pos < len(raw) && raw[pos] >= '0' && raw[pos] <= '9' {
			pos++
		}
		if start == pos || pos == len(raw) {
			return 0, fmt.Errorf("%s must use d, h, m, or s, for example 30d or 1d12h30m", name)
		}
		value, err := strconv.ParseInt(raw[start:pos], 10, 64)
		if err != nil || value < 1 {
			return 0, fmt.Errorf("%s must be a positive duration", name)
		}
		var unit time.Duration
		switch raw[pos] {
		case 'd':
			unit = 24 * time.Hour
		case 'h':
			unit = time.Hour
		case 'm':
			unit = time.Minute
		case 's':
			unit = time.Second
		default:
			return 0, fmt.Errorf("%s must use d, h, m, or s, for example 30d or 1d12h30m", name)
		}
		pos++
		const maxDuration = time.Duration(1<<63 - 1)
		if value > int64(maxDuration/unit) {
			return 0, fmt.Errorf("%s is too large", name)
		}
		component := time.Duration(value) * unit
		if total > maxDuration-component {
			return 0, fmt.Errorf("%s is too large", name)
		}
		total += component
	}
	return total, nil
}
