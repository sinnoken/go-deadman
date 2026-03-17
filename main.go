package main

import (
	_ "embed"
	"fmt"
	"math"
	"math/rand"
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
	VERSION        = "v1.7.1" // 升級一下小版本號慶祝 Bug 修復
	HIST_SIZE      = 30       // 歷史紀錄長度
	WINDOW_SIZE    = 50       // Moving Average 的樣本數
	MAX_CONCURRENT = 50       // 最大併發 Ping 數量
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
	Window     []float64 // 滑動窗口樣本
	LossRate   int
	History    [HIST_SIZE]Heartbeat // Ring Buffer
	HistoryIdx int
	HistCount  int
	Loading    bool
	IsNative   bool // 是否使用 Pro-bing 成功發包
}

type Config struct {
	Interval string    `yaml:"interval"`
	Jitter   float64   `yaml:"jitter"`
	Devices  []*Device `yaml:"devices"`
}

type model struct {
	cfg       Config
	devices   []*Device
	step      int
	width     int
	height    int
	hostname  string
	pingQueue []int
}

// Bubbletea 訊息類型
type startPingMsg struct{}
type pingRes struct {
	idx      int
	rtt      float64
	success  bool
	isNative bool
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
// 網路層邏輯：Ping 發送與字串解析
// ---------------------------------------------------------

func runPingTask(idx int, ip string) tea.Cmd {
	return func() tea.Msg {
		// 1. 嘗試使用原生 Pro-bing
		pinger, err := probing.NewPinger(ip)
		if err == nil {
			pinger.Count = 1
			pinger.Timeout = time.Second

			// 動態處理 OS 權限差異
			if runtime.GOOS == "windows" {
				pinger.SetPrivileged(true) // Windows 需 Admin 權限與 Raw Socket
			} else {
				pinger.SetPrivileged(false) // Unix-like 預設嘗試 UDP Ping
			}

			if err = pinger.Run(); err == nil {
				stats := pinger.Statistics()
				if stats.PacketsRecv > 0 {
					return pingRes{
						idx:      idx,
						rtt:      float64(stats.MaxRtt.Microseconds()) / 1000.0,
						success:  true,
						isNative: true,
					}
				}
			}
		}

		// 2. Fallback：使用系統指令
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
		} else {
			cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
		}
		out, _ := cmd.CombinedOutput()

		rtt, isSuccess := parseRTT(string(out))
		return pingRes{idx: idx, rtt: rtt, success: isSuccess, isNative: false}
	}
}

// 強健的字串解析器，支援多語系與極速 (<1ms) 情況
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

func (m model) Init() tea.Cmd {
	return func() tea.Msg { return startPingMsg{} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
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
		
		batch := MAX_CONCURRENT
		if len(m.pingQueue) < batch {
			batch = len(m.pingQueue)
		}
		for i := 0; i < batch; i++ {
			cmds = append(cmds, runPingTask(m.pingQueue[i], m.devices[m.pingQueue[i]].IP))
		}
		m.pingQueue = m.pingQueue[batch:]

		itv, _ := time.ParseDuration(m.cfg.Interval)
		if itv == 0 {
			itv = 2 * time.Second
		}
		jit := m.cfg.Jitter
		if jit == 0 {
			jit = 0.1
		}
		next := time.Duration(float64(itv) * (1 + (rand.Float64()*2 - 1)*jit))

		return m, tea.Batch(tea.Batch(cmds...), tea.Tick(next, func(t time.Time) tea.Msg { return startPingMsg{} }))

	case pingRes:
		d := m.devices[msg.idx]
		d.Loading, d.IsNative = false, msg.isNative
		d.UpdateStats(msg.rtt, msg.success)

		var next tea.Cmd
		if len(m.pingQueue) > 0 {
			idx := m.pingQueue[0]
			m.pingQueue = m.pingQueue[1:]
			next = runPingTask(idx, m.devices[idx].IP)
		}
		return m, next
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 {
		return " Loading..."
	}
	var s strings.Builder
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("NETWORK MONITOR [LOG-AVG MODE]")) + "\n")

	header := fmt.Sprintf("\n    %-15s %-15s %5s %5s %5s %5s  %-20s", "HOSTNAME", "ADDRESS", "LOSS", "RTT", "AVG(50)", "SNT", "LOG-STATUS")
	s.WriteString(headerStyle.Render(header) + "\n" + dimStyle.Render(strings.Repeat("─", 85)) + "\n")

	for _, d := range m.devices {
		if d.Name == "---" {
			s.WriteString(dimStyle.Render("  " + strings.Repeat("-", 80)) + "\n")
			continue
		}
		indicator := "  "
		if d.Loading {
			indicator = arrowStyle.Render("> ")
		}

		var hist strings.Builder
		for j := 1; j <= d.HistCount; j++ {
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
		line := fmt.Sprintf("%-15s %-15s %4d%% %5.1f %5.1f %5d%s ", d.Name, d.IP, d.LossRate, d.LastRTT, d.AvgRTT, d.Snt, tag)

		s.WriteString(indicator)
		if d.HistCount > 0 && !d.History[(d.HistoryIdx-1+HIST_SIZE)%HIST_SIZE].Success {
			s.WriteString(failRowStyle.Render(line))
		} else {
			s.WriteString(line)
		}
		s.WriteString(hist.String() + "\n")
	}

	footer := fmt.Sprintf("\n Interval: %s | Jitter: %.f%% | *: Native | Window: %d", m.cfg.Interval, m.cfg.Jitter*100, WINDOW_SIZE)
	s.WriteString(dimStyle.Render(footer))
	return s.String()
}

// ---------------------------------------------------------
// 程式進入點
// ---------------------------------------------------------

func main() {
	var cfg Config
	_ = yaml.Unmarshal(embeddedYaml, &cfg)

	if cfg.Interval == "" {
		cfg.Interval = "1s"
	}
	if cfg.Jitter <= 0 {
		cfg.Jitter = 0.1
	}

	hostname, _ := os.Hostname()
	p := tea.NewProgram(model{
		cfg:      cfg,
		devices:  cfg.Devices,
		hostname: hostname,
	}, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
	}
}
