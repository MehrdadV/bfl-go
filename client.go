package bfl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.bfl.ai"

const maxJSONResponseBytes = 8 << 20

// Client is a concurrency-safe client for the Black Forest Labs API.
type Client struct {
	Images *ImageService
	Videos *VideoService

	apiKey       string
	baseURL      *url.URL
	httpClient   *http.Client
	retryPolicy  RetryPolicy
	pollInterval time.Duration
	pollTimeout  time.Duration
}

type clientConfig struct {
	apiKey       string
	baseURL      string
	httpClient   *http.Client
	retryPolicy  RetryPolicy
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// ClientOption configures a Client.
type ClientOption func(*clientConfig) error

// New creates a client. An API key is required.
func New(opts ...ClientOption) (*Client, error) {
	cfg := clientConfig{
		baseURL:      defaultBaseURL,
		httpClient:   http.DefaultClient,
		pollInterval: 500 * time.Millisecond,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("bfl: nil client option")
		}
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(cfg.apiKey) == "" {
		return nil, errors.New("bfl: API key is required; use WithAPIKey")
	}
	if cfg.httpClient == nil {
		return nil, errors.New("bfl: HTTP client must not be nil")
	}
	if cfg.pollInterval <= 0 {
		return nil, errors.New("bfl: poll interval must be greater than zero")
	}
	if cfg.pollTimeout < 0 {
		return nil, errors.New("bfl: poll timeout must not be negative")
	}
	base, err := parseBaseURL(cfg.baseURL)
	if err != nil {
		return nil, err
	}

	c := &Client{
		apiKey:       cfg.apiKey,
		baseURL:      base,
		httpClient:   cfg.httpClient,
		retryPolicy:  cfg.retryPolicy,
		pollInterval: cfg.pollInterval,
		pollTimeout:  cfg.pollTimeout,
	}
	c.Images = &ImageService{client: c}
	c.Videos = &VideoService{client: c}
	return c, nil
}

// WithAPIKey sets the BFL API key sent in the x-key header.
func WithAPIKey(key string) ClientOption {
	return func(cfg *clientConfig) error {
		if strings.TrimSpace(key) == "" {
			return errors.New("bfl: API key must not be empty")
		}
		cfg.apiKey = key
		return nil
	}
}

// WithHTTPClient sets the HTTP client used for API and media requests.
// The caller's client, including its timeout and transport, is used as-is.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(cfg *clientConfig) error {
		if client == nil {
			return errors.New("bfl: HTTP client must not be nil")
		}
		cfg.httpClient = client
		return nil
	}
}

// WithBaseURL sets the API root. Official BFL HTTPS hosts are accepted.
// Plain HTTP is accepted only for loopback hosts, to support local test servers.
func WithBaseURL(baseURL string) ClientOption {
	return func(cfg *clientConfig) error {
		if _, err := parseBaseURL(baseURL); err != nil {
			return err
		}
		cfg.baseURL = baseURL
		return nil
	}
}

// RetryPolicy controls retries for explicitly retryable safe requests.
// Generation submission requests are never automatically retried.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	// Zero disables retries and makes safe requests run once.
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// WithRetryPolicy sets bounded retry behavior for safe requests.
func WithRetryPolicy(policy RetryPolicy) ClientOption {
	return func(cfg *clientConfig) error {
		if policy.MaxAttempts < 0 {
			return errors.New("bfl: retry MaxAttempts must not be negative")
		}
		if policy.BaseDelay < 0 {
			return errors.New("bfl: retry BaseDelay must not be negative")
		}
		if policy.MaxAttempts > 1 && policy.BaseDelay <= 0 {
			return errors.New("bfl: retry BaseDelay must be greater than zero")
		}
		if policy.MaxDelay < 0 || (policy.MaxDelay > 0 && policy.BaseDelay > policy.MaxDelay) {
			return errors.New("bfl: retry MaxDelay must be zero or at least BaseDelay")
		}
		cfg.retryPolicy = policy
		return nil
	}
}

// WithPollInterval sets the interval between status requests. The default is 500ms.
func WithPollInterval(interval time.Duration) ClientOption {
	return func(cfg *clientConfig) error {
		if interval <= 0 {
			return errors.New("bfl: poll interval must be greater than zero")
		}
		cfg.pollInterval = interval
		return nil
	}
}

// WithPollTimeout sets a maximum duration for Wait operations. Zero means no
// SDK-level timeout; callers can always bound operations with context deadlines.
func WithPollTimeout(timeout time.Duration) ClientOption {
	return func(cfg *clientConfig) error {
		if timeout < 0 {
			return errors.New("bfl: poll timeout must not be negative")
		}
		cfg.pollTimeout = timeout
		return nil
	}
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("bfl: invalid API base URL %q", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("bfl: API base URL must not include a path")
	}
	host := strings.ToLower(u.Hostname())
	loopback := host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("bfl: API base URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	if u.Scheme == "https" && !(host == "bfl.ai" || strings.HasSuffix(host, ".bfl.ai")) {
		return nil, errors.New("bfl: HTTPS API base URL must be a BFL host")
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

// doJSON performs one API request, decodes a successful JSON response, and
// returns structured errors for non-2xx responses. Retrying is deliberately
// not implicit here; callers must opt into the safe-request retry helper.
func (c *Client) doJSON(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("bfl: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJSONResponseBytes))
		return err
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxJSONResponseBytes+1))
	if err := dec.Decode(out); err != nil {
		return &ProtocolError{Operation: method + " " + path, Err: err}
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, fmt.Errorf("bfl: API path must be an absolute path, got %q", path)
	}
	u := *c.baseURL
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("bfl: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// validatePollingURL checks a BFL-issued URL before sending credentials to it.
// Polling URLs may use another BFL cluster hostname, so allow HTTPS subdomains
// of bfl.ai while preserving the exact URL returned by BFL.
func validatePollingURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("bfl: invalid polling URL")
	}
	host := strings.ToLower(u.Hostname())
	if host != "bfl.ai" && !strings.HasSuffix(host, ".bfl.ai") {
		return nil, errors.New("bfl: polling URL host is not a BFL domain")
	}
	return u, nil
}

type ImageService struct{ client *Client }
type VideoService struct{ client *Client }
