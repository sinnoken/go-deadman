package main

import (
	"context" // [新增] 用於控制備用系統指令的超時
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
	VERSION     = "v1.8.1" // 修正潛在邏輯隱患與時間偏移
	HIST_SIZE   = 30       // 歷史紀錄長度
	WINDOW_SIZE = 50       // Moving Average 的樣本數
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
	cfg      Config
	devices  []*Device
	width    int
	height   int
	hostname string
	sub      chan tea.Msg // [核心架構] 用來接收背景 Goroutine 的通訊頻道
}

// Bubbletea 訊息類型
type initMsg struct{}
type pingStartMsg struct{ idx int }
type pingResMsg struct {
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
// 網路層邏輯：獨立背景執行緒 (Goroutine Worker)
// ---------------------------------------------------------

func deviceWorker(idx int, ip string, interval time.Duration, jitter float64, sub chan<- tea.Msg) {
	// 【防踩踏機制】給每個設備一個隨機的初始延遲，讓它們不要在同一個毫秒起跑
	offset := time.Duration(rand.Float64() * float64(interval))
	time.Sleep(offset)

	for {
		start := time.Now() // [修正] 記錄開始時間，用以計算耗時

		// 通知 UI 開始測量 (顯示 > 箭頭)
		sub <- pingStartMsg{idx: idx}

		var res pingResMsg
		res.idx = idx

		// 1. 嘗試使用原生 Pro-bing
		pinger, err := probing.NewPinger(ip)
		if err == nil {
			pinger.Count = 1
			pinger.Timeout = interval

			if runtime.GOOS == "windows" {
				pinger.SetPrivileged(true)
			} else {
				pinger.SetPrivileged(false)
			}

			if err = pinger.Run(); err == nil {
				stats := pinger.Statistics()
				if stats.PacketsRecv > 0 {
					res.rtt = float64(stats.MaxRtt.Microseconds()) / 1000.0
					res.success = true
					res.isNative = true
				}
			}
		}

		// 2. Fallback：使用系統指令
		if !res.success {
			// [修正] 加入 Context 控制超時，避免 Linux 下 ping 卡死導致 UI 不更新
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			
			var cmd *exec.Cmd
			timeoutMs := strconv.Itoa(int(interval.Milliseconds()))
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", timeoutMs, ip)
			} else {
				cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip)
			}
			out, _ := cmd.CombinedOutput()
			cancel() // 釋放 Context 資源

			rtt, isSuccess := parseRTT(string(out))
			res.rtt = rtt
			res.success = isSuccess
			res.isNative = false
		}

		// 把結果傳回給 UI
		sub <- res

		// [修正] 計算 Ping 操作的耗時，從等待時間中扣除，避免「時間偏移」
		elapsed := time.Since(start)
		baseWait := float64(interval) - float64(elapsed)
		if baseWait < 0 {
			baseWait = 0 // 如果 Ping 花了太久，下一次就不用等了
		}

		// 加上 Jitter 進行下一次等待
		jit := jitter
		if jit <= 0 {
			jit = 0.1
		}
		nextWait := time.Duration(baseWait * (1 + (rand.Float64()*2 - 1)*jit))
		time.Sleep(nextWait)
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
// Bubbletea UI 渲染與事件更新
// ---------------------------------------------------------

// 監聽背景 Channel 的持續指令
func listenForMsg(sub chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

func (m model) Init() tea.Cmd {
	return func() tea.Msg { return initMsg{} }
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
		if itv == 0 {
			itv = 2 * time.Second
		}
		// 啟動所有設備的專屬背景工人
		for i, d := range m.devices {
			if d.Name != "---" {
				go deviceWorker(i, d.IP, itv, m.cfg.Jitter, m.sub)
			}
		}
		// 開始監聽回報
		return m, listenForMsg(m.sub)

	case pingStartMsg:
		m.devices[msg.idx].Loading = true
		return m, listenForMsg(m.sub) // 繼續監聽下一個事件

	case pingResMsg:
		d := m.devices[msg.idx]
		d.Loading, d.IsNative = false, msg.isNative
		d.UpdateStats(msg.rtt, msg.success)
		return m, listenForMsg(m.sub) // 繼續監聽下一個事件
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 || m.width == 0 {
		return " Loading..."
	}
	var s strings.Builder

	// 1. 標題與副標題 (維持動態置中)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("NETWORK MONITOR")) + "\n")
	subTitle := fmt.Sprintf("From: %s | Version: %s | 顯示: Log+Avg 圖表", m.hostname, VERSION)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, dimStyle.Render(subTitle)) + "\n")

	// 【寬度計算邏輯】
	// 固定欄位寬度加總：指示器(2) + Name(16) + IP(16) + Loss(6) + RTT(8) + AVG(8) + SNT(7) = 63 字元
	const fixedColsWidth = 63
	
	// 計算剩餘可給圖表的寬度
	maxHist := m.width - fixedColsWidth
	if maxHist > HIST_SIZE {
		maxHist = HIST_SIZE // 最高不超過 config 定義的紀錄上限
	} else if maxHist < 5 {
		maxHist = 5 // 給一個極小值的防呆，避免視窗太窄時破圖
	}

	// 計算分隔線寬度 (最少 80，不然會很醜)
	sepWidth := m.width
	if sepWidth < 80 {
		sepWidth = 80
	}

	// 2. 表頭【動態適配】
	// 將 LOG-STATUS 的寬度改為動態分配
	header := fmt.Sprintf("\n  %-15s %-15s %5s %7s %7s %6s  %-*s", 
		"HOSTNAME", "ADDRESS", "LOSS", "RTT(ms)", "AVG(ms)", "SNT", maxHist, "LOG-STATUS")
	s.WriteString(headerStyle.Render(header) + "\n" + dimStyle.Render(strings.Repeat("─", sepWidth)) + "\n")

	for _, d := range m.devices {
		if d.Name == "---" {
			// 無效設備的佔位符也改為動態長度
			s.WriteString(dimStyle.Render("  " + strings.Repeat("-", sepWidth-2)) + "\n")
			continue
		}

		// 資料列的指示器
		indicator := "  "
		if d.Loading {
			indicator = arrowStyle.Render("> ")
		}

		var hist strings.Builder
		
		// 3. 圖表【動態適配】：
		// 根據計算出的 maxHist 來決定要印出多少筆歷史紀錄
		showCount := d.HistCount
		if showCount > maxHist {
			showCount = maxHist
		}

		for j := 1; j <= showCount; j++ {
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

		// 4. 資料列
		line := fmt.Sprintf("%-15s %-15s %4d%% %7.1f %7.1f %5d%s  ", 
			d.Name, d.IP, d.LossRate, d.LastRTT, d.AvgRTT, d.Snt, tag)

		s.WriteString(indicator)
		if d.HistCount > 0 && !d.History[(d.HistoryIdx-1+HIST_SIZE)%HIST_SIZE].Success {
			s.WriteString(failRowStyle.Render(line))
		} else {
			s.WriteString(line)
		}
		s.WriteString(hist.String() + "\n")
	}

	footer := fmt.Sprintf("\n Interval: %s | Jitter: %.f%% | *: Native | Window: %d", 
		m.cfg.Interval, m.cfg.Jitter*100, WINDOW_SIZE)
	s.WriteString(dimStyle.Render(footer))
	return s.String()
}

// ---------------------------------------------------------
// 程式進入點
// ---------------------------------------------------------

func main() {
	// [修正] 初始化隨機數種子 (相容 Go 1.20 以下版本，確保 Jitter 是真正的隨機)
	rand.Seed(time.Now().UnixNano())

	var cfg Config
	err := yaml.Unmarshal(embeddedYaml, &cfg)
	// [修正] 接住並處理設定檔讀取失敗的狀況，避免靜默錯誤
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
	
	// 初始化 Channel (帶有緩衝區以防短暫高峰)
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
