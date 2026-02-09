package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRun_LongSingleLineOutputDoesNotScannerFail(t *testing.T) {
	r := New()
	res, err := r.Run(context.Background(), Spec{
		WorkspacePath:     ".",
		Command:           "head -c 200000 /dev/zero | tr '\\000' 'a'",
		InactivityTimeout: 10 * time.Second,
		ExecutionTimeout:  20 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("runner returned error: %v", err)
	}
	if res.ExitErr != nil {
		t.Fatalf("command exit error: %v", res.ExitErr)
	}
	if strings.Contains(res.CombinedOutput, "scanner_error:") {
		t.Fatalf("unexpected scanner_error in output")
	}
	if len(strings.TrimSpace(res.CombinedOutput)) < 150000 {
		t.Fatalf("expected long output, got length %d", len(strings.TrimSpace(res.CombinedOutput)))
	}
}
