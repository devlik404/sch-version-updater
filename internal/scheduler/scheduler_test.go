package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"sch-version-updater/internal/model"
)

type scheduleRepo struct {
	jobs      []model.Job
	recovered bool
}

func (r *scheduleRepo) ListReady(context.Context, int) ([]model.Job, error) { return r.jobs, nil }
func (r *scheduleRepo) Claim(context.Context, int64) (bool, error)          { return true, nil }
func (r *scheduleRepo) MarkSuccess(context.Context, int64, string) error    { return nil }
func (r *scheduleRepo) MarkFailed(context.Context, int64) error             { return nil }
func (r *scheduleRepo) RecoverStale(context.Context, time.Time) (int64, error) {
	r.recovered = true
	return 1, nil
}

type concurrencyProcessor struct {
	mu          sync.Mutex
	current     int
	maximum     int
	processed   int
	releaseJobs chan struct{}
}

func (p *concurrencyProcessor) Process(context.Context, model.Job) {
	p.mu.Lock()
	p.current++
	if p.current > p.maximum {
		p.maximum = p.current
	}
	p.mu.Unlock()
	<-p.releaseJobs
	p.mu.Lock()
	p.current--
	p.processed++
	p.mu.Unlock()
}

func TestTickRecoversAndLimitsConcurrency(t *testing.T) {
	repo := &scheduleRepo{jobs: []model.Job{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}}
	processor := &concurrencyProcessor{releaseJobs: make(chan struct{}, 4)}
	for range 4 {
		processor.releaseJobs <- struct{}{}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(repo, processor, time.Second, 2*time.Second, 2, logger)
	if err := s.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repo.recovered || processor.processed != 4 {
		t.Fatalf("recovered=%v processed=%d", repo.recovered, processor.processed)
	}
	if processor.maximum > 2 {
		t.Fatalf("maximum concurrency=%d", processor.maximum)
	}
}
