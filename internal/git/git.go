package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Manager struct {
	workspaceRoot string
	githubToken   string
}

type Workspace struct {
	Path      string
	JobBranch string
}

func NewManager(workspaceRoot, githubToken string) *Manager {
	return &Manager{workspaceRoot: workspaceRoot, githubToken: githubToken}
}

func (m *Manager) PrepareWorkspace(ctx context.Context, jobID, repo, baseBranch string) (Workspace, error) {
	repoName := filepath.Base(repo)
	path := filepath.Join(m.workspaceRoot, jobID, repoName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workspace{}, err
	}
	cloneURL := fmt.Sprintf("https://github.com/%s.git", repo)
	if m.githubToken != "" {
		cloneURL = fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", m.githubToken, repo)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		if err := run(ctx, "", "git", "clone", cloneURL, path); err != nil {
			return Workspace{}, fmt.Errorf("git clone: %w", err)
		}
	}
	if err := run(ctx, path, "git", "fetch", "origin"); err != nil {
		return Workspace{}, fmt.Errorf("git fetch: %w", err)
	}
	if err := run(ctx, path, "git", "checkout", baseBranch); err != nil {
		return Workspace{}, fmt.Errorf("git checkout base: %w", err)
	}
	if err := run(ctx, path, "git", "pull", "origin", baseBranch); err != nil {
		return Workspace{}, fmt.Errorf("git pull base: %w", err)
	}

	branch := branchForJob(jobID)
	_ = run(ctx, path, "git", "branch", "-D", branch)
	if err := run(ctx, path, "git", "checkout", "-b", branch); err != nil {
		return Workspace{}, fmt.Errorf("git checkout -b: %w", err)
	}
	return Workspace{Path: path, JobBranch: branch}, nil
}

func (m *Manager) DiffStat(ctx context.Context, workspace string) (string, error) {
	out, err := runOutput(ctx, workspace, "git", "diff", "--stat")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) CommitAll(ctx context.Context, workspace, message string) error {
	if err := run(ctx, workspace, "git", "add", "-A"); err != nil {
		return err
	}
	if err := run(ctx, workspace, "git", "commit", "-m", message); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			return nil
		}
		return err
	}
	return nil
}

func (m *Manager) PushBranch(ctx context.Context, workspace, branch string) error {
	return run(ctx, workspace, "git", "push", "-u", "origin", branch)
}

func run(ctx context.Context, dir, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v failed: %w (%s)", bin, args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runOutput(ctx context.Context, dir, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %v failed: %w (%s)", bin, args, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func branchForJob(jobID string) string {
	if len(jobID) > 12 {
		jobID = jobID[:12]
	}
	return "codex/" + strings.ReplaceAll(jobID, "_", "-")
}
