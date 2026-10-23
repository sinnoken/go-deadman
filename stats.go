package main

import (
	"fmt"
	"math"
	"sync"
)

const (
	HIST_SIZE   = 100
	WINDOW_SIZE = 50
)

type Heartbeat struct {
	Char    string
	Success bool
}

type Device struct {
	mu   sync.RWMutex // 讀寫鎖，分離「資料更新」與「畫面渲染」
	Name string       `yaml:"name"`
	IP   string       `yaml:"ip"`

	isSep bool // 分隔列；由 loadConfig 在 worker 啟動前設定，之後不再變動

	Loss     int
	Snt      int // 已結算（成功或逾時）的探測數，也是 LossRate 的分母
	LossRate float64

	LastRTT float64 // 最近一次「成功」的 RTT
	LastOK  bool    // 最近一次探測是否成功

	Window    []float64 // 最近 WINDOW_SIZE 筆成功 RTT
	WindowSum float64   // 增量維護，AvgRTT 為 O(1)
	AvgRTT    float64

	JitWindow []float64 // 最近 WINDOW_SIZE 筆相鄰成功 RTT 差值
	JitSum    float64
	Jitter    float64
	prevRTT   float64
	prevOK    bool

	History    [HIST_SIZE]Heartbeat
	HistoryIdx int
	HistCount  int

	Loading   bool
	IsNative  bool
	IsDNSFail bool
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

		if len(d.Window) >= WINDOW_SIZE {
			d.WindowSum -= d.Window[0]
			d.Window = d.Window[1:]
		}
		d.Window = append(d.Window, rtt)
		d.WindowSum += rtt
		d.AvgRTT = d.WindowSum / float64(len(d.Window))

		if d.prevOK {
			diff := math.Abs(rtt - d.prevRTT)
			if len(d.JitWindow) >= WINDOW_SIZE {
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
	d.HistoryIdx = (d.HistoryIdx + 1) % HIST_SIZE
	if d.HistCount < HIST_SIZE {
		d.HistCount++
	}

	signalUpdate()
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
