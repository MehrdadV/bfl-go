package bfl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func writeResponse(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func testHTTPClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "delivery.bfl.ai" {
			req = req.Clone(req.Context())
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
}

func TestGenerateDownloadsMedia(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			var polls atomic.Int32
			wantMedia := "generated media"
			if kind == "video" {
				wantMedia = strings.Repeat(wantMedia, 40)
			}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/media" && r.Header.Get("x-key") != "test-key" {
					t.Errorf("missing API key on %s", r.URL.Path)
				}
				switch r.URL.Path {
				case "/v1/flux-3-image", "/v1/flux-3-video":
					if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, kind) {
						t.Errorf("unexpected submission: %s %s", r.Method, r.URL.Path)
					}
					writeResponse(t, w, fmt.Sprintf(`{"id":"job","polling_url":%q}`, server.URL+"/poll?task=job"))
				case "/poll":
					if r.URL.RawQuery != "task=job" {
						t.Errorf("polling query = %q", r.URL.RawQuery)
					}
					if polls.Add(1) == 1 {
						writeResponse(t, w, `{"id":"job","status":"Pending"}`)
					} else {
						writeResponse(t, w, `{"id":"job","status":"Ready","result":{"sample":"https://delivery.bfl.ai/media"}}`)
					}
				case "/media":
					if r.Header.Get("x-key") != "" {
						t.Error("API key was sent to media host")
					}
					w.Header().Set("Content-Type", kind+"/test")
					writeResponse(t, w, wantMedia)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client, err := New(WithAPIKey("test-key"), WithBaseURL(server.URL),
				WithHTTPClient(testHTTPClient(t, server)), WithPollInterval(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var media interface {
				Bytes() ([]byte, error)
				Close() error
			}
			if kind == "image" {
				media, err = client.Images.Generate(ctx, "test")
			} else {
				media, err = client.Videos.Generate(ctx, "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := media.Close(); err != nil {
					t.Error(err)
				}
			})
			data, err := media.Bytes()
			if err != nil || string(data) != wantMedia {
				t.Fatalf("downloaded %q, error %v", data, err)
			}
			if got := polls.Load(); got != 2 {
				t.Errorf("polls = %d, want 2", got)
			}
		})
	}
}

func TestEmptyMediaResponse(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			client, err := New(WithAPIKey("test"), WithHTTPClient(&http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {kind + "/test"}},
						Body:       io.NopCloser(strings.NewReader("")),
						Request:    req,
					}, nil
				}),
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = client.downloadMedia(context.Background(), "https://delivery.bfl.ai/media", kind+"/")
			if err == nil || !strings.Contains(err.Error(), "empty") {
				t.Fatalf("expected empty media error, got %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cache directory entries = %v, error %v", entries, err)
			}
		})
	}
}

func TestRetryInterruptedMedia(t *testing.T) {
	for _, tc := range []struct {
		name        string
		prefix      int
		failures    int
		wantCalls   int
		wantErrorIs error
	}{
		{name: "prefix read", failures: 1, wantCalls: 2},
		{name: "stream read", prefix: 520, failures: 1, wantCalls: 2},
		{name: "exhausted", prefix: 520, failures: 2, wantCalls: 2, wantErrorIs: io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			calls := 0
			client, err := New(WithAPIKey("test"), WithRetryPolicy(RetryPolicy{
				MaxAttempts: 2, BaseDelay: time.Nanosecond,
			}), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"video/test"}}, Request: req}
				if calls <= tc.failures {
					resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(strings.Repeat("x", tc.prefix)), brokenBody{}))
				} else {
					resp.Body = io.NopCloser(strings.NewReader("complete media"))
				}
				return resp, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			path, contentType, size, err := client.downloadMedia(context.Background(), "https://delivery.bfl.ai/media", "video/")
			if tc.wantErrorIs != nil {
				if !errors.Is(err, tc.wantErrorIs) {
					t.Fatalf("error = %v, want %v", err, tc.wantErrorIs)
				}
				if path != "" {
					t.Errorf("failed download returned path %q", path)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile(path) // #nosec G304 -- Path was created in the test's temporary directory.
				if readErr != nil || string(data) != "complete media" || contentType != "video/test" || size != int64(len(data)) {
					t.Errorf("media = %q, type = %q, size = %d, error = %v", data, contentType, size, readErr)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if calls != tc.wantCalls {
				t.Errorf("attempts = %d, want %d", calls, tc.wantCalls)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Errorf("cache directory entries = %v, error %v", entries, err)
			}
		})
	}
}

func TestMediaRequestErrorRedactsURL(t *testing.T) {
	const signedURL = "https://delivery.bfl.ai/media?sig=fake-secret"
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprintf("redirect=%t", redirect), func(t *testing.T) {
			cause := errors.New("failure for " + signedURL)
			client, err := New(WithAPIKey("test"), WithHTTPClient(&http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return cause },
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if redirect {
						return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {signedURL}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
					}
					return nil, cause
				}),
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = client.downloadMedia(context.Background(), signedURL, "image/")
			if !errors.Is(err, cause) || strings.Contains(err.Error(), "fake-secret") {
				t.Fatalf("error = %v, want redacted error wrapping cause", err)
			}
		})
	}
}

func TestTransportFailureDuringCancellation(t *testing.T) {
	for _, kind := range []string{"poll", "media"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", kind, deadline), func(t *testing.T) {
				var ctx context.Context
				var cancel context.CancelFunc
				wantContextErr := error(context.Canceled)
				if deadline {
					ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
					wantContextErr = context.DeadlineExceeded
				} else {
					ctx, cancel = context.WithCancel(context.Background())
				}
				defer cancel()
				cause := errors.New("connection failed")
				calls := 0
				client, err := New(WithAPIKey("test"), WithRetryPolicy(RetryPolicy{
					MaxAttempts: 2, BaseDelay: time.Nanosecond,
				}), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if !deadline {
						cancel()
					}
					<-req.Context().Done()
					return nil, cause
				})}))
				if err != nil {
					t.Fatal(err)
				}
				if kind == "poll" {
					job := &jobCore{client: client, id: "job", pollingURL: "https://api.bfl.ai/poll"}
					_, _, err = job.statusResult(ctx)
				} else {
					_, _, _, err = client.downloadMedia(ctx, "https://delivery.bfl.ai/media?sig=fake-secret", "image/")
				}
				if !errors.Is(err, wantContextErr) || !errors.Is(err, cause) || calls != 1 {
					t.Errorf("error = %v, attempts = %d; want context and transport errors after one attempt", err, calls)
				}
				if kind == "media" && err != nil && strings.Contains(err.Error(), "fake-secret") {
					t.Errorf("media error exposes signed URL: %v", err)
				}
			})
		}
	}
}

func TestMediaRedirectSafety(t *testing.T) {
	const signedURL = "https://delivery.bfl.ai/media?sig=fake-secret"
	for _, tc := range []struct {
		name        string
		location    string
		custom      bool
		changeHost  bool
		wantAllowed bool
	}{
		{name: "BFL host", location: "https://delivery.eu.bfl.ai/media", custom: true, wantAllowed: true},
		{name: "Azure host", location: "https://bfldelivery123.blob.core.windows.net/media", wantAllowed: true},
		{name: "untrusted host", location: "https://other.example/media"},
		{name: "HTTP downgrade", location: "http://delivery.bfl.ai/media"},
		{name: "URL credentials", location: "https://user:password@delivery.bfl.ai/media"}, // #nosec G101 -- Test credentials are not real.
		{name: "callback changes host", location: "https://delivery.bfl.ai/media", custom: true, changeHost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			callbackCalls := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {tc.location}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
				}
				if req.Header.Get("Referer") != "" || req.Header.Get("x-key") != "" {
					t.Errorf("redirect headers include Referer or API key: %v", req.Header)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(strings.NewReader("media")), Request: req}, nil
			})}
			if tc.custom {
				httpClient.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
					callbackCalls++
					if req.Header.Get("Referer") != "" {
						t.Error("redirect callback received signed Referer")
					}
					req.Header.Set("Referer", signedURL)
					if tc.changeHost {
						req.URL.Host = "other.example"
					}
					return nil
				}
			}
			client, err := New(WithAPIKey("test"), WithHTTPClient(httpClient))
			if err != nil {
				t.Fatal(err)
			}
			image, err := client.downloadImage(context.Background(), signedURL)
			if tc.wantAllowed {
				if err != nil {
					t.Fatal(err)
				}
				if err := image.Close(); err != nil {
					t.Fatal(err)
				}
				if calls != 2 {
					t.Errorf("requests = %d, want 2", calls)
				}
			} else if err == nil || calls != 1 {
				t.Errorf("error = %v, requests = %d; want rejected redirect", err, calls)
			}
			if tc.custom && callbackCalls != 1 {
				t.Errorf("redirect callbacks = %d, want 1", callbackCalls)
			}
		})
	}
}

func TestSafetyToleranceSubmission(t *testing.T) {
	for _, model := range []string{"flux-3-image", "flux-3-video"} {
		for _, level := range []int{-1, 0, 7, 8} {
			t.Run(fmt.Sprintf("%s/%d", model, level), func(t *testing.T) {
				calls := 0
				client, err := New(WithAPIKey("test"), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					var body struct {
						SafetyTolerance *int `json:"safety_tolerance"`
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.SafetyTolerance == nil || *body.SafetyTolerance != level {
						t.Errorf("request safety tolerance = %v, decode error = %v", body.SafetyTolerance, err)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"job","polling_url":"https://api.bfl.ai/poll"}`)), Request: req}, nil
				})}))
				if err != nil {
					t.Fatal(err)
				}
				if model == "flux-3-video" {
					_, err = client.Videos.Submit(context.Background(), VideoRequest{Prompt: "test", Mode: VideoModeTextToVideo, SafetyTolerance: &level})
				} else {
					_, err = client.Images.Submit(context.Background(), ImageRequest{Prompt: "test", Model: ImageModel(model), SafetyTolerance: &level})
				}
				if level < 0 || level > 7 {
					if err == nil || calls != 0 {
						t.Errorf("error = %v, requests = %d; want local validation error", err, calls)
					}
				} else if err != nil || calls != 1 {
					t.Errorf("error = %v, requests = %d; want successful submission", err, calls)
				}
			})
		}
	}
}

func TestEditingRequestBodies(t *testing.T) {
	refs := []ImageInput{
		{URL: "https://example.com/image.png"},
		{Data: []byte("image"), ContentType: "image/png"},
	}
	for _, tc := range []struct {
		name     string
		path     string
		wantJSON string
		generate func(context.Context, *Client) error
	}{
		{
			name:     "flux 3 image edit",
			path:     "/v1/flux-3-image",
			wantJSON: `{"prompt":"edit","images":["https://example.com/image.png","aW1hZ2U="]}`,
			generate: func(ctx context.Context, c *Client) error {
				_, err := c.Images.Edit(ctx, "edit", refs)
				return err
			},
		},
		{
			name:     "flux 2 pro image edit",
			path:     "/v1/flux-2-pro",
			wantJSON: `{"prompt":"edit","input_image":"https://example.com/image.png","input_image_2":"aW1hZ2U=","seed":42}`,
			generate: func(ctx context.Context, c *Client) error {
				_, err := c.Images.Edit(ctx, "edit", refs, WithImageModel(ModelFlux2Pro), WithImageSeed(42))
				return err
			},
		},
		{
			name:     "image to video",
			path:     "/v1/flux-3-video",
			wantJSON: `{"mode":"i2v","prompt":"animate","version":"latest","duration":7,"generate_audio":false,"keyframes":"aW1hZ2U="}`,
			generate: func(ctx context.Context, c *Client) error {
				_, err := c.Videos.GenerateFromImage(ctx, "animate", refs[1], WithVideoDuration(7), WithGenerateAudio(false))
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.wantJSON), &want); err != nil {
				t.Fatal(err)
			}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/poll" {
					writeResponse(t, w, `{"id":"job","status":"Error"}`)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != tc.path {
					t.Errorf("unexpected submission: %s %s", r.Method, r.URL.Path)
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode request: %v", err)
				} else if !reflect.DeepEqual(got, want) {
					t.Errorf("request = %v, want %v", got, want)
				}
				writeResponse(t, w, fmt.Sprintf(`{"id":"job","polling_url":%q}`, server.URL+"/poll"))
			}))
			defer server.Close()
			client, err := New(WithAPIKey("test"), WithBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var jobErr *JobError
			if err := tc.generate(ctx, client); !errors.As(err, &jobErr) || jobErr.State != JobStateError {
				t.Fatalf("expected terminal job error, got %v", err)
			}
		})
	}
}

type brokenBody struct {
	cancel context.CancelFunc
}

func (b brokenBody) Read([]byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return 0, io.ErrUnexpectedEOF
}

func TestRetryTruncated503(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failures    int
		cancel      bool
		wantCalls   int
		wantErrorIs error
	}{
		{name: "recovers", failures: 1, wantCalls: 2},
		{name: "exhausted", failures: 2, wantCalls: 2, wantErrorIs: io.ErrUnexpectedEOF},
		{name: "canceled", failures: 1, cancel: true, wantCalls: 1, wantErrorIs: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client, err := New(WithAPIKey("test"), WithRetryPolicy(RetryPolicy{
				MaxAttempts: 2, BaseDelay: time.Nanosecond,
			}), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				resp := &http.Response{Header: make(http.Header), Request: req}
				if calls <= tc.failures {
					resp.StatusCode = http.StatusServiceUnavailable
					body := brokenBody{}
					if tc.cancel {
						body.cancel = cancel
					}
					resp.Body = io.NopCloser(body)
				} else {
					resp.StatusCode = http.StatusOK
					resp.Body = io.NopCloser(strings.NewReader(`{"status":"Pending"}`))
				}
				return resp, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse("https://api.bfl.ai/poll")
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.doSafeURL(ctx, target, nil)
			if tc.wantErrorIs == nil {
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Errorf("status = %d, want 200", resp.StatusCode)
				}
				if err := resp.Body.Close(); err != nil {
					t.Error(err)
				}
			} else if !errors.Is(err, tc.wantErrorIs) {
				t.Fatalf("error = %v, want %v", err, tc.wantErrorIs)
			}
			if calls != tc.wantCalls {
				t.Errorf("attempts = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestTerminalPollingResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		status JobState
	}{
		{name: "503 error", code: http.StatusServiceUnavailable, status: JobStateError},
		{name: "failed", code: http.StatusOK, status: JobStateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					writeResponse(t, w, fmt.Sprintf(`{"id":"job","polling_url":%q}`, server.URL+"/poll"))
					return
				}
				polls.Add(1)
				w.WriteHeader(tc.code)
				writeResponse(t, w, fmt.Sprintf(`{"id":"job","status":%q,"details":{"reason":"failed"}}`, tc.status))
			}))
			defer server.Close()
			client, err := New(WithAPIKey("test"), WithBaseURL(server.URL),
				WithRetryPolicy(RetryPolicy{MaxAttempts: 2, BaseDelay: time.Nanosecond}))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			job, err := client.Images.Submit(ctx, ImageRequest{Prompt: "test"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = job.Wait(ctx)
			var jobErr *JobError
			if !errors.As(err, &jobErr) || jobErr.State != tc.status || jobErr.Details["reason"] != "failed" {
				t.Fatalf("expected %q job error with details, got %v", tc.status, err)
			}
			if got := polls.Load(); got != 1 {
				t.Errorf("polls = %d, want 1", got)
			}
		})
	}
}

func TestWaitTimeoutIncludesDownload(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/flux-3-image":
			writeResponse(t, w, fmt.Sprintf(`{"id":"job","polling_url":%q}`, server.URL+"/poll"))
		case "/poll":
			writeResponse(t, w, `{"id":"job","status":"Ready","result":{"sample":"https://delivery.bfl.ai/media"}}`)
		case "/media":
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	client, err := New(WithAPIKey("test"), WithBaseURL(server.URL),
		WithHTTPClient(testHTTPClient(t, server)), WithPollTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = client.Images.Generate(ctx, "test")
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("expected SDK download timeout before caller deadline, got %v (caller: %v)", err, ctx.Err())
	}
}

func TestAuthenticatedRedirectDoesNotLeakKey(t *testing.T) {
	for _, redirectOn := range []string{"submit", "poll"} {
		t.Run(redirectOn, func(t *testing.T) {
			var redirected atomic.Bool
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Store(true)
			}))
			defer other.Close()
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (redirectOn == "submit" && r.Method == http.MethodPost) ||
					(redirectOn == "poll" && r.Method == http.MethodGet) {
					http.Redirect(w, r, other.URL, http.StatusFound)
					return
				}
				writeResponse(t, w, fmt.Sprintf(`{"id":"job","polling_url":%q}`, server.URL+"/poll"))
			}))
			defer server.Close()
			client, err := New(WithAPIKey("test"), WithBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			job, err := client.Images.Submit(context.Background(), ImageRequest{Prompt: "test"})
			if redirectOn == "poll" && err == nil {
				_, err = job.Status(context.Background())
			}
			if !IsAPIStatus(err, http.StatusFound) {
				t.Errorf("error = %v, want HTTP 302", err)
			}
			if redirected.Load() {
				t.Error("followed redirect to another host")
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"seconds", "5", 5 * time.Second, true},
		{"future HTTP date", now.Add(10 * time.Second).Format(http.TimeFormat), 10 * time.Second, true},
		{"invalid value ignored", "soon", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.value, now)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestWaitRetryDelay(t *testing.T) {
	policy := RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	tests := []struct {
		name         string
		retryNumber  int
		retryAfter   string
		wantDeadline bool
	}{
		{"backoff stops at MaxDelay", 20, "", false},
		{"Retry-After above MaxDelay is honored", 1, "3600", true},
		{"huge Retry-After does not overflow", 1, "99999999999999", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{retryPolicy: policy}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := client.waitRetry(ctx, tt.retryNumber, tt.retryAfter)
			if tt.wantDeadline {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("waitRetry error = %v, want deadline exceeded", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("waitRetry error = %v, want nil", err)
			}
		})
	}
}
