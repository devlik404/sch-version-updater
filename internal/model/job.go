package model

import "time"

type Job struct {
	ID                                        int64
	ProductID, ProductName, JobName, JobType  string
	RepoURL, BranchName, FilePath, TargetType string
	ImageName, CurrentVersion                 string
	TargetVersion                             string
	IsActive                                  bool
	FlagDone                                  uint8
	JobStatus                                 string
	CreatedAt                                 time.Time
	LastRunAt                                 *time.Time
}

const (
	StatusPending = "PENDING"
	StatusRunning = "RUNNING"
	StatusFailed  = "FAILED"
	StatusSuccess = "SUCCESS"
)
