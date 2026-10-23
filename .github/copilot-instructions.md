# Copilot instructions

## Build, test, and lint

`go.mod` and `go.sum` are committed; no setup step is needed. The target list is
embedded at compile time and selected by build tag:

```sh
go build -trimpath -ldflags="-s -w" -o go-deadman ./cmd/go-deadman              # presets/internet.yaml
go build -trimpath -ldflags="-s -w" -tags tasks -o go-deadman ./cmd/go-deadman  # presets/tasks.yaml
./go-deadman -config other.yaml   # runtime override of the embedded preset
```

Validation (mirrors CI in `.github/workflows/build.yml`):

```sh
go mod tidy -diff
gofmt -l .
go vet ./... && go vet -tags tasks ./...
go test -race -count=1 ./... && go test -race -count=1 -tags tasks ./...
golangci-lint run --timeout=5m
govulncheck ./...
gosec ./...
```

Run a single test with `go test ./internal/monitor -run '^TestName$' -count=1`.

CI builds Windows amd64 and macOS arm64 zips for both presets on `v*` tags or manual dispatch.
The tag is injected via `-X main.version` and `go-winres make --in
winres/winres.json --out cmd/go-deadman/rsrc --product-version/--file-version`;
binaries get a build-provenance attestation.

## Architecture

- `cmd/go-deadman` — flags, picks `presets.Data` or the `-config` file, calls
  `monitor.LoadConfig`, runs `tui.New`.
- `presets` — `internet.go` (`!tasks`) / `tasks.go` (`tasks`) embed the YAML.
- `internal/monitor`
  - `config.go` — `LoadConfig` parses, defaults and validates, marks `"---"`
    entries as separators; `Config.Start` launches one worker per target.
    Anything the renderer reads without the lock must be set here, before
    workers start.
  - `stats.go` — `Device.UpdateStats` is the only place probe results are
    recorded (loss, SNT, last RTT/`LastOK`, moving average, jitter, history).
    `Device.Snapshot` gives the renderer a consistent copy; `Updates()` signals
    data changes.
  - `ping.go` — `resolveAndStartWorker` fills a missing IP or name via DNS, then
    `deviceWorker` runs pro-bing in infinite mode. `OnSend`/`OnRecv` share one
    channel into `probeTracker`, which settles each sequence number exactly once
    (reply → success, `probeTimeout` → loss, late reply → ignored). On failure
    `fallbackWorker` shells out to `ping` (`pingArgs` per OS) and parses output
    with `parseRTT`.
- `internal/tui` — redraws on `monitor.Updates()`, throttled to `renderFPS`.
  `computeLayout` assigns column widths by priority so no line exceeds the
  terminal width.

## Conventions

- Record results from both ping paths only through `Device.UpdateStats`; guard
  shared `Device` fields with its mutex and read them via `Snapshot()`.
- Metric definitions are fixed (see README "Columns"): non-zero loss never
  renders as `0%`; a failed latest probe shows `FAIL`; jitter pairs only
  adjacent successful probes; SNT counts settled probes.
- Measure text with `lipgloss.Width` / `truncWidth` / `fitWidth`, not `len`, so
  CJK names lay out correctly (`TestViewFitsWidth` checks widths 20–200).
- Keep both presets on the same schema; `TestLoadConfigPresets` validates them.
- `parseRTT` must keep supporting English (`time=`/`time<`) and Chinese
  (`時間=`/`時間<`) ping output.
