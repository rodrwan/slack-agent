package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                 string
	SlackBotToken        string
	SlackSigningSecret   string
	DataDir              string
	DatabasePath         string
	WorkspaceRoot        string
	CodexCommand         string
	CodexTimeout         time.Duration
	RunnerInactivity     time.Duration
	MaxConcurrentJobs    int
	GitHubToken          string
	GitHubAPIBaseURL     string
	DefaultBaseBranch    string
	AllowAlwaysRepoScope bool
}

func Load() Config {
	dataDir := envOrDefault("DATA_DIR", "/data")
	workspaceRoot := envOrDefault("WORKSPACE_ROOT", dataDir+"/workspaces")
	dbPath := envOrDefault("DB_PATH", dataDir+"/state.db")
	return Config{
		Addr:                 envOrDefault("ADDR", ":8080"),
		SlackBotToken:        os.Getenv("SLACK_BOT_TOKEN"),
		SlackSigningSecret:   os.Getenv("SLACK_SIGNING_SECRET"),
		DataDir:              dataDir,
		DatabasePath:         dbPath,
		WorkspaceRoot:        workspaceRoot,
		CodexCommand:         envOrDefault("CODEX_COMMAND", "codex"),
		CodexTimeout:         durationOrDefault("CODEX_TIMEOUT", 30*time.Minute),
		RunnerInactivity:     durationOrDefault("RUNNER_INACTIVITY_TIMEOUT", 45*time.Second),
		MaxConcurrentJobs:    intOrDefault("MAX_CONCURRENT_JOBS", 2),
		GitHubToken:          os.Getenv("GITHUB_TOKEN"),
		GitHubAPIBaseURL:     strings.TrimRight(envOrDefault("GITHUB_API_BASE_URL", "https://api.github.com"), "/"),
		DefaultBaseBranch:    envOrDefault("DEFAULT_BASE_BRANCH", "main"),
		AllowAlwaysRepoScope: boolOrDefault("ALLOW_ALWAYS_REPO_SCOPE", false),
	}
}

func envOrDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func durationOrDefault(k string, d time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return d
	}
	return parsed
}

func intOrDefault(k string, d int) int {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return d
	}
	return n
}

func boolOrDefault(k string, d bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	if v == "" {
		return d
	}
	return v == "1" || v == "true" || v == "yes"
}
