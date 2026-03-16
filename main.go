package main

import (
	_ "embed"     // 即使沒直接調用，加了 //go:embed 也要保留
	"fmt"
	"os/exec"     // 用於執行 ping
	"runtime"      // 用於判斷 OS (Windows/Linux)
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"
)

//go:embed tasks.yaml
var embeddedYaml []byte

// --- 模擬 Python 的常量 ---
var (
	RTT_SCALE = 10.0
	WHEEL     = []string{"|", "/", "-", "\\"}
	
	// 樣式
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	upStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // 綠色
	downStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // 紅色
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
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
	devices []*Device
	step    int
	width   int
}

// --- 訊息定義 ---
type tickMsg time.Time
type spinMsg struct{}
type pingRes struct {
	idx     int
	rtt     float64
	success bool
}

// --- 核心：復刻 Python 的 get_result_char ---
func getResultChar(rtt float64, success bool) string {
	if !success { return "X" }
	if rtt < RTT_SCALE*1 { return "▁" }
	if rtt < RTT_SCALE*2 { return "▂" }
	if rtt < RTT_SCALE*3 { return "▃" }
	if rtt < RTT_SCALE*4 { return "▄" }
	if rtt < RTT_SCALE*5 { return "▅" }
	if rtt < RTT_SCALE*6 { return "▆" }
	if rtt < RTT_SCALE*7 { return "▇" }
	return "█"
}

// --- Ping 執行器 ---
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
	return tea.Batch(m.pingAll(), spinTick())
}

func (m model) pingAll() tea.Cmd {
	var cmds []tea.Cmd
	for i, d := range m.devices {
		if d.Name == "---" { continue }
		d.Loading = true
		cmds = append(cmds, runPing(i, d.IP))
	}
	cmds = append(cmds, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }))
	return tea.Batch(cmds...)
}

func spinTick() tea.Cmd {
	return tea.Tick(time.Millisecond*200, func(t time.Time) tea.Msg { return spinMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" { return m, tea.Quit }
		if msg.String() == "r" { // 復刻 Python 的 refresh
			for _, d := range m.devices {
				d.History = nil
				d.Snt, d.Loss, d.TotalRTT = 0, 0, 0
			}
		}

	case spinMsg:
		m.step++
		return m, spinTick()

	case tickMsg:
		return m, m.pingAll()

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
		if len(d.History) > 20 { d.History = d.History[:20] }
	}
	return m, nil
}

func (m model) View() string {
	var s strings.Builder
	
	// 1. 標題與旋轉木馬 (Wheel)
	wheelChar := WHEEL[m.step%len(WHEEL)]
	s.WriteString(fmt.Sprintf("\n %s %s  %s\n", titleStyle.Render("Dead Man"), wheelChar, dimStyle.Render("RTT Scale: "+strconv.Itoa(int(RTT_SCALE))+"ms")))
	
	// 2. 表頭
	s.WriteString(fmt.Sprintf("\n %-15s %-15s %5s %5s %5s %5s  %-20s\n", 
		"HOSTNAME", "ADDRESS", "LOSS", "RTT", "AVG", "SNT", "RESULT"))
	s.WriteString(dimStyle.Render(strings.Repeat("─", 80)) + "\n")

	// 3. 目錄
	for _, d := range m.devices {
		if d.Name == "---" {
			s.WriteString(dimStyle.Render(" " + strings.Repeat("-", 75)) + "\n")
			continue
		}

		lossRate := 0
		if d.Snt > 0 { lossRate = (d.Loss * 100) / d.Snt }
		avg := 0.0
		if d.Snt-d.Loss > 0 { avg = d.TotalRTT / float64(d.Snt-d.Loss) }

		// 歷史紀錄字串
		var histStr strings.Builder
		for _, h := range d.History {
			if h.Success {
				histStr.WriteString(upStyle.Render(h.Char))
			} else {
				histStr.WriteString(downStyle.Render(h.Char))
			}
		}

		// 判斷當前整行顏色 (Python 邏輯: 斷線會變粗體/亮色)
		rowStyle := lipgloss.NewStyle()
		if d.Snt > 0 && !d.History[0].Success {
			rowStyle = rowStyle.Bold(true).Foreground(lipgloss.Color("15"))
		}

		line := fmt.Sprintf(" %-15s %-15s %4d%% %5.0f %5.0f %5d  %s",
			d.Name, d.IP, lossRate, d.LastRTT, avg, d.Snt, histStr.String())
		
		if d.Loading { line += " 🛰️" }
		s.WriteString(rowStyle.Render(line) + "\n")
	}

	s.WriteString("\n " + dimStyle.Render("Keys: (q)uit, (r)efresh stats"))
	return s.String()
}

func main() {
	var cfg Config
	yaml.Unmarshal(embeddedYaml, &cfg)
	if cfg.Scale > 0 { RTT_SCALE = cfg.Scale }

	p := tea.NewProgram(model{devices: cfg.Devices}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
	}
}
