package jobs

import "github.com/rodrwan/slack-codex/internal/model"

var allowedTransitions = map[model.JobStatus]map[model.JobStatus]bool{
	model.StatusQueued: {
		model.StatusRunning: true,
		model.StatusAborted: true,
	},
	model.StatusRunning: {
		model.StatusNeedsInput:    true,
		model.StatusNeedsApproval: true,
		model.StatusNeedsReview:   true,
		model.StatusFailed:        true,
		model.StatusAborted:       true,
	},
	model.StatusNeedsInput: {
		model.StatusQueued:  true,
		model.StatusAborted: true,
	},
	model.StatusNeedsApproval: {
		model.StatusQueued:  true,
		model.StatusAborted: true,
	},
	model.StatusNeedsReview: {
		model.StatusDone:    true,
		model.StatusAborted: true,
		model.StatusFailed:  true,
	},
}

func CanTransition(from, to model.JobStatus) bool {
	if from == to {
		return true
	}
	if next, ok := allowedTransitions[from]; ok {
		return next[to]
	}
	return false
}
