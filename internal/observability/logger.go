package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Fields map[string]any

var (
	cfgOnce            sync.Once
	diagLevel          = "basic"
	includeSnippets    = false
	maxPayloadLogBytes = 2048

	reBearer      = regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"]+`)
	reBasic       = regexp.MustCompile(`(?i)(authorization:\s*basic\s+)[^\s"]+`)
	reOpenAIKey   = regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`)
	reGitHubPAT   = regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)
	reAccessToken = regexp.MustCompile(`x-access-token:[^@/\s]+@`)
)

func loadConfig() {
	cfgOnce.Do(func() {
		level := strings.ToLower(strings.TrimSpace(os.Getenv("DIAG_LOG_LEVEL")))
		switch level {
		case "verbose", "basic":
			diagLevel = level
		}
		raw := strings.ToLower(strings.TrimSpace(os.Getenv("DIAG_INCLUDE_SNIPPETS")))
		includeSnippets = raw == "1" || raw == "true" || raw == "yes"
		if v := strings.TrimSpace(os.Getenv("DIAG_MAX_PAYLOAD_BYTES")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 256 && n <= 65536 {
				maxPayloadLogBytes = n
			}
		}
	})
}

func Info(msg string, fields Fields) {
	write("info", msg, fields)
}

func Warn(msg string, fields Fields) {
	write("warn", msg, fields)
}

func Error(msg string, fields Fields) {
	write("error", msg, fields)
}

func IsVerbose() bool {
	loadConfig()
	return diagLevel == "verbose"
}

func IncludeSnippets() bool {
	loadConfig()
	return includeSnippets || IsVerbose()
}

func MaxPayloadBytes() int {
	loadConfig()
	return maxPayloadLogBytes
}

func HashText(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:8])
}

func Redact(v string) string {
	out := strings.TrimSpace(v)
	if out == "" {
		return out
	}
	out = reBearer.ReplaceAllString(out, `$1[REDACTED]`)
	out = reBasic.ReplaceAllString(out, `$1[REDACTED]`)
	out = reOpenAIKey.ReplaceAllString(out, "sk-[REDACTED]")
	out = reGitHubPAT.ReplaceAllString(out, "github_pat_[REDACTED]")
	out = reAccessToken.ReplaceAllString(out, "x-access-token:[REDACTED]@")
	return out
}

func Snippet(v string, max int) string {
	v = Redact(v)
	if max <= 0 {
		max = MaxPayloadBytes()
	}
	if len(v) <= max {
		return v
	}
	return v[:max] + "...(truncated)"
}

func write(level, msg string, fields Fields) {
	loadConfig()
	payload := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		payload[k] = sanitizeValue(v)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf(`{"ts":"%s","level":"error","msg":"observability marshal failed","error":"%s"}`,
			time.Now().UTC().Format(time.RFC3339Nano), Redact(err.Error()))
		return
	}
	log.Printf("%s", b)
}

func sanitizeValue(v any) any {
	switch t := v.(type) {
	case string:
		return Snippet(t, 0)
	case []byte:
		return Snippet(string(t), 0)
	default:
		return v
	}
}
