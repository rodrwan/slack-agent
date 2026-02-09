package slack

import "testing"

func TestParseCommandText(t *testing.T) {
	repo, branch, prompt, err := parseCommandText(`repo=acme/api branch=main "add /health endpoint"`, "develop")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if repo != "acme/api" {
		t.Fatalf("repo mismatch: %s", repo)
	}
	if branch != "main" {
		t.Fatalf("branch mismatch: %s", branch)
	}
	if prompt != "add /health endpoint" {
		t.Fatalf("prompt mismatch: %q", prompt)
	}
}

func TestParseCommandTextDefaults(t *testing.T) {
	repo, branch, prompt, err := parseCommandText(`repo=acme/api "fix tests"`, "develop")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if repo != "acme/api" || branch != "develop" || prompt != "fix tests" {
		t.Fatalf("unexpected parse: %s %s %s", repo, branch, prompt)
	}
}

func TestInteractionThreadTS(t *testing.T) {
	var p interactionPayload
	p.Container.ThreadTS = "123.456"
	if got := interactionThreadTS(p); got != "123.456" {
		t.Fatalf("expected container thread ts, got %q", got)
	}

	p = interactionPayload{}
	p.Message.ThreadTS = "222.333"
	if got := interactionThreadTS(p); got != "222.333" {
		t.Fatalf("expected message thread ts, got %q", got)
	}

	p = interactionPayload{}
	p.Message.TS = "999.000"
	if got := interactionThreadTS(p); got != "999.000" {
		t.Fatalf("expected fallback message ts, got %q", got)
	}
}
