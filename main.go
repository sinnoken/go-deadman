package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	probing "github.com/prometheus-community/pro-bing" // [新增]
	"gopkg.in/yaml.v3"
)

//go:embed config.yaml
var embeddedYaml []byte

const (
	VERSION              = "v1.6.0" // 更新版本號
	HIST_SIZE            = 30
	PING_INTERVAL        = 2
	TICK_RATE_MS         = 120
	TICKS_PER_PING       = (PING_INTERVAL * 1000) / TICK_RATE_MS
	MAX_CONCURRENT_PINGS = 50
)

var (
	RTT_SCALE    = 10.0
	WHEEL        = []string{"|", "/", "-", "\\"}
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	headerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	upStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	downStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	arrowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	failRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

type Heartbeat struct {
	Char    string
	Success bool
}

type Device struct {
	Name       string `yaml:"name"`
	IP         string `yaml:"ip"`
	Loss       int
	Snt        int
	TotalRTT   float64
	LastRTT    float64
	AvgRTT     float64
	LossRate   int
	History    [HIST_SIZE]Heartbeat
	HistoryIdx int
	HistCount  int
	Loading    bool
	IsNative   bool // [新增] 用來標記目前是否使用 Native 模式
}

type Config struct {
	Scale   float64   `yaml:"scale"`
	Devices []*Device `yaml:"devices"`
}

type model struct {
	devices   []*Device
	step      int
	width     int
	height    int
	offset    int
	hostname  string
	pingQueue []int
}

type startPingMsg struct{}
type tickMsg time.Time
type pingRes struct {
	idx      int
	rtt      float64
	success  bool
	isNative bool // [新增]
}

// ---------------------------------------------------------
// 核心 Ping 邏輯：優先 Pro-bing，失敗則回歸 OS Ping
// ---------------------------------------------------------

func runPingTask(idx int, ip string) tea.Cmd {
	return func() tea.Msg {
		// 1. 嘗試使用 pro-bing (Native ICMP)
		pinger, err := probing.NewPinger(ip)
		if err == nil {
			pinger.Count = 1
			pinger.Timeout = time.Second
			// Windows 下若非管理員，pro-bing 會自動嘗試用 UDP 模擬
			pinger.SetPrivileged(false) 
			
			err = pinger.Run()
			if err == nil {
				stats := pinger.Statistics()
				if stats.PacketsRecv > 0 {
					rttMs := float64(stats.MaxRtt.Microseconds()) / 1000.0
					return pingRes{idx: idx, rtt: rttMs, success: true, isNative: true}
				}
			}
		}

		// 2. 降級：使用系統原本的 Ping 指令
		return runOSPing(idx, ip)
	}
}

func runOSPing(idx int, ip string) pingRes {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
	} else {
		cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return pingRes{idx: idx, rtt: 0, success: false, isNative: false}
	}
	return pingRes{idx: idx, rtt: parsePingTime(string(out)), success: true, isNative: false}
}

func parsePingTime(out string) float64 {
	key := "time="
	if runtime.GOOS == "windows" {
		key = "時間="
		if !strings.Contains(out, key) { key = "time=" }
	}
	start := strings.Index(out, key)
	if start == -1 { return 0 }
	sub := out[start+len(key):]
	end := strings.Index(sub, "ms")
	if end == -1 { return 0 }
	res, _ := strconv.ParseFloat(strings.TrimSpace(sub[:end]), 64)
	return res
}

// ---------------------------------------------------------
// Bubbletea Model 邏輯 (Update/View 保持相容並微調)
// ---------------------------------------------------------

func (m model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return startPingMsg{} }, animationTick())
}

func animationTick() tea.Cmd {
	return tea.Tick(time.Millisecond*TICK_RATE_MS, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c": return m, tea.Quit
		case "r":
			for _, d := range m.devices {
				d.Snt, d.Loss, d.TotalRTT, d.LastRTT, d.AvgRTT, d.LossRate = 0, 0, 0, 0, 0, 0
				d.HistoryIdx, d.HistCount = 0, 0
			}
		}
	case startPingMsg:
		m.pingQueue = make([]int, 0, len(m.devices))
		var cmds []tea.Cmd
		for i, d := range m.devices {
			if d.Name != "---" {
				d.Loading = true
				m.pingQueue = append(m.pingQueue, i)
			}
		}
		batchSize := MAX_CONCURRENT_PINGS
		if len(m.pingQueue) < batchSize { batchSize = len(m.pingQueue) }
		for i := 0; i < batchSize; i++ {
			idx := m.pingQueue[i]
			cmds = append(cmds, runPingTask(idx, m.devices[idx].IP))
		}
		m.pingQueue = m.pingQueue[batchSize:]
		return m, tea.Batch(cmds...)

	case tickMsg:
		m.step++
		if m.step%TICKS_PER_PING == 0 {
			return m, tea.Batch(func() tea.Msg { return startPingMsg{} }, animationTick())
		}
		return m, animationTick()

	case pingRes:
		d := m.devices[msg.idx]
		d.Loading = false
		d.IsNative = msg.isNative // 更新測量模式狀態
		d.Snt++
		if msg.success && msg.rtt > 0 {
			d.LastRTT = msg.rtt
			d.TotalRTT += msg.rtt
			d.AvgRTT = d.TotalRTT / float64(d.Snt-d.Loss)
		} else {
			d.Loss++
		}
		d.LossRate = (d.Loss * 100) / d.Snt
		d.History[d.HistoryIdx] = Heartbeat{Char: getResultChar(msg.rtt, msg.success), Success: msg.success}
		d.HistoryIdx = (d.HistoryIdx + 1) % HIST_SIZE
		if d.HistCount < HIST_SIZE { d.HistCount++ }

		var nextCmd tea.Cmd
		if len(m.pingQueue) > 0 {
			nextIdx := m.pingQueue[0]
			m.pingQueue = m.pingQueue[1:]
			nextCmd = runPingTask(nextIdx, m.devices[nextIdx].IP)
		}
		return m, nextCmd
	}
	return m, nil
}

func getResultChar(rtt float64, success bool) string {
	if !success || rtt == 0 { return "·" }
	scales := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇"}
	for i, char := range scales {
		if rtt < RTT_SCALE*float64(i+1) { return char }
	}
	return "█"
}

func (m model) View() string {
	if m.height == 0 { return " Initializing..." }
	var s strings.Builder
	wheelChar := WHEEL[m.step%len(WHEEL)]
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render(fmt.Sprintf("dead man %s", wheelChar))) + "\n")
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Right, dimStyle.Render(fmt.Sprintf("From: %s [%s]", m.hostname, VERSION))) + "\n")
	headerLine := fmt.Sprintf("\n    %-15s %-15s %5s %5s %5s %5s  %-20s", "HOSTNAME", "ADDRESS", "LOSS", "RTT", "AVG", "SNT", "RESULT")
	s.WriteString(headerStyle.Render(headerLine) + "\n")
	s.WriteString(dimStyle.Render(" "+strings.Repeat("─", 85)) + "\n")

	visibleHeight := m.height - 8
	if visibleHeight < 1 { visibleHeight = 1 }
	endIndex := m.offset + visibleHeight
	if endIndex > len(m.devices) { endIndex = len(m.devices) }

	for i := m.offset; i < endIndex; i++ {
		d := m.devices[i]
		if d.Name == "---" {
			s.WriteString("  " + dimStyle.Render(strings.Repeat("-", 80)) + "\n")
			continue
		}
		indicator := "  "
		if d.Loading { indicator = arrowStyle.Render("> ") }

		var histStr strings.Builder
		for j := 1; j <= d.HistCount; j++ {
			idx := (d.HistoryIdx - j + HIST_SIZE) % HIST_SIZE
			h := d.History[idx]
			if h.Success { histStr.WriteString(upStyle.Render(h.Char)) } else { histStr.WriteString(downStyle.Render(h.Char)) }
		}

		isDown := d.HistCount > 0 && !d.History[(d.HistoryIdx-1+HIST_SIZE)%HIST_SIZE].Success
		modeTag := " "
		if d.IsNative { modeTag = "*" } // 用星號標記原生模式，增加區別度
		
		infoText := fmt.Sprintf("%-15s %-15s %4d%% %5.1f %5.1f %5d%s ",
			d.Name, d.IP, d.LossRate, d.LastRTT, d.AvgRTT, d.Snt, modeTag)

		s.WriteString(indicator)
		if isDown { s.WriteString(failRowStyle.Render(infoText)) } else { s.WriteString(infoText) }
		s.WriteString(histStr.String() + "\n")
	}

	footer := fmt.Sprintf(" RTT Scale: %.0fms | Total: %d | *: Native Mode | q: quit", RTT_SCALE, len(m.devices))
	s.WriteString("\n " + dimStyle.Render(footer))
	return s.String()
}

func main() {
	var cfg Config
	_ = yaml.Unmarshal(embeddedYaml, &cfg)
	if cfg.Scale > 0 { RTT_SCALE = cfg.Scale }
	hostname, _ := os.Hostname()
	p := tea.NewProgram(model{devices: cfg.Devices, hostname: hostname}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil { fmt.Printf("Error: %v", err) }
}
