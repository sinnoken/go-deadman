package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

const VERSION = "v1.1.1"

// 用於解析 ping 輸出的時間 (支援 Windows/Linux/macOS)
var timeRegex = regexp.MustCompile(`(?:time|時間)[=<]([0-9.]+) ?ms`)

// --- 樣式定義 ---
var (
	RTT_SCALE = 10.0
	WHEEL     = []string{"|", "/", "-", "\\"}

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	upStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	downStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	arrowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	failRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

type Heartbeat struct {
	Char    string
	Success bool
}

type Device struct {
	Name     string `yaml:"name"`
	IP       string `yaml:"ip"`
	Loss     int
	Snt      int
	TotalRTT float64
	LastRTT  float64
	History  []Heartbeat
	Loading  bool
}

type Config struct {
	Scale   float64   `yaml:"scale"`
	Devices []*Device `yaml:"devices"`
}

type model struct {
	devices   []*Device
	step      int
	scanIndex int
	width     int
	height    int
	offset    int
	hostname  string
}

type spinMsg struct{}
type pingRes struct {
	idx     int
	rtt     float64
	success bool
}

func getResultChar(rtt float64, success bool) string {
	if !success { return "·" }
	if rtt < RTT_SCALE*1 { return "▁" }
	if rtt < RTT_SCALE*2 { return "▂" }
	if rtt < RTT_SCALE*3 { return "▃" }
	if rtt < RTT_SCALE*4 { return "▄" }
	if rtt < RTT_SCALE*5 { return "▅" }
	if rtt < RTT_SCALE*6 { return "▆" }
	if rtt < RTT_SCALE*7 { return "▇" }
	return "█"
}

// 修正後的精準 Ping 計算方式
func runPing(idx int, ip string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			// -n 1: 只發一個包, -w 1000: 等待 1000ms
			cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
		} else {
			// -c 1: 只發一個包, -W 1: 等待 1秒
			cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
		}

		out, err := cmd.CombinedOutput()
		if err != nil {
			return pingRes{idx: idx, rtt: 0, success: false}
		}

		// 解析輸出字串中的 time=XXms
		matches := timeRegex.FindStringSubmatch(string(out))
		var rtt float64
		if len(matches) > 1 {
			// 抓取的是系統 ping 指令測得的精準網路往返時間
			rtt, _ = strconv.ParseFloat(matches[1], 64)
		} else {
			// 預防萬一：解析不到則設為 0
			rtt = 0
		}

		return pingRes{idx: idx, rtt: rtt, success: true}
	}
}

func (m model) Init() tea.Cmd {
	return spinTick()
}

func spinTick() tea.Cmd {
	return tea.Tick(time.Millisecond*120, func(t time.Time) tea.Msg { return spinMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			for _, d := range m.devices {
				d.History = nil
				d.Snt, d.Loss, d.TotalRTT = 0, 0, 0
			}
		}

	case spinMsg:
		m.step++
		if len(m.devices) > 0 {
			m.scanIndex = m.step % len(m.devices)
			visibleLines := m.height - 8
			if visibleLines < 1 { visibleLines = 1 }
			if m.scanIndex >= m.offset+visibleLines { m.offset = m.scanIndex - visibleLines + 1 }
			if m.scanIndex < m.offset { m.offset = m.scanIndex }
		}

		target := m.devices[m.scanIndex]
		var pingCmd tea.Cmd
		if target.Name != "---" {
			target.Loading = true
			pingCmd = runPing(m.scanIndex, target.IP)
		}
		return m, tea.Batch(pingCmd, spinTick())

	case pingRes:
		d := m.devices[msg.idx]
		d.Loading = false
		d.Snt++
		char := getResultChar(msg.rtt, msg.success)
		if msg.success {
			d.LastRTT = msg.rtt
			d.TotalRTT += msg.rtt
		} else {
			d.Loss++
		}
		d.History = append([]Heartbeat{{Char: char, Success: msg.success}}, d.History...)
		if len(d.History) > 30 { d.History = d.History[:30] }
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 { return " Initializing..." }

	var s strings.Builder

	// 1. 標題與來源 (置中與靠右)
	wheelChar := WHEEL[m.step%len(WHEEL)]
	titleText := strings.ToLower(fmt.Sprintf("dead man %s", wheelChar))
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render(titleText)) + "\n")
	
	fromInfo := fmt.Sprintf("From: %s [%s]", m.hostname, VERSION)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Right, dimStyle.Render(fromInfo)) + "\n")

	// 2. 表頭 (橘色)
	headerLine := fmt.Sprintf("\n    %-15s %-15s %5s %5s %5s %5s  %-20s",
		"HOSTNAME", "ADDRESS", "LOSS", "RTT", "AVG", "SNT", "RESULT")
	s.WriteString(headerStyle.Render(headerLine) + "\n")
	s.WriteString(dimStyle.Render(" "+strings.Repeat("─", 85)) + "\n")

	// 3. 渲染區間
	headerHeight, footerHeight := 6, 2
	visibleHeight := m.height - headerHeight - footerHeight
	if visibleHeight < 0 { visibleHeight = 0 }
	endIndex := m.offset + visibleHeight
	if endIndex > len(m.devices) { endIndex = len(m.devices) }

	// 4. 設備渲染 (支援小數點顯示)
	for i := m.offset; i < endIndex; i++ {
		d := m.devices[i]
		indicator := "  "
		if i == m.scanIndex { indicator = arrowStyle.Render("> ") }

		if d.Name == "---" {
			s.WriteString(fmt.Sprintf("%s%s\n", indicator, dimStyle.Render(strings.Repeat("-", 80))))
			continue
		}

		lossRate := 0
		if d.Snt > 0 { lossRate = (d.Loss * 100) / d.Snt }
		avg := 0.0
		if d.Snt-d.Loss > 0 { avg = d.TotalRTT / float64(d.Snt-d.Loss) }

		var histStr strings.Builder
		for _, h := range d.History {
			if h.Success { histStr.WriteString(upStyle.Render(h.Char)) } else { histStr.WriteString(downStyle.Render(h.Char)) }
		}

		isDown := len(d.History) > 0 && !d.History[0].Success
		// 這裡將 %5.0f 改為 %5.1f 以顯示小數點
		rowText := fmt.Sprintf("%s %-15s %-15s %4d%% %5.1f %5.1f %5d  %s",
			indicator, d.Name, d.IP, lossRate, d.LastRTT, avg, d.Snt, histStr.String())

		if isDown {
			s.WriteString(failRowStyle.Render(rowText) + "\n")
		} else {
			s.WriteString(rowText + "\n")
		}
	}

	renderedLines := endIndex - m.offset
	if renderedLines < visibleHeight { s.WriteString(strings.Repeat("\n", visibleHeight-renderedLines)) }
	
	footer := fmt.Sprintf(" RTT Scale: %.0fms | Total: %d | q: quit", RTT_SCALE, len(m.devices))
	s.WriteString("\n " + dimStyle.Render(footer))

	return s.String()
}

func main() {
	var cfg Config
	_ = yaml.Unmarshal(embeddedYaml, &cfg)
	if cfg.Scale > 0 { RTT_SCALE = cfg.Scale }
	hostname, _ := os.Hostname()
	
	p := tea.NewProgram(model{
		devices:  cfg.Devices,
		hostname: hostname,
	}, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
	}
}
