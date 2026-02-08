package jobs

import (
	"testing"

	"github.com/rodrwan/slack-codex/internal/model"
)

func TestCanTransition(t *testing.T) {
	if !CanTransition(model.StatusQueued, model.StatusRunning) {
		t.Fatalf("queued -> running should be allowed")
	}
	if CanTransition(model.StatusDone, model.StatusRunning) {
		t.Fatalf("done -> running should not be allowed")
	}
	if !CanTransition(model.StatusNeedsReview, model.StatusDone) {
		t.Fatalf("needs_review -> done should be allowed")
	}
}
