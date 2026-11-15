# bfl-go Public API Specification (Draft)

Status: Phase 0 proposal for review. This is a design artifact, not an implementation. Do not begin Phase 1 until this specification is approved.

## Supported MVP operations

| Operation | Endpoint | Scope |
| --- | --- | --- |
| Image generation/editing | `POST /v1/flux-3-image` | Default image model; prompt, references, aspect ratio, resolution/dimensions and documented controls. |
| Image generation/editing | `POST /v1/flux-2-pro` | Explicit compatibility model; separate endpoint adapter, including documented seed and up to 8 `input_image*` references. |
| Text-to-video | `POST /v1/flux-3-video` | `mode: "t2v"`. |
| Single-image-to-video | `POST /v1/flux-3-video` | `mode: "i2v"`; one opening image. |

FLUX 3 Image is the default because it is the newest general-purpose image endpoint in the current API reference. FLUX.2 Pro remains selectable as a stable documented alternative, with its own request-field mapping. Seed is only exposed for FLUX.2 Pro. FLUX 3 Video continuation, storyboard keyframes, draft enhancement, video edit/upscale, and specialized image tools are out of MVP scope until their exact payloads are separately reviewed.

Official references:

- [FLUX 3 Image API](https://docs.bfl.ml/api-reference/utility/generate-an-image-with-flux-3)
- [FLUX.2 Pro API](https://docs.bfl.ml/api-reference/models/generate-or-edit-an-image-with-flux2-%5Bpro%5D)
- [FLUX 3 Video API](https://docs.bfl.ml/api-reference/utility/generate-a-video-with-flux-3)
- [FLUX 3 Video guide](https://docs.bfl.ml/flux_3/flux3_video)
- [Image-to-video guide](https://docs.bfl.ml/guides/prompting_video_image_to_video)
- [FLUX API integration guide](https://docs.bfl.ml/api_integration/integration_guidelines)
- [Errors and job statuses](https://docs.bfl.ml/api_integration/errors)

## Package and imports

Module path: `github.com/<owner>/bfl-go` (choose and set before creating `go.mod`). Package name: `bfl`. Runtime dependencies: Go standard library only.

## Compile-ready public surface

The following is the intended exported surface. Private config names in option function types intentionally prevent callers from mutating internal configuration directly; callers use the provided option constructors.

```go
package bfl

import (
	"context"
	"io"
	"net/http"
	"time"
)

type Client struct {
	Images *ImageService
	Videos *VideoService
}

type ClientOption func(*clientConfig) error

func New(opts ...ClientOption) (*Client, error)
func WithAPIKey(key string) ClientOption
func WithHTTPClient(client *http.Client) ClientOption
func WithBaseURL(baseURL string) ClientOption
func WithRetryPolicy(policy RetryPolicy) ClientOption
func WithPollInterval(interval time.Duration) ClientOption
func WithPollTimeout(timeout time.Duration) ClientOption

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

type ImageService struct { /* unexported implementation */ }
type VideoService struct { /* unexported implementation */ }

func (s *ImageService) Generate(ctx context.Context, prompt string, opts ...ImageOption) (*Image, error)
func (s *ImageService) Edit(ctx context.Context, prompt string, refs []ImageInput, opts ...ImageOption) (*Image, error)
func (s *ImageService) Submit(ctx context.Context, req ImageRequest) (*ImageJob, error)

func (s *VideoService) Generate(ctx context.Context, prompt string, opts ...VideoOption) (*Video, error)
func (s *VideoService) GenerateFromImage(ctx context.Context, prompt string, image ImageInput, opts ...VideoOption) (*Video, error)
func (s *VideoService) Submit(ctx context.Context, req VideoRequest) (*VideoJob, error)

type ImageModel string

const (
	ModelFlux3Image ImageModel = "flux-3-image"
	ModelFlux2Pro   ImageModel = "flux-2-pro"
)

type ImageOption func(*ImageRequest) error

func WithImageModel(model ImageModel) ImageOption
func WithAspectRatio(ratio string) ImageOption
func WithResolution(resolution string) ImageOption
func WithDimensions(width, height int) ImageOption
func WithImageSeed(seed int64) ImageOption // valid only for ModelFlux2Pro
func WithSafetyTolerance(level int) ImageOption

type ImageRequest struct {
	Prompt          string
	Model           ImageModel
	References      []ImageInput
	AspectRatio     string
	Resolution      string
	Width           int
	Height          int
	Seed            *int64 // FLUX.2 Pro only
	SafetyTolerance *int
}

type ImageInput struct {
	URL         string
	Data        []byte
	ContentType string
}

type VideoMode string

const (
	VideoModeTextToVideo  VideoMode = "t2v"
	VideoModeImageToVideo VideoMode = "i2v"
)

type VideoResolution string

const (
	VideoResolutionHD  VideoResolution = "hd"
	VideoResolutionFHD VideoResolution = "fhd"
	VideoResolutionQHD VideoResolution = "qhd"
	VideoResolutionUHD VideoResolution = "uhd"
)

type VideoOption func(*VideoRequest) error

func WithVideoAspectRatio(ratio string) VideoOption
func WithVideoDuration(seconds int) VideoOption
func WithVideoResolution(resolution VideoResolution) VideoOption
func WithGenerateAudio(enabled bool) VideoOption
func WithVideoSafetyTolerance(level int) VideoOption

type VideoRequest struct {
	Prompt          string
	Mode            VideoMode
	StartImage      *ImageInput // required for i2v; omitted for t2v
	AspectRatio     string
	Duration        *int // nil means BFL "auto"; documented integer range is 5-20 seconds
	Resolution      VideoResolution
	GenerateAudio   *bool
	SafetyTolerance *int
}

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
)

type JobStatus struct {
	ID       string
	State    JobState
	Progress *float64
	Details  map[string]any
}

type ImageJob struct { /* unexported implementation */ }
type VideoJob struct { /* unexported implementation */ }

func (j *ImageJob) ID() string
func (j *ImageJob) Status(ctx context.Context) (JobStatus, error)
func (j *ImageJob) Wait(ctx context.Context) (*Image, error)
func (j *VideoJob) ID() string
func (j *VideoJob) Status(ctx context.Context) (JobStatus, error)
func (j *VideoJob) Wait(ctx context.Context) (*Video, error)

type Image struct { /* downloaded, file-backed media */ }
type Video struct { /* downloaded, file-backed media */ }

func (m *Image) URL() string
func (m *Image) ContentType() string
func (m *Image) Size() int64
func (m *Image) Bytes() ([]byte, error)
func (m *Image) WriteTo(w io.Writer) (int64, error)
func (m *Image) Save(path string) error
func (m *Image) Close() error

func (m *Video) URL() string
func (m *Video) ContentType() string
func (m *Video) Size() int64
func (m *Video) Bytes() ([]byte, error)
func (m *Video) WriteTo(w io.Writer) (int64, error)
func (m *Video) Save(path string) error
func (m *Video) Close() error

type APIError struct {
	StatusCode int
	Message    string
	Details    map[string]any
	RequestID  string
}

func (e *APIError) Error() string
func (e *APIError) Unwrap() error

type JobError struct {
	ID      string
	State   JobState
	Details map[string]any
}

func (e *JobError) Error() string
```

## Request mapping and validation

### Images

- `ImageRequest.Model == ""` means `ModelFlux3Image`.
- `Generate` creates an image request with no references. `Edit` requires at least one reference. `Submit` uses request fields as supplied, and defaults an empty model as above.
- `ImageInput` must contain exactly one of `URL` or `Data`. A URL must be absolute HTTPS. `Data` is raw image bytes; `ContentType` is required with bytes and must be an image media type. The client encodes bytes using the representation required by the chosen official endpoint; callers do not pass pre-encoded data URIs.
- FLUX 3 Image serializes references into its documented `images` array (1–10 references). FLUX.2 Pro serializes references to `input_image`, `input_image_2`, … `input_image_8` (maximum 8). A model adapter validates reference counts and rejects unsupported fields.
- For FLUX 3 Image, `Width` and `Height` must be both zero or both set; when set, they take precedence over aspect ratio/resolution per the API docs. For FLUX.2 Pro, pass documented width/height, seed, safety tolerance, and image inputs only. Do not send FLUX 3-only fields to FLUX.2 Pro.
- Seed is accepted only for FLUX.2 Pro. Any seed set for FLUX 3 Image returns a local validation error before network activity.
- Empty option values mean “leave server default”; pointers are used where false/zero are meaningful. The client does not hardcode model-specific safety maxima beyond documented validation; BFL may enforce organization-specific ceilings and return 422.

### Video

- `Videos.Generate` always submits `t2v`; `Videos.GenerateFromImage` always submits `i2v`. Their mode cannot be overridden by options.
- `VideoRequest.Mode` is required for `Submit`; `t2v` forbids `StartImage`, while `i2v` requires it. Only these two modes are in MVP. The single i2v image maps to the documented `keyframes` field.
- `Duration == nil` serializes as BFL `"auto"`; otherwise only whole seconds from 5 through 20 are valid. Aspect ratio and resolution use documented FLUX 3 Video enumerations. Nil audio/safety options use BFL defaults.
- For the MVP single-image i2v operation, serialize the opening frame into `keyframes` as one URL string or base64 string, as shown in BFL's official example. This is distinct from the image endpoint's `images` field. Image bytes are base64 encoded. Do not add end-frame or timestamped storyboard keyframes until their richer public type and validations are separately specified.

## Job and polling behavior

- Submit returns a job retaining both BFL `id` and the exact returned `polling_url`.
- `Status` GETs the returned polling URL with `x-key`; it does not synthesize a URL from `id`. `Wait` polls until `Ready`, a documented terminal failure, context cancellation, or configured poll timeout. Defaults: 500 ms poll interval and no SDK-level maximum wait other than caller context; these remain configurable.
- `Ready` returns downloaded media. Moderation statuses, `Error`, `No Card Generated`, and `Task not found` return `*JobError` with state/details. Unknown states are represented as their raw `JobState` and treated as non-success; polling continues unless the caller cancels or timeout expires.
- Before attaching `x-key` to a server-returned polling URL, validate that it is HTTPS and belongs to the configured BFL API host or an explicitly configured BFL regional API host. Reject unexpected hosts to avoid credential disclosure/SSRF. Do not rewrite the URL path/query.

## Media lifetime and file handling

- `Generate` and `Wait` download result bytes to an SDK-managed temporary file before returning; this avoids relying on BFL's expiring URL for later operations and avoids loading large videos into RAM.
- `URL` returns the original signed BFL URL for observability/interoperability. It can expire; `WriteTo`, `Save`, `Bytes`, `Size`, and `ContentType` use local cached media.
- `Size` returns the cached byte length. `Bytes` reads the entire file and may use substantial memory for videos. `WriteTo` streams from disk. `Save` writes to a temporary file in the destination directory, syncs/closes it, and renames it into place; on failure it removes the temporary destination file. It does not create missing parent directories.
- `Close` removes the SDK-managed temporary file and is idempotent. Calls using local media after `Close` return an error. Callers should close media when finished.
- Temporary file creation honors the OS temp directory. If caching the result locally fails, the operation returns an error; it does not return a media object that silently depends on an expiring URL.

## Transport, errors, and retries

- Default API base URL: `https://api.bfl.ai`; `WithBaseURL` supports official BFL regional bases. Normalize to a root URL and reject non-HTTPS URLs except loopback HTTP for local test servers.
- Authentication header: `x-key`. JSON submission uses `Content-Type: application/json`; JSON responses are expected for submit and poll.
- `WithHTTPClient` injects the complete transport and timeout policy. The SDK does not impose a second hidden HTTP timeout; all requests use the caller context.
- `RetryPolicy{}` disables retries. When configured, retry polling and media GETs on transport errors and 429/500/503 with bounded exponential backoff. Honor `Retry-After` when valid and cap delay by `MaxDelay`. Never automatically retry task-creating POST requests.
- `APIError` represents non-2xx HTTP responses and supports `errors.As`; network errors remain wrapped with `%w`. `JobError` represents terminal task states. Malformed JSON and missing required `id`/`polling_url` are protocol errors with context.

## Examples against this specification

```go
client, err := bfl.New(bfl.WithAPIKey(apiKey))
if err != nil { return err }

image, err := client.Images.Generate(ctx, "A mountain lake at sunset")
if err != nil { return err }
defer image.Close()
if err := image.Save("image.png"); err != nil { return err }

video, err := client.Videos.Generate(ctx, "A fox running through dawn mist")
if err != nil { return err }
defer video.Close()
if err := video.Save("video.mp4"); err != nil { return err }

job, err := client.Images.Submit(ctx, bfl.ImageRequest{
	Prompt: "A mountain lake at sunset",
})
if err != nil { return err }
status, err := job.Status(ctx)
if err != nil { return err }
_ = status
image, err = job.Wait(ctx)
if err != nil { return err }
defer image.Close()
```

## Approval checklist

- [ ] Approve MVP model set and FLUX 3 Image default.
- [ ] Approve FLUX.2 Pro seed as model-specific, with no seed for FLUX 3 Image.
- [ ] Approve t2v and single-image i2v scope; defer other video modes.
- [ ] Approve temporary-file-backed media and required `Close` lifecycle.
- [ ] Approve polling URL host validation and retry behavior.
- [ ] Approve public names/signatures and error types.

Approval of this spec is the gate to Phase 1 implementation.
