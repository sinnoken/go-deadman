package main

// 加入這行自動化指令
//go:generate go-winres make

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	VERSION     = "v1.10.0-perf" // 優化高精度與非同步渲染
	HIST_SIZE   = 30
	WINDOW_SIZE = 50
	RENDER_FPS  = 20 // 畫面刷新頻率 (每秒 20 次)
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
	mu         sync.RWMutex // 讀寫鎖，分離「資料更新」與「畫面渲染」
	Name       string       `yaml:"name"`
	IP         string       `yaml:"ip"`
	Loss       int
	Snt        int
	LastRTT    float64
	AvgRTT     float64
	Jitter     float64
	Window     []float64
	LossRate   int
	History    [HIST_SIZE]Heartbeat
	HistoryIdx int
	HistCount  int
	Loading    bool
	IsNative   bool
	IsDNSFail  bool
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
}

// Bubbletea 訊息類型 (只留下用於渲染的 Tick)
type tickMsg time.Time

// ---------------------------------------------------------
// 輔助函式：字串截斷
// ---------------------------------------------------------

func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) > maxLen {
		return string(r[:maxLen-2]) + ".."
	}
	return s
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
	d.mu.Lock()
	defer d.mu.Unlock()

	d.Snt++
	if !success {
		d.Loss++
	} else {
		d.LastRTT = rtt
		d.Window = append(d.Window, rtt)
		if len(d.Window) > WINDOW_SIZE {
			d.Window = d.Window[1:]
		}

		// 1. 計算平均延遲
		var sum float64
		for _, v := range d.Window {
			sum += v
		}
		d.AvgRTT = sum / float64(len(d.Window))

		// 2. 計算 Jitter (相鄰 RTT 絕對誤差的平均)
		if len(d.Window) >= 2 {
			var jitterSum float64
			for i := 1; i < len(d.Window); i++ {
				jitterSum += math.Abs(d.Window[i] - d.Window[i-1])
			}
			d.Jitter = jitterSum / float64(len(d.Window)-1)
		} else {
			d.Jitter = 0.0
		}
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

func resolveAndStartWorker(d *Device, interval time.Duration, jitter float64) {
	d.mu.RLock()
	rawName, rawIP := d.Name, d.IP
	d.mu.RUnlock()

	name := strings.TrimSpace(rawName)
	ip := strings.TrimSpace(rawIP)
	isFail := false

	// 1. DNS 正反解邏輯
	if name == "" && ip != "" {
		names, err := net.LookupAddr(ip)
		if err == nil && len(names) > 0 {
			name = strings.TrimSuffix(names[0], ".")
		} else {
			name = ip
		}
	} else if ip == "" && name != "" {
		ips, err := net.LookupIP(name)
		if err == nil && len(ips) > 0 {
			ip = ips[0].String()
		} else {
			ip = "DNS_FAIL"
			isFail = true
		}
	}

	// 2. 截斷過長字串
	name = truncate(name, 15)
	ip = truncate(ip, 15)

	// 3. 寫入狀態 (UI 渲染緒會自動抓取)
	d.mu.Lock()
	d.Name = name
	d.IP = ip
	d.IsDNSFail = isFail
	d.Loading = !isFail
	d.mu.Unlock()

	// 4. 如果 DNS 成功，才進入無限循環的 Ping Worker
	if !isFail {
		deviceWorker(d, ip, interval, jitter)
	}
}

func deviceWorker(d *Device, ip string, interval time.Duration, jitter float64) {
	// [進階優化] 鎖定 OS 執行緒，降低排程器 Context Switch 帶來的微秒級延遲誤差
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// [物件重用] 建立一個 Timer 並在迴圈內 Reset，取代不斷呼叫 time.Sleep()
	timer := time.NewTimer(0)
	<-timer.C

	offset := time.Duration(rand.Float64() * float64(interval))
	timer.Reset(offset)
	<-timer.C

	// [物件重用] 備用方案的 Buffer，避免迴圈內不斷分配記憶體
	var outBuf bytes.Buffer

	for {
		start := time.Now()

		d.mu.Lock()
		d.Loading = true
		d.mu.Unlock()

		rtt := 0.0
		success := false
		isNative := false

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
					// [顯示精度提升] 使用 Nanoseconds 轉換來精準捕捉微秒級數值
					rtt = float64(stats.MaxRtt.Nanoseconds()) / 1000000.0
					success = true
					isNative = true
				}
			}
		}

		// Fallback
		if !success {
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			var cmd *exec.Cmd
			timeoutMs := strconv.Itoa(int(interval.Milliseconds()))
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", timeoutMs, ip)
			} else {
				cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip)
			}

			outBuf.Reset()
			cmd.Stdout = &outBuf
			_ = cmd.Run()
			cancel()

			rtt, success = parseRTT(outBuf.String())
			isNative = false
		}

		d.mu.Lock()
		d.Loading = false
		d.IsNative = isNative
		d.mu.Unlock()

		// 寫入數據紀錄
		d.UpdateStats(rtt, success)

		// 計算間距與 Jitter
		elapsed := time.Since(start)
		baseWait := float64(interval) - float64(elapsed)
		if baseWait < 0 {
			baseWait = 0
		}

		jit := jitter
		if jit <= 0 {
			jit = 0.1
		}
		nextWait := time.Duration(baseWait * (1 + (rand.Float64()*2 - 1)*jit))

		// 使用重用的 Timer
		timer.Reset(nextWait)
		<-timer.C
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

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second/RENDER_FPS, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Init() tea.Cmd {
	itv, _ := time.ParseDuration(m.cfg.Interval)
	if itv == 0 {
		itv = 2 * time.Second
	}
	for _, d := range m.devices {
		if d.Name != "---" {
			d.mu.Lock()
			d.Loading = true
			d.mu.Unlock()
			// [解耦] Worker 獨立運行，不再依賴 Channel 卡住 UI Thread
			go resolveAndStartWorker(d, itv, m.cfg.Jitter)
		}
	}
	return tickCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tickMsg:
		// 定期觸發渲染
		return m, tickCmd()
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 || m.width == 0 {
		return " Loading..."
	}
	var s strings.Builder

	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("go-deadman")) + "\n")
	subTitle := fmt.Sprintf("From: %s | Version: %s | 顯示: Log+Avg 圖表", m.hostname, VERSION)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, dimStyle.Render(subTitle)) + "\n")

	const fixedColsWidth = 76
	maxHist := m.width - fixedColsWidth
	if maxHist > HIST_SIZE {
		maxHist = HIST_SIZE
	} else if maxHist < 5 {
		maxHist = 5
	}

	sepWidth := m.width
	if sepWidth < 85 {
		sepWidth = 85
	}

	// [顯示精度提升] 修改排版配置，容納 %8.3f 的寬度
	header := fmt.Sprintf("\n  %-15s %-15s %5s %8s %8s %8s %6s  %-*s",
		"HOSTNAME", "ADDRESS", "LOSS", "RTT(ms)", "AVG(ms)", "JIT(ms)", "SNT", maxHist, "LOG-STATUS")
	s.WriteString(headerStyle.Render(header) + "\n" + dimStyle.Render(strings.Repeat("─", sepWidth)) + "\n")

	for _, d := range m.devices {
		if d.Name == "---" {
			s.WriteString(dimStyle.Render("  " + strings.Repeat("-", sepWidth-2)) + "\n")
			continue
		}

		// [讀寫分離] 短暫獲取 RLock()，將資料拷貝出，不阻礙 Worker 更新
		d.mu.RLock()
		isLoading := d.Loading
		isNative := d.IsNative
		isDNSFail := d.IsDNSFail
		displayName := d.Name
		displayIP := d.IP
		lossRate := d.LossRate
		lastRTT := d.LastRTT
		avgRTT := d.AvgRTT
		jitter := d.Jitter
		snt := d.Snt
		histCount := d.HistCount
		histIdx := d.HistoryIdx
		var historyCopy [HIST_SIZE]Heartbeat
		copy(historyCopy[:], d.History[:])
		d.mu.RUnlock()

		indicator := "  "
		if isLoading {
			indicator = arrowStyle.Render("> ")
		}

		var hist strings.Builder
		showCount := histCount
		if showCount > maxHist {
			showCount = maxHist
		}
		for j := 1; j <= showCount; j++ {
			h := historyCopy[(histIdx-j+HIST_SIZE)%HIST_SIZE]
			if h.Success {
				hist.WriteString(upStyle.Render(h.Char))
			} else {
				hist.WriteString(downStyle.Render(h.Char))
			}
		}

		tag := " "
		if isNative {
			tag = "*"
		}

		if displayName == "" {
			displayName = "resolving..."
		}
		if displayIP == "" {
			displayIP = "resolving..."
		}

		// [顯示精度提升] RTT/AVG/Jitter 修改為 %8.3f
		line := fmt.Sprintf("%-15s %-15s %4d%% %8.3f %8.3f %8.3f %5d%s  ",
			displayName, displayIP, lossRate, lastRTT, avgRTT, jitter, snt, tag)

		s.WriteString(indicator)

		if isDNSFail {
			s.WriteString(failRowStyle.Render(line) + downStyle.Render("DNS RESOLVE FAILED\n"))
		} else if histCount > 0 && !historyCopy[(histIdx-1+HIST_SIZE)%HIST_SIZE].Success {
			s.WriteString(failRowStyle.Render(line) + hist.String() + "\n")
		} else {
			s.WriteString(line + hist.String() + "\n")
		}
	}

	footer := fmt.Sprintf("\n Interval: %s | App Jitter: %.f%% | *: Native | Window: %d",
		m.cfg.Interval, m.cfg.Jitter*100, WINDOW_SIZE)
	s.WriteString(dimStyle.Render(footer))
	return s.String()
}

// ---------------------------------------------------------
// 程式進入點
// ---------------------------------------------------------

func main() {
	rand.Seed(time.Now().UnixNano())

	var cfg Config
	err := yaml.Unmarshal(embeddedYaml, &cfg)
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

	p := tea.NewProgram(model{
		cfg:      cfg,
		devices:  cfg.Devices,
		hostname: hostname,
	}, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
	}
}
