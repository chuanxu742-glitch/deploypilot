package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the Bailu OpenAI-compatible endpoint.
const DefaultBaseURL = "https://bailucode.com/openapi/v1"

// DefaultModel is used when no model is configured. bailu-auto was verified
// to support streaming and tool calling.
const DefaultModel = "bailu-auto"

// Client talks to an OpenAI-compatible chat API.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// Config configures a Client.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// HTTPClient is optional; a sensible default is used when nil.
	HTTPClient *http.Client
}

// New creates a Client. BaseURL defaults to DefaultBaseURL.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		baseURL: base,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		model:   model,
		http:    hc,
	}
}

// FromEnv builds a Client from BAILU_API_KEY / BAILU_MODEL (and optional
// BAILU_BASE_URL). The API key may be empty if not yet provided.
func FromEnv() *Client {
	return New(Config{
		BaseURL: os.Getenv("BAILU_BASE_URL"),
		APIKey:  os.Getenv("BAILU_API_KEY"),
		Model:   os.Getenv("BAILU_MODEL"),
	})
}

// SetAPIKey updates the in-memory API key (set by the operator at runtime).
// Surrounding whitespace (e.g. a pasted trailing newline) is removed.
func (c *Client) SetAPIKey(k string) { c.apiKey = strings.TrimSpace(k) }

// SetModel updates the model name.
func (c *Client) SetModel(m string) { c.model = strings.TrimSpace(m) }

// Model returns the configured model.
func (c *Client) Model() string { return c.model }

// HasKey reports whether an API key is configured.
func (c *Client) HasKey() bool { return c.apiKey != "" }

// Redact removes the API key from a string so it is never leaked in logs.
func (c *Client) Redact(s string) string {
	if c.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "***")
}

// ListModels returns available model IDs from GET /models.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp)
	}
	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mr.Data))
	for _, d := range mr.Data {
		out = append(out, d.ID)
	}
	return out, nil
}

// StreamChat performs a streaming completion. onDelta is called with each
// incremental content chunk (may be nil). The fully assembled assistant
// message and finish reason are returned.
func (c *Client) StreamChat(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	if c.apiKey == "" {
		return nil, errors.New("no API key configured")
	}
	if req.Model == "" {
		req.Model = c.model
	}
	if req.Model == "" {
		return nil, errors.New("no model configured")
	}
	req.Stream = true

	var result *ChatResult
	err := c.withRetry(ctx, func() error {
		r, err := c.doStream(ctx, req, onDelta)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	return result, err
}

func (c *Client) doStream(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.auth(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp)
	}

	asm := newAssembler()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // tolerate keep-alives / malformed partials
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" && onDelta != nil {
			onDelta(ch.Delta.Content)
		}
		asm.add(ch.Delta)
		if ch.FinishReason != "" {
			asm.finishReason = ch.FinishReason
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return asm.result(), nil
}

func (c *Client) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "deploypilot")
}

// statusError builds a redacted error from a non-200 response.
func (c *Client) statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := c.Redact(string(bytes.TrimSpace(b)))
	return &apiError{status: resp.StatusCode, body: msg}
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("llm api error: status %d: %s", e.status, e.body)
}

// retryable reports whether an error warrants a retry.
func retryable(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status == http.StatusTooManyRequests || ae.status >= 500
	}
	// Network errors are retryable.
	return err != nil
}

func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	const maxAttempts = 4
	delay := 500 * time.Millisecond
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = fn()
		if err == nil || !retryable(err) || attempt == maxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return err
}
