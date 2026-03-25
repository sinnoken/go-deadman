# 💀 go-deadman

![screenshot](./screenshot.jpg)

> A high-performance, event-driven Terminal UI (TUI) network monitor that tracks **ICMP ping latency, jitter, and packet loss** across multiple targets simultaneously — all from a single, zero-dependency executable.

![Version](https://img.shields.io/badge/version-v1.11.0-blue)
![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux%20%7C%20macOS-lightgrey)
![License](https://img.shields.io/badge/license-MIT-green)

---

## 📖 Project Overview

`go-deadman` is a real-time network health monitor built for network engineers and system administrators. It continuously pings a configurable list of devices — from global Tier-1 ISPs and cloud Anycast endpoints to local LAN hosts — and renders a live, color-coded dashboard directly in your terminal.

Key design goals:

- **Zero external dependencies at runtime** — the config YAML is compiled directly into the binary via Go's `//go:embed`
- **Event-driven, socket-reuse architecture** — avoids the overhead of spawning a new process or socket per ping cycle
- **Graceful degradation** — automatically falls back to the OS native `ping` command when raw socket privileges are unavailable
- **Single-file simplicity** — the entire application logic lives in `main.go`

> Inspired by https://github.com/upa/deadman

---

## ✨ Features

| Feature | Description |
|---|---|
| 🎨 **Rich TUI Dashboard** | Built with Bubble Tea + Lipgloss; fully responsive to terminal window resize |
| ⚡ **Event-Driven Ping Engine** | Uses `pro-bing` in infinite-send mode with socket reuse — no per-ping overhead |
| 📊 **Real-Time Metrics** | Tracks RTT, moving-average RTT, jitter, packet loss %, and total sent count |
| 📈 **Sparkline History** | Visual ping history using Unicode block chars (`▁▂▃▄▅▆▇█`) scaled logarithmically |
| 🔄 **Smart Fallback** | Falls back to OS `ping` command parsing when raw socket privileges are missing |
| 🌐 **Auto DNS Resolution** | Resolves hostname → IP or IP → hostname automatically if only one is provided |
| 🧩 **Embed Config** | Config YAML is embedded at compile time; ships as a single portable binary |
| 🪟 **Windows Native** | Supports Windows `.exe` with custom app icon via `go-winres` |
| 🔒 **Security Scanned** | CI pipeline runs `golangci-lint`, `govulncheck`, and `gosec` on every push |

---

## 🏗️ Tech Stack

| Category | Library / Tool | Role |
|---|---|---|
| **Language** | https://go.dev/ (stable, 1.21+) | Core runtime |
| **TUI Framework** | https://github.com/charmbracelet/bubbletea | Elm-architecture UI event loop |
| **Terminal Styling** | [Lipgloss](https://github.com/charmbracelet/lipgloss) | Color, layout, and text styling |
| **ICMP Ping Engine** | [pro-bing](https://github.com/prometheus-community/pro-bing) | Native Go ICMP socket management |
| **Config Format** | YAML (`config.yaml`) | Device list and runtime parameters |
| **Static Embedding** | Go `//go:embed` | Bundle config YAML into the binary |
| **Windows Resources** | https://github.com/tc-hib/go-winres | Embed app icon and version info into `.exe` |
| **CI/CD** | GitHub Actions | Lint, security scan, multi-config build, and release |
| **Dependency Updates** | Dependabot | Weekly automated Go module and Actions updates |
| **Linting** | golangci-lint | Static analysis |
| **Vulnerability Scan** | govulncheck + gosec | Security audit |

---

## 📁 Project Structure

```
go-deadman/
├── main.go              # Entire application: config, ping engine, TUI, entry point
├── internet.yaml        # Preset: Global ISPs, CDNs, IXPs, and Taiwan ISPs
├── tasks.yaml           # Preset: Local LAN targets (192.168.x.x)
├── config.yaml          # ⚠️ Required at build time — copy from internet.yaml or tasks.yaml
├── screenshot.jpg       # Dashboard screenshot for README
├── winres/
│   ├── icon.png         # App icon (256×256)
│   ├── icon16.png       # App icon (16×16)
│   └── winres.json      # Windows resource manifest (version, icon, UAC level)
└── .github/
    ├── workflows/
    │   └── build.yml    # CI: lint → build → release pipeline
    └── dependabot.yml   # Automated dependency update config
```

---

## 🚀 Getting Started

### Prerequisites

- **Go 1.21+** — [Download](https://go.dev/dl/)
- **`config.yaml`** must exist in the project root before building (see below)

---

### Step 1 — Clone the Repository

```bash
git clone https://github.com/<your-org>/go-deadman.git
cd go-deadman
```

---

### Step 2 — Prepare the Configuration

The binary embeds `config.yaml` at compile time using `//go:embed`. Choose one of the two presets and copy it:

```bash
# Option A: Monitor global internet infrastructure (ISPs, CDNs, IXPs)
cp internet.yaml config.yaml

# Option B: Monitor a local LAN subnet (192.168.77.x)
cp tasks.yaml config.yaml
```

> ⚠️ **Build will fail** if `config.yaml` does not exist — it is a required embed source.

---

### Step 3 — Install Dependencies

```bash
go mod tidy
go mod download
```

---

### Step 4 — Build

```bash
# Standard build
go build -ldflags="-s -w" -trimpath -o go-deadman .

# Windows (cross-compile from Linux/macOS)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o go-deadman.exe .
```

---

### Step 5 — Run

```bash
# Linux / macOS
./go-deadman

# Windows
.\go-deadman.exe
```

Press **`q`** or **`Ctrl+C`** to exit.

---

### Raw Socket Privileges (for Native Ping Mode)

`go-deadman` uses native Go ICMP sockets by default for maximum performance. This requires elevated privileges:

| OS | How to Grant |
|---|---|
| **Linux** | `sudo ./go-deadman` **or** `sudo setcap cap_net_raw=+ep ./go-deadman` |
| **Windows** | Run the `.exe` as **Administrator** |
| **macOS** | `sudo ./go-deadman` |

> If privileges are not available, the app **automatically falls back** to parsing the OS native `ping` command. Targets using the fallback will not show the `*` indicator.

---

## ⚙️ Configuration Reference

Both `internet.yaml` and `tasks.yaml` follow the same schema:

```yaml
interval: 1s       # Base ping interval (e.g., "1s", "500ms", "2s")
jitter: 0.1        # ±10% random timing variation applied in fallback mode
devices:
  - name: "Cloudflare-Anycast"
    ip: "1.1.1.1"
  - name: "Google-DNS"
    ip: "8.8.8.8"
  - name: "---"          # Renders as a visual separator line
  - name: "My-Router"
    ip: "192.168.1.1"
```

### Field Reference

| Field | Type | Description |
|---|---|---|
| `interval` | `string` | Ping send interval. Parsed as Go `time.Duration`. Default: `1s` |
| `jitter` | `float64` | Fractional timing jitter for fallback mode (0.1 = ±10%). Default: `0.1` |
| `devices[].name` | `string` | Display name. Use `"---"` for a separator row. Omit to auto-resolve from IP |
| `devices[].ip` | `string` | Target IP address. Omit to auto-resolve from hostname |

> 💡 You can provide **only `name`** (hostname) or **only `ip`** — the app resolves the missing field automatically via DNS.

---

## 📊 Dashboard Reference

```
                              go-deadman
       From: myhost  Version: v1.11.0  loss+rtt+avg+jitter+history

  HOSTNAME              ADDRESS          LOSS  RTT(ms)  AVG(ms)  JIT(ms)   SNT  HISTORY
  ─────────────────────────────────────────────────────────────────────────────────────────
  Cloudflare-Anycast    1.1.1.1           0%    2.341    2.298    0.103   142*  ▄▄▃▃▄▃▄▄▅▃
  Google-DNS            8.8.8.8           0%    4.102    4.011    0.214   142*  ▃▃▃▄▃▃▄▃▃▃
  ─────────────────────────────────────────────────────────────────────────────────────────
  My-Router             192.168.1.1       2%    0.812    0.799    0.031   142*  ▁▁▁·▁▁▁▁▁▁

  Interval: 1s  App Jitter: 10%  *: Native  Window: 50
```

| Column | Description |
|---|---|
| **HOSTNAME** | Resolved or configured device name |
| **ADDRESS** | Target IP address |
| **LOSS** | Cumulative packet loss percentage |
| **RTT(ms)** | Round-trip time of the most recent successful ping |
| **AVG(ms)** | Moving average RTT over the last 50 packets (`WINDOW_SIZE`) |
| **JIT(ms)** | Average absolute deviation between consecutive RTT values |
| **SNT** | Total packets sent since start |
| **`*` Tag** | Present when using native Go ICMP socket; absent in OS fallback mode |
| **HISTORY** | Sparkline of the last N pings; height = log-scaled RTT; `·` = dropped packet |

---

## 🧠 Core Module Logic

### 1. `//go:embed` — Zero-Config Binary

```go
//go:embed config.yaml
var embeddedYaml []byte
```

The YAML configuration is compiled directly into the binary at build time. At runtime, `main()` unmarshals this byte slice — no external files required. [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

### 2. `resolveAndStartWorker` — DNS Resolution & Worker Dispatch

Each device entry launches a goroutine that:
1. **Resolves** missing hostname ↔ IP pairs via `net.LookupIP` / `net.LookupAddr`
2. Sets `IsDNSFail = true` and skips pinging if resolution fails
3. Hands off to `deviceWorker` (native) or `fallbackWorker` (OS command) [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

### 3. `deviceWorker` — Event-Driven ICMP Engine (Primary Path)

This is the performance-critical path, implementing a **socket-reuse, event-callback** architecture:

```
┌─────────────────────────────────────────────────────────┐
│  probing.NewPinger  →  Count = -1 (infinite send mode)  │
│                                                         │
│  OnRecv callback → recvCh (buffered channel, cap 100)   │
│                                                         │
│  Main select loop:                                      │
│   ├─ recvCh:   packet received → fill gap losses →      │
│   │            UpdateStats(rtt, true)                   │
│   ├─ timer.C:  timeout → UpdateStats(0, false)          │
│   └─ errCh:    pinger error → fallback                  │
└─────────────────────────────────────────────────────────┘
```

**Sequence gap filling**: If `pkt.Seq` skips ahead (consecutive drops), the worker immediately back-fills `UpdateStats(0, false)` for each missing sequence number before recording the received packet. This guarantees history graph accuracy even during burst loss events. [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

### 4. `fallbackWorker` — OS `ping` Command Fallback

When native ICMP sockets are unavailable, the worker:
1. Spawns `ping -n 1` (Windows) or `ping -c 1` (Linux/macOS) per interval
2. Parses RTT from stdout via `parseRTT()` — supports both English (`time=`) and Chinese (`時間=`) system locales
3. Applies configurable `jitter` to the sleep interval to prevent thundering-herd effects across many devices  [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

### 5. `UpdateStats` — Thread-Safe Metrics Engine

```go
func (d *Device) UpdateStats(rtt float64, success bool) {
    d.mu.Lock()
    defer d.mu.Unlock()
    // Updates: Snt, Loss, LossRate, Window (sliding), AvgRTT, Jitter, History ring buffer
}
```

- Uses a **`sync.RWMutex`** — write-locked during stats update, read-locked during UI rendering
- **Sliding window** (`WINDOW_SIZE = 50`): RTT history for avg and jitter calculation
- **Ring buffer** (`HIST_SIZE = 100`): Fixed-size circular history for the sparkline  [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

### 6. `getLogChar` — Logarithmic Sparkline Encoder

```go
idx := int(math.Log(math.Max(1, rtt)) / math.Log(2.4))
scales := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
```

RTT is mapped to a block character using **base-2.4 logarithm**, so:
- `~1ms` → `▁`, `~6ms` → `▃`, `~35ms` → `▆`, `>200ms` → `█`
- `·` is shown for dropped packets (`success == false`) [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

### 7. Bubble Tea Event Loop — TUI Lifecycle

| Method | Responsibility |
|---|---|
| `Init()` | Parses interval, spawns one goroutine per device, starts `tickCmd()` |
| `Update()` | Handles `WindowSizeMsg`, `KeyMsg` (`q`/`Ctrl+C`), and `tickMsg` for re-render |
| `View()` | Renders the full dashboard string on every tick (20 FPS) with dynamic column widths |

The UI re-renders at **20 FPS** (`RENDER_FPS = 20`) driven by `tea.Tick`. All device data is read under `d.mu.RLock()` to prevent data races with background goroutines. [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

## 🤖 CI/CD Pipeline

| Trigger | Jobs Executed |
|---|---|
| Push to `main` | `lint` (golangci-lint + govulncheck + gosec) |
| Push tag `v*` | `lint` → `build` (matrix) → `release` |
| Manual dispatch | `lint` → `build` |

### Build Matrix

| Config | OS | Arch | Output |
|---|---|---|---|
| `internet` | Windows | amd64 | `deadman-internet-windows-amd64.exe` |
| `tasks` | Windows | amd64 | `deadman-tasks-windows-amd64.exe` |

Each build automatically:
1. Copies the appropriate preset YAML as `config.yaml`
2. Embeds Windows resources (icon + version) via `go-winres`
3. Compiles with `-ldflags="-s -w" -trimpath` for minimal binary size
4. Uploads artifact with 7-day retention; publishes to GitHub Release on tag push [1](https://cht365-my.sharepoint.com/personal/hsieh_cht_com_tw/Documents/Microsoft%20Copilot%20Chat%20%E6%AA%94%E6%A1%88/repomix-output-sinnoken-go-deadman.md)

---

## 📄 License

This project is licensed under the **MIT License**.  
Copyright © 2026 go-deadman.
