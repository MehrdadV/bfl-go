# bfl-go

An independent Go SDK for the Black Forest Labs API. It uses Go's standard HTTP and JSON packages.

**Go requirement:** Go 1.27.4 or newer. The module selects the Go 1.27.4 toolchain.

**Module path:** `github.com/MehrdadV/bfl-go`.

## Install

```sh
go get github.com/MehrdadV/bfl-go
```

Set the API key from your BFL dashboard:

```sh
export BFL_API_KEY="your-api-key"
```

The examples submit real generation requests to BFL, which may consume account credits.

## Generate an image

```go
package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/MehrdadV/bfl-go"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (retErr error) {
	client, err := bfl.New(bfl.WithAPIKey(os.Getenv("BFL_API_KEY")))
	if err != nil {
		return err
	}

	image, err := client.Images.Generate(context.Background(), "A mountain lake at sunset")
	if err != nil {
		return err
	}
	return image.Save("image.png")
}
```

`Generate` submits the request, polls BFL, downloads the result into a temporary backing file, and returns when the media is ready. FLUX 3 Image is the default. Options can select FLUX.2 Pro or set dimensions and other documented controls:

```go
image, err := client.Images.Generate(ctx, prompt,
	bfl.WithImageModel(bfl.ModelFlux2Pro),
	bfl.WithDimensions(1024, 1024),
	bfl.WithImageSeed(42), // FLUX.2 Pro only
)
```

Image editing accepts a URL or raw bytes for each reference:

```go
image, err := client.Images.Edit(ctx, "Turn the scene into a rainy evening", []bfl.ImageInput{
	{URL: "https://example.com/photo.jpg"},
})
```

For byte inputs, set both `Data` and `ContentType`, for example `image/png`. Exactly one of `URL` or `Data` must be set.

## Generate video

```go
func saveVideo(ctx context.Context, client *bfl.Client) (retErr error) {
	video, err := client.Videos.Generate(ctx, "A fox running through dawn mist")
	if err != nil {
		return err
	}
	return video.Save("video.mp4")
}
```

Image-to-video uses the image as the opening frame:

```go
video, err := client.Videos.GenerateFromImage(ctx,
	"Push forward through the trees",
	bfl.ImageInput{URL: "https://example.com/opening-frame.jpg"},
)
```

## Advanced job control

Use `Submit` when you need the job ID or want to inspect status before waiting:

```go
job, err := client.Images.Submit(ctx, bfl.ImageRequest{
	Prompt: "A mountain lake at sunset",
})
if err != nil {
	log.Fatal(err)
}

status, err := job.Status(ctx)
if err != nil {
	log.Fatal(err)
}
log.Printf("job %s: %s", status.ID, status.State)

image, err := job.Wait(ctx)
if err != nil {
	log.Fatal(err)
}
defer image.Close()
```

The SDK polls the exact `polling_url` returned by BFL. Every operation accepts a context; use a deadline to bound the full operation. `WithPollTimeout` covers polling and downloading the result. Configure the polling interval, SDK polling timeout, HTTP client, regional API base URL, or safe GET retry policy when constructing the client.

## Media handling

Image and video results are backed by temporary files so large video downloads do not need to stay in memory. Call `Close` when finished to remove the backing file. `WriteTo` and `Save` stream from disk; `Bytes` reads the entire media into memory. `Save` writes through a temporary file in the destination directory and renames it into place. It does not create missing directories.

`URL()` returns BFL's signed delivery URL. BFL says result URLs expire after 10 minutes; the SDK downloads media before `Generate` or `Wait` returns, so local media methods do not depend on that URL remaining valid.

Media request error text hides the delivery URL and reports common causes such as timeouts and refused connections. The underlying error remains available through error unwrapping and may contain the signed URL.
Media redirects are limited to trusted HTTPS delivery hosts and do not send the signed URL in a `Referer` header.

`Save` writes the downloaded bytes as-is and does not convert formats to match the filename extension. Use `ContentType()` to choose an appropriate extension.

## Configuration

```go
client, err := bfl.New(
	bfl.WithAPIKey(os.Getenv("BFL_API_KEY")),
	bfl.WithHTTPClient(&http.Client{Timeout: 2 * time.Minute}),
	bfl.WithPollInterval(750*time.Millisecond),
	bfl.WithPollTimeout(10*time.Minute),
	bfl.WithRetryPolicy(bfl.RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   250 * time.Millisecond,
		MaxDelay:    5 * time.Second,
	}),
)
```

Retries apply to safe GET requests such as polling and media downloads, including interrupted response reads. Task-creating POST requests are never retried automatically, to avoid duplicate jobs.

## Supported API surface

- FLUX 3 Image generation and reference-based editing.
- FLUX.2 Pro generation and reference-based editing, including its documented seed parameter.
- FLUX 3 Video text-to-video and single-opening-image-to-video.

Only fields verified against the [official API reference](https://docs.bfl.ml/api-reference/utility/generate-an-image-with-flux-3) are exposed. FLUX 3 storyboard keyframes, video continuation, draft enhancement, video editing/upscaling, and specialized image tools are outside the current scope.

## Errors

HTTP failures are returned as `*bfl.APIError`; terminal generation states are `*bfl.JobError`; malformed successful responses are `*bfl.ProtocolError`. Use `errors.As` to inspect them. Context cancellation and deadline errors remain discoverable with `errors.Is`.

## Development

Install the tools in `mise.toml` with `mise install`, then enable the pre-push lint hook:

```sh
git config --local core.hooksPath .githooks
```

The hook runs `golangci-lint` against the working tree and blocks the push if linting fails.

## Compatibility

The documented exported API is treated as stable. New model constants and options may be added; existing names and field meanings should not change incompatibly without a major version. Endpoint schemas and accepted values are rechecked against official BFL documentation for each release.
