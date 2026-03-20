package main

// 加入這行自動化指令
//go:generate go-winres make --product-version=git-tag --file-version=git-tag

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
	VERSION     = "v1.11.0" // 升級為 Socket 重用與 Event Callback 模式
	HIST_SIZE   = 100
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

type tickMsg time.Time

// ---------------------------------------------------------
// 輔助函式：字串截斷
// ---------------------------------------------------------

func truncate(s string, maxLen int) string {
	r := []rune(s)
	// 避免 maxLen 過小導致的越界錯誤
	if maxLen < 2 {
		return string(r)
	}
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

		var sum float64
		for _, v := range d.Window {
			sum += v
		}
		d.AvgRTT = sum / float64(len(d.Window))

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
// 網路層邏輯：事件驅動與連線重用 (Option A)
// ---------------------------------------------------------

func resolveAndStartWorker(d *Device, interval time.Duration, jitter float64) {
	d.mu.RLock()
	rawName, rawIP := d.Name, d.IP
	d.mu.RUnlock()

	name := strings.TrimSpace(rawName)
	ip := strings.TrimSpace(rawIP)
	isFail := false

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

	name = truncate(name, 50) // 放寬解析時的字串長度，實際顯示時會動態截斷
	ip = truncate(ip, 50)

	d.mu.Lock()
	d.Name = name
	d.IP = ip
	d.IsDNSFail = isFail
	d.Loading = !isFail
	d.mu.Unlock()

	if !isFail {
		deviceWorker(d, ip, interval, jitter)
	}
}

func deviceWorker(d *Device, ip string, interval time.Duration, jitter float64) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	
	// #nosec G404
	offset := time.Duration(rand.Float64() * float64(interval))
	time.Sleep(offset)
	
	pinger, err := probing.NewPinger(ip)
	if err != nil {
		fallbackWorker(d, ip, interval, jitter)
		return
	}

	// [核心優化] 設定為無窮發送模式，完美重用 Socket
	pinger.Count = -1
	pinger.Interval = interval
	if runtime.GOOS == "windows" {
		pinger.SetPrivileged(true)
	} else {
		pinger.SetPrivileged(false)
	}

	recvCh := make(chan *probing.Packet, 100)
	pinger.OnRecv = func(pkt *probing.Packet) {
		recvCh <- pkt
	}

	// 捕捉 Pinger 啟動狀態，若失敗立刻轉交 Fallback
	errCh := make(chan error, 1)
	go func() {
		errCh <- pinger.Run()
	}()

	select {
	case err := <-errCh:
		if err != nil {
			fallbackWorker(d, ip, interval, jitter)
			return
		}
	case <-time.After(time.Millisecond * 50):
		// Pinger 正常執行中
	}

	expectedSeq := 0
	timeoutDuration := interval + time.Second
	timer := time.NewTimer(timeoutDuration)

	for {
		d.mu.Lock()
		d.Loading = true
		d.mu.Unlock()

		select {
		case err := <-errCh:
			if err != nil {
				fallbackWorker(d, ip, interval, jitter)
				return
			}

		case pkt := <-recvCh:
			// 如果收到過遲的封包 (Seq 已經落後於預期)，代表已經被計為 Timeout，直接捨棄以確保圖表一致性
			if pkt.Seq < expectedSeq {
				continue
			}

			// 如果發生連續掉包，透過 Seq 的差距，立刻補齊遺失的歷史紀錄
			for expectedSeq < pkt.Seq {
				d.mu.Lock()
				d.IsNative = true
				d.Loading = false
				d.mu.Unlock()
				d.UpdateStats(0, false)
				expectedSeq++
			}

			// 寫入當前成功數據
			rtt := float64(pkt.Rtt.Nanoseconds()) / 1000000.0
			d.mu.Lock()
			d.IsNative = true
			d.Loading = false
			d.mu.Unlock()
			d.UpdateStats(rtt, true)

			expectedSeq = pkt.Seq + 1

			// 精準重置超時定時器
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeoutDuration)

		case <-timer.C:
			// 觸發超時機制 (遺失封包)
			d.mu.Lock()
			d.IsNative = true
			d.Loading = false
			d.mu.Unlock()
			d.UpdateStats(0, false)

			expectedSeq++
			timer.Reset(interval)
		}
	}
}

// ---------------------------------------------------------
// 備援方案 (Fallback)：相容無權限環境，維持客製化 Jitter
// ---------------------------------------------------------

func fallbackWorker(d *Device, ip string, interval time.Duration, jitter float64) {
	timer := time.NewTimer(0)
	<-timer.C
	// #nosec G404
	timer.Reset(time.Duration(rand.Float64() * float64(interval)))
	<-timer.C

	var outBuf bytes.Buffer

	for {
		start := time.Now()

		d.mu.Lock()
		d.Loading = true
		d.mu.Unlock()
		
		if net.ParseIP(ip) == nil {
			// 如果不是合法 IP，可能是域名或非法字串。
			// 雖然先前 resolve 已經處理過，但為了 Gosec 審核與安全，此處必須顯式檢查。
			d.UpdateStats(0, false)
			time.Sleep(interval)
			continue
		}
				
		ctx, cancel := context.WithTimeout(context.Background(), interval)
		var cmd *exec.Cmd
		timeoutMs := strconv.Itoa(int(interval.Milliseconds()))
		if runtime.GOOS == "windows" {
			// #nosec G204
			cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", timeoutMs, ip)
		} else {
			// #nosec G204
			cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", ip)
		}

		outBuf.Reset()
		cmd.Stdout = &outBuf
		_ = cmd.Run()
		cancel()

		rtt, success := parseRTT(outBuf.String())

		d.mu.Lock()
		d.Loading = false
		d.IsNative = false
		d.mu.Unlock()

		d.UpdateStats(rtt, success)

		elapsed := time.Since(start)
		baseWait := float64(interval) - float64(elapsed)
		if baseWait < 0 {
			baseWait = 0
		}
		jit := jitter
		if jit <= 0 {
			jit = 0.1
		}
		// #nosec G404
		timer.Reset(time.Duration(baseWait * (1 + (rand.Float64()*2 - 1)*jit)))
		<-timer.C
	}
}

func parseRTT(out string) (float64, bool) {
	keys := []string{"time=", "time<", "時間=", "時間<"}
	start := -1
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

	// --- 1. 動態寬度計算區 ---
	// 數據欄位固定所需寬度 (LOSS + RTT + AVG + JIT + SNT + 空白間隔)
	const fixedStatsWidth = 45 

	// 計算 History 圖表寬度，預留最少 30 給 Name 和 IP
	maxHist := HIST_SIZE
	if m.width-fixedStatsWidth-30 < HIST_SIZE {
		maxHist = m.width - fixedStatsWidth - 30
	}
	if maxHist < 5 {
		maxHist = 5
	}

	// 剩下空間分給 Name 和 IP
	remainingWidth := m.width - fixedStatsWidth - maxHist
	colWidth := remainingWidth / 2
	if colWidth < 12 {
		colWidth = 12 // 最小寬度
	} else if colWidth > 35 {
		colWidth = 35 // 最大寬度，避免畫面太過鬆散
	}

	sepWidth := m.width
	if sepWidth < 85 {
		sepWidth = 85
	}

	// --- 2. 組合動態表頭 ---
	headerFormat := fmt.Sprintf("\n  %%-*s %%-*s %%5s %%8s %%8s %%8s %%6s  %%-%ds", maxHist)
	header := fmt.Sprintf(headerFormat,
		colWidth, "HOSTNAME",
		colWidth, "ADDRESS",
		"LOSS", "RTT(ms)", "AVG(ms)", "JIT(ms)", "SNT", "LOG-STATUS")
	s.WriteString(headerStyle.Render(header) + "\n" + dimStyle.Render(strings.Repeat("─", sepWidth)) + "\n")

	// --- 3. 渲染資料列 ---
	for _, d := range m.devices {
		if d.Name == "---" {
			s.WriteString(dimStyle.Render("  " + strings.Repeat("-", sepWidth-2)) + "\n")
			continue
		}

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

		// ⭐ 進行動態安全截斷
		dispNameTrunc := truncate(displayName, colWidth)
		dispIPTrunc := truncate(displayIP, colWidth)

		rowFormatStr := fmt.Sprintf("%%-*s %%-*s %%4d%%%% %%8.3f %%8.3f %%8.3f %%5d%%s  ")
		line := fmt.Sprintf(rowFormatStr,
			colWidth, dispNameTrunc,
			colWidth, dispIPTrunc,
			lossRate, lastRTT, avgRTT, jitter, snt, tag)

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