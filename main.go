package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

//go:embed tasks.yaml
var embeddedYaml []byte

const VERSION = "v1.1.0"

// --- 樣式定義 ---
var (
	RTT_SCALE = 10.0
	WHEEL     = []string{"|", "/", "-", "\\"}

	// 標題：小寫 + 置中 (在 View 中處理對齊)
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Lower(true)
	
	// 表頭：橘色
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	
	// 基礎樣式
	upStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // 綠色紀錄
	downStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // 紅色紀錄
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	arrowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	
	// 整行失敗樣式：紅色
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

func runPing(idx int, ip string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
		} else {
			cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
		}
		err := cmd.Run()
		rtt := float64(time.Since(start).Milliseconds())
		return pingRes{idx: idx, rtt: rtt, success: err == nil}
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
			// 自動滾動邏輯 (預留標題與表頭高度約 8 行)
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

	// 1. 第一行：小寫 dead man 置中
	wheelChar := WHEEL[m.step%len(WHEEL)]
	titleText := fmt.Sprintf("go-deadman %s", wheelChar)
	centeredTitle := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render(titleText))
	s.WriteString(centeredTitle + "\n")

	// 2. 第二行：From 資訊 靠右
	fromInfo := fmt.Sprintf("From: %s [%s]", m.hostname, VERSION)
	rightInfo := lipgloss.PlaceHorizontal(m.width, lipgloss.Right, dimStyle.Render(fromInfo))
	s.WriteString(rightInfo + "\n")

	// 3. 表頭區 (橘色)
	headerLine := fmt.Sprintf("\n    %-15s %-15s %5s %5s %5s %5s  %-20s",
		"HOSTNAME", "ADDRESS", "LOSS", "RTT", "AVG", "SNT", "RESULT")
	s.WriteString(headerStyle.Render(headerLine) + "\n")
	s.WriteString(dimStyle.Render(" "+strings.Repeat("─", 85)) + "\n")

	// 4. 計算渲染區間
	headerHeight := 6
	footerHeight := 2
	visibleHeight := m.height - headerHeight - footerHeight
	if visibleHeight < 0 { visibleHeight = 0 }
	endIndex := m.offset + visibleHeight
	if endIndex > len(m.devices) { endIndex = len(m.devices) }

	// 5. 渲染設備行
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

		// 判斷是否失敗 (整行紅色)
		isDown := len(d.History) > 0 && !d.History[0].Success
		rowText := fmt.Sprintf("%s %-15s %-15s %4d%% %5.0f %5.0f %5d  %s",
			indicator, d.Name, d.IP, lossRate, d.LastRTT, avg, d.Snt, histStr.String())

		if isDown {
			s.WriteString(failRowStyle.Render(rowText) + "\n")
		} else {
			s.WriteString(rowText + "\n")
		}
	}

	// 6. 補白與頁尾
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
