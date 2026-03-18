package main

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	probing "github.com/prometheus-community/pro-bing"
	"gopkg.in/yaml.v3"
)

//go:embed config.yaml
var embeddedYaml []byte

// ---------------------------------------------------------
// 常數與全域樣式設定
// ---------------------------------------------------------

const (
	VERSION     = "v1.9.0" // 非同步 DNS 解析架構升級
	HIST_SIZE   = 30
	WINDOW_SIZE = 50
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	headerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	upStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	downStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	arrowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	failRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

// ---------------------------------------------------------
// 資料結構定義
// ---------------------------------------------------------

type Heartbeat struct {
	Char    string
	Success bool
}

type Device struct {
	Name       string `yaml:"name"`
	IP         string `yaml:"ip"`
	Loss       int
	Snt        int
	LastRTT    float64
	AvgRTT     float64
	Window     []float64
	LossRate   int
	History    [HIST_SIZE]Heartbeat
	HistoryIdx int
	HistCount  int
	Loading    bool
	IsNative   bool
	IsDNSFail  bool
}

type Config struct {
	Interval string    `yaml:"interval"`
	Jitter   float64   `yaml:"jitter"`
	Devices  []*Device `yaml:"devices"`
}

type model struct {
	cfg      Config
	devices  []*Device
	width    int
	height   int
	hostname string
	sub      chan tea.Msg
}

// Bubbletea 訊息類型
type initMsg struct{}
type pingStartMsg struct{ idx int }
type pingResMsg struct {
	idx      int
	rtt      float64
	success  bool
	isNative bool
}

// [新增] DNS 解析完成的訊息回傳
type dnsResMsg struct {
	idx    int
	name   string
	ip     string
	isFail bool
}

// ---------------------------------------------------------
// 輔助函式：字串截斷
// ---------------------------------------------------------

func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) > maxLen {
		return string(r[:maxLen-2]) + ".."
	}
	return s
}

// ---------------------------------------------------------
// 核心演算法：狀態更新與視覺化
// ---------------------------------------------------------

func getLogChar(rtt, avg float64, success bool) string {
	if !success || rtt <= 0 {
		return "·"
	}
	if avg <= 0 {
		return "▄"
	}
	ratio := rtt / avg
	diff := math.Log2(ratio) * 2.0
	idx := 3 + int(math.Round(diff))
	scales := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(scales) {
		idx = len(scales) - 1
	}
	return scales[idx]
}

func (d *Device) UpdateStats(rtt float64, success bool) {
	d.Snt++
	if !success {
		d.Loss++
	} else {
		d.LastRTT = rtt
		d.Window = append(d.Window, rtt)
		if len(d.Window) > WINDOW_SIZE {
			d.Window = d.Window[1:]
		}
		var sum float64
		for _, v := range d.Window {
			sum += v
		}
		d.AvgRTT = sum / float64(len(d.Window))
	}
	d.LossRate = (d.Loss * 100) / d.Snt

	char := getLogChar(rtt, d.AvgRTT, success)
	d.History[d.HistoryIdx] = Heartbeat{Char: char, Success: success}
	d.HistoryIdx = (d.HistoryIdx + 1) % HIST_SIZE
	if d.HistCount < HIST_SIZE {
		d.HistCount++
	}
}

// ---------------------------------------------------------
// 網路層邏輯：獨立背景執行緒 (Goroutine Worker)
// ---------------------------------------------------------

// [新增] 負責處理 DNS 並接續啟動 Ping 的統籌 Worker
func resolveAndStartWorker(idx int, rawName, rawIP string, interval time.Duration, jitter float64, sub chan<- tea.Msg) {
	name := strings.TrimSpace(rawName)
	ip := strings.TrimSpace(rawIP)
	isFail := false

	// 1. DNS 正反解邏輯
	if name == "" && ip != "" {
		names, err := net.LookupAddr(ip)
		if err == nil && len(names) > 0 {
			name = strings.TrimSuffix(names[0], ".")
		} else {
			name = ip
		}
	} else if ip == "" && name != "" {
		ips, err := net.LookupIP(name)
		if err == nil && len(ips) > 0 {
			ip = ips[0].String()
		} else {
			ip = "DNS_FAIL"
			isFail = true
		}
	}

	// 2. 截斷過長字串
	name = truncate(name, 15)
	ip = truncate(ip, 15)

	// 3. 把解析結果傳回給 UI 顯示
	sub <- dnsResMsg{
		idx:    idx,
		name:   name,
		ip:     ip,
		isFail: isFail,
	}

	// 4. 如果 DNS 成功，才進入無限循環的 Ping Worker
	if !isFail {
		deviceWorker(idx, ip, interval, jitter, sub)
	}
}

func deviceWorker(idx int, ip string, interval time.Duration, jitter float64, sub chan<- tea.Msg) {
	offset := time.Duration(rand.Float64() * float64(interval))
	time.Sleep(offset)

	for {
		start := time.Now()
		sub <- pingStartMsg{idx: idx}

		var res pingResMsg
		res.idx = idx

		pinger, err := probing.NewPinger(ip)
		if err == nil {
			pinger.Count = 1
			pinger.Timeout = interval
			if runtime.GOOS == "windows" {
				pinger.SetPrivileged(true)
			} else {
				pinger.SetPrivileged(false)
			}
			if err = pinger.Run(); err == nil {
				stats := pinger.Statistics()
				if stats.PacketsRecv > 0 {
					res.rtt = float64(stats.MaxRtt.Microseconds()) / 1000.0
					res.success = true
					res.isNative = true
				}
			}
		}

		if !res.success {
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			var cmd *exec.Cmd
			timeoutMs := strconv.Itoa(int(interval.Milliseconds()))
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", timeoutMs, ip)
			} else {
				cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip)
			}
			out, _ := cmd.CombinedOutput()
			cancel()

			rtt, isSuccess := parseRTT(string(out))
			res.rtt = rtt
			res.success = isSuccess
			res.isNative = false
		}

		sub <- res

		elapsed := time.Since(start)
		baseWait := float64(interval) - float64(elapsed)
		if baseWait < 0 {
			baseWait = 0
		}

		jit := jitter
		if jit <= 0 {
			jit = 0.1
		}
		nextWait := time.Duration(baseWait * (1 + (rand.Float64()*2 - 1)*jit))
		time.Sleep(nextWait)
	}
}

// ... parseRTT 保持不變 ...
func parseRTT(out string) (float64, bool) {
	keys := []string{"time=", "time<", "時間=", "時間<"}
	var start int = -1
	var matchKey string
	for _, k := range keys {
		start = strings.Index(out, k)
		if start != -1 {
			matchKey = k
			break
		}
	}
	if start == -1 {
		return 0, false
	}
	sub := out[start+len(matchKey):]
	end := strings.Index(sub, "ms")
	if end == -1 {
		return 0, false
	}
	timeStr := strings.TrimSpace(sub[:end])
	res, err := strconv.ParseFloat(timeStr, 64)
	if err != nil {
		return 0, false
	}
	return res, true
}

// ---------------------------------------------------------
// Bubbletea UI 渲染與事件更新
// ---------------------------------------------------------

func listenForMsg(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-sub }
}

func (m model) Init() tea.Cmd {
	return func() tea.Msg { return initMsg{} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case initMsg:
		itv, _ := time.ParseDuration(m.cfg.Interval)
		if itv == 0 {
			itv = 2 * time.Second
		}
		for i, d := range m.devices {
			if d.Name != "---" {
				d.Loading = true // [標記] 剛啟動時顯示 Loading 狀態，表示正在解析 DNS
				go resolveAndStartWorker(i, d.Name, d.IP, itv, m.cfg.Jitter, m.sub)
			}
		}
		return m, listenForMsg(m.sub)

	// [接收] 收到 DNS 解析完成的訊息，更新設備的名稱與 IP
	case dnsResMsg:
		d := m.devices[msg.idx]
		d.Name = msg.name
		d.IP = msg.ip
		d.IsDNSFail = msg.isFail
		if msg.isFail {
			d.Loading = false // 如果解析失敗，就取消 Loading 狀態
		}
		return m, listenForMsg(m.sub)

	case pingStartMsg:
		m.devices[msg.idx].Loading = true
		return m, listenForMsg(m.sub)

	case pingResMsg:
		d := m.devices[msg.idx]
		d.Loading, d.IsNative = false, msg.isNative
		d.UpdateStats(msg.rtt, msg.success)
		return m, listenForMsg(m.sub)
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 || m.width == 0 {
		return " Loading..."
	}
	var s strings.Builder

	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("go-deadman")) + "\n")
	subTitle := fmt.Sprintf("From: %s | Version: %s | 顯示: Log+Avg 圖表", m.hostname, VERSION)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, dimStyle.Render(subTitle)) + "\n")

	const fixedColsWidth = 63
	maxHist := m.width - fixedColsWidth
	if maxHist > HIST_SIZE {
		maxHist = HIST_SIZE
	} else if maxHist < 5 {
		maxHist = 5
	}

	sepWidth := m.width
	if sepWidth < 80 {
		sepWidth = 80
	}

	header := fmt.Sprintf("\n  %-15s %-15s %5s %7s %7s %6s  %-*s", 
		"HOSTNAME", "ADDRESS", "LOSS", "RTT(ms)", "AVG(ms)", "SNT", maxHist, "LOG-STATUS")
	s.WriteString(headerStyle.Render(header) + "\n" + dimStyle.Render(strings.Repeat("─", sepWidth)) + "\n")

	for _, d := range m.devices {
		if d.Name == "---" {
			s.WriteString(dimStyle.Render("  " + strings.Repeat("-", sepWidth-2)) + "\n")
			continue
		}

		indicator := "  "
		if d.Loading {
			indicator = arrowStyle.Render("> ")
		}

		var hist strings.Builder
		showCount := d.HistCount
		if showCount > maxHist {
			showCount = maxHist
		}
		for j := 1; j <= showCount; j++ {
			h := d.History[(d.HistoryIdx-j+HIST_SIZE)%HIST_SIZE]
			if h.Success {
				hist.WriteString(upStyle.Render(h.Char))
			} else {
				hist.WriteString(downStyle.Render(h.Char))
			}
		}

		tag := " "
		if d.IsNative {
			tag = "*"
		}

		// [優化] 當還在解析 DNS 時，可能 Name 或是 IP 還是空的，我們給個佔位符確保版面不跑掉
		displayName := d.Name
		if displayName == "" {
			displayName = "resolving..."
		}
		displayIP := d.IP
		if displayIP == "" {
			displayIP = "resolving..."
		}

		line := fmt.Sprintf("%-15s %-15s %4d%% %7.1f %7.1f %5d%s  ", 
			displayName, displayIP, d.LossRate, d.LastRTT, d.AvgRTT, d.Snt, tag)

		s.WriteString(indicator)

		if d.IsDNSFail {
			s.WriteString(failRowStyle.Render(line) + downStyle.Render("DNS RESOLVE FAILED\n"))
		} else if d.HistCount > 0 && !d.History[(d.HistoryIdx-1+HIST_SIZE)%HIST_SIZE].Success {
			s.WriteString(failRowStyle.Render(line) + hist.String() + "\n")
		} else {
			s.WriteString(line + hist.String() + "\n")
		}
	}

	footer := fmt.Sprintf("\n Interval: %s | Jitter: %.f%% | *: Native | Window: %d", 
		m.cfg.Interval, m.cfg.Jitter*100, WINDOW_SIZE)
	s.WriteString(dimStyle.Render(footer))
	return s.String()
}

// ---------------------------------------------------------
// 程式進入點
// ---------------------------------------------------------

func main() {
	rand.Seed(time.Now().UnixNano())

	var cfg Config
	err := yaml.Unmarshal(embeddedYaml, &cfg)
	if err != nil {
		fmt.Printf("讀取設定檔 config.yaml 失敗: %v\n", err)
		os.Exit(1)
	}

	if cfg.Interval == "" {
		cfg.Interval = "1s"
	}
	if cfg.Jitter <= 0 {
		cfg.Jitter = 0.1
	}

	hostname, _ := os.Hostname()
	subChan := make(chan tea.Msg, 1000)

	p := tea.NewProgram(model{
		cfg:      cfg,
		devices:  cfg.Devices,
		hostname: hostname,
		sub:      subChan,
	}, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
	}
}
