package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const RENDER_FPS = 20 // 重繪頻率上限

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
// 按需重繪：資料有變才通知 UI，並以 RENDER_FPS 為上限
// ---------------------------------------------------------

var updateCh = make(chan struct{}, 1)

// signalUpdate 不會阻塞；多次更新會合併成一次重繪
func signalUpdate() {
	select {
	case updateCh <- struct{}{}:
	default:
	}
}

type dataMsg struct{}
type frameMsg struct{}

func waitForUpdate() tea.Cmd {
	return func() tea.Msg {
		<-updateCh
		return dataMsg{}
	}
}

type model struct {
	cfg      Config
	devices  []*Device
	width    int
	height   int
	hostname string
}

func (m model) Init() tea.Cmd {
	for _, d := range m.devices {
		if !d.isSep {
			go resolveAndStartWorker(d, m.cfg.interval, m.cfg.Jitter)
		}
	}
	return waitForUpdate()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case dataMsg:
		// 本次已重繪；等一個 frame 再接收下一批更新
		return m, tea.Tick(time.Second/RENDER_FPS, func(time.Time) tea.Msg { return frameMsg{} })
	case frameMsg:
		return m, waitForUpdate()
	}
	return m, nil
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
		l.hist = min(free-histGapW, HIST_SIZE)
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

// ---------------------------------------------------------
// 畫面渲染
// ---------------------------------------------------------

type deviceSnapshot struct {
	name, ip                         string
	loading, native, dnsFail, lastOK bool
	hasRTT                           bool
	lossRate, lastRTT, avgRTT, jit   float64
	snt, histCount, histIdx          int
	history                          [HIST_SIZE]Heartbeat
}

func (d *Device) snapshot() deviceSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return deviceSnapshot{
		name:      d.Name,
		ip:        d.IP,
		loading:   d.Loading,
		native:    d.IsNative,
		dnsFail:   d.IsDNSFail,
		lastOK:    d.LastOK,
		hasRTT:    len(d.Window) > 0,
		lossRate:  d.LossRate,
		lastRTT:   d.LastRTT,
		avgRTT:    d.AvgRTT,
		jit:       d.Jitter,
		snt:       d.Snt,
		histCount: d.HistCount,
		histIdx:   d.HistoryIdx,
		history:   d.History,
	}
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

func (m model) View() string {
	if m.height == 0 || m.width == 0 {
		return " Loading..."
	}
	l := computeLayout(m.width)
	var s strings.Builder

	s.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, titleStyle.Render("go-deadman")) + "\n")
	subTitle := fmt.Sprintf("From: %s | Version: %s | loss+rtt+avg+jitter+history", m.hostname, version)
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

	for _, d := range m.devices {
		if d.isSep {
			s.WriteString(dimStyle.Render(strings.Repeat(" ", indW)+strings.Repeat("-", max(m.width-indW, 0))) + "\n")
			continue
		}
		s.WriteString(m.renderRow(l, d.snapshot()) + "\n")
	}

	footer := fmt.Sprintf(" Interval: %s | App Jitter: %.f%% | *: Native | Window: %d | RTT FAIL: 最近一次探測失敗",
		m.cfg.Interval, m.cfg.Jitter*100, WINDOW_SIZE)
	s.WriteString("\n" + dimStyle.Render(truncWidth(footer, m.width)))
	return s.String()
}

func (m model) renderRow(l layout, ds deviceSnapshot) string {
	indicator := strings.Repeat(" ", indW)
	if ds.loading {
		indicator = arrowStyle.Render("> ")
	}

	name, ip := ds.name, ds.ip
	if name == "" {
		name = "resolving..."
	}
	if ip == "" {
		ip = "resolving..."
	}

	rtt := fmtMs(ds.lastRTT, ds.hasRTT)
	if ds.snt > 0 && !ds.lastOK {
		rtt = "FAIL"
	}
	tag := " "
	if ds.native {
		tag = "*"
	}
	snt := "-" + tag
	if ds.snt > 0 {
		snt = fmt.Sprintf("%d%s", ds.snt, tag)
	}

	line := l.render(rowCells{
		name: name,
		addr: ip,
		loss: formatLoss(ds.lossRate),
		rtt:  rtt,
		avg:  fmtMs(ds.avgRTT, ds.hasRTT),
		jit:  fmtMs(ds.jit, ds.hasRTT),
		snt:  snt,
	})

	var hist strings.Builder
	if l.hist > 0 {
		hist.WriteString(strings.Repeat(" ", histGapW))
		if ds.dnsFail {
			hist.WriteString(downStyle.Render(truncWidth("DNS RESOLVE FAILED", l.hist)))
		} else {
			for j := 1; j <= min(ds.histCount, l.hist); j++ {
				h := ds.history[(ds.histIdx-j+HIST_SIZE)%HIST_SIZE]
				if h.Success {
					hist.WriteString(upStyle.Render(h.Char))
				} else {
					hist.WriteString(downStyle.Render(h.Char))
				}
			}
		}
	}

	if ds.dnsFail || (ds.snt > 0 && !ds.lastOK) {
		line = failRowStyle.Render(line)
	}
	return indicator + line + hist.String()
}
