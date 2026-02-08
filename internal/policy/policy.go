package policy

import (
	"strings"
)

type Decision string

const (
	Allow         Decision = "allow"
	Deny          Decision = "deny"
	NeedsApproval Decision = "needs_approval"
)

type Result struct {
	Decision Decision
	Reason   string
}

type Engine struct {
	allowPrefixes []string
	denyFragments []string
}

func NewEngine() *Engine {
	return &Engine{
		allowPrefixes: []string{
			"codex",
			"go test",
			"go fmt",
			"gofmt",
			"git status",
			"git diff",
			"git add",
			"git commit",
			"git push",
		},
		denyFragments: []string{
			"rm -rf",
			"sudo",
			"curl | sh",
			"wget | sh",
			":(){:|:&};:",
		},
	}
}

func (e *Engine) Evaluate(cmd string) Result {
	norm := strings.ToLower(strings.TrimSpace(cmd))
	for _, d := range e.denyFragments {
		if strings.Contains(norm, d) {
			return Result{Decision: Deny, Reason: "command denied by policy"}
		}
	}
	for _, a := range e.allowPrefixes {
		if strings.HasPrefix(norm, a) {
			if strings.HasPrefix(norm, "git push") && strings.Contains(norm, " main") {
				return Result{Decision: NeedsApproval, Reason: "push to main requires approval"}
			}
			return Result{Decision: Allow}
		}
	}
	return Result{Decision: NeedsApproval, Reason: "command outside allowlist"}
}
