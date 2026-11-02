package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sch-version-updater/internal/gitservice"
	"sch-version-updater/internal/model"
	"sch-version-updater/internal/repository"
	"sch-version-updater/internal/updater"
	"sch-version-updater/internal/validation"
)

type Processor interface {
	Process(context.Context, model.Job)
}
type Worker struct {
	repo      repository.Jobs
	git       gitservice.Service
	updater   updater.Service
	validator validation.Service
	tempRoot  string
	timeout   time.Duration
	logger    *slog.Logger
	now       func() time.Time
}

func New(repo repository.Jobs, git gitservice.Service, up updater.Service, validator validation.Service, tempRoot string, timeout time.Duration, logger *slog.Logger) *Worker {
	return &Worker{repo: repo, git: git, updater: up, validator: validator, tempRoot: tempRoot, timeout: timeout, logger: logger, now: time.Now}
}

func (w *Worker) Process(parent context.Context, job model.Job) {
	start := w.now()
	log := w.logger.With("job_id", job.ID, "product_id", job.ProductID, "repo", job.RepoURL)
	claimed, err := w.repo.Claim(parent, job.ID)
	if err != nil {
		log.Error("job claim failed", "error", err)
		return
	}
	if !claimed {
		log.Info("job skipped because claim was lost")
		return
	}
	ctx, cancel := context.WithTimeout(parent, w.timeout)
	defer cancel()
	err = w.execute(ctx, job, log)
	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer finalCancel()
	if err != nil {
		if markErr := w.repo.MarkFailed(finalCtx, job.ID); markErr != nil {
			log.Error("job failed and status update failed", "error", err, "status_error", markErr, "duration", time.Since(start))
			return
		}
		log.Error("job failed", "error", err, "duration", time.Since(start))
		return
	}
	if err := w.repo.MarkSuccess(finalCtx, job.ID, job.TargetVersion); err != nil {
		log.Error("job completed but success status update failed", "error", err, "duration", time.Since(start))
		return
	}
	log.Info("job succeeded", "duration", time.Since(start))
}

func (w *Worker) execute(ctx context.Context, job model.Job, log *slog.Logger) error {
	if err := validateJob(job); err != nil {
		return err
	}
	jobRoot, err := os.MkdirTemp(w.tempRoot, fmt.Sprintf("version-job-%d-", job.ID))
	if err != nil {
		return fmt.Errorf("create job workspace: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(jobRoot); err != nil {
			log.Error("workspace cleanup failed", "path", jobRoot, "error", err)
		}
	}()
	repoDir := filepath.Join(jobRoot, "repository")
	branch := fmt.Sprintf("chore/version-update-%d-%s", job.ID, w.now().UTC().Format("20060102-150405"))
	log.Info("preparing repository", "stage", "clone", "branch", branch)
	if err := w.git.Prepare(ctx, job, repoDir, branch); err != nil {
		return err
	}
	noChange := false
	if err := w.updater.Update(repoDir, job); err != nil {
		if errors.Is(err, updater.ErrNoChange) {
			log.Info("no change; target state already present", "stage", "update")
			noChange = true
		} else {
			return fmt.Errorf("update target: %w", err)
		}
	}
	log.Info("validating dependencies", "stage", "dependency_validation", "target_type", job.TargetType)
	allowed, err := w.validator.ValidateAndFix(ctx, repoDir, job)
	if err != nil {
		return fmt.Errorf("dependency validation: %w", err)
	}
	changed, err := w.git.ChangedFiles(ctx, repoDir)
	if err != nil {
		return err
	}
	if len(changed) == 0 && noChange {
		return nil
	}
	allowed = append(allowed, job.FilePath)
	if err := validateChangedFiles(changed, allowed); err != nil {
		return err
	}
	if len(changed) == 0 {
		return errors.New("updater reported a change but git diff is empty")
	}
	log.Info("committing repository update", "stage", "push", "branch", branch)
	if err := w.git.CommitAndPush(ctx, repoDir, job, branch, changed); err != nil {
		return err
	}
	return nil
}

func validateChangedFiles(changed, allowed []string) error {
	set := make(map[string]bool, len(allowed))
	for _, file := range allowed {
		set[filepath.ToSlash(filepath.Clean(file))] = true
	}
	for _, file := range changed {
		if !set[filepath.ToSlash(filepath.Clean(file))] {
			return fmt.Errorf("unexpected changed file %q; allowed files are %v", file, allowed)
		}
	}
	return nil
}

func validateJob(job model.Job) error {
	for name, value := range map[string]string{"repo_url": job.RepoURL, "branch_name": job.BranchName, "file_path": job.FilePath, "target_type": job.TargetType, "target_version": job.TargetVersion} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if strings.HasPrefix(job.BranchName, "-") {
		return errors.New("branch_name must not start with '-'")
	}
	if strings.ContainsAny(job.BranchName, "\r\n \t") {
		return errors.New("branch_name contains invalid whitespace")
	}
	return nil
}
