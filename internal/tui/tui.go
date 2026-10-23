// Package tui 以 Bubble Tea 繪製監控儀表板。
package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go-deadman/internal/monitor"
)

const renderFPS = 20 // 重繪頻率上限

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	headerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	upStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	downStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	arrowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	failRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

type model struct {
	cfg      monitor.Config
	width    int
	height   int
	offset   int // 第一個可見資料列的索引
	hostname string
	version  string
}

// New 建立儀表板 model；worker 由呼叫端以 cfg.Start 啟動
func New(cfg monitor.Config, hostname, version string) tea.Model {
	return model{cfg: cfg, hostname: hostname, version: version}
}

// ---------------------------------------------------------
// 按需重繪：資料有變才重繪，並以 renderFPS 為上限
// ---------------------------------------------------------

type dataMsg struct{}
type frameMsg struct{}

func waitForUpdate() tea.Cmd {
	return func() tea.Msg {
		<-monitor.Updates()
		return dataMsg{}
	}
}

func (m model) Init() tea.Cmd {
	return waitForUpdate()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.offset = m.clampOffset(m.offset)
	case tea.KeyMsg:
		page := m.visibleRows()
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.offset = m.clampOffset(m.offset - 1)
		case "down", "j":
			m.offset = m.clampOffset(m.offset + 1)
		case "pgup", "b":
			m.offset = m.clampOffset(m.offset - page)
		case "pgdown", "f", " ":
			m.offset = m.clampOffset(m.offset + page)
		case "home", "g":
			m.offset = 0
		case "end", "G":
			m.offset = m.clampOffset(len(m.cfg.Devices))
		}
	case dataMsg:
		// 本次已重繪；等一個 frame 再接收下一批更新
		return m, tea.Tick(time.Second/renderFPS, func(time.Time) tea.Msg { return frameMsg{} })
	case frameMsg:
		return m, waitForUpdate()
	}
	return m, nil
}

// ---------------------------------------------------------
// 垂直捲動
// ---------------------------------------------------------

// 資料列以外的固定行數：標題、副標、空行、表頭、橫線（上方 5 行）＋ 空行、footer（下方 2 行）
const chromeRows = 7

// visibleRows 是一頁可顯示的資料列數（含分隔列），至少 1
func (m model) visibleRows() int {
	return max(m.height-chromeRows, 1)
}

func (m model) clampOffset(off int) int {
	return max(min(off, len(m.cfg.Devices)-m.visibleRows()), 0)
}

// ---------------------------------------------------------
// 依終端字元格寬度排版
// ---------------------------------------------------------

const (
	indW     = 2 // 列首指示符號
	lossW    = 5
	numW     = 8
	sntW     = 6 // 數字 + native 標記
	colMin   = 8
	colMid   = 20
	colMax   = 30
	histMin  = 10
	histGapW = 2
)

// layout 描述各欄位寬度；0 / false 表示隱藏
type layout struct {
	name, addr          int
	loss, avg, jit, snt bool
	hist                int
}

// computeLayout 依優先順序配置欄位：HOSTNAME 與 RTT 一定保留，
// 接著依序是 LOSS、ADDRESS、AVG、JIT、SNT，剩餘空間給 HISTORY，
// 名稱欄則在空間充裕時加寬。整列寬度不會超過 width。
func computeLayout(width int) layout {
	var l layout
	free := width - indW - (1 + numW)
	l.name = max(min(free, colMin), 1)
	free -= l.name

	take := func(w int) bool {
		if free >= w {
			free -= w
			return true
		}
		return false
	}
	grow := func(col *int, to int) {
		if *col == 0 {
			return
		}
		if g := min(free, to-*col); g > 0 {
			*col += g
			free -= g
		}
	}

	l.loss = take(1 + lossW)
	if take(1 + colMin) {
		l.addr = colMin
	}
	l.avg = take(1 + numW)
	l.jit = take(1 + numW)
	l.snt = take(1 + sntW)

	grow(&l.name, colMid)
	grow(&l.addr, colMid)
	if free >= histGapW+histMin {
		l.hist = min(free-histGapW, monitor.HistSize)
		free -= histGapW + l.hist
	}
	grow(&l.name, colMax)
	grow(&l.addr, colMax)
	return l
}

type rowCells struct {
	name, addr, loss, rtt, avg, jit, snt string
}

// render 組出不含指示符號與 HISTORY 的欄位部分
func (l layout) render(c rowCells) string {
	var b strings.Builder
	b.WriteString(fitWidth(c.name, l.name))
	if l.addr > 0 {
		b.WriteString(" " + fitWidth(c.addr, l.addr))
	}
	if l.loss {
		b.WriteString(" " + padLeft(c.loss, lossW))
	}
	b.WriteString(" " + padLeft(c.rtt, numW))
	if l.avg {
		b.WriteString(" " + padLeft(c.avg, numW))
	}
	if l.jit {
		b.WriteString(" " + padLeft(c.jit, numW))
	}
	if l.snt {
		b.WriteString(" " + padLeft(c.snt, sntW))
	}
	return b.String()
}

// truncWidth 依顯示寬度截斷（CJK 字元佔 2 格），超出時以 ".." 結尾
func truncWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	tail := ".."
	if w < 3 {
		tail = ""
	}
	limit := w - len(tail)
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > limit {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	return b.String() + tail
}

func fitWidth(s string, w int) string {
	s = truncWidth(s, w)
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

func padLeft(s string, w int) string {
	s = truncWidth(s, w)
	return strings.Repeat(" ", max(w-lipgloss.Width(s), 0)) + s
}

// formatLoss 回傳最多 5 字元的丟包率字串；非零丟包永遠不會顯示成 0%，
// 未滿 100% 也不會被進位成 100%。
func formatLoss(rate float64) string {
	switch {
	case rate <= 0:
		return "0%"
	case rate < 0.1:
		return "<0.1%"
	case rate < 10:
		return fmt.Sprintf("%.1f%%", math.Floor(rate*10)/10)
	case rate < 100:
		return fmt.Sprintf("%.0f%%", math.Floor(rate))
	default:
		return "100%"
	}
}

// ---------------------------------------------------------
// HISTORY sparkline
// ---------------------------------------------------------

// 柱高沿用對數刻度（底數 2.4：1ms 為 ▁、約 190ms 以上為 ▇）。
// 延遲柱最高只到 ▇，█ 保留給掉包，即使同為紅色也能以形狀區分。
var barChars = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇"}

const (
	lossChar = "█"
	warnRTT  = 50.0  // 毫秒；以上為黃色
	badRTT   = 150.0 // 毫秒；以上為紅色
)

func barLevel(rtt float64) int {
	lvl := int(math.Log(math.Max(1, rtt)) / math.Log(2.4))
	return max(min(lvl, len(barChars)-1), 0)
}

// latencyStyle 以絕對延遲上色：<50ms 綠、<150ms 黃、其餘紅
func latencyStyle(rtt float64) lipgloss.Style {
	switch {
	case rtt < warnRTT:
		return upStyle
	case rtt < badRTT:
		return warnStyle
	default:
		return downStyle
	}
}

// renderHistory 畫出最近 width 筆探測（最新在左）；顏色代表延遲高低，掉包為整格紅色
func renderHistory(ds monitor.Snapshot, width int) string {
	var b strings.Builder
	for j := 1; j <= min(ds.HistCount, width); j++ {
		h := ds.Recent(j)
		if !h.Success {
			b.WriteString(downStyle.Render(lossChar))
			continue
		}
		b.WriteString(latencyStyle(h.RTT).Render(barChars[barLevel(h.RTT)]))
	}
	return b.String()
}

func fmtMs(v float64, ok bool) string {
	switch {
	case !ok:
		return "-"
	case v >= 1000:
		return fmt.Sprintf("%.1f", v) // 保持在 numW 欄寬內
	default:
		return fmt.Sprintf("%.3f", v)
	}
}

// ---------------------------------------------------------
// 畫面渲染
// ---------------------------------------------------------

func (m model) View() string {
	if m.height == 0 || m.width == 0 {
		return " Loading..."
	}
	l := computeLayout(m.width)
	var s strings.Builder

	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("go-deadman")) + "\n")
	subTitle := fmt.Sprintf("From: %s | Version: %s | loss+rtt+avg+jitter+history", m.hostname, m.version)
	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, dimStyle.Render(truncWidth(subTitle, m.width))) + "\n\n")

	header := strings.Repeat(" ", indW) + l.render(rowCells{
		name: "HOSTNAME", addr: "ADDRESS", loss: "LOSS",
		rtt: "RTT(ms)", avg: "AVG(ms)", jit: "JIT(ms)", snt: "SNT ",
	})
	if l.hist > 0 {
		header += strings.Repeat(" ", histGapW) + "HISTORY"
	}
	s.WriteString(headerStyle.Render(truncWidth(header, m.width)) + "\n")
	s.WriteString(dimStyle.Render(strings.Repeat("─", m.width)) + "\n")

	devices := m.cfg.Devices
	off := m.clampOffset(m.offset)
	end := min(off+m.visibleRows(), len(devices))
	for _, d := range devices[off:end] {
		if d.IsSeparator() {
			s.WriteString(dimStyle.Render(strings.Repeat(" ", indW)+strings.Repeat("-", max(m.width-indW, 0))) + "\n")
			continue
		}
		s.WriteString(renderRow(l, d.Snapshot()) + "\n")
	}

	footer := fmt.Sprintf(" Interval: %s | App Jitter: %.f%% | *: Native | Window: %d | FAIL: latest probe failed",
		m.cfg.Interval, m.cfg.Jitter*100, monitor.WindowSize)
	if off > 0 || end < len(devices) {
		footer = fmt.Sprintf(" Rows %d-%d/%d (↑↓ PgUp PgDn) |", off+1, end, len(devices)) + footer
	}
	s.WriteString("\n" + dimStyle.Render(truncWidth(footer, m.width)))
	return s.String()
}

func renderRow(l layout, ds monitor.Snapshot) string {
	indicator := strings.Repeat(" ", indW)
	if ds.Loading {
		indicator = arrowStyle.Render("> ")
	}

	name, ip := ds.Name, ds.IP
	if name == "" {
		name = "resolving..."
	}
	if ip == "" {
		ip = "resolving..."
	}

	rtt := fmtMs(ds.LastRTT, ds.HasRTT)
	if ds.Snt > 0 && !ds.LastOK {
		rtt = "FAIL"
	}
	tag := " "
	if ds.Native {
		tag = "*"
	}
	snt := "-" + tag
	if ds.Snt > 0 {
		snt = fmt.Sprintf("%d%s", ds.Snt, tag)
	}

	line := l.render(rowCells{
		name: name,
		addr: ip,
		loss: formatLoss(ds.LossRate),
		rtt:  rtt,
		avg:  fmtMs(ds.AvgRTT, ds.HasRTT),
		jit:  fmtMs(ds.Jit, ds.HasRTT),
		snt:  snt,
	})

	var hist strings.Builder
	if l.hist > 0 {
		hist.WriteString(strings.Repeat(" ", histGapW))
		if ds.DNSFail {
			hist.WriteString(downStyle.Render(truncWidth("DNS RESOLVE FAILED, retrying", l.hist)))
		} else {
			hist.WriteString(renderHistory(ds, l.hist))
		}
	}

	if ds.DNSFail || (ds.Snt > 0 && !ds.LastOK) {
		line = failRowStyle.Render(line)
	}
	return indicator + line + hist.String()
}
