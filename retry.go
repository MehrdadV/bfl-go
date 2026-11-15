package bfl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// doSafe is intentionally restricted to GET: it retries only operations that
// do not create a new generation task. The caller owns resp.Body on success.
func (c *Client) doSafe(ctx context.Context, path string) (*http.Response, error) {
	attempts := c.retryPolicy.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := c.newRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("bfl: GET %s: %w", path, err)
			if !retryableTransportError(ctx, err) || attempt == attempts {
				return nil, lastErr
			}
			if err := c.waitRetry(ctx, attempt, ""); err != nil {
				return nil, errors.Join(lastErr, err)
			}
			continue
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

func retryableTransportError(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError || status == http.StatusServiceUnavailable
}

func (c *Client) waitRetry(ctx context.Context, retryNumber int, retryAfter string) error {
	delay := c.retryPolicy.BaseDelay
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	for n := 1; n < retryNumber; n++ {
		if delay >= time.Duration(1<<62) {
			break
		}
		delay *= 2
	}
	if parsed, ok := parseRetryAfter(retryAfter, time.Now()); ok && parsed > delay {
		delay = parsed
	}
	if max := c.retryPolicy.MaxDelay; max > 0 && delay > max {
		delay = max
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
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
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
