package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rodrwan/slack-codex/internal/model"
)

type JobController interface {
	CreateAndQueueJob(ctx context.Context, repo, baseBranch, prompt, channelID, threadTS, userID string) (model.Job, error)
	HandleThreadInput(jobID, text string) error
	HandleChatMessage(channelID, threadTS, userID, text string) (string, []map[string]any, bool, error)
	ApplyChatSession(sessionKey string) error
	CancelChatSession(sessionKey string) error
	AllowOnce(jobID string) error
	Abort(jobID string) error
	RunTests(jobID string) error
	ApproveAndCreatePR(jobID string) error
}

type Server struct {
	client        *Client
	signingSecret string
	defaultBranch string
	jobs          JobController
}

func NewServer(client *Client, signingSecret, defaultBranch string, jobs JobController) *Server {
	return &Server{client: client, signingSecret: signingSecret, defaultBranch: defaultBranch, jobs: jobs}
}

func (s *Server) SetJobs(j JobController) {
	s.jobs = j
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/slack/commands", s.handleSlashCommand)
	mux.HandleFunc("/slack/interactions", s.handleInteractions)
	mux.HandleFunc("/slack/events", s.handleEvents)
}

func (s *Server) PostJobStatus(ctx context.Context, j model.Job, summary string, actions []map[string]any) error {
	blocks := s.client.FormatStatusBlocks(string(j.Status), summary, actions)
	threadTS := j.SlackThreadTS
	_, err := s.client.PostMessage(ctx, j.SlackChannelID, threadTS, summary, blocks)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) PostJobLog(ctx context.Context, j model.Job, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	msg := text
	if len(msg) > 2800 {
		msg = msg[:2800] + "\n..."
	}
	_, err := s.client.PostMessage(ctx, j.SlackChannelID, j.SlackThreadTS, "`"+escapeBackticks(msg)+"`", nil)
	return err
}

func (s *Server) handleSlashCommand(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if !s.verifyRequest(r, body) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.jobs == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	repo, branch, prompt, err := parseCommandText(r.Form.Get("text"), s.defaultBranch)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Uso: /codex repo=org/repo branch=main \"tu objetivo\""))
		return
	}
	channelID := r.Form.Get("channel_id")
	userID := r.Form.Get("user_id")
	if _, err := s.jobs.CreateAndQueueJob(r.Context(), repo, branch, prompt, channelID, "", userID); err != nil {
		http.Error(w, "failed creating job", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Job recibido. Mira el thread del bot para seguir el estado."))
}

type interactionPayload struct {
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

func (s *Server) handleInteractions(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payload := r.Form.Get("payload")
	if payload == "" {
		http.Error(w, "missing payload", http.StatusBadRequest)
		return
	}
	var p interactionPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if len(p.Actions) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	a := p.Actions[0]
	var err error
	switch a.ActionID {
	case "chat_apply":
		err = s.jobs.ApplyChatSession(a.Value)
	case "chat_cancel":
		err = s.jobs.CancelChatSession(a.Value)
	case "allow_once":
		err = s.jobs.AllowOnce(a.Value)
	case "deny", "abort":
		err = s.jobs.Abort(a.Value)
	case "run_tests":
		err = s.jobs.RunTests(a.Value)
	case "approve_pr":
		err = s.jobs.ApproveAndCreatePR(a.Value)
	case "resume_default":
		err = s.jobs.HandleThreadInput(a.Value, "continue with current strategy")
	}
	if err != nil {
		http.Error(w, "action failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type eventEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Event     struct {
		Channel  string `json:"channel"`
		User     string `json:"user"`
		Type     string `json:"type"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
		BotID    string `json:"bot_id"`
		Subtype  string `json:"subtype"`
	} `json:"event"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if !s.verifyRequest(r, body) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.jobs == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	var env eventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if env.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(env.Challenge))
		return
	}
	if env.Event.Type == "message" && env.Event.Subtype == "" && env.Event.BotID == "" && env.Event.ThreadTS != "" {
		text := strings.TrimSpace(env.Event.Text)
		if strings.HasPrefix(strings.TrimSpace(env.Event.Text), "job_") {
			parts := strings.SplitN(text, " ", 2)
			if len(parts) == 2 {
				_ = s.jobs.HandleThreadInput(parts[0], parts[1])
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		summary, actions, handled, err := s.jobs.HandleChatMessage(env.Event.Channel, env.Event.ThreadTS, env.Event.User, text)
		if err != nil {
			http.Error(w, "chat handling failed", http.StatusInternalServerError)
			return
		}
		if handled {
			blocks := s.client.FormatStatusBlocks("chat", summary, actions)
			_, _ = s.client.PostMessage(r.Context(), env.Event.Channel, env.Event.ThreadTS, summary, blocks)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func parseCommandText(text, defaultBranch string) (repo, branch, prompt string, err error) {
	branch = defaultBranch
	for _, token := range splitTokens(text) {
		switch {
		case strings.HasPrefix(token, "repo="):
			repo = strings.TrimPrefix(token, "repo=")
		case strings.HasPrefix(token, "branch="):
			branch = strings.TrimPrefix(token, "branch=")
		default:
			if prompt == "" {
				prompt = token
			} else {
				prompt += " " + token
			}
		}
	}
	prompt = strings.Trim(prompt, "\"")
	if repo == "" || prompt == "" {
		return "", "", "", fmt.Errorf("repo and prompt are required")
	}
	return repo, branch, prompt, nil
}

func splitTokens(s string) []string {
	vals := make([]string, 0)
	cur := strings.Builder{}
	inQuotes := false
	for _, r := range s {
		switch r {
		case '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case ' ':
			if inQuotes {
				cur.WriteRune(r)
				continue
			}
			if cur.Len() > 0 {
				vals = append(vals, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		vals = append(vals, cur.String())
	}
	return vals
}

func (s *Server) verifyRequest(r *http.Request, body []byte) bool {
	if s.signingSecret == "" {
		return true
	}
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")
	if timestamp == "" || signature == "" {
		return false
	}
	base := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	h := hmac.New(sha256.New, []byte(s.signingSecret))
	_, _ = h.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(h.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err == nil && time.Since(time.Unix(ts, 0)) > 5*time.Minute {
		return false
	}
	return true
}

func escapeBackticks(v string) string {
	return strings.ReplaceAll(v, "`", "'")
}
