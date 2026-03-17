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
	"gopkg.in/yaml.v3"
)

//go:embed config.yaml
var embeddedYaml []byte

const (
	VERSION              = "v1.5.0"
	HIST_SIZE            = 30
	PING_INTERVAL        = 2
	TICK_RATE_MS         = 120
	TICKS_PER_PING       = (PING_INTERVAL * 1000) / TICK_RATE_MS
	MAX_CONCURRENT_PINGS = 50 // [新增] 一次最多允許 50 個 ping 執行緒同時運作
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
	pingQueue []int // [新增] 存放正在排隊等待發送 Ping 的設備 Index
}

type startPingMsg struct{} // [新增] 觸發一輪全新 Ping 任務的訊號
type tickMsg time.Time
type pingRes struct {
	idx     int
	rtt     float64
	success bool
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

func runPingTask(idx int, ip string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
		} else {
			cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return pingRes{idx: idx, rtt: 0, success: false}
		}
		return pingRes{idx: idx, rtt: parsePingTime(string(out)), success: true}
	}
}

func (m model) Init() tea.Cmd {
	// 程式啟動時，同時啟動時鐘動畫，並發送第一次的全體 Ping 訊號
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
		// [新增邏輯] 開始新的一輪：把所有設備放進排隊佇列
		m.pingQueue = make([]int, 0, len(m.devices))
		var cmds []tea.Cmd

		for i, d := range m.devices {
			if d.Name != "---" {
				d.Loading = true // 點亮所有的 > 指標
				m.pingQueue = append(m.pingQueue, i)
			}
		}

		// 決定第一批要發送的數量 (最多 MAX_CONCURRENT_PINGS)
		batchSize := MAX_CONCURRENT_PINGS
		if len(m.pingQueue) < batchSize {
			batchSize = len(m.pingQueue)
		}

		// 發出第一批
		for i := 0; i < batchSize; i++ {
			idx := m.pingQueue[i]
			cmds = append(cmds, runPingTask(idx, m.devices[idx].IP))
		}

		// 將已經發出的任務從 Queue 中移除
		m.pingQueue = m.pingQueue[batchSize:]
		return m, tea.Batch(cmds...)

	case tickMsg:
		m.step++
		if m.step%TICKS_PER_PING == 0 {
			// 時間到，發送 startPingMsg 來啟動新的一輪
			return m, tea.Batch(func() tea.Msg { return startPingMsg{} }, animationTick())
		}
		return m, animationTick()

	case pingRes:
		// 1. 處理回傳的數據 (與原本相同)
		d := m.devices[msg.idx]
		d.Loading = false // 收到回應，熄滅 > 指標
		d.Snt++
		if msg.success {
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

		// 2. [新增邏輯] 滑動視窗：完成了一個任務，看看 Queue 裡還有沒有排隊的？
		var nextCmd tea.Cmd
		if len(m.pingQueue) > 0 {
			nextIdx := m.pingQueue[0]
			m.pingQueue = m.pingQueue[1:] // 隊列往前推進
			nextCmd = runPingTask(nextIdx, m.devices[nextIdx].IP)
		}

		// 如果有抽到新任務，就把它加到 Bubbletea 的事件迴圈裡
		if nextCmd != nil {
			return m, nextCmd
		}
	}
	return m, nil
}

func getResultChar(rtt float64, success bool) string {
	if !success { return "·" }
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

	headerHeight, footerHeight := 6, 2
	visibleHeight := m.height - headerHeight - footerHeight
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
		if d.Loading {
			indicator = arrowStyle.Render("> ")
		}

		var histStr strings.Builder
		for j := 1; j <= d.HistCount; j++ {
			idx := (d.HistoryIdx - j + HIST_SIZE) % HIST_SIZE
			h := d.History[idx]
			if h.Success { histStr.WriteString(upStyle.Render(h.Char)) } else { histStr.WriteString(downStyle.Render(h.Char)) }
		}

		isDown := d.HistCount > 0 && !d.History[(d.HistoryIdx-1+HIST_SIZE)%HIST_SIZE].Success
		infoText := fmt.Sprintf(" %-15s %-15s %4d%% %5.1f %5.1f %5d  ",
			d.Name, d.IP, d.LossRate, d.LastRTT, d.AvgRTT, d.Snt)

		s.WriteString(indicator)
		if isDown {
			s.WriteString(failRowStyle.Render(infoText))
		} else {
			s.WriteString(infoText)
		}
		s.WriteString(histStr.String() + "\n")
	}

	if rendered := endIndex - m.offset; rendered < visibleHeight {
		s.WriteString(strings.Repeat("\n", visibleHeight-rendered))
	}
	footer := fmt.Sprintf(" RTT Scale: %.0fms | Total: %d | Interval: %ds | q: quit", RTT_SCALE, len(m.devices), PING_INTERVAL)
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
