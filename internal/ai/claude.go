// Package ai is AndRho's thin client for Anthropic's Messages API. The AI
// in AndRho translates (numbers to plain language, a free-text description
// to structured answers); the analysis itself is statistics. Every call
// records its token usage through the Recorder, which feeds the staff-only
// Tokens Usage view.
//
// Without ANTHROPIC_API_KEY the client is disabled: Enabled() is false and
// callers fall back to their rule-based path.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrDisabled is returned when no API key is configured.
var ErrDisabled = errors.New("ai: ANTHROPIC_API_KEY is not set")

// Usage is what one call spent.
type Usage struct {
	Source       string // what the tokens were for: analyzer, insights, whatsapp, development, ...
	AccountID    string // empty for calls not tied to an account (signup analysis)
	Model        string
	InputTokens  int
	OutputTokens int
}

// Recorder stores usage; errors are the recorder's problem, never the caller's.
type Recorder func(ctx context.Context, u Usage)

// Client calls the Messages API.
type Client struct {
	key    string
	model  string
	url    string
	http   *http.Client
	record Recorder
}

// New builds a client. An empty key gives a disabled client.
func New(key, model string, record Recorder) *Client {
	if model == "" {
		model = "claude-sonnet-5-5"
	}
	return &Client{
		key: key, model: model, url: "https://api.anthropic.com/v1/messages",
		http: &http.Client{Timeout: 45 * time.Second}, record: record,
	}
}

// Enabled reports whether calls can be made.
func (c *Client) Enabled() bool { return c != nil && c.key != "" }

// Model is the model calls use.
func (c *Client) Model() string { return c.model }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends one user message and returns the text of the answer.
// source/accountID label the usage record.
func (c *Client) Complete(ctx context.Context, source, accountID, system, user string, maxTokens int) (string, error) {
	if !c.Enabled() {
		return "", ErrDisabled
	}
	body, _ := json.Marshal(request{Model: c.model, MaxTokens: maxTokens, System: system, Messages: []message{{Role: "user", Content: user}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.key)
	req.Header.Set("anthropic-version", "2023-06-01")

	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai: request: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("ai: decode (%d): %w", res.StatusCode, err)
	}
	if res.StatusCode != http.StatusOK {
		msg := res.Status
		if out.Error != nil {
			msg = out.Error.Message
		}
		return "", fmt.Errorf("ai: %s", msg)
	}
	if c.record != nil {
		c.record(ctx, Usage{Source: source, AccountID: accountID, Model: c.model,
			InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens})
	}
	var text strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	return text.String(), nil
}

// JSON asks for a JSON answer and decodes it into v. The model is told to
// answer with JSON only; a ```json fence or text around it is tolerated.
func (c *Client) JSON(ctx context.Context, source, accountID, system, user string, maxTokens int, v any) error {
	text, err := c.Complete(ctx, source, accountID, system+"\n\nResponde únicamente con un objeto JSON válido, sin texto antes ni después.", user, maxTokens)
	if err != nil {
		return err
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return fmt.Errorf("ai: no JSON object in answer")
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), v); err != nil {
		return fmt.Errorf("ai: bad JSON: %w", err)
	}
	return nil
}
