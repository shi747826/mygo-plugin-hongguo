# In-process mobile adapter

Import `github.com/shi747826/mygo-plugin-hongguo/mobile`.

```go
func New(ctx context.Context, dataDir string) (*Source, error)
func NewWithManifest(ctx context.Context, dataDir string, manifest json.RawMessage) (*Source, error)
func (*Source) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)
func (*Source) Close() error
```

`New` opens source state in the private app data directory. Keep the source alive while consuming returned media. Its parent context owns the entire source; individual `Call` contexts own individual requests. `Close` cancels active calls, waits for completion, and closes all media resources. Repeated/concurrent Close calls return the same result. No plugin executable or SDK rewrite is involved.

Methods and JSON fields are delegated unchanged to the existing source handler, including Discover, Search, Rank, Detail, Resolve, Release, Covers, Seasons, Calendar, Danmaku, ResetDevice and account methods. Info returns the complete manifest object with runtime replaced by `{"type":"inprocess","api":"mobile-v1"}`. Unsupported methods retain the pluginapi error classification.

The default manifest is an embedded exact snapshot of the repository's plugin.json. Refresh it from the repository root using `go generate ./mobile` before a release. A temporary Android assembly script can instead embed each independent repository's plugin.json and pass its bytes to `NewWithManifest`, then inject neutral registry entries using the returned Info JSON. The host need only the standard-library Call/Close interface; it should not import this repository's pluginapi snapshot or hardcode source identifiers. JSON formatting/order may change; all fields except runtime are preserved, including unknown future fields.

Mobile always selects the explicit direct-only playback helper. It ignores desktop FFmpeg configuration and never runs FFmpeg. Direct, unprotected streams without a separate audio track remain available. Protected streams or streams requiring audio/video merging return an error matching `mobile.ErrUnsupported` with an explicit FFmpeg/mobile explanation. The ordinary desktop constructor keeps its existing behavior.

Offline verification from the repository root (use the workspace's `.tools/go/bin/go` executable):

```sh
go test -race ./mobile ./internal/playback ./internal/sourceplugins
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./mobile
```

These tests use local fixtures and loopback servers. They verify interfaces, manifest preservation, JSON forwarding, request/parent cancellation, idempotent Close and direct-only stream handling. Cross-compilation checks Go packages; it does not validate Android player codecs or live source availability. The Android app must permit its local HTTP media URLs and release/close source sessions when no longer needed.
