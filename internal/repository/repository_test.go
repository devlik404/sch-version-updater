package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"sch-version-updater/internal/model"
)

func setup(t *testing.T) (*SQLServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewSQLServer(db), mock
}
func TestListReady(t *testing.T) {
	r, m := setup(t)
	now := time.Now()
	rows := sqlmock.NewRows([]string{"id", "product_id", "product_name", "job_name", "job_type", "repo_url", "branch_name", "file_path", "target_type", "image_name", "current_version", "target_version", "is_active", "flag_done", "job_status", "created_at", "last_run_at"}).AddRow(1, "p1", "Product", "job", "VERSION", "git@bitbucket.org:w/r.git", "main", "go.mod", "GO", "", "1.21", "1.22", true, 0, "PENDING", now, nil)
	m.ExpectQuery(`SELECT TOP \(@p1\).*flag_done = 0 AND target_version IS NOT NULL AND LTRIM\(RTRIM\(target_version\)\) <> ''`).WithArgs(5).WillReturnRows(rows)
	jobs, err := r.ListReady(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 1 {
		t.Fatalf("got %#v", jobs)
	}
}
func TestAtomicClaim(t *testing.T) {
	r, m := setup(t)
	q := regexp.QuoteMeta("UPDATE dbo.VersionUpdateJob SET flag_done = 1, job_status = 'RUNNING', last_run_at = SYSDATETIME() WHERE id = @p1 AND is_active = 1 AND flag_done = 0 AND target_version IS NOT NULL AND LTRIM(RTRIM(target_version)) <> ''")
	m.ExpectExec(q).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	ok, err := r.Claim(context.Background(), 7)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	m.ExpectExec(q).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	ok, err = r.Claim(context.Background(), 7)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
func TestTransitionsAndRecovery(t *testing.T) {
	r, m := setup(t)
	m.ExpectExec("SET current_version = @p2, target_version = NULL, flag_done = 3").WithArgs(int64(2), "1.2.3").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := r.MarkSuccess(context.Background(), 2, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	m.ExpectExec("SET flag_done = 2").WithArgs(int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := r.MarkFailed(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now()
	m.ExpectExec("last_run_at < @p1").WithArgs(cutoff).WillReturnResult(sqlmock.NewResult(0, 2))
	n, err := r.RecoverStale(context.Background(), cutoff)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
func TestDatabaseErrors(t *testing.T) {
	r, m := setup(t)
	m.ExpectExec("UPDATE dbo.VersionUpdateJob SET flag_done = 1").WithArgs(int64(1)).WillReturnError(errors.New("db down"))
	if _, err := r.Claim(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpsertDiscoveredCreatesInactiveJob(t *testing.T) {
	r, m := setup(t)
	j := model.Job{ProductID: "CASHREOPSC", ProductName: "Cash Recon", JobName: "repo", JobType: "VERSION_UPDATE", RepoURL: "ssh://git@host:7999/p/r.git", BranchName: "release/development", FilePath: "go.mod", TargetType: "GO", CurrentVersion: "1.26.9"}
	m.ExpectExec("IF NOT EXISTS").WithArgs(j.RepoURL, j.BranchName, j.FilePath, j.TargetType, j.ProductID, j.ProductName, j.JobName, j.JobType, nil, j.CurrentVersion).WillReturnResult(sqlmock.NewResult(0, 1))
	created, err := r.UpsertDiscovered(context.Background(), j)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
}

func TestLatestCreatedAt(t *testing.T) {
	r, m := setup(t)
	want := time.Date(2026, time.October, 15, 10, 0, 0, 0, time.UTC)
	m.ExpectQuery(`SELECT MAX\(created_at\) FROM dbo.VersionUpdateJob`).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(want))
	got, err := r.LatestCreatedAt(context.Background())
	if err != nil || got == nil || !got.Equal(want) {
		t.Fatalf("LatestCreatedAt() = %v, %v; want %v", got, err, want)
	}

	m.ExpectQuery(`SELECT MAX\(created_at\) FROM dbo.VersionUpdateJob`).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(nil))
	got, err = r.LatestCreatedAt(context.Background())
	if err != nil || got != nil {
		t.Fatalf("LatestCreatedAt() for empty table = %v, %v; want nil", got, err)
	}
}
