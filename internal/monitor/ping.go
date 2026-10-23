package monitor

import (
	"bytes"
	"context"
	"math"
	"math/rand"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// probeTimeout 是 native 與 fallback 共用的單次探測逾時
func probeTimeout(interval time.Duration) time.Duration {
	return max(interval, time.Second)
}

func resolveAndStartWorker(d *Device, interval time.Duration, jitter float64) {
	d.mu.RLock()
	name, ip := d.Name, d.IP
	d.mu.RUnlock()

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

	d.mu.Lock()
	d.Name = name
	d.IP = ip
	d.IsDNSFail = isFail
	d.Loading = !isFail
	d.mu.Unlock()
	signalUpdate()

	if !isFail {
		deviceWorker(d, ip, interval, jitter)
	}
}

// ---------------------------------------------------------
// Native：pro-bing 無窮發送模式重用同一個 socket
// ---------------------------------------------------------

// probeTracker 以序號追蹤在途探測，確保每個序號最多結算一次：
// 先回覆的記成功，逾時的記丟包，逾時後才到的回覆直接丟棄。
// 以 map 為 key，pro-bing 序號在 65535 繞回時也不受影響。
type probeTracker struct {
	pending map[int]time.Time
}

func newProbeTracker() *probeTracker {
	return &probeTracker{pending: make(map[int]time.Time)}
}

func (t *probeTracker) sent(seq int, at time.Time) {
	t.pending[seq] = at
}

// recv 回傳該序號是否仍在途中（true 表示應記為成功）
func (t *probeTracker) recv(seq int) bool {
	if _, ok := t.pending[seq]; !ok {
		return false
	}
	delete(t.pending, seq)
	return true
}

// expire 移除已逾時的在途探測，回傳應記為丟包的數量
func (t *probeTracker) expire(now time.Time, timeout time.Duration) int {
	n := 0
	for seq, at := range t.pending {
		if now.Sub(at) >= timeout {
			delete(t.pending, seq)
			n++
		}
	}
	return n
}

type probeEvent struct {
	seq  int
	recv bool
	rtt  float64
}

func deviceWorker(d *Device, ip string, interval time.Duration, jitter float64) {
	// #nosec G404
	time.Sleep(time.Duration(rand.Float64() * float64(interval)))

	pinger, err := probing.NewPinger(ip)
	if err != nil {
		fallbackWorker(d, ip, interval, jitter)
		return
	}
	pinger.Count = -1
	pinger.Interval = interval
	pinger.RecordRtts = false // 無窮發送時避免 RTT 陣列無限增長
	pinger.SetPrivileged(runtime.GOOS == "windows")

	// 送出與回覆走同一個 channel，保證 sent 一定先於對應的 recv 被處理
	events := make(chan probeEvent, 100)
	pinger.OnSend = func(pkt *probing.Packet) {
		events <- probeEvent{seq: pkt.Seq}
	}
	pinger.OnRecv = func(pkt *probing.Packet) {
		events <- probeEvent{seq: pkt.Seq, recv: true, rtt: float64(pkt.Rtt) / float64(time.Millisecond)}
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pinger.Run()
	}()

	tracker := newProbeTracker()
	timeout := probeTimeout(interval)
	ticker := time.NewTicker(interval / 2)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			// 無窮模式下 Run 結束即代表 native 無法使用（不論 err 是否為 nil），改用系統 ping
			_ = err
			fallbackWorker(d, ip, interval, jitter)
			return

		case ev := <-events:
			if !ev.recv {
				tracker.sent(ev.seq, time.Now())
			} else if tracker.recv(ev.seq) {
				d.UpdateStats(ev.rtt, true, true)
			}

		case now := <-ticker.C:
			for n := tracker.expire(now, timeout); n > 0; n-- {
				d.UpdateStats(0, false, true)
			}
		}
	}
}

// ---------------------------------------------------------
// Fallback：無 ICMP 權限時改呼叫系統 ping，保留自訂 jitter
// ---------------------------------------------------------

func pingArgs(goos, ip string, timeout time.Duration) []string {
	ms := strconv.FormatInt(timeout.Milliseconds(), 10)
	switch goos {
	case "windows":
		return []string{"-n", "1", "-w", ms, ip}
	case "darwin":
		return []string{"-c", "1", "-W", ms, ip} // macOS 的 -W 單位為毫秒
	default:
		sec := strconv.FormatFloat(math.Ceil(timeout.Seconds()), 'f', 0, 64)
		return []string{"-c", "1", "-W", sec, ip}
	}
}

func fallbackWorker(d *Device, ip string, interval time.Duration, jitter float64) {
	// #nosec G404
	time.Sleep(time.Duration(rand.Float64() * float64(interval)))

	timeout := probeTimeout(interval)
	var outBuf bytes.Buffer

	for {
		start := time.Now()

		d.mu.Lock()
		d.Loading = true
		d.mu.Unlock()
		signalUpdate()

		// resolve 階段已確保是 IP，這裡再次檢查以滿足 gosec
		if net.ParseIP(ip) == nil {
			d.UpdateStats(0, false, false)
			time.Sleep(interval)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout+500*time.Millisecond)
		// #nosec G204 -- ip 已通過 net.ParseIP 驗證
		cmd := exec.CommandContext(ctx, "ping", pingArgs(runtime.GOOS, ip, timeout)...)
		outBuf.Reset()
		cmd.Stdout = &outBuf
		_ = cmd.Run()
		cancel()

		rtt, success := parseRTT(outBuf.String())
		d.UpdateStats(rtt, success, false)

		baseWait := max(float64(interval-time.Since(start)), 0)
		// #nosec G404
		time.Sleep(time.Duration(baseWait * (1 + (rand.Float64()*2-1)*jitter)))
	}
}

// parseRTT 從系統 ping 輸出取出 RTT，支援英文（time= / time<）與中文（時間= / 時間<）
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
	res, err := strconv.ParseFloat(strings.TrimSpace(sub[:end]), 64)
	if err != nil {
		return 0, false
	}
	return res, true
}
