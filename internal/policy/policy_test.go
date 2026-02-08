package policy

import "testing"

func TestEvaluate(t *testing.T) {
	e := NewEngine()
	if got := e.Evaluate("go test ./..."); got.Decision != Allow {
		t.Fatalf("expected allow, got %v", got.Decision)
	}
	if got := e.Evaluate("rm -rf /"); got.Decision != Deny {
		t.Fatalf("expected deny, got %v", got.Decision)
	}
	if got := e.Evaluate("terraform apply"); got.Decision != NeedsApproval {
		t.Fatalf("expected needs approval, got %v", got.Decision)
	}
}
