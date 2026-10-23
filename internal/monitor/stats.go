package monitor

import (
	"math"
	"sync"
)

const (
	HistSize   = 100 // sparkline 歷史筆數
	WindowSize = 50  // AvgRTT 與 Jitter 的滑動視窗大小
)

type Heartbeat struct {
	Char    string
	Success bool
}

type Device struct {
	mu   sync.RWMutex // 讀寫鎖，分離「資料更新」與「畫面渲染」
	Name string       `yaml:"name"`
	IP   string       `yaml:"ip"`

	isSep bool // 分隔列；由 LoadConfig 在 worker 啟動前設定，之後不再變動

	Loss     int
	Snt      int // 已結算（成功或逾時）的探測數，也是 LossRate 的分母
	LossRate float64

	LastRTT float64 // 最近一次「成功」的 RTT
	LastOK  bool    // 最近一次探測是否成功

	Window    []float64 // 最近 WindowSize 筆成功 RTT
	WindowSum float64   // 增量維護，AvgRTT 為 O(1)
	AvgRTT    float64

	JitWindow []float64 // 最近 WindowSize 筆相鄰成功 RTT 差值
	JitSum    float64
	Jitter    float64
	prevRTT   float64
	prevOK    bool

	History    [HistSize]Heartbeat
	HistoryIdx int
	HistCount  int

	Loading   bool
	IsNative  bool
	IsDNSFail bool
}

// IsSeparator 不需加鎖：isSep 只在 worker 啟動前寫入
func (d *Device) IsSeparator() bool {
	return d.isSep
}

// UpdateStats 是 native 與 fallback 兩條 ping 路徑唯一的結果寫入點，
// 確保兩者的丟包、平均與 jitter 規則一致。
//
// jitter 定義為「相鄰兩次成功探測 RTT 差值的平均絕對值」；
// 中間有丟包時不跨越配對。
func (d *Device) UpdateStats(rtt float64, success bool, isNative bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.IsNative = isNative
	d.Loading = false

	d.Snt++
	d.LastOK = success
	if !success {
		d.Loss++
		d.prevOK = false
	} else {
		d.LastRTT = rtt

		if len(d.Window) >= WindowSize {
			d.WindowSum -= d.Window[0]
			d.Window = d.Window[1:]
		}
		d.Window = append(d.Window, rtt)
		d.WindowSum += rtt
		d.AvgRTT = d.WindowSum / float64(len(d.Window))

		if d.prevOK {
			diff := math.Abs(rtt - d.prevRTT)
			if len(d.JitWindow) >= WindowSize {
				d.JitSum -= d.JitWindow[0]
				d.JitWindow = d.JitWindow[1:]
			}
			d.JitWindow = append(d.JitWindow, diff)
			d.JitSum += diff
			d.Jitter = d.JitSum / float64(len(d.JitWindow))
		}
		d.prevRTT, d.prevOK = rtt, true
	}
	d.LossRate = float64(d.Loss) * 100.0 / float64(d.Snt)

	d.History[d.HistoryIdx] = Heartbeat{Char: getLogChar(rtt, success), Success: success}
	d.HistoryIdx = (d.HistoryIdx + 1) % HistSize
	if d.HistCount < HistSize {
		d.HistCount++
	}

	signalUpdate()
}

// Snapshot 是 Device 在某一刻的唯讀複本，供畫面渲染使用
type Snapshot struct {
	Name, IP                         string
	Loading, Native, DNSFail, LastOK bool
	HasRTT                           bool
	LossRate, LastRTT, AvgRTT, Jit   float64
	Snt, HistCount, HistIdx          int
	History                          [HistSize]Heartbeat
}

func (d *Device) Snapshot() Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return Snapshot{
		Name:      d.Name,
		IP:        d.IP,
		Loading:   d.Loading,
		Native:    d.IsNative,
		DNSFail:   d.IsDNSFail,
		LastOK:    d.LastOK,
		HasRTT:    len(d.Window) > 0,
		LossRate:  d.LossRate,
		LastRTT:   d.LastRTT,
		AvgRTT:    d.AvgRTT,
		Jit:       d.Jitter,
		Snt:       d.Snt,
		HistCount: d.HistCount,
		HistIdx:   d.HistoryIdx,
		History:   d.History,
	}
}

// Recent 回傳倒數第 j 筆歷史（j=1 為最新）
func (s Snapshot) Recent(j int) Heartbeat {
	return s.History[(s.HistIdx-j+HistSize)%HistSize]
}

// getLogChar 以對數刻度把 RTT 對應到 sparkline 字元，底數 2.4 讓約 500ms 落在最高階
func getLogChar(rtt float64, success bool) string {
	if !success || rtt <= 0 {
		return "·"
	}

	const base = 2.4
	idx := int(math.Log(math.Max(1, rtt)) / math.Log(base))

	scales := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if idx >= len(scales) {
		idx = len(scales) - 1
	}
	return scales[idx]
}

// ---------------------------------------------------------
// 資料變更通知：多次更新合併成一次，供 UI 按需重繪
// ---------------------------------------------------------

var updateCh = make(chan struct{}, 1)

func signalUpdate() {
	select {
	case updateCh <- struct{}{}:
	default:
	}
}

// Updates 在任何 Device 資料變更後可讀；不保證每次變更各一則
func Updates() <-chan struct{} {
	return updateCh
}
