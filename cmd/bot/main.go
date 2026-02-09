package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/rodrwan/slack-codex/internal/config"
	gitmgr "github.com/rodrwan/slack-codex/internal/git"
	gh "github.com/rodrwan/slack-codex/internal/github"
	"github.com/rodrwan/slack-codex/internal/jobs"
	"github.com/rodrwan/slack-codex/internal/policy"
	"github.com/rodrwan/slack-codex/internal/runner"
	"github.com/rodrwan/slack-codex/internal/slack"
	"github.com/rodrwan/slack-codex/internal/storage"
)

func main() {
	cfg := config.Load()
	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		log.Fatalf("create workspace root: %v", err)
	}

	store, err := storage.NewSQLiteStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("init storage: %v", err)
	}
	defer store.Close()

	slackClient := slack.NewClient(cfg.SlackBotToken)
	srv := slack.NewServer(slackClient, cfg.SlackSigningSecret, cfg.DefaultBaseBranch, nil)

	svc := jobs.NewService(
		store,
		srv,
		runner.New(),
		policy.NewEngine(),
		gitBridge{gitmgr.NewManager(cfg.WorkspaceRoot, cfg.GitHubToken)},
		gh.NewClient(cfg.GitHubAPIBaseURL, cfg.GitHubToken),
		cfg.CodexCommand,
		cfg.CodexSandboxMode,
		cfg.CodexModel,
		cfg.AgentOutputMode,
		cfg.AgentOutputSchemaVer,
		cfg.SlackLogMode,
		cfg.RunnerInactivity,
		cfg.CodexTimeout,
		cfg.MaxConcurrentJobs,
		cfg.NoDiffAutoRetry,
		cfg.NoDiffAutoRetryMax,
	)
	srv.SetJobs(svc)
	svc.RecoverPendingJobs()

	mux := http.NewServeMux()
	srv.Register(mux)
	log.Printf("listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

type gitBridge struct{ m *gitmgr.Manager }

func (g gitBridge) PrepareWorkspace(ctx context.Context, jobID, repo, baseBranch string) (jobs.Workspace, error) {
	ws, err := g.m.PrepareWorkspace(ctx, jobID, repo, baseBranch)
	if err != nil {
		return jobs.Workspace{}, err
	}
	return jobs.Workspace{Path: ws.Path, JobBranch: ws.JobBranch}, nil
}

func (g gitBridge) DiffStat(ctx context.Context, workspace string) (string, error) {
	return g.m.DiffStat(ctx, workspace)
}

func (g gitBridge) CommitAll(ctx context.Context, workspace, message string) error {
	return g.m.CommitAll(ctx, workspace, message)
}

func (g gitBridge) PushBranch(ctx context.Context, workspace, branch string) error {
	return g.m.PushBranch(ctx, workspace, branch)
}
