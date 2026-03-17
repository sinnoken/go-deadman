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

const (
	VERSION        = "v1.7.0"
	HIST_SIZE      = 30
	WINDOW_SIZE    = 50  // Moving Average 的樣本數
	TICK_RATE_MS   = 120
	MAX_CONCURRENT = 50
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
	History    [HIST_SIZE]Heartbeat
	HistoryIdx int
	HistCount  int
	Loading    bool
	IsNative   bool
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

type startPingMsg struct{}
type pingRes struct {
	idx      int
	rtt      float64
	success  bool
	isNative bool
}

// ---------------------------------------------------------
// 核心演算法：Moving Average + Log Scaling
// ---------------------------------------------------------

func getLogChar(rtt, avg float64, success bool) string {
	if !success || rtt <= 0 { return "·" }
	if avg <= 0 { return "▄" }

	// 計算當前 RTT 相對於平均值的 Log2 偏移
	// 基礎索引 3 為中間值 (▄)
	// 比率每翻一倍，索引增加 2
	ratio := rtt / avg
	diff := math.Log2(ratio) * 2.0
	idx := 3 + int(math.Round(diff))

	scales := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if idx < 0 { idx = 0 }
	if idx >= len(scales) { idx = len(scales) - 1 }
	return scales[idx]
}

func (d *Device) UpdateStats(rtt float64, success bool) {
	d.Snt++
	if !success {
		d.Loss++
	} else {
		d.LastRTT = rtt
		// 更新滑動窗口 (Moving Average)
		d.Window = append(d.Window, rtt)
		if len(d.Window) > WINDOW_SIZE {
			d.Window = d.Window[1:]
		}
		// 計算平均值
		var sum float64
		for _, v := range d.Window { sum += v }
		d.AvgRTT = sum / float64(len(d.Window))
	}
	d.LossRate = (d.Loss * 100) / d.Snt
	
	char := getLogChar(rtt, d.AvgRTT, success)
	d.History[d.HistoryIdx] = Heartbeat{Char: char, Success: success}
	d.HistoryIdx = (d.HistoryIdx + 1) % HIST_SIZE
	if d.HistCount < HIST_SIZE { d.HistCount++ }
}

// ---------------------------------------------------------
// Ping 執行邏輯 (Pro-bing + Fallback)
// ---------------------------------------------------------

func runPingTask(idx int, ip string) tea.Cmd {
	return func() tea.Msg {
		pinger, err := probing.NewPinger(ip)
		if err == nil {
			pinger.Count = 1
			pinger.Timeout = time.Second
			
			// 【關鍵修正】根據作業系統動態調整權限
			if runtime.GOOS == "windows" {
				pinger.SetPrivileged(true) // Windows 強制使用 Raw ICMP
			} else {
				pinger.SetPrivileged(false) // Mac/Linux 嘗試使用 Unprivileged
			}

			if err = pinger.Run(); err == nil {
				stats := pinger.Statistics()
				if stats.PacketsRecv > 0 {
					return pingRes{
						idx:      idx, 
						rtt:      float64(stats.MaxRtt.Microseconds()) / 1000.0, 
						success:  true, 
						isNative: true, // 成功的話，畫面上會多一個 * 號
					}
				}
			}
		}

		// Fallback (系統 Ping)
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

// [修正點] 回傳 (float64, bool) 確保能分辨真的抓到時間還是字串亂碼/逾時
// [終極修正版] 支援多語系與 <1ms 的極端情況
func parseRTT(out string) (float64, bool) {
	// 涵蓋英文版、中文版，以及小於 1ms 的情況
	keys := []string{"time=", "time<", "時間=", "時間<"}
	var start int = -1
	var matchKey string

	// 找出到底中了哪一個關鍵字
	for _, k := range keys {
		start = strings.Index(out, k)
		if start != -1 {
			matchKey = k
			break
		}
	}

	if start == -1 {
		return 0, false // 真的找不到時間，代表超時或斷線
	}

	// 擷取關鍵字後面的字串
	sub := out[start+len(matchKey):]
	end := strings.Index(sub, "ms")
	if end == -1 {
		return 0, false
	}

	// 轉換為浮點數 (如果是 time<1ms，這裡會切出 "1"，也能順利轉型)
	timeStr := strings.TrimSpace(sub[:end])
	res, err := strconv.ParseFloat(timeStr, 64)
	if err != nil {
		return 0, false
	}
	return res, true
}

// ---------------------------------------------------------
// Bubbletea Model
// ---------------------------------------------------------

func (m model) Init() tea.Cmd {
	return func() tea.Msg { return startPingMsg{} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" { return m, tea.Quit }
	
	case startPingMsg:
		m.pingQueue = make([]int, 0, len(m.devices))
		var cmds []tea.Cmd
		for i, d := range m.devices {
			if d.Name != "---" {
				d.Loading = true
				m.pingQueue = append(m.pingQueue, i)
			}
		}
		// 發送第一批
		batch := MAX_CONCURRENT
		if len(m.pingQueue) < batch { batch = len(m.pingQueue) }
		for i := 0; i < batch; i++ {
			cmds = append(cmds, runPingTask(m.pingQueue[i], m.devices[m.pingQueue[i]].IP))
		}
		m.pingQueue = m.pingQueue[batch:]

		// 計算下一輪帶 Jitter 的時間
		itv, _ := time.ParseDuration(m.cfg.Interval)
		if itv == 0 { itv = 2 * time.Second }
		jit := m.cfg.Jitter
		if jit == 0 { jit = 0.1 }
		next := time.Duration(float64(itv) * (1 + (rand.Float64()*2-1)*jit))
		
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
	if m.height == 0 { return " Loading..." }
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
		if d.Loading { indicator = arrowStyle.Render("> ") }
		
		var hist strings.Builder
		for j := 1; j <= d.HistCount; j++ {
			h := d.History[(d.HistoryIdx-j+HIST_SIZE)%HIST_SIZE]
			if h.Success { hist.WriteString(upStyle.Render(h.Char)) } else { hist.WriteString(downStyle.Render(h.Char)) }
		}

		tag := " "
		if d.IsNative { tag = "*" }
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

func main() {
	var cfg Config
	// 讀取檔案內容 (這裡假設你用 os.ReadFile 或 embeddedYaml)
	_ = yaml.Unmarshal(embeddedYaml, &cfg)

	// --- 統一設定預設值區塊 ---
	if cfg.Interval == "" { cfg.Interval = "1s" }
	if cfg.Jitter <= 0    { cfg.Jitter = 0.1 }   // 防止使用者填 0 或負數
	// -----------------------

	hostname, _ := os.Hostname()
	p := tea.NewProgram(model{
		cfg:      cfg, 
		devices:  cfg.Devices, 
		hostname: hostname,
	}, tea.WithAltScreen())
	
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
	}
}
