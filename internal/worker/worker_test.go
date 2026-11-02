package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sch-version-updater/internal/model"
	"sch-version-updater/internal/updater"
)

type fakeRepo struct {
	claim                               bool
	claimErr, errorSuccess, errorFailed error
	success, failed                     int
}

func (f *fakeRepo) ListReady(context.Context, int) ([]model.Job, error) { return nil, nil }
func (f *fakeRepo) Claim(context.Context, int64) (bool, error)          { return f.claim, f.claimErr }
func (f *fakeRepo) MarkSuccess(context.Context, int64, string) error {
	f.success++
	return f.errorSuccess
}
func (f *fakeRepo) MarkFailed(context.Context, int64) error                { f.failed++; return f.errorFailed }
func (f *fakeRepo) RecoverStale(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeGit struct {
	prepareErr, diffErr, pushErr error
	changed                      []string
	preparedDir                  string
	pushed                       int
}

func (f *fakeGit) CloneBranch(context.Context, string, string, string) error { return nil }

func (f *fakeGit) Prepare(_ context.Context, _ model.Job, dir, _ string) error {
	f.preparedDir = dir
	if f.prepareErr == nil {
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, ".nvmrc"), []byte("18\n"), 0644)
	}
	return f.prepareErr
}
func (f *fakeGit) ChangedFiles(context.Context, string) ([]string, error) {
	if f.changed == nil {
		return []string{".nvmrc"}, f.diffErr
	}
	return f.changed, f.diffErr
}
func (f *fakeGit) CommitAndPush(context.Context, string, model.Job, string, []string) error {
	f.pushed++
	return f.pushErr
}
func testJob() model.Job {
	return model.Job{ID: 1, ProductID: "p", RepoURL: "git@bitbucket.org:w/r.git", BranchName: "main", FilePath: ".nvmrc", TargetType: "NODEJS", TargetVersion: "20"}
}
func logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeValidator struct {
	allowed []string
	err     error
}

func (f fakeValidator) ValidateAndFix(context.Context, string, model.Job) ([]string, error) {
	return f.allowed, f.err
}

func TestProcessSuccessAndCleanup(t *testing.T) {
	r := &fakeRepo{claim: true}
	g := &fakeGit{}
	root := t.TempDir()
	w := New(r, g, updater.New(), fakeValidator{}, root, time.Second, logger())
	w.Process(context.Background(), testJob())
	if r.success != 1 || r.failed != 0 || g.pushed != 1 {
		t.Fatalf("success=%d failed=%d pushed=%d", r.success, r.failed, g.pushed)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("workspace not cleaned: %v", entries)
	}
}
func TestNoChangeIsExplicitSuccess(t *testing.T) {
	r := &fakeRepo{claim: true}
	g := &fakeGit{changed: []string{}}
	root := t.TempDir()
	j := testJob()
	j.TargetVersion = "18"
	New(r, g, updater.New(), fakeValidator{}, root, time.Second, logger()).Process(context.Background(), j)
	if r.success != 1 || g.pushed != 0 {
		t.Fatalf("success=%d pushed=%d", r.success, g.pushed)
	}
}
func TestGitErrorMarksFailedAndCleans(t *testing.T) {
	r := &fakeRepo{claim: true}
	g := &fakeGit{prepareErr: errors.New("clone failed")}
	root := t.TempDir()
	New(r, g, updater.New(), fakeValidator{}, root, time.Second, logger()).Process(context.Background(), testJob())
	if r.failed != 1 || r.success != 0 {
		t.Fatalf("failed=%d success=%d", r.failed, r.success)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("workspace not cleaned")
	}
}
func TestClaimErrorDoesNotProcess(t *testing.T) {
	r := &fakeRepo{claimErr: errors.New("db")}
	g := &fakeGit{}
	New(r, g, updater.New(), fakeValidator{}, t.TempDir(), time.Second, logger()).Process(context.Background(), testJob())
	if g.preparedDir != "" || r.failed != 0 {
		t.Fatal("job should not process")
	}
}
func TestLostClaimDoesNotProcess(t *testing.T) {
	r := &fakeRepo{}
	g := &fakeGit{}
	New(r, g, updater.New(), fakeValidator{}, t.TempDir(), time.Second, logger()).Process(context.Background(), testJob())
	if g.preparedDir != "" {
		t.Fatal("job should not process")
	}
}

func TestDependencyValidationErrorMarksFailedWithoutPush(t *testing.T) {
	r := &fakeRepo{claim: true}
	g := &fakeGit{}
	v := fakeValidator{err: errors.New("vulnerability remains")}
	New(r, g, updater.New(), v, t.TempDir(), time.Second, logger()).Process(context.Background(), testJob())
	if r.failed != 1 || g.pushed != 0 {
		t.Fatalf("failed=%d pushed=%d", r.failed, g.pushed)
	}
}
