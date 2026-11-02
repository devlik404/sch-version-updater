package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sch-version-updater/internal/model"
)

type Jobs interface {
	ListReady(context.Context, int) ([]model.Job, error)
	Claim(context.Context, int64) (bool, error)
	MarkSuccess(context.Context, int64, string) error
	MarkFailed(context.Context, int64) error
	RecoverStale(context.Context, time.Time) (int64, error)
}

type DiscoveryJobs interface {
	LatestCreatedAt(context.Context) (*time.Time, error)
	UpsertDiscovered(context.Context, model.Job) (bool, error)
}

type SQLServer struct{ db *sql.DB }

func NewSQLServer(db *sql.DB) *SQLServer { return &SQLServer{db: db} }

const selectColumns = `id, product_id, product_name, job_name, job_type, repo_url, branch_name, file_path, target_type, COALESCE(image_name,''), COALESCE(current_version,''), COALESCE(target_version,''), is_active, flag_done, job_status, created_at, last_run_at`

func (r *SQLServer) ListReady(ctx context.Context, limit int) ([]model.Job, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT TOP (@p1) `+selectColumns+` FROM dbo.VersionUpdateJob WITH (READPAST) WHERE is_active = 1 AND flag_done = 0 AND target_version IS NOT NULL AND LTRIM(RTRIM(target_version)) <> '' ORDER BY id`, limit)
	if err != nil {
		return nil, fmt.Errorf("list ready jobs: %w", err)
	}
	defer rows.Close()
	var jobs []model.Job
	for rows.Next() {
		var j model.Job
		if err := rows.Scan(&j.ID, &j.ProductID, &j.ProductName, &j.JobName, &j.JobType, &j.RepoURL, &j.BranchName, &j.FilePath, &j.TargetType, &j.ImageName, &j.CurrentVersion, &j.TargetVersion, &j.IsActive, &j.FlagDone, &j.JobStatus, &j.CreatedAt, &j.LastRunAt); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (r *SQLServer) Claim(ctx context.Context, id int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE dbo.VersionUpdateJob SET flag_done = 1, job_status = 'RUNNING', last_run_at = SYSDATETIME() WHERE id = @p1 AND is_active = 1 AND flag_done = 0 AND target_version IS NOT NULL AND LTRIM(RTRIM(target_version)) <> ''`, id)
	if err != nil {
		return false, fmt.Errorf("claim job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func (r *SQLServer) MarkSuccess(ctx context.Context, id int64, targetVersion string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE dbo.VersionUpdateJob SET current_version = @p2, target_version = NULL, flag_done = 3, job_status = 'SUCCESS' WHERE id = @p1 AND flag_done = 1 AND job_status = 'RUNNING'`, id, targetVersion)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("job %d state changed concurrently", id)
	}
	return nil
}
func (r *SQLServer) MarkFailed(ctx context.Context, id int64) error {
	return r.transition(ctx, id, `UPDATE dbo.VersionUpdateJob SET flag_done = 2, job_status = 'FAILED' WHERE id = @p1 AND flag_done = 1 AND job_status = 'RUNNING'`)
}
func (r *SQLServer) transition(ctx context.Context, id int64, query string) error {
	res, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("job %d state changed concurrently", id)
	}
	return nil
}
func (r *SQLServer) RecoverStale(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE dbo.VersionUpdateJob SET flag_done = 2, job_status = 'FAILED' WHERE flag_done = 1 AND job_status = 'RUNNING' AND last_run_at < @p1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("recover stale jobs: %w", err)
	}
	return res.RowsAffected()
}

func (r *SQLServer) UpsertDiscovered(ctx context.Context, job model.Job) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
IF NOT EXISTS (SELECT 1 FROM dbo.VersionUpdateJob WITH (UPDLOCK, HOLDLOCK) WHERE repo_url = @p1 AND branch_name = @p2 AND file_path = @p3 AND target_type = @p4)
BEGIN
    INSERT dbo.VersionUpdateJob (product_id, product_name, job_name, job_type, repo_url, branch_name, file_path, target_type, image_name, current_version, target_version, is_active, flag_done, job_status)
    VALUES (@p5, @p6, @p7, @p8, @p1, @p2, @p3, @p4, @p9, @p10, NULL, 0, 0, 'PENDING')
END`, job.RepoURL, job.BranchName, job.FilePath, job.TargetType, job.ProductID, job.ProductName, job.JobName, job.JobType, nullable(job.ImageName), nullable(job.CurrentVersion))
	if err != nil {
		return false, fmt.Errorf("upsert discovered repository %q: %w", job.RepoURL, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *SQLServer) LatestCreatedAt(ctx context.Context) (*time.Time, error) {
	var latest sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(created_at) FROM dbo.VersionUpdateJob`).Scan(&latest); err != nil {
		return nil, fmt.Errorf("read latest job creation time: %w", err)
	}
	if !latest.Valid {
		return nil, nil
	}
	return &latest.Time, nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
