package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sch-version-updater/internal/model"
	"sch-version-updater/internal/repository"
)

type Processor interface {
	Process(context.Context, model.Job)
}
type Scheduler struct {
	repo                 repository.Jobs
	worker               Processor
	interval, staleAfter time.Duration
	concurrency          int
	logger               *slog.Logger
}

func New(repo repository.Jobs, worker Processor, interval, staleAfter time.Duration, concurrency int, logger *slog.Logger) *Scheduler {
	return &Scheduler{repo: repo, worker: worker, interval: interval, staleAfter: staleAfter, concurrency: concurrency, logger: logger}
}
func (s *Scheduler) Run(ctx context.Context) error {
	if s.concurrency < 1 || s.interval <= 0 || s.staleAfter <= 0 {
		return fmt.Errorf("invalid scheduler configuration")
	}
	if err := s.tick(ctx); err != nil {
		s.logger.Error("scheduler tick failed", "error", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.logger.Error("scheduler tick failed", "error", err)
			}
		}
	}
}
func (s *Scheduler) tick(ctx context.Context) error {
	recovered, err := s.repo.RecoverStale(ctx, time.Now().Add(-s.staleAfter))
	if err != nil {
		return err
	}
	if recovered > 0 {
		s.logger.Warn("recovered stale running jobs", "count", recovered)
	}
	jobs, err := s.repo.ListReady(ctx, s.concurrency*2)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(j model.Job) { defer wg.Done(); defer func() { <-sem }(); s.worker.Process(ctx, j) }(job)
	}
	wg.Wait()
	return nil
}
