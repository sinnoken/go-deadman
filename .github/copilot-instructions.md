# Copilot instructions

## Build, test, and lint

`go.mod` and `go.sum` are committed; no setup step is needed. Every
`presets/*.yaml` is embedded and chosen at runtime:

```sh
go build -trimpath -ldflags="-s -w" -o go-deadman ./cmd/go-deadman
./go-deadman -preset tasks        # default: internet (or go-deadman-<preset> exe name)
./go-deadman -config other.yaml   # external file instead of a preset
```

Validation (mirrors CI in `.github/workflows/build.yml`):

```sh
go mod tidy -diff
gofmt -l .
go vet ./... && GOOS=windows go vet ./... && GOOS=darwin go vet ./...
go test -race -count=1 ./...
golangci-lint run --timeout=5m
govulncheck ./...
gosec ./...
```

Run a single test with `go test ./internal/monitor -run '^TestName$' -count=1`.

CI builds one zip per platform (Windows amd64, macOS arm64) on `v*` tags or manual dispatch.
The tag is injected via `-X main.version` and `go-winres make --in
winres/winres.json --out cmd/go-deadman/rsrc --product-version/--file-version`;
binaries get a build-provenance attestation.

## Architecture

- `cmd/go-deadman` — flags (`-preset`, `-config`, `-list-presets`,
  `-version`), `choosePreset` (flag > `go-deadman-<preset>` exe name >
  `presets.Default`), `monitor.LoadConfig`, `cfg.Start(ctx)`, `tui.New`.
- `presets` — embeds `*.yaml`; `Names()`, `Get(name)`, `Default`.
- `internal/monitor`
  - `config.go` — `LoadConfig` parses, defaults and validates, marks `"---"`
    entries as separators; `Config.Start(ctx)` launches one worker per target
    and returns a `wait` func. `main` cancels the context on quit, which stops
    pingers, `ping` child processes and DNS retries.
    Anything the renderer reads without the lock must be set here, before
    workers start.
  - `stats.go` — `Device.UpdateStats` is the only place probe results are
    recorded (loss, SNT, last RTT/`LastOK`, moving average, jitter, history).
    `Device.Snapshot` gives the renderer a consistent copy; `Updates()` signals
    data changes.
  - `ping.go` — `runDevice` fills a missing name (reverse DNS) or IP (retrying
    with backoff until it resolves), then `deviceWorker` runs `runNative`: an
    `echoSession` (pro-bing in infinite mode) whose send/receive callbacks share
    one channel into `probeTracker`, which settles each sequence number exactly
    once (reply → success, `probeTimeout` → loss, late reply → ignored). On
    Windows, IPv4 sessions use `IcmpSendEcho` (no admin needed, RTT timed with
    `QueryPerformanceCounter`; `icmp_windows.go`). If the
    session can't start or stops on its own, `fallbackWorker` shells out to
    `ping` (`pingArgs` per OS) and parses output with `parseRTT`.
  - Seams for tests: `newEchoSession`, `runPing`, `lookupIP`, `lookupAddr`,
    `dnsRetryMin/Max` are package variables. Worker tests replace them and must
    stop their goroutines before restoring (see `goWorker` in `worker_test.go`).
- `internal/tui` — redraws on `monitor.Updates()`, throttled to `renderFPS`.
  `computeLayout` assigns column widths by priority so no line exceeds the
  terminal width; rows beyond `height - chromeRows` scroll via `offset`.

## Conventions

- Record results from both ping paths only through `Device.UpdateStats`; guard
  shared `Device` fields with its mutex and read them via `Snapshot()`.
- Metric definitions are fixed (see README "Columns"): non-zero loss never
  renders as `0%`; a failed latest probe shows `FAIL`; jitter pairs only
  adjacent successful probes; SNT counts settled probes.
- Measure text with `lipgloss.Width` / `truncWidth` / `fitWidth`, not `len`, so
  CJK names lay out correctly (`TestViewFitsWidth` checks widths 20–200).
- Keep presets on the same schema; `TestLoadConfigPresets` validates every one.
- CI pins actions to commit SHAs and tools to versions; keep `internal/monitor`
  coverage ≥ 80% (CI gate).
- `parseRTT` must keep supporting English (`time=`/`time<`) and Chinese
  (`時間=`/`時間<`) ping output.
