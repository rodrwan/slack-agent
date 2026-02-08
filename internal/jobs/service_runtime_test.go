package jobs

import (
	"strings"
	"testing"
	"time"
)

func TestBuildCodexCommand_AppendsRuntimeFlags(t *testing.T) {
	s := &Service{
		codexCmd:     "codex exec",
		codexSandbox: "workspace-write",
		codexAsk:     "never",
		codexModel:   "gpt-5.2-codex",
	}
	cmd := s.buildCodexCommand("do something")
	if !strings.Contains(cmd, "--sandbox 'workspace-write'") {
		t.Fatalf("missing sandbox flag in %q", cmd)
	}
	if !strings.Contains(cmd, "--ask-for-approval 'never'") {
		t.Fatalf("missing approval flag in %q", cmd)
	}
	if !strings.Contains(cmd, "--model 'gpt-5.2-codex'") {
		t.Fatalf("missing model flag in %q", cmd)
	}
}

func TestBuildCodexCommand_DoesNotDuplicateFlags(t *testing.T) {
	s := &Service{
		codexCmd:     "codex exec --sandbox workspace-write --model gpt-5.2-codex --ask-for-approval never",
		codexSandbox: "workspace-write",
		codexAsk:     "never",
		codexModel:   "gpt-5.2-codex",
	}
	cmd := s.buildCodexCommand("do something")
	if strings.Count(cmd, "--sandbox") != 1 {
		t.Fatalf("expected one sandbox flag, got %q", cmd)
	}
	if strings.Count(cmd, "--model") != 1 {
		t.Fatalf("expected one model flag, got %q", cmd)
	}
	if strings.Count(cmd, "--ask-for-approval") != 1 {
		t.Fatalf("expected one approval flag, got %q", cmd)
	}
}

func TestCodexInactivityTimeout_MinimumForCodex(t *testing.T) {
	s := &Service{codexCmd: "codex exec", inactivity: 45 * time.Second}
	if got := s.codexInactivityTimeout(); got != 2*time.Minute {
		t.Fatalf("expected 2m, got %s", got)
	}
}
