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
