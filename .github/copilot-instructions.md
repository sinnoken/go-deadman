# Copilot instructions

## Build, test, and lint

The application is a Go module, but this repository currently does not commit
`go.mod` or `go.sum`. CI initializes the module as `go-deadman` and runs
`go mod tidy` and `go mod download`. For a local checkout, initialize it once
before using the Go commands:

```sh
go mod init go-deadman
go mod tidy
go mod download
```

`main.go` embeds `config.yaml`, which is not checked in. Copy one of the
presets into the repository root before building or testing:

```sh
cp internet.yaml config.yaml  # global and Taiwan network targets
# or: cp tasks.yaml config.yaml  # local LAN targets
```

Build and run:

```sh
go build -ldflags="-s -w" -trimpath -o go-deadman .
./go-deadman
```

CI builds Windows amd64 binaries for both presets, using `go-winres make` for
Windows resources. Its validation commands are:

```sh
golangci-lint run --timeout=5m
govulncheck ./...
gosec ./...
```

There are currently no Go test files. Run all package tests with
`go test ./...`; run a single named test with
`go test . -run '^TestName$' -count=1` after adding one.

## Architecture

- The application logic is in `main.go`. The YAML preset selected at build
  time is copied to `config.yaml` and embedded with `//go:embed`; the running
  binary does not load an external configuration file.
- `Config` and `Device` represent the YAML settings and per-target state.
  `model.Init` starts a worker goroutine for each non-separator device and
  starts the Bubble Tea render tick. `model.Update` handles terminal resize,
  quit keys, and render ticks; `model.View` builds the dashboard.
- Each target resolves a missing hostname or IP before starting its ping
  worker. The primary `deviceWorker` uses pro-bing with a reused ICMP socket
  and records sequence gaps and timeouts as losses. If native ping setup or
  execution fails, `fallbackWorker` invokes the platform `ping` command;
  `parseRTT` extracts its output.
- Both ping paths feed `Device.UpdateStats`, which maintains packet loss,
  current and moving-average RTT, jitter, and a fixed-size history ring. The
  device mutex protects updates from background workers and snapshots read by
  the renderer.
- `internet.yaml` and `tasks.yaml` are alternative presets with the same
  schema. CI builds one binary per preset by copying each in turn to
  `config.yaml`; the `"---"` device name renders a visual separator instead
  of starting a worker.

## Repository-specific conventions

- Keep runtime behavior and UI changes in the existing `main.go` flow unless
  there is a clear reason to split the single-file application.
- When changing ping or statistics behavior, keep native and fallback results
  consistent by recording them through `Device.UpdateStats`; protect shared
  device state with its mutex.
- Configuration keys use YAML tags on `Config` and `Device`. Preserve support
  for entries containing only a hostname or only an IP, and for `"---"`
  separator entries.
- The fallback RTT parser recognizes both English (`time=` / `time<`) and
  Chinese (`時間=` / `時間<`) ping output.
- Keep the two preset files aligned with the shared configuration schema.
  `config.yaml` is a build input selected from a preset, not a separate
  committed configuration.
