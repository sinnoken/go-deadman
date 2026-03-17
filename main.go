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
	VERSION        = "v1.3.0"
	HIST_SIZE      = 30  // 固定歷史紀錄長度
	PING_INTERVAL  = 2   // 每 2 秒全體 Ping 一次
	TICK_RATE_MS   = 120 // 動畫更新頻率 (毫秒)
	TICKS_PER_PING = (PING_INTERVAL * 1000) / TICK_RATE_MS // 預先算好觸發 Ping 的 Tick 數
)

// --- 樣式定義 ---
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
	Name     string `yaml:"name"`
	IP       string `yaml:"ip"`
	Loss     int
	Snt      int
	TotalRTT float64
	LastRTT  float64
	AvgRTT   float64 // 預運算
	LossRate int     // 預運算

	// [終極優化 1] 真正的 Ring Buffer：零記憶體分配 (Zero Allocation)
	History    [HIST_SIZE]Heartbeat // 固定長度陣列
	HistoryIdx int                  // 下一次寫入的索引
	HistCount  int                  // 目前已存的紀錄數量 (上限 HIST_SIZE)
	
	Loading  bool
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
}

type tickMsg time.Time
type pingRes struct {
	idx     int
	rtt     float64
	success bool
}

// 捨棄正則表達式，改用字串處理
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

// 單一 Ping 任務
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

// 併發發送所有設備的 Ping 指令
func pingAll(devices []*Device) tea.Cmd {
	var cmds []tea.Cmd
	for i, d := range devices {
		if d.Name != "---" {
			d.Loading = true
			cmds = append(cmds, runPingTask(i, d.IP))
		}
	}
	return tea.Batch(cmds...)
}

func (m model) Init() tea.Cmd {
	return tea.Batch(pingAll(m.devices), animationTick())
}

func animationTick() tea.Cmd {
	return tea.Tick(time.Millisecond*time.Duration(TICK_RATE_MS), func(t time.Time) tea.Msg { return tickMsg(t) })
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
				d.HistoryIdx, d.HistCount = 0, 0 // 重置 Ring Buffer 狀態
			}
		}

	case tickMsg:
		m.step++
		// 使用預先算好的常數，避免每次 Tick 做運算
		if m.step % TICKS_PER_PING == 0 { 
			return m, tea.Batch(pingAll(m.devices), animationTick())
		}
		return m, animationTick()

	case pingRes:
		d := m.devices[msg.idx]
		d.Loading = false
		d.Snt++
		if msg.success {
			d.LastRTT = msg.rtt
			d.TotalRTT += msg.rtt
			d.AvgRTT = d.TotalRTT / float64(d.Snt - d.Loss)
		} else {
			d.Loss++
		}
		d.LossRate = (d.Loss * 100) / d.Snt

		// [終極優化 1] 寫入 Ring Buffer，無任何記憶體分配
		char := getResultChar(msg.rtt, msg.success)
		d.History[d.HistoryIdx] = Heartbeat{Char: char, Success: msg.success}
		d.HistoryIdx = (d.HistoryIdx + 1) % HIST_SIZE
		if d.HistCount < HIST_SIZE {
			d.HistCount++
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

		var histStr strings.Builder
		
		// [終極優化 1] 反向讀取 Ring Buffer (從最新的一筆開始往回讀)
		for j := 1; j <= d.HistCount; j++ {
			idx := (d.HistoryIdx - j + HIST_SIZE) % HIST_SIZE
			h := d.History[idx]
			if h.Success {
				histStr.WriteString(upStyle.Render(h.Char))
			} else {
				histStr.WriteString(downStyle.Render(h.Char))
			}
		}

		// 判斷最新的一筆是否失敗
		isDown := false
		if d.HistCount > 0 {
			lastIdx := (d.HistoryIdx - 1 + HIST_SIZE) % HIST_SIZE
			isDown = !d.History[lastIdx].Success
		}

		// [終極優化 2] 渲染分離：避免 lipgloss 重複解析顏色代碼
		infoText := fmt.Sprintf("   %-15s %-15s %4d%% %5.1f %5.1f %5d  ",
			d.Name, d.IP, d.LossRate, d.LastRTT, d.AvgRTT, d.Snt)

		if isDown {
			s.WriteString(failRowStyle.Render(infoText))
		} else {
			s.WriteString(infoText)
		}
		
		// 歷史紀錄已經上好色了，直接接在後面，不包進 failRowStyle 裡
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
	
	p := tea.NewProgram(model{
		devices:  cfg.Devices,
		hostname: hostname,
	}, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
	}
}
