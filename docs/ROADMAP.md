# bfl-go SDK Roadmap

## Objective

Build an independent, idiomatic Go SDK for the official Black Forest Labs API. Prioritize a simple default workflow, with configurable options and explicit job controls for advanced use. Use only capabilities verified in official BFL documentation. Do not copy, wrap, or depend on `psyb0t/go-bfl`.

## Working rules

- Verify endpoint, request, response, and authentication details against official BFL docs before implementing each capability.
- Treat returned `polling_url` values as authoritative; never reconstruct them from a task ID.
- Keep generation POST retries disabled by default unless BFL documents a safe idempotency mechanism.
- Avoid generated OpenAPI structures as public API types.
- Do not implement undocumented parameters or endpoints.
- Keep implementation paused until the public API design in Phase 0 is approved.

## Phase 0 — API design review (spec drafted; awaiting approval)

Resolve the following decisions and publish a compile-ready API specification before implementation:

- [x] Define supported MVP models and defaults: FLUX 3 Image default; FLUX.2 Pro as a separate selectable adapter.
- [x] Make seed model-specific: supported only for FLUX.2 Pro, not FLUX 3 Image.
- [x] Define `ImageInput` validation, endpoint-specific encoding, and reference field mapping.
- [x] Define convenience method mode behavior and reject conflicting combinations.
- [x] Limit MVP video modes to t2v and single-image i2v; defer unpinned keyframes, continuation, and draft enhancement payloads.
- [x] Specify exported signatures, model/mode/state values, retry policy, and compiling usage examples.
- [x] Choose temporary-file-backed media, streaming, size semantics, `Close` cleanup, and atomic save behavior.
- [x] Define signed URL behavior and returned-polling-URL handling, including credential host validation.
- [x] Define job state, cancellation, timeout, terminal failure, and retry semantics.
- [x] Recheck official docs and record endpoint/request mappings.

**Exit criteria:** Draft specification is in [API_SPEC.md](API_SPEC.md). The single-image i2v payload is verified as `keyframes` containing a URL or base64 string in the official [video guide](https://docs.bfl.ml/flux_3/flux3_video). User reviews and approves the specification before Phase 1.

## Phase 1 — Client and transport (implemented)

- [x] Create module/package `bfl` with standard-library-only runtime dependencies.
- [x] Implement `New`, API key configuration, injectable `*http.Client`, configurable API base URL, and context-aware JSON requests.
- [x] Add typed HTTP/API errors with status code, decoded server details, and wrapped network/decode errors.
- [x] Add bounded response handling and safe handling of unexpected response bodies.
- [x] Implement retry policy for approved safe GET operations only; task-creating POSTs have no retry path.

**Exit criteria:** transport behavior follows the approved API spec; no endpoint-specific generation implementation is required in this phase.

## Phase 2 — Shared job lifecycle

- [ ] Model submit responses (`id`, returned `polling_url`, and any documented metadata).
- [ ] Implement typed job status, status polling, configurable interval, and context cancellation.
- [ ] Map documented statuses including `Pending`, `Reasoning`, `Generating`, `Ready`, moderation statuses, `Error`, and `Task not found` without treating unknown future status strings as success.
- [ ] Surface details and moderation reasons through structured errors/results.
- [ ] Make terminal state handling consistent for images and videos.

**Exit criteria:** jobs can be submitted, inspected, and waited on without media-specific assumptions.

## Phase 3 — Image workflows

- [ ] Implement the approved default image generation endpoint and typed request.
- [ ] Implement documented image editing/reference inputs and model-specific request mapping.
- [ ] Add simple `Images.Generate`/`Images.Edit` flows that submit, wait, and retrieve media.
- [ ] Implement image media URL/content type/size, `Bytes`, `WriteTo`, and atomic `Save` with documented temp-file cleanup.
- [ ] Download result URLs promptly because BFL delivery links expire; do not assume delivery hostnames or browser CORS support.

**Exit criteria:** simple and advanced image APIs work using documented fields and consistent lifecycle/error behavior.

## Phase 4 — Video workflows

- [ ] Implement the approved FLUX 3 video modes, initially t2v and i2v unless Phase 0 approves more.
- [ ] Represent video request mode and image/keyframe inputs with typed fields validated against the official request schema.
- [ ] Implement `Videos.Generate` and `Videos.GenerateFromImage` with automatic polling.
- [ ] Implement video media with file-backed streaming for `WriteTo` and `Save`; `Bytes` remains an explicit full-memory operation.

**Exit criteria:** supported video workflows stream large results without requiring full in-memory buffering.

## Phase 5 — Documentation and release readiness

- [ ] Add minimal image generation, editing, video, and advanced job examples.
- [ ] Document authentication, model selection, regional base URLs, polling/cancellation, signed URL expiration, media lifetime, and retry behavior.
- [ ] Review exported API for consistency and stability; document compatibility policy.
- [ ] Verify every model/field/endpoint against official docs before release.

**Exit criteria:** examples match the exported API, docs explain operational behavior, and release scope contains no unverified API capability.

## API design checkpoint

The initial target remains:

```go
client, err := bfl.New(bfl.WithAPIKey(apiKey))
image, err := client.Images.Generate(ctx, prompt)
err = image.Save("image.png")

video, err := client.Videos.Generate(ctx, prompt)
err = video.Save("video.mp4")

job, err := client.Images.Submit(ctx, request)
status, err := job.Status(ctx)
image, err := job.Wait(ctx)
```

These are design targets. Do not begin Phase 1 until Phase 0 has been completed and approved.
