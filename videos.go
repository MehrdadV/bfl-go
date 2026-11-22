package bfl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// VideoMode selects a documented FLUX 3 Video workflow.
type VideoMode string

const (
	VideoModeTextToVideo  VideoMode = "t2v"
	VideoModeImageToVideo VideoMode = "i2v"
)

// VideoResolution selects the delivered resolution class.
type VideoResolution string

const (
	VideoResolutionHD  VideoResolution = "hd"
	VideoResolutionFHD VideoResolution = "fhd"
	VideoResolutionQHD VideoResolution = "qhd"
	VideoResolutionUHD VideoResolution = "uhd"
)

// VideoOption configures a FLUX 3 Video request.
type VideoOption func(*VideoRequest) error

// VideoRequest is a typed text-to-video or single-image-to-video request.
type VideoRequest struct {
	Prompt          string
	Mode            VideoMode
	StartImage      *ImageInput
	AspectRatio     string
	Duration        *int
	Resolution      VideoResolution
	GenerateAudio   *bool
	SafetyTolerance *int
}

// WithVideoAspectRatio sets a supported FLUX 3 Video ratio or "auto".
// The possible values are "21:9", "2:1", "16:9", "4:3", "1:1", "3:4", "9:16", "9:21", or "auto".
func WithVideoAspectRatio(ratio string) VideoOption {
	return func(req *VideoRequest) error {
		req.AspectRatio = ratio
		return nil
	}
}

// WithVideoDuration sets the output duration in whole seconds (5-20).
func WithVideoDuration(seconds int) VideoOption {
	return func(req *VideoRequest) error {
		req.Duration = &seconds
		return nil
	}
}

// WithVideoResolution sets the delivered video resolution class.
// The possible values are "hd", "fhd", "qhd", or "uhd" and default to "hd".
func WithVideoResolution(resolution VideoResolution) VideoOption {
	return func(req *VideoRequest) error {
		req.Resolution = resolution
		return nil
	}
}

// WithGenerateAudio controls synchronized audio generation.
// The default is to generate audio.
func WithGenerateAudio(enabled bool) VideoOption {
	return func(req *VideoRequest) error {
		req.GenerateAudio = &enabled
		return nil
	}
}

// WithVideoSafetyTolerance sets the FLUX 3 Video moderation tolerance.
// BFL may impose a lower organization-specific maximum.
func WithVideoSafetyTolerance(level int) VideoOption {
	return func(req *VideoRequest) error {
		req.SafetyTolerance = &level
		return nil
	}
}

// Generate submits a text-to-video request and waits for a downloaded result.
func (s *VideoService) Generate(ctx context.Context, prompt string, opts ...VideoOption) (*Video, error) {
	req := VideoRequest{Prompt: prompt, Mode: VideoModeTextToVideo}
	if err := applyVideoOptions(&req, opts); err != nil {
		return nil, err
	}
	job, err := s.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	return job.Wait(ctx)
}

// GenerateFromImage starts a clip from the supplied opening image.
func (s *VideoService) GenerateFromImage(ctx context.Context, prompt string, image ImageInput, opts ...VideoOption) (*Video, error) {
	input := image
	req := VideoRequest{Prompt: prompt, Mode: VideoModeImageToVideo, StartImage: &input}
	if err := applyVideoOptions(&req, opts); err != nil {
		return nil, err
	}
	job, err := s.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	return job.Wait(ctx)
}

// Submit creates an asynchronous FLUX 3 Video task.
func (s *VideoService) Submit(ctx context.Context, req VideoRequest) (*VideoJob, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("bfl: video service is not initialized")
	}
	if err := validateVideoRequest(&req); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(videoRequestPayload(req))
	if err != nil {
		return nil, err
	}
	var response submitResponse
	if err := s.client.doJSON(ctx, http.MethodPost, "/v1/flux-3-video", strings.NewReader(string(payload)), &response); err != nil {
		return nil, err
	}
	return newVideoJob(s.client, response, s.client.downloadVideo)
}

func applyVideoOptions(req *VideoRequest, opts []VideoOption) error {
	for _, opt := range opts {
		if opt == nil {
			return errors.New("bfl: nil video option")
		}
		if err := opt(req); err != nil {
			return err
		}
	}
	return nil
}

func validateVideoRequest(req *VideoRequest) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return errors.New("bfl: video prompt must not be empty")
	}
	if req.Mode != VideoModeTextToVideo && req.Mode != VideoModeImageToVideo {
		return fmt.Errorf("bfl: unsupported video mode %q", req.Mode)
	}
	if req.Mode == VideoModeTextToVideo && req.StartImage != nil {
		return errors.New("bfl: text-to-video request must not include a start image")
	}
	if req.Mode == VideoModeImageToVideo {
		if req.StartImage == nil {
			return errors.New("bfl: image-to-video request requires a start image")
		}
		if err := validateImageInput(*req.StartImage); err != nil {
			return fmt.Errorf("bfl: start image: %w", err)
		}
	}
	if req.AspectRatio != "" && !oneOf(req.AspectRatio, "auto", "21:9", "2:1", "16:9", "4:3", "1:1", "3:4", "9:16", "9:21") {
		return fmt.Errorf("bfl: unsupported FLUX 3 Video aspect ratio %q", req.AspectRatio)
	}
	if req.Duration != nil && (*req.Duration < 5 || *req.Duration > 20) {
		return errors.New("bfl: FLUX 3 Video duration must be a whole number of seconds from 5 through 20")
	}
	if req.Resolution != "" && !oneOf(string(req.Resolution), "hd", "fhd", "qhd", "uhd") {
		return fmt.Errorf("bfl: unsupported FLUX 3 Video resolution %q", req.Resolution)
	}
	if req.SafetyTolerance != nil && (*req.SafetyTolerance < 0 || *req.SafetyTolerance > 7) {
		return errors.New("bfl: FLUX 3 Video safety tolerance must be between 0 and 7")
	}
	return nil
}

func videoRequestPayload(req VideoRequest) map[string]any {
	payload := map[string]any{
		"mode":    req.Mode,
		"prompt":  req.Prompt,
		"version": "latest",
	}
	if req.AspectRatio != "" {
		payload["aspect_ratio"] = req.AspectRatio
	}
	if req.Duration == nil {
		payload["duration"] = "auto"
	} else {
		payload["duration"] = *req.Duration
	}
	if req.Resolution != "" {
		payload["resolution"] = req.Resolution
	}
	if req.GenerateAudio != nil {
		payload["generate_audio"] = *req.GenerateAudio
	}
	if req.SafetyTolerance != nil {
		payload["safety_tolerance"] = *req.SafetyTolerance
	}
	if req.Mode == VideoModeImageToVideo && req.StartImage != nil {
		if req.StartImage.URL != "" {
			payload["keyframes"] = req.StartImage.URL
		} else {
			payload["keyframes"] = base64.StdEncoding.EncodeToString(req.StartImage.Data)
		}
	}
	return payload
}

// Video is a file-backed generated video. Close removes its temporary file.
type Video struct{ mediaFile }

// URL returns the original BFL signed URL, which expires after a short period.
func (m *Video) URL() string { return m.url }

// ContentType returns the downloaded video media type.
func (m *Video) ContentType() string { return m.contentType }

// Size returns the cached video size in bytes.
func (m *Video) Size() int64 { return m.size }

// Bytes reads the full video into memory.
func (m *Video) Bytes() ([]byte, error) {
	path, err := m.localPath()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// WriteTo streams the cached video to w.
func (m *Video) WriteTo(w io.Writer) (int64, error) {
	path, err := m.localPath()
	if err != nil {
		return 0, err
	}
	return writeFileTo(path, w)
}

// Save atomically writes the cached video to path.
func (m *Video) Save(path string) error {
	local, err := m.localPath()
	if err != nil {
		return err
	}
	return copyFileAtomically(local, path)
}

// Close removes the temporary backing file. It is safe to call more than once.
func (m *Video) Close() error { return m.close() }

func (c *Client) downloadVideo(ctx context.Context, rawURL string) (*Video, error) {
	file, contentType, size, err := c.downloadMedia(ctx, rawURL, "video/")
	if err != nil {
		return nil, err
	}
	return &Video{mediaFile{path: file, url: rawURL, contentType: contentType, size: size}}, nil
}
