package model

import "time"

type JobStatus string

const (
	StatusQueued        JobStatus = "queued"
	StatusRunning       JobStatus = "running"
	StatusNeedsInput    JobStatus = "needs_input"
	StatusNeedsApproval JobStatus = "needs_approval"
	StatusNeedsReview   JobStatus = "needs_review"
	StatusDone          JobStatus = "done"
	StatusFailed        JobStatus = "failed"
	StatusAborted       JobStatus = "aborted"
)

type Job struct {
	ID             string
	Repo           string
	BaseBranch     string
	Prompt         string
	Status         JobStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
	SlackChannelID string
	SlackThreadTS  string
	SlackUserID    string
	WorkspacePath  string
	JobBranch      string
	LastInput      string
	LastError      string
	LastDiffStat   string
	PRURL          string
}

type JobEvent struct {
	ID        int64
	JobID     string
	Type      string
	Payload   string
	CreatedAt time.Time
}

func IsTerminalStatus(s JobStatus) bool {
	switch s {
	case StatusDone, StatusFailed, StatusAborted:
		return true
	default:
		return false
	}
}
