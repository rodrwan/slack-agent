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
	"sync/atomic"
	"time"

	"github.com/rodrwan/slack-codex/internal/model"
	"github.com/rodrwan/slack-codex/internal/observability"
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
	requestSeq    uint64
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
	reqID := s.requestID(r)
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if ok, reason := s.verifyRequest(r, body); !ok {
		observability.Warn("slack_request_rejected", observability.Fields{
			"request_id": reqID,
			"endpoint":   "/slack/commands",
			"reason":     reason,
		})
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	observability.Info("slack_command_received", observability.Fields{
		"request_id": reqID,
		"endpoint":   "/slack/commands",
		"body_len":   len(body),
	})
	if s.jobs == nil {
		observability.Error("slack_jobs_unavailable", observability.Fields{
			"request_id": reqID,
			"endpoint":   "/slack/commands",
		})
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		observability.Warn("slack_command_parse_form_failed", observability.Fields{
			"request_id": reqID,
			"error":      err.Error(),
		})
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	repo, branch, prompt, err := parseCommandText(r.Form.Get("text"), s.defaultBranch)
	if err != nil {
		observability.Warn("slack_command_invalid", observability.Fields{
			"request_id":   reqID,
			"channel_id":   r.Form.Get("channel_id"),
			"user_id":      r.Form.Get("user_id"),
			"prompt_len":   len(strings.TrimSpace(r.Form.Get("text"))),
			"parse_reason": err.Error(),
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Uso: /codex repo=org/repo branch=main \"tu objetivo\""))
		return
	}
	channelID := r.Form.Get("channel_id")
	userID := r.Form.Get("user_id")
	if _, err := s.jobs.CreateAndQueueJob(r.Context(), repo, branch, prompt, channelID, "", userID); err != nil {
		observability.Error("slack_command_job_create_failed", observability.Fields{
			"request_id":  reqID,
			"channel_id":  channelID,
			"user_id":     userID,
			"repo":        repo,
			"base_branch": branch,
			"error":       err.Error(),
		})
		http.Error(w, "failed creating job", http.StatusInternalServerError)
		return
	}
	observability.Info("slack_command_job_queued", observability.Fields{
		"request_id":  reqID,
		"channel_id":  channelID,
		"user_id":     userID,
		"repo":        repo,
		"base_branch": branch,
		"prompt_hash": observability.HashText(prompt),
		"prompt_len":  len(prompt),
	})
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Job recibido. Mira el thread del bot para seguir el estado."))
}

type interactionPayload struct {
	Type    string `json:"type"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Container struct {
		ThreadTS string `json:"thread_ts"`
	} `json:"container"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

func (s *Server) handleInteractions(w http.ResponseWriter, r *http.Request) {
	reqID := s.requestID(r)
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if ok, reason := s.verifyRequest(r, body); !ok {
		observability.Warn("slack_request_rejected", observability.Fields{
			"request_id": reqID,
			"endpoint":   "/slack/interactions",
			"reason":     reason,
		})
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.jobs == nil {
		observability.Error("slack_jobs_unavailable", observability.Fields{
			"request_id": reqID,
			"endpoint":   "/slack/interactions",
		})
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		observability.Warn("slack_interaction_parse_form_failed", observability.Fields{
			"request_id": reqID,
			"error":      err.Error(),
		})
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payload := r.Form.Get("payload")
	if payload == "" {
		observability.Warn("slack_interaction_missing_payload", observability.Fields{
			"request_id": reqID,
		})
		http.Error(w, "missing payload", http.StatusBadRequest)
		return
	}
	var p interactionPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		observability.Warn("slack_interaction_invalid_payload", observability.Fields{
			"request_id": reqID,
			"error":      err.Error(),
		})
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if len(p.Actions) == 0 {
		observability.Info("slack_interaction_no_actions", observability.Fields{
			"request_id": reqID,
			"channel_id": p.Channel.ID,
			"user_id":    p.User.ID,
			"thread_ts":  p.Container.ThreadTS,
		})
		w.WriteHeader(http.StatusOK)
		return
	}
	a := p.Actions[0]
	observability.Info("slack_interaction_action_received", observability.Fields{
		"request_id": reqID,
		"channel_id": p.Channel.ID,
		"user_id":    p.User.ID,
		"thread_ts":  p.Container.ThreadTS,
		"action_id":  a.ActionID,
		"value":      a.Value,
	})
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
		observability.Error("slack_interaction_action_failed", observability.Fields{
			"request_id": reqID,
			"action_id":  a.ActionID,
			"value":      a.Value,
			"error":      err.Error(),
		})
		http.Error(w, "action failed", http.StatusInternalServerError)
		return
	}
	observability.Info("slack_interaction_action_ok", observability.Fields{
		"request_id": reqID,
		"action_id":  a.ActionID,
		"value":      a.Value,
	})
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
	reqID := s.requestID(r)
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if ok, reason := s.verifyRequest(r, body); !ok {
		observability.Warn("slack_request_rejected", observability.Fields{
			"request_id": reqID,
			"endpoint":   "/slack/events",
			"reason":     reason,
		})
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	observability.Info("slack_event_received", observability.Fields{
		"request_id": reqID,
		"endpoint":   "/slack/events",
		"body_len":   len(body),
	})
	if s.jobs == nil {
		observability.Error("slack_jobs_unavailable", observability.Fields{
			"request_id": reqID,
			"endpoint":   "/slack/events",
		})
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	var env eventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		observability.Warn("slack_event_invalid_payload", observability.Fields{
			"request_id": reqID,
			"error":      err.Error(),
		})
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if env.Type == "url_verification" {
		observability.Info("slack_url_verification", observability.Fields{
			"request_id": reqID,
		})
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(env.Challenge))
		return
	}
	if env.Event.Type == "message" && env.Event.Subtype == "" && env.Event.BotID == "" && env.Event.ThreadTS != "" {
		text := strings.TrimSpace(env.Event.Text)
		observability.Info("slack_thread_message_received", observability.Fields{
			"request_id": reqID,
			"channel_id": env.Event.Channel,
			"user_id":    env.Event.User,
			"thread_ts":  env.Event.ThreadTS,
			"text_hash":  observability.HashText(text),
			"text_len":   len(text),
		})
		if strings.HasPrefix(strings.TrimSpace(env.Event.Text), "job_") {
			parts := strings.SplitN(text, " ", 2)
			if len(parts) == 2 {
				if err := s.jobs.HandleThreadInput(parts[0], parts[1]); err != nil {
					observability.Error("slack_thread_input_forward_failed", observability.Fields{
						"request_id": reqID,
						"job_id":     parts[0],
						"error":      err.Error(),
					})
				} else {
					observability.Info("slack_thread_input_forwarded", observability.Fields{
						"request_id": reqID,
						"job_id":     parts[0],
					})
				}
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		summary, actions, handled, err := s.jobs.HandleChatMessage(env.Event.Channel, env.Event.ThreadTS, env.Event.User, text)
		if err != nil {
			observability.Error("slack_chat_message_failed", observability.Fields{
				"request_id": reqID,
				"channel_id": env.Event.Channel,
				"thread_ts":  env.Event.ThreadTS,
				"error":      err.Error(),
			})
			http.Error(w, "chat handling failed", http.StatusInternalServerError)
			return
		}
		if handled {
			blocks := s.client.FormatStatusBlocks("chat", summary, actions)
			if _, postErr := s.client.PostMessage(r.Context(), env.Event.Channel, env.Event.ThreadTS, summary, blocks); postErr != nil {
				observability.Error("slack_chat_reply_failed", observability.Fields{
					"request_id": reqID,
					"channel_id": env.Event.Channel,
					"thread_ts":  env.Event.ThreadTS,
					"error":      postErr.Error(),
				})
			} else {
				observability.Info("slack_chat_reply_sent", observability.Fields{
					"request_id":  reqID,
					"channel_id":  env.Event.Channel,
					"thread_ts":   env.Event.ThreadTS,
					"summary_len": len(summary),
				})
			}
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

func (s *Server) verifyRequest(r *http.Request, body []byte) (bool, string) {
	if s.signingSecret == "" {
		return true, "signature_check_disabled"
	}
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := r.Header.Get("X-Slack-Signature")
	if timestamp == "" || signature == "" {
		return false, "missing_signature_headers"
	}
	base := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	h := hmac.New(sha256.New, []byte(s.signingSecret))
	_, _ = h.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(h.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return false, "signature_mismatch"
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err == nil && time.Since(time.Unix(ts, 0)) > 5*time.Minute {
		return false, "timestamp_too_old"
	}
	return true, "ok"
}

func (s *Server) requestID(r *http.Request) string {
	if rid := strings.TrimSpace(r.Header.Get("X-Request-ID")); rid != "" {
		return rid
	}
	n := atomic.AddUint64(&s.requestSeq, 1)
	return fmt.Sprintf("req-%d-%d", time.Now().UTC().UnixNano(), n)
}

func escapeBackticks(v string) string {
	return strings.ReplaceAll(v, "`", "'")
}
