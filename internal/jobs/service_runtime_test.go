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
		codexModel:   "gpt-5.2-codex",
	}
	cmd := s.buildCodexCommand("do something", "/tmp/schema.json")
	if !strings.Contains(cmd, "--sandbox 'workspace-write'") {
		t.Fatalf("missing sandbox flag in %q", cmd)
	}
	if !strings.Contains(cmd, "--model 'gpt-5.2-codex'") {
		t.Fatalf("missing model flag in %q", cmd)
	}
	if !strings.Contains(cmd, "--output-schema '/tmp/schema.json'") {
		t.Fatalf("missing schema flag in %q", cmd)
	}
}

func TestBuildCodexCommand_DoesNotDuplicateFlags(t *testing.T) {
	s := &Service{
		codexCmd:     "codex exec --sandbox workspace-write --model gpt-5.2-codex --output-schema /tmp/schema.json",
		codexSandbox: "workspace-write",
		codexModel:   "gpt-5.2-codex",
	}
	cmd := s.buildCodexCommand("do something", "/tmp/schema.json")
	if strings.Count(cmd, "--sandbox") != 1 {
		t.Fatalf("expected one sandbox flag, got %q", cmd)
	}
	if strings.Count(cmd, "--model") != 1 {
		t.Fatalf("expected one model flag, got %q", cmd)
	}
	if strings.Count(cmd, "--output-schema") != 1 {
		t.Fatalf("expected one schema flag, got %q", cmd)
	}
}

func TestCodexInactivityTimeout_MinimumForCodex(t *testing.T) {
	s := &Service{codexCmd: "codex exec", inactivity: 45 * time.Second}
	if got := s.codexInactivityTimeout(); got != 2*time.Minute {
		t.Fatalf("expected 2m, got %s", got)
	}
}
