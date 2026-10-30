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
	"strconv"
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
	mu                sync.RWMutex
	baseURL           string
	apiKey            string
	model             string
	http              *http.Client
	streamIdleTimeout time.Duration
	onRetryWait       func(attempt int, delay time.Duration)
}

// Config configures a Client.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// HTTPClient is optional; a sensible default is used when nil.
	HTTPClient *http.Client
	// StreamIdleTimeout limits the time without response-body data. Zero uses
	// the 180-second default; this is not a total stream duration limit.
	StreamIdleTimeout time.Duration
	// OnRetryWait is called before each retry delay with the failed attempt
	// number and effective wait (including Retry-After when longer).
	OnRetryWait func(attempt int, delay time.Duration)
}

const defaultStreamIdleTimeout = 180 * time.Second

// New creates a Client. BaseURL defaults to DefaultBaseURL.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// No overall timeout: a long streaming completion must not be cut off
		// mid-response. The transport bounds the first response header; doStream
		// separately bounds response-body idle time. Inherit proxy settings.
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
		streamIdleTimeout: func() time.Duration {
			if cfg.StreamIdleTimeout <= 0 {
				return defaultStreamIdleTimeout
			}
			return cfg.StreamIdleTimeout
		}(),
		onRetryWait: cfg.OnRetryWait,
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
	return &Client{baseURL: c.baseURL, apiKey: c.apiKey, model: c.model, http: c.http, streamIdleTimeout: c.streamIdleTimeout, onRetryWait: c.onRetryWait}
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
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
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
	// Most stream errors are not retried once content has reached the UI. The
	// one explicit upstream_busy case may retry the same model request; this
	// attempt's assembler is local and discarded, and tools only run after the
	// entire response has passed validation. Previously streamed text can repeat.
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
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
	idleBody := newIdleTimeoutBody(resp.Body, c.streamIdleTimeout, cancelStream)
	defer idleBody.Close()

	asm := newAssembler()
	totalBytes := 0
	sawDone := false
	sc := bufio.NewScanner(idleBody)
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
			sawDone = true
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("invalid model stream event: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			var providerErr struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(chunk.Error, &providerErr) == nil && providerErr.Code == upstreamBusyCode {
				return nil, &upstreamBusyError{code: providerErr.Code}
			}
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
	if idleBody.TimedOut() {
		return nil, fmt.Errorf("%w: no response data for %s", ErrStreamInterrupted, c.streamIdleTimeout)
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The connection broke mid-response: a transport failure, not a bad reply.
		return nil, fmt.Errorf("%w: %v", ErrStreamInterrupted, err)
	}
	if !sawDone && asm.finishReason == "" {
		// The body ended without [DONE] or a finish reason (proxy/tunnel drop).
		return nil, fmt.Errorf("%w: response ended early", ErrStreamInterrupted)
	}
	result = asm.result()
	if err := validateResult(result); err != nil {
		return nil, &streamError{err: err}
	}
	return result, nil
}

// ErrStreamInterrupted marks a response whose connection dropped before the
// model finished. No tools from it were executed, so the caller may safely
// request the same turn again; already-streamed text may then appear twice.
var ErrStreamInterrupted = errors.New("model stream interrupted")

const upstreamBusyCode = "upstream_busy"

type upstreamBusyError struct{ code string }

func (e *upstreamBusyError) Error() string {
	return fmt.Sprintf("模型服务持续繁忙，请稍后继续当前任务或更换模型（code: %s）", e.code)
}

// IsStreamInterrupted reports whether err is a transport interruption.
func IsStreamInterrupted(err error) bool { return errors.Is(err, ErrStreamInterrupted) }

// IsContextOverflow reports whether the provider rejected the request because
// the prompt exceeds the model's context window. The byte budget is only an
// estimate, so the caller can compact harder and retry.
func IsContextOverflow(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) || (ae.status != http.StatusBadRequest && ae.status != http.StatusRequestEntityTooLarge) {
		return false
	}
	body := strings.ToLower(ae.body)
	if ae.status == http.StatusRequestEntityTooLarge {
		return true
	}
	for _, marker := range []string{"context length", "context_length", "maximum context", "context window", "too many tokens", "prompt is too long", "token limit", "tokens exceed", "max_tokens", "reduce the length"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// streamError prevents replaying partial replies for ordinary stream failures.
// The exact upstream_busy provider code is checked before this wrapper by
// retryable; its incomplete assembled result is discarded before retry.
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
	return &apiError{status: resp.StatusCode, body: msg, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
}

// parseRetryAfter reads the delay-seconds form of Retry-After (the form used by
// rate limiters). HTTP-date values and garbage yield zero.
func parseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

type apiError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("llm api error: status %d: %s", e.status, e.body)
}

// retryable reports whether an error warrants a retry.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var be *upstreamBusyError
	if errors.As(err, &be) {
		return true
	}
	var se *streamError
	if errors.As(err, &se) || errors.Is(err, ErrStreamInterrupted) {
		return false
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status == http.StatusTooManyRequests || ae.status >= 500
	}
	// Network errors are retryable.
	return err != nil
}

// retryBaseDelay is the first backoff step; a variable so tests can shorten it.
var retryBaseDelay = 2 * time.Second

// maxRetryAfter caps a provider-requested wait so a bad header cannot stall a
// task indefinitely.
const maxRetryAfter = 60 * time.Second

type retryWaitContextKey struct{}

// WithRetryWait returns a context whose callback observes each internal API
// retry delay. A callback attached to the request context takes precedence
// over the Client's Config.OnRetryWait callback.
func WithRetryWait(ctx context.Context, callback func(attempt int, delay time.Duration)) context.Context {
	return context.WithValue(ctx, retryWaitContextKey{}, callback)
}

// withRetry retries pre-stream failures and stream responses with the exact
// upstream_busy provider code.
// Delays grow exponentially from 2s to 60s (about three minutes at the limit),
// or use the provider's Retry-After when it asks for longer.
func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	const maxAttempts = 8
	delay := retryBaseDelay
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) || attempt == maxAttempts {
			return err
		}
		wait := delay
		var ae *apiError
		if errors.As(err, &ae) && ae.retryAfter > wait {
			wait = min(ae.retryAfter, maxRetryAfter)
		}
		onWait := c.onRetryWait
		if callback, ok := ctx.Value(retryWaitContextKey{}).(func(int, time.Duration)); ok && callback != nil {
			onWait = callback
		}
		if onWait != nil {
			onWait(attempt, wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, maxRetryAfter)
	}
	return err
}
