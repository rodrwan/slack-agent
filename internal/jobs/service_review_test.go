package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/rodrwan/slack-codex/internal/model"
)

type reviewTestStore struct {
	job    model.Job
	events []string
}

func (s *reviewTestStore) CreateJob(j model.Job) error {
	s.job = j
	return nil
}

func (s *reviewTestStore) GetJob(jobID string) (model.Job, error) {
	return s.job, nil
}

func (s *reviewTestStore) UpdateJob(j model.Job) error {
	s.job = j
	return nil
}

func (s *reviewTestStore) AddEvent(_ string, eventType, _ string) error {
	s.events = append(s.events, eventType)
	return nil
}

func (s *reviewTestStore) ListRecoverableJobs() ([]model.Job, error) {
	return nil, nil
}

type reviewTestNotifier struct {
	lastSummary string
}

func (n *reviewTestNotifier) PostJobStatus(_ context.Context, _ model.Job, summary string, _ []map[string]any) error {
	n.lastSummary = summary
	return nil
}

func (n *reviewTestNotifier) PostJobLog(_ context.Context, _ model.Job, _ string) error {
	return nil
}

type reviewTestGit struct {
	diffStat     string
	commitCalled bool
	pushCalled   bool
}

func (g *reviewTestGit) PrepareWorkspace(_ context.Context, _, _, _ string) (Workspace, error) {
	return Workspace{}, nil
}

func (g *reviewTestGit) DiffStat(_ context.Context, _ string) (string, error) {
	return g.diffStat, nil
}

func (g *reviewTestGit) CommitAll(_ context.Context, _, _ string) error {
	g.commitCalled = true
	return nil
}

func (g *reviewTestGit) PushBranch(_ context.Context, _, _ string) error {
	g.pushCalled = true
	return nil
}

type reviewTestGitHub struct{}

func (g *reviewTestGitHub) CreatePR(_ context.Context, _, _, _, _, _ string) (string, error) {
	return "https://example.com/pr/1", nil
}

func TestHasMeaningfulDiff(t *testing.T) {
	if hasMeaningfulDiff("") {
		t.Fatalf("empty diff must not be meaningful")
	}
	if hasMeaningfulDiff("   ") {
		t.Fatalf("blank diff must not be meaningful")
	}
	if hasMeaningfulDiff("(sin cambios detectados)") {
		t.Fatalf("placeholder diff must not be meaningful")
	}
	if !hasMeaningfulDiff(" file.go | 2 +-") {
		t.Fatalf("non-empty git stat should be meaningful")
	}
}

func TestApproveAndCreatePR_BlocksWhenNoDiff(t *testing.T) {
	store := &reviewTestStore{
		job: model.Job{
			ID:            "job_1",
			Status:        model.StatusNeedsReview,
			WorkspacePath: "/tmp/w",
			JobBranch:     "codex/job_1",
			BaseBranch:    "main",
			Repo:          "org/repo",
		},
	}
	notifier := &reviewTestNotifier{}
	git := &reviewTestGit{diffStat: ""}
	svc := &Service{
		store:    store,
		notifier: notifier,
		git:      git,
		github:   &reviewTestGitHub{},
	}

	if err := svc.ApproveAndCreatePR("job_1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.job.Status != model.StatusNeedsInput {
		t.Fatalf("expected status needs_input, got %s", store.job.Status)
	}
	if git.commitCalled {
		t.Fatalf("commit should not run when diff is empty")
	}
	if git.pushCalled {
		t.Fatalf("push should not run when diff is empty")
	}
	if !strings.Contains(notifier.lastSummary, "No detecté cambios") {
		t.Fatalf("unexpected notifier summary: %q", notifier.lastSummary)
	}
}
