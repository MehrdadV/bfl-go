package bfl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// APIError describes a non-2xx response from BFL.
type APIError struct {
	StatusCode int
	Message    string
	Details    map[string]any
	RequestID  string
	cause      error
}

func (e *APIError) Error() string {
	if e == nil {
		return "bfl: API error"
	}
	if e.Message != "" {
		return fmt.Sprintf("bfl: API returned HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("bfl: API returned HTTP %d", e.StatusCode)
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ProtocolError indicates a successful HTTP response that could not be
// interpreted according to the expected API response contract.
type ProtocolError struct {
	Operation string
	Err       error
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "bfl: protocol error"
	}
	return fmt.Sprintf("bfl: invalid response for %s: %v", e.Operation, e.Err)
}

func (e *ProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func decodeAPIError(resp *http.Response) error {
	apiErr := &APIError{StatusCode: resp.StatusCode, RequestID: resp.Header.Get("x-request-id")}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes))
	if err != nil {
		apiErr.cause = err
		apiErr.Message = http.StatusText(resp.StatusCode)
		return apiErr
	}
	if len(data) == 0 {
		apiErr.Message = http.StatusText(resp.StatusCode)
		return apiErr
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		apiErr.Message = http.StatusText(resp.StatusCode)
		apiErr.cause = fmt.Errorf("decode error response: %w", err)
		return apiErr
	}
	apiErr.Details = payload
	if detail, ok := payload["detail"]; ok {
		apiErr.Message = fmt.Sprint(detail)
	} else if message, ok := payload["message"].(string); ok {
		apiErr.Message = message
	} else {
		apiErr.Message = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

// IsAPIStatus reports whether err is an APIError with the given status code.
func IsAPIStatus(err error, statusCode int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}
