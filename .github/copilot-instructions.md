# Copilot instructions

## Build, test, and lint

`go.mod` and `go.sum` are committed; no setup step is needed. The target list is
embedded at compile time and selected by build tag:

```sh
go build -trimpath -ldflags="-s -w" -o go-deadman .              # presets/internet.yaml
go build -trimpath -ldflags="-s -w" -tags tasks -o go-deadman .  # presets/tasks.yaml
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

Run a single test with `go test . -run '^TestName$' -count=1`.

CI builds Windows amd64 binaries for both presets on `v*` tags or manual
dispatch; the tag is injected via `-X main.version` and `go-winres make
--product-version/--file-version`.

## Architecture

Single `main` package split by responsibility:

- `config.go` — `loadConfig` parses YAML, applies defaults, validates, marks
  `"---"` entries as separators (`Device.isSep`) and stores the parsed
  interval. Everything about a device that the renderer reads without the lock
  must be set here, before workers start.
- `stats.go` — `Device` and `UpdateStats`, the only place probe results are
  recorded (loss, SNT, last RTT/`LastOK`, moving average, jitter, history ring).
  It also calls `signalUpdate()`.
- `ping.go` — `resolveAndStartWorker` fills a missing IP or name via DNS, then
  `deviceWorker` runs pro-bing in infinite mode. `OnSend`/`OnRecv` go through one
  channel into `probeTracker`, which settles each sequence number exactly once
  (reply → success, `probeTimeout` → loss, late reply → ignored). If pro-bing
  fails, `fallbackWorker` shells out to `ping` (`pingArgs` per OS) and parses
  output with `parseRTT`.
- `ui.go` — Bubble Tea model. Redraws are on demand: workers signal
  `updateCh`, `Update` throttles to `RENDER_FPS`. `computeLayout` assigns
  column widths by priority so no line exceeds the terminal width.
- `preset_internet.go` / `preset_tasks.go` — `//go:embed` of the selected preset.

## Conventions

- Record results from both ping paths only through `Device.UpdateStats`; guard
  shared `Device` fields with `Device.mu` and read them in `View` via
  `Device.snapshot()`.
- Metric definitions are fixed (see README "Dashboard"): non-zero loss never
  renders as `0%`; a failed latest probe shows `FAIL`; jitter pairs only
  adjacent successful probes; SNT counts settled probes.
- Measure text with `lipgloss.Width` / `truncWidth` / `fitWidth`, not
  `len`, so CJK names lay out correctly.
- Keep `presets/internet.yaml` and `presets/tasks.yaml` on the same schema; they
  must pass `loadConfig` (covered by `TestLoadConfigPresets`).
- `parseRTT` must keep supporting English (`time=`/`time<`) and Chinese
  (`時間=`/`時間<`) ping output.
