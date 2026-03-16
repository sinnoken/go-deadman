package main

import (
	_ "embed"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

//go:embed tasks.yaml
var embeddedYaml []byte

// --- 常量與樣式 ---
var (
	RTT_SCALE = 10.0
	WHEEL     = []string{"|", "/", "-", "\\"}

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	upStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // 綠色
	downStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // 紅色
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	// 掃描箭頭樣式 (青色)
	arrowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
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
}

// 移除 tickMsg，現在全部由 spinMsg 驅動
type spinMsg struct{}
type pingRes struct {
	idx     int
	rtt     float64
	success bool
}

func getResultChar(rtt float64, success bool) string {
	if !success {
		return "X"
	}
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
	// 啟動時不再發起大批次 Ping，只啟動掃描計時器
	return spinTick()
}

func spinTick() tea.Cmd {
	// 100ms ~ 150ms 左右最為滑順
	return tea.Tick(time.Millisecond*120, func(t time.Time) tea.Msg { return spinMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if msg.String() == "r" {
			for _, d := range m.devices {
				d.History = nil
				d.Snt, d.Loss, d.TotalRTT = 0, 0, 0
			}
		}

	case spinMsg:
		m.step++
		// 1. 更新掃描指標
		if len(m.devices) > 0 {
			m.scanIndex = m.step % len(m.devices)
		}

		// 2. 【核心改動】箭頭指到誰，就執行誰的 Ping
		target := m.devices[m.scanIndex]
		var pingCmd tea.Cmd
		if target.Name != "---" {
			target.Loading = true
			pingCmd = runPing(m.scanIndex, target.IP)
		}

		// 3. 同步觸發下一次動畫與這一次的 Ping 任務
		return m, tea.Batch(pingCmd, spinTick())

	case pingRes:
		// 接收非同步回傳
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

		// 歷史紀錄
		d.History = append([]Heartbeat{{Char: char, Success: msg.success}}, d.History...)
		if len(d.History) > 30 {
			d.History = d.History[:30]
		}
	}
	return m, nil
}

func (m model) View() string {
	var s strings.Builder

	// 1. 標題與旋轉木馬
	wheelChar := WHEEL[m.step%len(WHEEL)]
	s.WriteString(fmt.Sprintf("\n %s %s  %s\n", titleStyle.Render("Dead Man"), wheelChar, dimStyle.Render("RTT Scale: "+strconv.Itoa(int(RTT_SCALE))+"ms")))

	// 2. 表頭
	s.WriteString(fmt.Sprintf("\n    %-15s %-15s %5s %5s %5s %5s  %-20s\n",
		"HOSTNAME", "ADDRESS", "LOSS", "RTT", "AVG", "SNT", "RESULT"))
	s.WriteString(dimStyle.Render(" " + strings.Repeat("─", 85)) + "\n")

	// 3. 設備目錄
	for i, d := range m.devices {
		indicator := "  "
		if i == m.scanIndex {
			indicator = arrowStyle.Render("> ")
		}

		if d.Name == "---" {
			s.WriteString(fmt.Sprintf("%s%s\n", indicator, dimStyle.Render(strings.Repeat("-", 80))))
			continue
		}

		lossRate := 0
		if d.Snt > 0 {
			lossRate = (d.Loss * 100) / d.Snt
		}
		avg := 0.0
		if d.Snt-d.Loss > 0 {
			avg = d.TotalRTT / float64(d.Snt-d.Loss)
		}

		var histStr strings.Builder
		for _, h := range d.History {
			if h.Success {
				histStr.WriteString(upStyle.Render(h.Char))
			} else {
				histStr.WriteString(downStyle.Render(h.Char))
			}
		}

		rowStyle := lipgloss.NewStyle()
		if len(d.History) > 0 && !d.History[0].Success {
			rowStyle = rowStyle.Bold(true).Foreground(lipgloss.Color("15"))
		}

		// 移除 Loading 🛰️ 的渲染邏輯或改為只有在當前掃描時顯示，讓畫面乾淨一點
		line := fmt.Sprintf("%s %-15s %-15s %4d%% %5.0f %5.0f %5d  %s",
			indicator, d.Name, d.IP, lossRate, d.LastRTT, avg, d.Snt, histStr.String())

		s.WriteString(rowStyle.Render(line) + "\n")
	}

	s.WriteString("\n " + dimStyle.Render("Keys: (q)uit, (r)efresh stats | Scan-on-Pulse Mode"))
	return s.String()
}

func main() {
	var cfg Config
	err := yaml.Unmarshal(embeddedYaml, &cfg)
	if err != nil {
		fmt.Printf("YAML Error: %v", err)
		return
	}
	if cfg.Scale > 0 {
		RTT_SCALE = cfg.Scale
	}

	// 建議開啟 WithAltScreen 讓 TUI 體驗更完整
	p := tea.NewProgram(model{devices: cfg.Devices}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
	}
}
