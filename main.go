package main

//go:generate go-winres make

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
	VERSION           = "v2.0.0-pro" // 精度優化版
	HIST_SIZE         = 30
	WINDOW_SIZE       = 50
	UI_TICK_INTERVAL  = 50 * time.Millisecond // UI 刷新頻率 (20 FPS)
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
	Jitter     float64
	Window     []float64
	LossRate   int
	History    [HIST_SIZE]Heartbeat
	HistoryIdx int
	HistCount  int
	Loading    bool
	IsNative   bool
	IsDNSFail  bool
	LastChar   string // 預計算的視覺化字元
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
type tickMsg struct{} // UI 定時刷新
type pingStartMsg struct{ idx int }
type pingResMsg struct {
	idx      int
	rtt      float64
	success  bool
	isNative bool
}
type dnsResMsg struct {
	idx    int
	name   string
	ip     string
	isFail bool
}

// ---------------------------------------------------------
// 核心演算法：狀態更新與預計算
// ---------------------------------------------------------

func calculateLogChar(rtt, avg float64, success bool) string {
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
		d.LastChar = "·"
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

		if len(d.Window) >= 2 {
			var jitterSum float64
			for i := 1; i < len(d.Window); i++ {
				jitterSum += math.Abs(d.Window[i] - d.Window[i-1])
			}
			d.Jitter = jitterSum / float64(len(d.Window)-1)
		}
		d.LastChar = calculateLogChar(rtt, d.AvgRTT, success)
	}
	d.LossRate = (d.Loss * 100) / d.Snt
	d.History[d.HistoryIdx] = Heartbeat{Char: d.LastChar, Success: success}
	d.HistoryIdx = (d.HistoryIdx + 1) % HIST_SIZE
	if d.HistCount < HIST_SIZE {
		d.HistCount++
	}
}

// ---------------------------------------------------------
// 網路層：高效能長駐 Worker (微秒優化)
// ---------------------------------------------------------

func deviceWorker(idx int, ip string, interval time.Duration, jitter float64, sub chan<- tea.Msg) {
	// [優化] 鎖定 OS 執行緒，減少 CPU 調度造成的微秒抖動
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 隨機初始偏移，避免所有設備同時發送
	time.Sleep(time.Duration(rand.Float64() * float64(interval)))

	// [優化] 物件重用：嘗試建立長駐型 Pinger (Raw Socket)
	pinger, err := probing.NewPinger(ip)
	if err == nil {
		pinger.Interval = interval
		pinger.Jitter = true // 讓套件處理隨機抖動
		
		// Windows 或有權限的 Linux 預設使用特權模式 (Raw Socket)
		if runtime.GOOS == "windows" {
			pinger.SetPrivileged(true)
		} else {
			// Linux 環境若要微秒精度，建議開啟 SetPrivileged(true) 並賦予 CAP_NET_RAW
			pinger.SetPrivileged(false) 
		}

		pinger.OnSend = func(pkt *probing.Packet) {
			sub <- pingStartMsg{idx: idx}
		}

		pinger.OnRecv = func(pkt *probing.Packet) {
			sub <- pingResMsg{
				idx:      idx,
				rtt:      float64(pkt.Rtt.Microseconds()) / 1000.0, // 保留三位小數(微秒)
				success:  true,
				isNative: true,
			}
		}

		// Run 會阻塞直到錯誤發生
		if err := pinger.Run(); err != nil {
			// 若長駐 Pinger 失敗，進入 Fallback 模式
		}
	}

	// [相容性] Fallback 模式：傳統循環模式 (exec ping)
	for {
		start := time.Now()
		sub <- pingStartMsg{idx: idx}

		ctx, cancel := context.WithTimeout(context.Background(), interval)
		timeoutMs := strconv.Itoa(int(interval.Milliseconds()))
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", timeoutMs, ip)
		} else {
			cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip)
		}
		out, _ := cmd.CombinedOutput()
		cancel()

		rtt, isSuccess := parseRTT(string(out))
		sub <- pingResMsg{
			idx:      idx,
			rtt:      rtt,
			success:  isSuccess,
			isNative: false,
		}

		elapsed := time.Since(start)
		wait := interval - elapsed
		if wait < 0 { wait = 0 }
		// 套用 Jitter 邏輯
		jit := jitter
		if jit <= 0 { jit = 0.1 }
		time.Sleep(time.Duration(float64(wait) * (1 + (rand.Float64()*2-1)*jit)))
	}
}

// ---------------------------------------------------------
// 網路層邏輯：獨立背景執行緒 (Goroutine Worker)
// ---------------------------------------------------------

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
// Bubbletea UI：頻率分離渲染
// ---------------------------------------------------------

func tick() tea.Cmd {
	return tea.Tick(UI_TICK_INTERVAL, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return initMsg{} }, tick())
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
		if itv == 0 { itv = 1 * time.Second }
		for i, d := range m.devices {
			if d.Name != "---" {
				d.Loading = true
				go resolveAndStartWorker(i, d.Name, d.IP, itv, m.cfg.Jitter, m.sub)
			}
		}
		// 啟動訊息監聽
		return m, listenForMsg(m.sub)

	case tickMsg:
		// 定時觸發渲染更新
		return m, tick()

	case dnsResMsg:
		d := m.devices[msg.idx]
		d.Name, d.IP, d.IsDNSFail = msg.name, msg.ip, msg.isFail
		return m, listenForMsg(m.sub)

	case pingStartMsg:
		m.devices[msg.idx].Loading = true
		return m, listenForMsg(m.sub)

	case pingResMsg:
		d := m.devices[msg.idx]
		d.Loading, d.IsNative = false, msg.isNative
		d.UpdateStats(msg.rtt, msg.success)
		// 這裡繼續監聽下一則訊息，但不顯式觸發渲染(渲染交給 tickMsg)
		return m, listenForMsg(m.sub)
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 || m.width == 0 {
		return " Loading..."
	}
	var s strings.Builder

	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("GO-DEADMAN PRECISION")) + "\n")
	subTitle := fmt.Sprintf("From: %s | Version: %s | Mode: High-Precision", m.hostname, VERSION)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, dimStyle.Render(subTitle)) + "\n")

	const fixedColsWidth = 84 // 增加寬度以容納更高的精度顯示
	maxHist := m.width - fixedColsWidth
	if maxHist > HIST_SIZE { maxHist = HIST_SIZE } else if maxHist < 5 { maxHist = 5 }

	// 表頭顯示調整：RTT(ms) 空間拉大
	header := fmt.Sprintf("\n  %-15s %-15s %5s %9s %9s %9s %6s  %-*s", 
		"HOSTNAME", "ADDRESS", "LOSS", "RTT(ms)", "AVG(ms)", "JIT(ms)", "SNT", maxHist, "STATUS")
	s.WriteString(headerStyle.Render(header) + "\n" + dimStyle.Render(strings.Repeat("─", m.width)) + "\n")

	for _, d := range m.devices {
		if d.Name == "---" {
			s.WriteString(dimStyle.Render("  " + strings.Repeat("-", m.width-4)) + "\n")
			continue
		}

		indicator := "  "
		if d.Loading { indicator = arrowStyle.Render("> ") }

		var hist strings.Builder
		showCount := d.HistCount
		if showCount > maxHist { showCount = maxHist }
		for j := 1; j <= showCount; j++ {
			h := d.History[(d.HistoryIdx-j+HIST_SIZE)%HIST_SIZE]
			if h.Success { hist.WriteString(upStyle.Render(h.Char)) } else { hist.WriteString(downStyle.Render(h.Char)) }
		}

		tag := " "
		if d.IsNative { tag = "*" }

		// [優化] 顯示精度改為 %.3f
		line := fmt.Sprintf("%-15s %-15s %4d%% %9.3f %9.3f %9.3f %5d%s  ", 
			truncate(d.Name, 15), truncate(d.IP, 15), d.LossRate, d.LastRTT, d.AvgRTT, d.Jitter, d.Snt, tag)

		s.WriteString(indicator)
		if d.IsDNSFail {
			s.WriteString(failRowStyle.Render(line) + "DNS FAIL\n")
		} else {
			s.WriteString(line + hist.String() + "\n")
		}
	}
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
