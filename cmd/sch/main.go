package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/microsoft/go-mssqldb"

	"sch-version-updater/internal/config"
	"sch-version-updater/internal/discovery"
	"sch-version-updater/internal/gitservice"
	"sch-version-updater/internal/repository"
	"sch-version-updater/internal/scheduler"
	"sch-version-updater/internal/updater"
	"sch-version-updater/internal/validation"
	"sch-version-updater/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("sqlserver", cfg.DatabaseURL())
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := db.PingContext(ctx); err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}

	repo := repository.NewSQLServer(db)
	git := gitservice.New(cfg.BitbucketAPITokenPath, cfg.GitUserName, cfg.GitUserEmail, cfg.PushRetries)
	if cfg.BitbucketProjectURL != "" {
		api, err := discovery.NewBitbucketClient(cfg.BitbucketProjectURL, cfg.BitbucketAPITokenPath)
		if err != nil {
			logger.Error("configure repository discovery", "error", err)
			os.Exit(1)
		}
		d := discovery.New(api, repo, git, os.TempDir(), cfg.DiscoveryInterval, cfg.JobTimeout, logger)
		go d.Run(ctx)
	}
	w := worker.New(repo, git, updater.New(), validation.New(), os.TempDir(), cfg.JobTimeout, logger)
	s := scheduler.New(repo, w, cfg.SchedulerInterval, cfg.StaleAfter, cfg.WorkerConcurrency, logger)
	if err := s.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("scheduler stopped", "error", err)
		os.Exit(1)
	}
}
