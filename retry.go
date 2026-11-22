package bfl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (c *Client) doSafeURL(ctx context.Context, target *url.URL, consume func(*http.Response) (bool, error)) (*http.Response, error) {
	return c.doSafeURLWithAuth(ctx, target, true, consume)
}

// mediaHTTPClient returns a copy of base that re-validates every redirect target as a delivery URL.
func mediaHTTPClient(base *http.Client) *http.Client {
	client := *base
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Referer")
		if checkRedirect != nil {
			if err := checkRedirect(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL == nil {
			return errors.New("bfl: invalid media redirect URL")
		}
		if _, err := validateDeliveryURL(req.URL.String()); err != nil {
			return err
		}
		req.Header.Del("Referer")
		return nil
	}
	return &client
}

func (c *Client) doSafeURLWithAuth(ctx context.Context, target *url.URL, authenticate bool, consume func(*http.Response) (bool, error)) (*http.Response, error) {
	var mediaClient *http.Client
	if !authenticate {
		mediaClient = mediaHTTPClient(c.httpClient)
	}
	attempts := c.retryPolicy.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			if !authenticate {
				return nil, &mediaRequestError{err}
			}
			return nil, fmt.Errorf("bfl: create polling request: %w", err)
		}
		if authenticate {
			req.Header.Set("Accept", "application/json")
			req.Header.Set("x-key", c.apiKey)
		}
		var resp *http.Response
		if authenticate {
			resp, err = c.doAuthenticated(req)
		} else {
			resp, err = mediaClient.Do(req)
		}
		if err != nil {
			if authenticate {
				lastErr = fmt.Errorf("bfl: GET polling URL: %w", err)
			} else {
				lastErr = &mediaRequestError{err}
			}
			if !retryableTransportError(ctx, err) || attempt == attempts {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, errors.Join(lastErr, ctxErr)
				}
				return nil, lastErr
			}
			if err := c.waitRetry(ctx, attempt, ""); err != nil {
				return nil, errors.Join(lastErr, err)
			}
			continue
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes+1))
			closeErr := resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("bfl: read 503 response: %w", readErr)
				if closeErr != nil {
					lastErr = errors.Join(lastErr, fmt.Errorf("bfl: close 503 response: %w", closeErr))
				}
				if ctx.Err() != nil {
					return nil, errors.Join(lastErr, ctx.Err())
				}
				if closeErr != nil || !retryableTransportError(ctx, readErr) || attempt == attempts {
					return nil, lastErr
				}
				if err := c.waitRetry(ctx, attempt, resp.Header.Get("Retry-After")); err != nil {
					return nil, errors.Join(lastErr, err)
				}
				continue
			}
			if closeErr != nil {
				return nil, fmt.Errorf("bfl: close 503 response: %w", closeErr)
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			if (authenticate && isTerminalJobResponse(body)) || attempt == attempts {
				return resp, nil
			}
			retryAfter := resp.Header.Get("Retry-After")
			_ = resp.Body.Close()
			if err := c.waitRetry(ctx, attempt, retryAfter); err != nil {
				return nil, err
			}
			continue
		}
		if isSuccessfulHTTPStatus(resp.StatusCode) && consume != nil {
			retry, consumeErr := consume(resp)
			if consumeErr != nil {
				lastErr = consumeErr
				if ctx.Err() != nil {
					return nil, errors.Join(lastErr, ctx.Err())
				}
				if !retry || !retryableTransportError(ctx, consumeErr) || attempt == attempts {
					return nil, lastErr
				}
				if err := c.waitRetry(ctx, attempt, ""); err != nil {
					return nil, errors.Join(lastErr, err)
				}
				continue
			}
		}
		if !retryableStatus(resp.StatusCode) || attempt == attempts {
			return resp, nil
		}
		retryAfter := resp.Header.Get("Retry-After")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJSONResponseBytes))
		_ = resp.Body.Close()
		if err := c.waitRetry(ctx, attempt, retryAfter); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

type mediaRequestError struct{ err error }

func (e *mediaRequestError) Error() string {
	cause := e.err
	if urlErr, ok := cause.(*url.Error); ok {
		cause = urlErr.Err
	}
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.Is(e.err, context.Canceled):
		return "bfl: media request canceled"
	case errors.Is(e.err, context.DeadlineExceeded), errors.As(cause, &netErr) && netErr.Timeout():
		return "bfl: media request timed out"
	case errors.Is(e.err, syscall.ECONNREFUSED):
		return "bfl: media connection refused"
	case errors.As(e.err, &dnsErr):
		return "bfl: media DNS lookup failed"
	default:
		return "bfl: media request failed"
	}
}

func (e *mediaRequestError) Unwrap() error { return e.err }

func retryableTransportError(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError
}

func (c *Client) waitRetry(ctx context.Context, retryNumber int, retryAfter string) error {
	maxDelay := c.retryPolicy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	delay := c.retryPolicy.BaseDelay
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	for n := 1; n < retryNumber; n++ {
		if delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if parsed, ok := parseRetryAfter(retryAfter, time.Now()); ok && parsed > delay {
		delay = parsed
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		if seconds > int64((1<<63-1)/int64(time.Second)) {
			return time.Duration(1<<63 - 1), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	d := when.Sub(now)
	if d < 0 {
		d = 0
	}
	return d, true
}
