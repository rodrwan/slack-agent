package observability

import "testing"

func TestRedact(t *testing.T) {
	in := "Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz123456 " +
		"https://x-access-token:github_pat_abc12345678901234567890@github.com/org/repo"
	out := Redact(in)
	if out == in {
		t.Fatalf("expected redaction")
	}
	if containsTokenLike(out) {
		t.Fatalf("found token-like secret after redaction: %q", out)
	}
}

func containsTokenLike(v string) bool {
	return reOpenAIKey.MatchString(v) || reGitHubPAT.MatchString(v)
}
