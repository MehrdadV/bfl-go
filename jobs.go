package bfl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// JobState is a status string returned by BFL. Unknown future values are
// preserved so clients can report them without treating them as success.
type JobState string

const (
	JobStatePending          JobState = "Pending"
	JobStateReasoning        JobState = "Reasoning"
	JobStateGenerating       JobState = "Generating"
	JobStateReady            JobState = "Ready"
	JobStateRequestModerated JobState = "Request Moderated"
	JobStateContentModerated JobState = "Content Moderated"
	JobStateNoCardGenerated  JobState = "No Card Generated"
	JobStateError            JobState = "Error"
	JobStateNotFound         JobState = "Task not found"
	JobStateFailed           JobState = "Failed"
)

// JobStatus is the latest status returned for an asynchronous task.
type JobStatus struct {
	ID       string
	State    JobState
	Progress *float64
	Details  map[string]any
}

type submitResponse struct {
	ID         string   `json:"id"`
	PollingURL string   `json:"polling_url"`
	Cost       *float64 `json:"cost"`
	InputMP    *float64 `json:"input_mp"`
	OutputMP   *float64 `json:"output_mp"`
}

type statusResponse struct {
	ID       string          `json:"id"`
	Status   JobState        `json:"status"`
	Result   json.RawMessage `json:"result"`
	Progress *float64        `json:"progress"`
	Details  map[string]any  `json:"details"`
}

type jobCore struct {
	client     *Client
	id         string
	pollingURL string
}

// ImageJob is a handle for an asynchronous image task.
type ImageJob struct {
	core        *jobCore
	materialize func(context.Context, string) (*Image, error)
}

// VideoJob is a handle for an asynchronous video task.
type VideoJob struct {
	core        *jobCore
	materialize func(context.Context, string) (*Video, error)
}

func newImageJob(client *Client, response submitResponse, materialize func(context.Context, string) (*Image, error)) (*ImageJob, error) {
	core, err := newJobCore(client, response)
	if err != nil {
		return nil, err
	}
	return &ImageJob{core: core, materialize: materialize}, nil
}

func newVideoJob(client *Client, response submitResponse, materialize func(context.Context, string) (*Video, error)) (*VideoJob, error) {
	core, err := newJobCore(client, response)
	if err != nil {
		return nil, err
	}
	return &VideoJob{core: core, materialize: materialize}, nil
}

// ID returns the BFL task identifier.
func (j *ImageJob) ID() string {
	if j == nil || j.core == nil {
		return ""
	}
	return j.core.id
}

// ID returns the BFL task identifier.
func (j *VideoJob) ID() string {
	if j == nil || j.core == nil {
		return ""
	}
	return j.core.id
}

// Status fetches the latest task status using the polling URL returned by BFL.
func (j *ImageJob) Status(ctx context.Context) (JobStatus, error) {
	if j == nil || j.core == nil {
		return JobStatus{}, errors.New("bfl: invalid image job")
	}
	status, _, err := j.core.statusResult(ctx)
	return status, err
}

// Status fetches the latest task status using the polling URL returned by BFL.
func (j *VideoJob) Status(ctx context.Context) (JobStatus, error) {
	if j == nil || j.core == nil {
		return JobStatus{}, errors.New("bfl: invalid video job")
	}
	status, _, err := j.core.statusResult(ctx)
	return status, err
}

// Wait polls until the image is ready or the context/SDK polling timeout ends.
func (j *ImageJob) Wait(ctx context.Context) (*Image, error) {
	if j == nil || j.core == nil {
		return nil, errors.New("bfl: nil image job")
	}
	if ctx == nil {
		return nil, errors.New("bfl: context must not be nil")
	}
	if j.materialize == nil {
		return nil, errors.New("bfl: image result downloader is not configured")
	}
	waitCtx, cancel := j.core.waitContext(ctx)
	defer cancel()
	resultURL, err := j.core.wait(waitCtx, ctx)
	if err != nil {
		return nil, err
	}
	return j.materialize(waitCtx, resultURL)
}

// Wait polls until the video is ready or the context/SDK polling timeout ends.
func (j *VideoJob) Wait(ctx context.Context) (*Video, error) {
	if j == nil || j.core == nil {
		return nil, errors.New("bfl: nil video job")
	}
	if ctx == nil {
		return nil, errors.New("bfl: context must not be nil")
	}
	if j.materialize == nil {
		return nil, errors.New("bfl: video result downloader is not configured")
	}
	waitCtx, cancel := j.core.waitContext(ctx)
	defer cancel()
	resultURL, err := j.core.wait(waitCtx, ctx)
	if err != nil {
		return nil, err
	}
	return j.materialize(waitCtx, resultURL)
}

func (j *jobCore) statusResult(ctx context.Context) (status JobStatus, result json.RawMessage, retErr error) {
	if j == nil || j.client == nil {
		return JobStatus{}, nil, errors.New("bfl: invalid job handle")
	}
	if ctx == nil {
		return JobStatus{}, nil, errors.New("bfl: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return JobStatus{}, nil, err
	}
	u, err := j.client.validatePollingURL(j.pollingURL)
	if err != nil {
		return JobStatus{}, nil, err
	}
	resp, err := j.client.doSafeURL(ctx, u, func(resp *http.Response) (bool, error) {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			err := error(&ProtocolError{Operation: "GET polling URL", Err: readErr})
			if closeErr != nil {
				err = errors.Join(err, fmt.Errorf("bfl: close polling response body: %w", closeErr))
			}
			return closeErr == nil, err
		}
		if closeErr != nil {
			return false, fmt.Errorf("bfl: close polling response body: %w", closeErr)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return false, nil
	})
	if err != nil {
		return JobStatus{}, nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("bfl: close polling response body: %w", err))
		}
	}()
	if !isSuccessfulHTTPStatus(resp.StatusCode) {
		if resp.StatusCode == http.StatusServiceUnavailable {
			// doSafeURL buffers 503 bodies in memory, so this read cannot fail.
			body, _ := io.ReadAll(resp.Body)
			var payload statusResponse
			if json.Unmarshal(body, &payload) == nil && isTerminalJobState(payload.Status) {
				return jobStatus(j.id, payload)
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
		return JobStatus{}, nil, decodeAPIError(resp)
	}
	var payload statusResponse
	if err := decodeLimitedJSON(resp.Body, &payload); err != nil {
		return JobStatus{}, nil, &ProtocolError{Operation: "GET polling URL", Err: err}
	}
	if payload.Status == "" {
		return JobStatus{}, nil, &ProtocolError{Operation: "GET polling URL", Err: errors.New("missing status")}
	}
	return jobStatus(j.id, payload)
}

func jobStatus(jobID string, payload statusResponse) (JobStatus, json.RawMessage, error) {
	if payload.Status == "" {
		return JobStatus{}, nil, &ProtocolError{Operation: "GET polling URL", Err: errors.New("missing status")}
	}
	if payload.ID == "" {
		payload.ID = jobID
	} else if payload.ID != jobID {
		return JobStatus{}, nil, &ProtocolError{Operation: "GET polling URL", Err: fmt.Errorf("response task id %q does not match submitted task %q", payload.ID, jobID)}
	}
	return JobStatus{ID: payload.ID, State: payload.Status, Progress: payload.Progress, Details: payload.Details}, payload.Result, nil
}

func isTerminalJobResponse(body []byte) bool {
	var payload struct {
		Status JobState `json:"status"`
	}
	return json.Unmarshal(body, &payload) == nil && isTerminalJobState(payload.Status)
}

func isTerminalJobState(state JobState) bool {
	switch state {
	case JobStateError, JobStateFailed, JobStateRequestModerated, JobStateContentModerated, JobStateNoCardGenerated, JobStateNotFound:
		return true
	default:
		return false
	}
}

func (j *jobCore) waitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if j.client.pollTimeout > 0 {
		return context.WithTimeout(ctx, j.client.pollTimeout)
	}
	return ctx, func() {}
}

func (j *jobCore) wait(ctx, callerCtx context.Context) (string, error) {
	if j == nil || j.client == nil {
		return "", errors.New("bfl: invalid job handle")
	}
	if ctx == nil {
		return "", errors.New("bfl: context must not be nil")
	}
	for {
		status, rawResult, err := j.statusResult(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && callerCtx.Err() == nil {
				return "", fmt.Errorf("bfl: wait for job %s: polling timeout: %w", j.id, err)
			}
			return "", err
		}
		switch status.State {
		case JobStateReady:
			resultURL, err := extractSampleURL(rawResult)
			if err != nil {
				return "", err
			}
			return resultURL, nil
		case JobStateError, JobStateFailed, JobStateRequestModerated, JobStateContentModerated, JobStateNoCardGenerated, JobStateNotFound:
			return "", &JobError{ID: status.ID, State: status.State, Details: status.Details}
		}

		timer := time.NewTimer(j.client.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && callerCtx.Err() == nil {
				return "", fmt.Errorf("bfl: wait for job %s: polling timeout: %w", j.id, ctx.Err())
			}
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func extractSampleURL(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", &ProtocolError{Operation: "job result", Err: errors.New("ready response has no result")}
	}
	var result struct {
		Sample string `json:"sample"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", &ProtocolError{Operation: "job result", Err: err}
	}
	if result.Sample == "" {
		return "", &ProtocolError{Operation: "job result", Err: errors.New("ready response has no result.sample URL")}
	}
	return result.Sample, nil
}

func newJobCore(client *Client, response submitResponse) (*jobCore, error) {
	if client == nil {
		return nil, errors.New("bfl: nil client")
	}
	if response.ID == "" || response.PollingURL == "" {
		return nil, &ProtocolError{Operation: "submit", Err: errors.New("missing required id or polling_url")}
	}
	if _, err := client.validatePollingURL(response.PollingURL); err != nil {
		return nil, &ProtocolError{Operation: "submit", Err: fmt.Errorf("invalid polling_url: %w", err)}
	}
	return &jobCore{client: client, id: response.ID, pollingURL: response.PollingURL}, nil
}
