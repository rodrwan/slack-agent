package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
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
	store        Store
	notifier     SlackNotifier
	runner       *runner.Runner
	policy       *policy.Engine
	git          GitManager
	github       GitHubClient
	codexCmd     string
	outputMode   string
	schemaVer    string
	slackLogMode string
	inactivity   time.Duration
	execTimeout  time.Duration

	queue    chan string
	repoLock sync.Map
}

var rateLimitRetryRe = regexp.MustCompile(`Please try again in ([0-9]+(?:\.[0-9]+)?)s`)

func NewService(store Store, notifier SlackNotifier, run *runner.Runner, pol *policy.Engine, gm GitManager, gh GitHubClient, codexCmd, outputMode, schemaVer, slackLogMode string, inactivity, execTimeout time.Duration, workers int) *Service {
	if workers < 1 {
		workers = 1
	}
	s := &Service{
		store:        store,
		notifier:     notifier,
		runner:       run,
		policy:       pol,
		git:          gm,
		github:       gh,
		codexCmd:     codexCmd,
		outputMode:   normalizeOutputMode(outputMode),
		schemaVer:    strings.TrimSpace(schemaVer),
		slackLogMode: normalizeLogMode(slackLogMode),
		inactivity:   inactivity,
		execTimeout:  execTimeout,
		queue:        make(chan string, 128),
	}
	if s.schemaVer == "" {
		s.schemaVer = "v1"
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

	codexEnv := buildCodexEnv()
	if strings.Contains(strings.ToLower(s.codexCmd), "codex") && s.slackLogMode == LogModeStream {
		diag := "OPENAI_API_KEY missing"
		if len(codexEnv) > 0 {
			diag = fmt.Sprintf("OPENAI_API_KEY present (len=%d)", len(strings.TrimPrefix(codexEnv[0], "OPENAI_API_KEY=")))
		}
		_ = s.notifier.PostJobLog(context.Background(), j, "[diag] "+diag)
	}

	promptText := strings.TrimSpace(j.Prompt + "\n" + j.LastInput)
	if s.outputMode == OutputModeStructured {
		promptText = buildCodexPrompt(j.Prompt, j.LastInput, s.schemaVer, false)
	}

	res, runErr, policyErr := s.runCodexWithRateLimitRetry(ctx, j, promptText, codexEnv)
	if policyErr != nil {
		return policyErr
	}
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
		return s.notifier.PostJobStatus(ctx, j, buildNeedsInputStatus(j.ID, res.CombinedOutput), actions)
	}
	if runErr != nil || res.ExitErr != nil {
		s.postFailureContext(j, res.CombinedOutput)
		if runErr != nil {
			return s.failJob(ctx, j, runErr)
		}
		return s.failJob(ctx, j, res.ExitErr)
	}

	var parsed structuredOutput
	if s.outputMode == OutputModeStructured {
		parsed, err = parseStructuredOutput(res.CombinedOutput)
		if err != nil {
			s.store.AddEvent(j.ID, "output_parse_failed", trimForStore(err.Error(), 1000))
			if s.slackLogMode != LogModeStream {
				_ = s.notifier.PostJobLog(ctx, j, "La salida no cumplió el formato esperado; reintentando en modo estructurado.")
			}
			retryPrompt := buildCodexPrompt(j.Prompt, j.LastInput, s.schemaVer, true)
			retryRes, retryErr, retryPolicyErr := s.runCodexWithRateLimitRetry(ctx, j, retryPrompt, codexEnv)
			if retryPolicyErr != nil {
				return retryPolicyErr
			}
			res = retryRes
			if retryRes.CombinedOutput != "" {
				s.store.AddEvent(j.ID, "runner_output_retry", trimForStore(retryRes.CombinedOutput, 8000))
			}
			if errors.Is(retryErr, runner.ErrNeedsInput) || retryRes.NeedsInput {
				j.Status = model.StatusNeedsInput
				j.LastError = "runner quedó esperando input"
				if upErr := s.store.UpdateJob(j); upErr != nil {
					return upErr
				}
				actions := []map[string]any{
					{"type": "button", "action_id": "resume_default", "text": map[string]any{"type": "plain_text", "text": "Resume"}, "value": j.ID, "style": "primary"},
					{"type": "button", "action_id": "abort", "text": map[string]any{"type": "plain_text", "text": "Abort"}, "value": j.ID, "style": "danger"},
				}
				return s.notifier.PostJobStatus(ctx, j, buildNeedsInputStatus(j.ID, retryRes.CombinedOutput), actions)
			}
			if retryErr != nil || retryRes.ExitErr != nil {
				s.postFailureContext(j, retryRes.CombinedOutput)
				if retryErr != nil {
					return s.failJob(ctx, j, retryErr)
				}
				return s.failJob(ctx, j, retryRes.ExitErr)
			}
			parsed, err = parseStructuredOutput(retryRes.CombinedOutput)
			if err != nil {
				s.store.AddEvent(j.ID, "output_parse_failed_final", trimForStore(err.Error(), 1000))
				if s.slackLogMode != LogModeStream {
					fallback := sanitizeOutputForSlack(retryRes.CombinedOutput, 1800)
					if fallback != "" {
						_ = s.notifier.PostJobLog(ctx, j, "*Salida degradada*\n```"+escapeBackticksForSlack(fallback)+"```")
					}
				}
			}
		}
	}

	if s.slackLogMode != LogModeStream {
		if parsed.TaskSummary != "" {
			if payload, mErr := json.Marshal(parsed); mErr == nil {
				s.store.AddEvent(j.ID, "structured_output", trimForStore(string(payload), 8000))
			}
			_ = s.notifier.PostJobLog(ctx, j, formatStructuredSummary(parsed))
		} else {
			preview := sanitizeOutputForSlack(res.CombinedOutput, 1600)
			if preview != "" {
				_ = s.notifier.PostJobLog(ctx, j, "*Salida de Codex*\n```"+escapeBackticksForSlack(preview)+"```")
			}
		}
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

func escapeBackticksForSlack(v string) string {
	return strings.ReplaceAll(v, "`", "'")
}

func buildCodexEnv() []string {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return nil
	}
	return []string{"OPENAI_API_KEY=" + key}
}

func (s *Service) runCodexOnce(ctx context.Context, j model.Job, prompt string, codexEnv []string) (runner.Result, error, error) {
	command := strings.TrimSpace(s.codexCmd + " " + shellQuote(prompt))
	pd := s.policy.Evaluate(command)
	switch pd.Decision {
	case policy.Deny:
		return runner.Result{}, nil, s.failJob(ctx, j, errors.New(pd.Reason))
	case policy.NeedsApproval:
		j.Status = model.StatusNeedsApproval
		j.LastError = pd.Reason
		if err := s.store.UpdateJob(j); err != nil {
			return runner.Result{}, nil, err
		}
		actions := []map[string]any{
			{"type": "button", "action_id": "allow_once", "text": map[string]any{"type": "plain_text", "text": "Allow once"}, "value": j.ID, "style": "primary"},
			{"type": "button", "action_id": "deny", "text": map[string]any{"type": "plain_text", "text": "Deny"}, "value": j.ID, "style": "danger"},
		}
		return runner.Result{}, nil, s.notifier.PostJobStatus(ctx, j, "Acción requiere aprobación: "+pd.Reason, actions)
	}
	var onLine func(string)
	if s.slackLogMode == LogModeStream {
		onLine = func(line string) {
			if strings.TrimSpace(line) != "" {
				_ = s.notifier.PostJobLog(context.Background(), j, line)
			}
		}
	}
	res, runErr := s.runner.Run(ctx, runner.Spec{
		WorkspacePath:     j.WorkspacePath,
		Command:           command,
		Env:               codexEnv,
		InactivityTimeout: s.inactivity,
		ExecutionTimeout:  s.execTimeout,
	}, onLine)
	return res, runErr, nil
}

func (s *Service) runCodexWithRateLimitRetry(ctx context.Context, j model.Job, prompt string, codexEnv []string) (runner.Result, error, error) {
	res, runErr, policyErr := s.runCodexOnce(ctx, j, prompt, codexEnv)
	if policyErr != nil {
		return res, runErr, policyErr
	}
	if runErr == nil && res.ExitErr == nil {
		return res, runErr, nil
	}
	wait, ok := detectRateLimitRetryAfter(res.CombinedOutput)
	if !ok {
		return res, runErr, nil
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	if wait < 2*time.Second {
		wait = 2 * time.Second
	}
	s.store.AddEvent(j.ID, "rate_limit_retry", wait.String())
	if s.slackLogMode != LogModeStream {
		_ = s.notifier.PostJobLog(ctx, j, fmt.Sprintf("Rate limit de OpenAI detectado. Reintentando automáticamente en %s.", wait.Round(time.Second)))
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return res, runErr, nil
	}
	return s.runCodexOnce(ctx, j, prompt, codexEnv)
}

func detectRateLimitRetryAfter(output string) (time.Duration, bool) {
	if !strings.Contains(output, "Rate limit reached") {
		return 0, false
	}
	m := rateLimitRetryRe.FindStringSubmatch(output)
	if len(m) != 2 {
		return 5 * time.Second, true
	}
	secs, err := strconv.ParseFloat(m[1], 64)
	if err != nil || secs <= 0 {
		return 5 * time.Second, true
	}
	return time.Duration(secs * float64(time.Second)), true
}

func (s *Service) postFailureContext(j model.Job, output string) {
	if s.slackLogMode == LogModeStream {
		return
	}
	tail := extractErrorTail(output, 24, 1800)
	if tail == "" {
		return
	}
	_ = s.notifier.PostJobLog(context.Background(), j, "*Contexto de error*\n```"+escapeBackticksForSlack(tail)+"```")
}

func buildNeedsInputStatus(jobID, output string) string {
	contextTail := extractErrorTail(output, 10, 700)
	msg := "Codex se quedó esperando más contexto.\n"
	msg += "Si no quieres agregar nada, pulsa *Resume* y continúa con la estrategia actual.\n"
	msg += "Si quieres orientar mejor el resultado, responde en este thread usando:\n"
	msg += "`" + jobID + " <tu instrucción>`"
	if contextTail != "" {
		msg += "\n\nÚltimo contexto observado:\n```" + escapeBackticksForSlack(contextTail) + "```"
	}
	return msg
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
