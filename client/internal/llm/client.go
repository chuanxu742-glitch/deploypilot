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
	"sync"
	"time"
)

// DefaultBaseURL is the Bailu OpenAI-compatible endpoint.
const DefaultBaseURL = "https://bailucode.com/openapi/v1"

// DefaultModel is used when no model is configured. bailu-auto was verified
// to support streaming and tool calling.
const DefaultModel = "bailu-auto"

// Client talks to an OpenAI-compatible chat API.
type Client struct {
	mu      sync.RWMutex
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
		// No overall timeout: a long streaming completion must not be cut off
		// mid-response. Bound only the time to the first response header, and
		// inherit proxy settings from the environment via the default transport.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = 60 * time.Second
		hc = &http.Client{Transport: tr}
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
func (c *Client) SetAPIKey(k string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = strings.TrimSpace(k)
}

// SetModel updates the model name.
func (c *Client) SetModel(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = strings.TrimSpace(m)
}

// Model returns the configured model.
func (c *Client) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// HasKey reports whether an API key is configured.
func (c *Client) HasKey() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey != ""
}

// Snapshot keeps a running task on one configuration while settings may change.
func (c *Client) Snapshot() *Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &Client{baseURL: c.baseURL, apiKey: c.apiKey, model: c.model, http: c.http}
}

// Redact removes the API key from a string so it is never leaked in logs.
func (c *Client) Redact(s string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.apiKey, "***")
}

// ListModels returns available model IDs from GET /models.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	c = c.Snapshot()
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
	c = c.Snapshot()
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

func (c *Client) doStream(ctx context.Context, req ChatRequest, onDelta func(string)) (result *ChatResult, err error) {
	// Once a stream has begun, retrying would replay already displayed text.
	// Tools only run after the entire response has passed validation.
	started := false
	defer func() {
		if err != nil && started {
			err = &streamError{err: err}
		}
	}()
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
	totalBytes := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		totalBytes += len(sc.Bytes())
		if totalBytes > 2*1024*1024 {
			return nil, &streamError{err: errors.New("model response exceeds 2 MiB limit")}
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		started = true
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("invalid model stream event: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return nil, fmt.Errorf("model stream error: %s", c.Redact(string(chunk.Error)))
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" && onDelta != nil {
			onDelta(ch.Delta.Content)
		}
		if err := asm.add(ch.Delta); err != nil {
			return nil, err
		}
		if ch.FinishReason != "" {
			asm.finishReason = ch.FinishReason
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	result = asm.result()
	if err := validateResult(result); err != nil {
		return nil, &streamError{err: err}
	}
	return result, nil
}

// streamError is not retried: malformed/truncated output is not a completed
// assistant turn, and its partial content may already have reached the UI.
type streamError struct{ err error }

func (e *streamError) Error() string { return e.err.Error() }
func (e *streamError) Unwrap() error { return e.err }

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
	var se *streamError
	if errors.As(err, &se) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
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
