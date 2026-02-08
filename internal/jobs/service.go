package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rodrwan/slack-codex/internal/model"
	"github.com/rodrwan/slack-codex/internal/policy"
	"github.com/rodrwan/slack-codex/internal/runner"
)

type Store interface {
	CreateJob(j model.Job) error
	GetJob(jobID string) (model.Job, error)
	UpdateJob(j model.Job) error
	AddEvent(jobID, eventType, payload string) error
	ListRecoverableJobs() ([]model.Job, error)
}

type SlackNotifier interface {
	PostJobStatus(ctx context.Context, j model.Job, summary string, actions []map[string]any) error
	PostJobLog(ctx context.Context, j model.Job, text string) error
}

type GitManager interface {
	PrepareWorkspace(ctx context.Context, jobID, repo, baseBranch string) (Workspace, error)
	DiffStat(ctx context.Context, workspace string) (string, error)
	CommitAll(ctx context.Context, workspace, message string) error
	PushBranch(ctx context.Context, workspace, branch string) error
}

type Workspace struct {
	Path      string
	JobBranch string
}

type GitHubClient interface {
	CreatePR(ctx context.Context, repo, title, head, base, body string) (string, error)
}

type Service struct {
	store       Store
	notifier    SlackNotifier
	runner      *runner.Runner
	policy      *policy.Engine
	git         GitManager
	github      GitHubClient
	codexCmd    string
	inactivity  time.Duration
	execTimeout time.Duration

	queue    chan string
	repoLock sync.Map
}

func NewService(store Store, notifier SlackNotifier, run *runner.Runner, pol *policy.Engine, gm GitManager, gh GitHubClient, codexCmd string, inactivity, execTimeout time.Duration, workers int) *Service {
	if workers < 1 {
		workers = 1
	}
	s := &Service{
		store:       store,
		notifier:    notifier,
		runner:      run,
		policy:      pol,
		git:         gm,
		github:      gh,
		codexCmd:    codexCmd,
		inactivity:  inactivity,
		execTimeout: execTimeout,
		queue:       make(chan string, 128),
	}
	for i := 0; i < workers; i++ {
		go s.worker()
	}
	return s
}

func (s *Service) RecoverPendingJobs() {
	jobs, err := s.store.ListRecoverableJobs()
	if err != nil {
		log.Printf("recover jobs failed: %v", err)
		return
	}
	for _, j := range jobs {
		if j.Status == model.StatusQueued || j.Status == model.StatusRunning {
			s.Enqueue(j.ID)
		}
	}
}

func (s *Service) CreateAndQueueJob(_ context.Context, repo, baseBranch, prompt, channelID, userID string) (model.Job, error) {
	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())
	j := model.Job{
		ID:             jobID,
		Repo:           repo,
		BaseBranch:     baseBranch,
		Prompt:         prompt,
		Status:         model.StatusQueued,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		SlackChannelID: channelID,
		SlackUserID:    userID,
	}
	if err := s.store.CreateJob(j); err != nil {
		return model.Job{}, err
	}
	if err := s.store.AddEvent(j.ID, "job_created", prompt); err != nil {
		log.Printf("add event failed: %v", err)
	}

	// Avoid blocking slash-command acknowledgement on outbound Slack API latency.
	go func(job model.Job) {
		postCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := s.notifier.PostJobStatus(postCtx, job, "Job creado y en cola.", nil); err != nil {
			log.Printf("notify queued failed: %v", err)
		}
	}(j)
	stored, err := s.store.GetJob(jobID)
	if err == nil && stored.SlackThreadTS == "" {
		stored.SlackThreadTS = j.SlackThreadTS
		s.store.UpdateJob(stored)
	}
	s.Enqueue(jobID)
	return j, nil
}

func (s *Service) Enqueue(jobID string) {
	select {
	case s.queue <- jobID:
	default:
		go func() { s.queue <- jobID }()
	}
}

func (s *Service) worker() {
	for jobID := range s.queue {
		if err := s.runJob(context.Background(), jobID); err != nil {
			log.Printf("job %s failed: %v", jobID, err)
		}
	}
}

func (s *Service) runJob(ctx context.Context, jobID string) error {
	j, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if model.IsTerminalStatus(j.Status) {
		return nil
	}
	if j.Status == model.StatusNeedsApproval || j.Status == model.StatusNeedsReview {
		return nil
	}

	mu := s.repoMutex(j.Repo)
	mu.Lock()
	defer mu.Unlock()

	j.Status = model.StatusRunning
	if err := s.store.UpdateJob(j); err != nil {
		return err
	}
	s.notifier.PostJobStatus(ctx, j, "Ejecutando Codex en workspace aislado.", nil)

	if j.WorkspacePath == "" {
		ws, err := s.git.PrepareWorkspace(ctx, j.ID, j.Repo, j.BaseBranch)
		if err != nil {
			return s.failJob(ctx, j, fmt.Errorf("prepare workspace: %w", err))
		}
		j.WorkspacePath = ws.Path
		j.JobBranch = ws.JobBranch
		if err := s.store.UpdateJob(j); err != nil {
			return err
		}
	}

	command := strings.TrimSpace(s.codexCmd + " " + shellQuote(j.Prompt+"\n"+j.LastInput))
	pd := s.policy.Evaluate(command)
	switch pd.Decision {
	case policy.Deny:
		return s.failJob(ctx, j, errors.New(pd.Reason))
	case policy.NeedsApproval:
		j.Status = model.StatusNeedsApproval
		j.LastError = pd.Reason
		if err := s.store.UpdateJob(j); err != nil {
			return err
		}
		actions := []map[string]any{
			{"type": "button", "action_id": "allow_once", "text": map[string]any{"type": "plain_text", "text": "Allow once"}, "value": j.ID, "style": "primary"},
			{"type": "button", "action_id": "deny", "text": map[string]any{"type": "plain_text", "text": "Deny"}, "value": j.ID, "style": "danger"},
		}
		return s.notifier.PostJobStatus(ctx, j, "Acción requiere aprobación: "+pd.Reason, actions)
	}

	res, runErr := s.runner.Run(ctx, runner.Spec{
		WorkspacePath:     j.WorkspacePath,
		Command:           command,
		InactivityTimeout: s.inactivity,
		ExecutionTimeout:  s.execTimeout,
	}, func(line string) {
		if strings.TrimSpace(line) != "" {
			s.notifier.PostJobLog(context.Background(), j, line)
		}
	})

	if res.CombinedOutput != "" {
		s.store.AddEvent(j.ID, "runner_output", trimForStore(res.CombinedOutput, 8000))
	}

	if errors.Is(runErr, runner.ErrNeedsInput) || res.NeedsInput {
		j.Status = model.StatusNeedsInput
		j.LastError = "runner quedó esperando input"
		if err := s.store.UpdateJob(j); err != nil {
			return err
		}
		actions := []map[string]any{
			{"type": "button", "action_id": "resume_default", "text": map[string]any{"type": "plain_text", "text": "Resume"}, "value": j.ID, "style": "primary"},
			{"type": "button", "action_id": "abort", "text": map[string]any{"type": "plain_text", "text": "Abort"}, "value": j.ID, "style": "danger"},
		}
		return s.notifier.PostJobStatus(ctx, j, "Codex necesita input. Responde en el thread con contexto adicional y luego pulsa Resume.", actions)
	}
	if runErr != nil || res.ExitErr != nil {
		if runErr != nil {
			return s.failJob(ctx, j, runErr)
		}
		return s.failJob(ctx, j, res.ExitErr)
	}

	diff, err := s.git.DiffStat(ctx, j.WorkspacePath)
	if err != nil {
		diff = "(sin cambios detectados)"
	}
	j.LastDiffStat = diff
	j.Status = model.StatusNeedsReview
	if err := s.store.UpdateJob(j); err != nil {
		return err
	}
	actions := []map[string]any{
		{"type": "button", "action_id": "run_tests", "text": map[string]any{"type": "plain_text", "text": "Run tests"}, "value": j.ID},
		{"type": "button", "action_id": "approve_pr", "text": map[string]any{"type": "plain_text", "text": "Approve & Create PR"}, "value": j.ID, "style": "primary"},
		{"type": "button", "action_id": "abort", "text": map[string]any{"type": "plain_text", "text": "Abort"}, "value": j.ID, "style": "danger"},
	}
	return s.notifier.PostJobStatus(ctx, j, "Diff listo para revisión:\n```"+diff+"```", actions)
}

func (s *Service) failJob(ctx context.Context, j model.Job, err error) error {
	j.Status = model.StatusFailed
	j.LastError = err.Error()
	if upErr := s.store.UpdateJob(j); upErr != nil {
		return upErr
	}
	s.store.AddEvent(j.ID, "job_failed", j.LastError)
	s.notifier.PostJobStatus(ctx, j, "Job falló: `"+j.LastError+"`", nil)
	return err
}

func (s *Service) repoMutex(repo string) *sync.Mutex {
	mu, _ := s.repoLock.LoadOrStore(repo, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'"
}

func trimForStore(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

func (s *Service) HandleThreadInput(jobID, text string) error {
	j, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if j.Status != model.StatusNeedsInput {
		return nil
	}
	j.LastInput = strings.TrimSpace(text)
	j.Status = model.StatusQueued
	if err := s.store.UpdateJob(j); err != nil {
		return err
	}
	s.store.AddEvent(j.ID, "input_received", trimForStore(text, 1000))
	s.Enqueue(j.ID)
	return nil
}

func (s *Service) AllowOnce(jobID string) error {
	j, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if j.Status != model.StatusNeedsApproval {
		return nil
	}
	j.Status = model.StatusQueued
	j.LastError = ""
	if err := s.store.UpdateJob(j); err != nil {
		return err
	}
	s.store.AddEvent(j.ID, "approval_granted", "allow_once")
	s.Enqueue(j.ID)
	return nil
}

func (s *Service) Abort(jobID string) error {
	j, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if model.IsTerminalStatus(j.Status) {
		return nil
	}
	j.Status = model.StatusAborted
	if err := s.store.UpdateJob(j); err != nil {
		return err
	}
	s.store.AddEvent(j.ID, "job_aborted", "aborted by user")
	return s.notifier.PostJobStatus(context.Background(), j, "Job abortado por usuario.", nil)
}

func (s *Service) RunTests(jobID string) error {
	j, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if j.WorkspacePath == "" {
		return fmt.Errorf("job has no workspace")
	}
	res, runErr := s.runner.Run(context.Background(), runner.Spec{
		WorkspacePath:     j.WorkspacePath,
		Command:           "go test ./...",
		InactivityTimeout: s.inactivity,
		ExecutionTimeout:  10 * time.Minute,
	}, nil)
	if res.CombinedOutput != "" {
		s.store.AddEvent(j.ID, "test_output", trimForStore(res.CombinedOutput, 8000))
	}
	if runErr != nil || res.ExitErr != nil {
		s.notifier.PostJobStatus(context.Background(), j, "Tests fallaron. Revisa logs y decide si continuar con PR.", nil)
		return nil
	}
	s.notifier.PostJobStatus(context.Background(), j, "Tests ejecutaron correctamente.", nil)
	return nil
}

func (s *Service) ApproveAndCreatePR(jobID string) error {
	j, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	if j.Status != model.StatusNeedsReview {
		return nil
	}

	if err := s.git.CommitAll(context.Background(), j.WorkspacePath, "chore: codex job "+j.ID); err != nil {
		return s.failJob(context.Background(), j, fmt.Errorf("commit: %w", err))
	}
	if err := s.git.PushBranch(context.Background(), j.WorkspacePath, j.JobBranch); err != nil {
		return s.failJob(context.Background(), j, fmt.Errorf("push: %w", err))
	}

	prURL, err := s.github.CreatePR(context.Background(), j.Repo, "Codex job "+j.ID, j.JobBranch, j.BaseBranch, "Generated by Slack Codex runner")
	if err != nil {
		return s.failJob(context.Background(), j, fmt.Errorf("create pr: %w", err))
	}

	j.PRURL = prURL
	j.Status = model.StatusDone
	if err := s.store.UpdateJob(j); err != nil {
		return err
	}
	s.store.AddEvent(j.ID, "pr_created", prURL)
	return s.notifier.PostJobStatus(context.Background(), j, "PR creado: "+prURL, nil)
}
