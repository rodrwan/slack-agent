package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Client struct {
	token string
	http  *http.Client
}

type postMessageRequest struct {
	Channel   string `json:"channel"`
	Text      string `json:"text"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Blocks    any    `json:"blocks,omitempty"`
	UnfurlLks bool   `json:"unfurl_links,omitempty"`
}

type postMessageResponse struct {
	OK      bool   `json:"ok"`
	TS      string `json:"ts"`
	Channel string `json:"channel"`
	Error   string `json:"error"`
}

func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{}}
}

func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string, blocks any) (string, error) {
	if c.token == "" {
		return "", fmt.Errorf("missing slack token")
	}
	body := postMessageRequest{Channel: channel, ThreadTS: threadTS, Text: text, Blocks: blocks}
	if threadTS == "" {
		body.ThreadTS = ""
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out postMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack post message failed: %s", out.Error)
	}
	return out.TS, nil
}

func (c *Client) FormatStatusBlocks(status, summary string, actions []map[string]any) []map[string]any {
	blocks := []map[string]any{
		{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Status:* `%s`\n%s", strings.TrimSpace(status), summary),
			},
		},
	}
	if len(actions) > 0 {
		blocks = append(blocks, map[string]any{
			"type":     "actions",
			"elements": actions,
		})
	}
	return blocks
}

func Button(actionID, text, value, style string) map[string]any {
	btn := map[string]any{
		"type":      "button",
		"action_id": actionID,
		"text": map[string]any{
			"type": "plain_text",
			"text": text,
		},
		"value": value,
	}
	if style != "" {
		btn["style"] = style
	}
	return btn
}
