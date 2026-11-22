package bfl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ImageModel selects a documented BFL image generation endpoint.
type ImageModel string

const (
	// ModelFlux3Image is the default, current FLUX 3 image endpoint.
	ModelFlux3Image ImageModel = "flux-3-image"
	// ModelFlux2Pro selects the stable FLUX.2 Pro endpoint.
	ModelFlux2Pro ImageModel = "flux-2-pro"
)

// ImageOption configures an image generation or editing request.
type ImageOption func(*ImageRequest) error

// ImageRequest is a typed request for one of the supported image endpoints.
type ImageRequest struct {
	Prompt          string
	Model           ImageModel
	References      []ImageInput
	AspectRatio     string
	Resolution      string
	Width           int
	Height          int
	Seed            *int64
	SafetyTolerance *int
}

// ImageInput supplies an image by HTTPS URL or raw bytes. Exactly one must be set.
type ImageInput struct {
	URL         string
	Data        []byte
	ContentType string
}

// WithImageModel selects the image endpoint. The default is ModelFlux3Image.
func WithImageModel(model ImageModel) ImageOption {
	return func(req *ImageRequest) error {
		if model != ModelFlux3Image && model != ModelFlux2Pro {
			return fmt.Errorf("bfl: unsupported image model %q", model)
		}
		req.Model = model
		return nil
	}
}

// WithAspectRatio sets a FLUX 3 Image output aspect ratio.
func WithAspectRatio(ratio string) ImageOption {
	return func(req *ImageRequest) error {
		req.AspectRatio = ratio
		return nil
	}
}

// WithResolution sets a FLUX 3 Image resolution tier.
func WithResolution(resolution string) ImageOption {
	return func(req *ImageRequest) error {
		req.Resolution = resolution
		return nil
	}
}

// WithDimensions sets exact output dimensions. Both dimensions are required.
func WithDimensions(width, height int) ImageOption {
	return func(req *ImageRequest) error {
		req.Width, req.Height = width, height
		return nil
	}
}

// WithImageSeed sets the reproducibility seed supported by FLUX.2 Pro.
func WithImageSeed(seed int64) ImageOption {
	return func(req *ImageRequest) error {
		req.Seed = &seed
		return nil
	}
}

// WithSafetyTolerance sets the endpoint's safety tolerance. BFL may impose a
// lower organization-specific maximum and return a validation error.
func WithSafetyTolerance(level int) ImageOption {
	return func(req *ImageRequest) error {
		req.SafetyTolerance = &level
		return nil
	}
}

// Generate submits a text-to-image request and waits until the image has been
// downloaded to a temporary backing file.
func (s *ImageService) Generate(ctx context.Context, prompt string, opts ...ImageOption) (*Image, error) {
	req := ImageRequest{Prompt: prompt}
	if err := applyImageOptions(&req, opts); err != nil {
		return nil, err
	}
	job, err := s.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	return job.Wait(ctx)
}

// Edit submits an image-to-image request and waits for a downloaded result.
func (s *ImageService) Edit(ctx context.Context, prompt string, refs []ImageInput, opts ...ImageOption) (*Image, error) {
	req := ImageRequest{Prompt: prompt, References: append([]ImageInput(nil), refs...)}
	if err := applyImageOptions(&req, opts); err != nil {
		return nil, err
	}
	job, err := s.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	return job.Wait(ctx)
}

// Submit creates an asynchronous image task and returns a job handle.
func (s *ImageService) Submit(ctx context.Context, req ImageRequest) (*ImageJob, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("bfl: image service is not initialized")
	}
	if err := validateImageRequest(&req); err != nil {
		return nil, err
	}
	payload, err := marshalImageRequest(req)
	if err != nil {
		return nil, err
	}
	var response submitResponse
	if err := s.client.doJSON(ctx, http.MethodPost, "/v1/"+string(req.Model), strings.NewReader(string(payload)), &response); err != nil {
		return nil, err
	}
	return newImageJob(s.client, response, s.client.downloadImage)
}

func applyImageOptions(req *ImageRequest, opts []ImageOption) error {
	for _, opt := range opts {
		if opt == nil {
			return errors.New("bfl: nil image option")
		}
		if err := opt(req); err != nil {
			return err
		}
	}
	return nil
}

func validateImageRequest(req *ImageRequest) error {
	if strings.TrimSpace(req.Prompt) == "" {
		return errors.New("bfl: image prompt must not be empty")
	}
	if req.Model == "" {
		req.Model = ModelFlux3Image
	}
	if req.Model != ModelFlux3Image && req.Model != ModelFlux2Pro {
		return fmt.Errorf("bfl: unsupported image model %q", req.Model)
	}
	if req.Seed != nil && req.Model != ModelFlux2Pro {
		return errors.New("bfl: image seed is supported only by FLUX.2 Pro")
	}
	maxReferences := 10
	if req.Model == ModelFlux2Pro {
		maxReferences = 8
		if req.AspectRatio != "" || req.Resolution != "" {
			return errors.New("bfl: aspect ratio and resolution options are supported only by FLUX 3 Image")
		}
	}
	if len(req.References) > maxReferences {
		return fmt.Errorf("bfl: %s accepts at most %d reference images", req.Model, maxReferences)
	}
	if req.Width < 0 || req.Height < 0 || (req.Width == 0) != (req.Height == 0) {
		return errors.New("bfl: width and height must both be zero or both be positive")
	}
	if req.Model == ModelFlux3Image {
		if req.Width > 0 && (req.Width < 256 || req.Width > 6256 || req.Height < 256 || req.Height > 6256) {
			return errors.New("bfl: FLUX 3 Image dimensions must be between 256 and 6256 pixels")
		}
		if req.Width > 0 {
			pixels := req.Width * req.Height
			maxPixels := 16822272
			if len(req.References) > 0 {
				maxPixels = 4194304
			}
			if pixels > maxPixels {
				return fmt.Errorf("bfl: FLUX 3 Image output exceeds the %d-pixel limit", maxPixels)
			}
		}
		if req.Resolution != "" && !oneOf(req.Resolution, "768sq", "1k", "1.5k", "2k", "4k", "match_input") {
			return fmt.Errorf("bfl: unsupported FLUX 3 Image resolution %q", req.Resolution)
		}
		if req.Resolution == "match_input" && len(req.References) == 0 {
			return errors.New("bfl: match_input resolution requires a reference image")
		}
		if req.AspectRatio != "" && !oneOf(req.AspectRatio, "auto", "21:9", "2:1", "16:9", "3:2", "7:5", "4:3", "5:4", "1:1", "4:5", "3:4", "5:7", "2:3", "9:16", "1:2", "9:21") {
			return fmt.Errorf("bfl: unsupported FLUX 3 Image aspect ratio %q", req.AspectRatio)
		}
	}
	if req.Model == ModelFlux2Pro && req.Width > 0 && (req.Width < 64 || req.Height < 64) {
		return errors.New("bfl: FLUX.2 Pro dimensions must be at least 64 pixels")
	}
	if req.SafetyTolerance != nil && (*req.SafetyTolerance < 0 || *req.SafetyTolerance > 7) {
		return fmt.Errorf("bfl: safety tolerance for %s must be between 0 and 7", req.Model)
	}
	for i, input := range req.References {
		if err := validateImageInput(input); err != nil {
			return fmt.Errorf("bfl: reference image %d: %w", i+1, err)
		}
	}
	return nil
}

func validateImageInput(input ImageInput) error {
	hasURL := input.URL != ""
	hasData := len(input.Data) != 0
	if hasURL == hasData {
		return errors.New("exactly one of URL or Data must be set")
	}
	if hasURL {
		u, err := url.Parse(input.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return errors.New("URL must be an absolute HTTPS URL without user information")
		}
		if input.ContentType != "" {
			return errors.New("ContentType is only used with Data")
		}
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(input.ContentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "image/") {
		return errors.New("ContentType must be a valid image media type when Data is set")
	}
	return nil
}

func marshalImageRequest(req ImageRequest) ([]byte, error) {
	references := make([]string, 0, len(req.References))
	for _, ref := range req.References {
		if ref.URL != "" {
			references = append(references, ref.URL)
		} else {
			references = append(references, base64.StdEncoding.EncodeToString(ref.Data))
		}
	}
	if req.Model == ModelFlux3Image {
		payload := struct {
			Prompt          string   `json:"prompt"`
			Images          []string `json:"images,omitempty"`
			AspectRatio     string   `json:"aspect_ratio,omitempty"`
			Resolution      string   `json:"resolution,omitempty"`
			Width           int      `json:"width,omitempty"`
			Height          int      `json:"height,omitempty"`
			SafetyTolerance *int     `json:"safety_tolerance,omitempty"`
		}{req.Prompt, references, req.AspectRatio, req.Resolution, req.Width, req.Height, req.SafetyTolerance}
		return json.Marshal(payload)
	}
	payload := map[string]any{"prompt": req.Prompt}
	for i, ref := range references {
		key := "input_image"
		if i > 0 {
			key = fmt.Sprintf("input_image_%d", i+1)
		}
		payload[key] = ref
	}
	if req.Width > 0 {
		payload["width"], payload["height"] = req.Width, req.Height
	}
	if req.Seed != nil {
		payload["seed"] = *req.Seed
	}
	if req.SafetyTolerance != nil {
		payload["safety_tolerance"] = *req.SafetyTolerance
	}
	return json.Marshal(payload)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// Image is a file-backed generated image. Close removes its temporary file.
type Image struct{ mediaFile }

type mediaFile struct {
	mu          sync.RWMutex
	path        string
	url         string
	contentType string
	size        int64
	closed      bool
}

// URL returns the original BFL signed URL, which expires after a short period.
func (m *Image) URL() string { return m.url }

// ContentType returns the downloaded image media type.
func (m *Image) ContentType() string { return m.contentType }

// Size returns the cached image size in bytes.
func (m *Image) Size() int64 { return m.size }

// Bytes reads the full image into memory.
func (m *Image) Bytes() ([]byte, error) {
	path, err := m.localPath()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// WriteTo streams the cached image to w.
func (m *Image) WriteTo(w io.Writer) (int64, error) {
	path, err := m.localPath()
	if err != nil {
		return 0, err
	}
	return writeFileTo(path, w)
}

// Save atomically writes the cached image to path.
func (m *Image) Save(path string) error {
	local, err := m.localPath()
	if err != nil {
		return err
	}
	return copyFileAtomically(local, path)
}

// Close removes the temporary backing file. It is safe to call more than once.
func (m *Image) Close() error { return m.close() }

func (m *mediaFile) localPath() (string, error) {
	if m == nil {
		return "", errors.New("bfl: nil media")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return "", errors.New("bfl: media is closed")
	}
	return m.path, nil
}

func (m *mediaFile) close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.closed = true
	m.path = ""
	return nil
}

func (c *Client) downloadImage(ctx context.Context, rawURL string) (*Image, error) {
	file, contentType, size, err := c.downloadMedia(ctx, rawURL, "image/")
	if err != nil {
		return nil, err
	}
	return &Image{mediaFile{path: file, url: rawURL, contentType: contentType, size: size}}, nil
}

func (c *Client) downloadMedia(ctx context.Context, rawURL, expectedPrefix string) (path string, contentType string, size int64, retErr error) {
	u, err := validateDeliveryURL(rawURL)
	if err != nil {
		return "", "", 0, err
	}
	resp, err := c.doSafeURLWithAuth(ctx, u, false, func(resp *http.Response) (bool, error) {
		var retry bool
		var consumeErr error
		path, contentType, size, retry, consumeErr = cacheMedia(resp, expectedPrefix)
		return retry, consumeErr
	})
	if err != nil {
		return "", "", 0, err
	}
	if isSuccessfulHTTPStatus(resp.StatusCode) {
		return path, contentType, size, nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("bfl: close media response body: %w", err))
		}
	}()
	return "", "", 0, decodeAPIError(resp)
}

func cacheMedia(resp *http.Response, expectedPrefix string) (path string, contentType string, size int64, retry bool, retErr error) {
	bodyClosed := false
	defer func() {
		if !bodyClosed {
			if err := resp.Body.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("bfl: close media response body: %w", err))
				retry = false
			}
		}
	}()
	first, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return "", "", 0, true, fmt.Errorf("bfl: read media response: %w", err)
	}
	if len(first) == 0 {
		return "", "", 0, false, errors.New("bfl: media response is empty")
	}
	contentType = resp.Header.Get("Content-Type")
	mediaType, _, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil || mediaType == "" || mediaType == "application/octet-stream" {
		contentType = http.DetectContentType(first)
		mediaType, _, _ = mime.ParseMediaType(contentType)
	}
	if !strings.HasPrefix(strings.ToLower(mediaType), expectedPrefix) {
		return "", "", 0, false, fmt.Errorf("bfl: expected %s media, received %q", strings.TrimSuffix(expectedPrefix, "/"), mediaType)
	}
	tmp, err := os.CreateTemp("", "bfl-media-*")
	if err != nil {
		return "", "", 0, false, fmt.Errorf("bfl: create media cache: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("bfl: remove media cache: %w", err))
				retry = false
			}
		}
	}()

	nWritten, err := tmp.Write(first)
	written := int64(nWritten)
	if err == nil && nWritten != len(first) {
		err = io.ErrShortWrite
	}
	if err == nil {
		var copied int64
		reader := &mediaReader{Reader: resp.Body}
		copied, err = io.Copy(tmp, reader)
		written += copied
		retry = err != nil && reader.err != nil && errors.Is(err, reader.err)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if closeErr != nil {
		err = errors.Join(err, closeErr)
		retry = false
	}
	if err != nil {
		return "", "", 0, retry, fmt.Errorf("bfl: cache media: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		bodyClosed = true
		return "", "", 0, false, fmt.Errorf("bfl: close media response body: %w", err)
	}
	bodyClosed = true
	ok = true
	return tmpPath, mediaType, written, false, nil
}

type mediaReader struct {
	io.Reader
	err error
}

func (r *mediaReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func validateDeliveryURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return nil, errors.New("bfl: invalid media delivery URL")
	}
	host := strings.ToLower(u.Hostname())
	if !isBFLHost(host) && !isBFLAzureDeliveryHost(host) {
		return nil, fmt.Errorf("bfl: media delivery host %q is not a recognized BFL delivery host", host)
	}
	return u, nil
}

func isBFLAzureDeliveryHost(host string) bool {
	return strings.HasPrefix(host, "bfldelivery") && strings.HasSuffix(host, ".blob.core.windows.net")
}

func writeFileTo(path string, w io.Writer) (n int64, retErr error) {
	if w == nil {
		return 0, errors.New("bfl: writer must not be nil")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("bfl: close media file: %w", err))
		}
	}()
	return io.Copy(w, f)
}

func copyFileAtomically(source, destination string) (err error) {
	dir := filepath.Dir(destination)
	tmp, err := os.CreateTemp(dir, ".bfl-save-*")
	if err != nil {
		return fmt.Errorf("bfl: create temporary output file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	in, err := os.Open(source)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, in)
	closeInErr := in.Close()
	if err == nil {
		err = closeInErr
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeTmpErr := tmp.Close()
	if err == nil {
		err = closeTmpErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("bfl: replace %q: %w", destination, err)
	}
	return nil
}
