package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sch-version-updater/internal/model"
)

func TestBitbucketListsRepositoriesAndUsesBearerToken(t *testing.T) {
	var authorized bool
	var requestedPath string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		authorized = r.Header.Get("Authorization") == "Bearer token-value"
		requestedPath = r.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"values":[{"name":"Repo One","slug":"repo-one","project":{"key":"CASHREOPSC","name":"Cash Recon"},"links":{"clone":[{"name":"http","href":"https://host/repo.git"},{"name":"ssh","href":"ssh://git@host:7999/cashreopsc/repo-one.git"}]}}],"isLastPage":true}`))}, nil
	})
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("token-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewBitbucketClient("https://bitbucket.example/projects/CASHREOPSC", token)
	if err != nil {
		t.Fatal(err)
	}
	client.client = &http.Client{Transport: transport}
	repos, err := client.ListRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !authorized || requestedPath != "/rest/api/1.0/projects/CASHREOPSC/repos" || len(repos) != 1 || repos[0].HTTPSURL != "https://host/repo.git" {
		t.Fatalf("authorized=%v path=%q repos=%#v", authorized, requestedPath, repos)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type scheduleJobs struct {
	latest *time.Time
	err    error
}

func (j scheduleJobs) LatestCreatedAt(context.Context) (*time.Time, error) {
	return j.latest, j.err
}
func (scheduleJobs) UpsertDiscovered(context.Context, model.Job) (bool, error) {
	return false, nil
}

func TestInitialDelayUsesLatestCreatedAt(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	latest := now.Add(-5 * 24 * time.Hour)
	s := &Service{jobs: scheduleJobs{latest: &latest}, interval: 30 * 24 * time.Hour, now: func() time.Time { return now }}
	delay, gotLatest, err := s.initialDelay(context.Background())
	if err != nil || delay != 25*24*time.Hour || gotLatest == nil || !gotLatest.Equal(latest) {
		t.Fatalf("initialDelay() = %s, %v, %v", delay, gotLatest, err)
	}
}

func TestInitialDelayRunsImmediatelyWhenDueOrEmpty(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	for name, jobs := range map[string]scheduleJobs{
		"due":   {latest: timePointer(now.Add(-31 * 24 * time.Hour))},
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Service{jobs: jobs, interval: 30 * 24 * time.Hour, now: func() time.Time { return now }}
			delay, _, err := s.initialDelay(context.Background())
			if err != nil || delay != 0 {
				t.Fatalf("initialDelay() = %s, %v; want immediate", delay, err)
			}
		})
	}
}

func TestInitialDelayReturnsRepositoryError(t *testing.T) {
	want := errors.New("database unavailable")
	s := &Service{jobs: scheduleJobs{err: want}, interval: 30 * 24 * time.Hour, now: time.Now}
	if _, _, err := s.initialDelay(context.Background()); !errors.Is(err, want) {
		t.Fatalf("initialDelay() error = %v; want %v", err, want)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func TestDetectUsesLanguagePriorityAndCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.26.9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	target, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if target.Type != "GO" || target.FilePath != "go.mod" || target.Version != "1.26.9" {
		t.Fatalf("target=%#v", target)
	}
}

func TestDetectDockerImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM registry.local/team/app:2.4 AS runtime\n"), 0644); err != nil {
		t.Fatal(err)
	}
	target, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if target.Type != "DOCKER_IMAGE" || target.ImageName != "registry.local/team/app" || target.Version != "2.4" {
		t.Fatalf("target=%#v", target)
	}
}
