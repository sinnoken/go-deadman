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

// sleepCtx 等待 d；ctx 先被取消時回傳 false
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// randOffset 讓各目標的發送時間錯開
func randOffset(interval time.Duration) time.Duration {
	// #nosec G404 -- 只用於錯開時間，不涉及安全
	return time.Duration(rand.Float64() * float64(interval))
}

// ---------------------------------------------------------
// DNS 解析：名稱解析失敗時以退避方式持續重試
// ---------------------------------------------------------

// 可於測試替換
var (
	dnsRetryMin = 5 * time.Second
	dnsRetryMax = 60 * time.Second

	lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	lookupAddr = net.DefaultResolver.LookupAddr
)

func nextBackoff(cur time.Duration) time.Duration {
	if cur < dnsRetryMin {
		return dnsRetryMin
	}
	return min(cur*2, dnsRetryMax)
}

func runDevice(ctx context.Context, d *Device, interval time.Duration, jitter float64) {
	d.mu.RLock()
	name, ip := d.Name, d.IP
	d.mu.RUnlock()

	if name == "" && ip != "" {
		if names, err := lookupAddr(ctx, ip); err == nil && len(names) > 0 {
			name = strings.TrimSuffix(names[0], ".")
		} else {
			name = ip
		}
	}

	var backoff time.Duration
	for ip == "" {
		if ips, err := lookupIP(ctx, name); err == nil && len(ips) > 0 {
			ip = ips[0].String()
			break
		}
		d.mu.Lock()
		d.Name, d.IP, d.IsDNSFail, d.Loading = name, "DNS_FAIL", true, false
		d.mu.Unlock()
		signalUpdate()

		backoff = nextBackoff(backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
	}

	d.mu.Lock()
	d.Name, d.IP, d.IsDNSFail, d.Loading = name, ip, false, true
	d.mu.Unlock()
	signalUpdate()

	deviceWorker(ctx, d, ip, interval, jitter)
}

func deviceWorker(ctx context.Context, d *Device, ip string, interval time.Duration, jitter float64) {
	if !sleepCtx(ctx, randOffset(interval)) {
		return
	}
	if runNative(ctx, d, ip, interval) {
		return
	}
	fallbackWorker(ctx, d, ip, interval, jitter)
}

// ---------------------------------------------------------
// Native：持續發送的 ICMP session，以序號結算每個探測
// ---------------------------------------------------------

// echoSession 是 native ICMP 實作的抽象，讓 worker 主迴圈可以用假物件測試。
// Run 會持續發送直到 Stop 或發生錯誤；每次送出與收到回覆都要呼叫對應的 handler。
type echoSession interface {
	Run() error
	Stop()
}

type echoHandlers struct {
	onSend func(seq int)
	onRecv func(seq int, rtt time.Duration)
}

// newEchoSession 可於測試替換
var newEchoSession = newProBingSession

func newProBingSession(ip string, interval time.Duration, h echoHandlers) (echoSession, error) {
	p, err := probing.NewPinger(ip)
	if err != nil {
		return nil, err
	}
	p.Count = -1
	p.Interval = interval
	p.RecordRtts = false // 無窮發送時避免 RTT 陣列無限增長
	p.SetPrivileged(runtime.GOOS == "windows")
	p.OnSend = func(pkt *probing.Packet) { h.onSend(pkt.Seq) }
	p.OnRecv = func(pkt *probing.Packet) { h.onRecv(pkt.Seq, pkt.Rtt) }
	return p, nil
}

// probeTracker 以序號追蹤在途探測，確保每個序號最多結算一次：
// 先回覆的記成功，逾時的記丟包，逾時後才到的回覆直接丟棄。
// 以 map 為 key，序號在 65535 繞回時也不受影響。
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

// runNative 回傳 true 表示因 ctx 取消而正常結束；false 表示 native 無法使用，應改用 fallback
func runNative(ctx context.Context, d *Device, ip string, interval time.Duration) bool {
	// 送出與回覆走同一個 channel，保證 sent 一定先於對應的 recv 被處理
	events := make(chan probeEvent, 100)
	sess, err := newEchoSession(ip, interval, echoHandlers{
		onSend: func(seq int) { events <- probeEvent{seq: seq} },
		onRecv: func(seq int, rtt time.Duration) {
			events <- probeEvent{seq: seq, recv: true, rtt: float64(rtt) / float64(time.Millisecond)}
		},
	})
	if err != nil {
		return false
	}

	// Run 結束（不論 error 為何）只代表 session 停了；是取消造成或 native 不可用，由 ctx 判斷
	runDone := make(chan struct{})
	go func() {
		_ = sess.Run()
		close(runDone)
	}()

	tracker := newProbeTracker()
	timeout := probeTimeout(interval)
	ticker := time.NewTicker(interval / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sess.Stop()
			for { // 持續排空 events，避免 Run 卡在 handler 上
				select {
				case <-runDone:
					return true
				case <-events:
				}
			}

		case <-runDone:
			// 無窮模式下 Run 自行結束即代表 native 無法使用
			return false

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

// runPing 執行一次系統 ping 並回傳 stdout；可於測試替換
var runPing = func(ctx context.Context, ip string, timeout time.Duration) string {
	cctx, cancel := context.WithTimeout(ctx, timeout+500*time.Millisecond)
	defer cancel()
	// #nosec G204 -- ip 已通過 net.ParseIP 驗證
	cmd := exec.CommandContext(cctx, "ping", pingArgs(runtime.GOOS, ip, timeout)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run()
	return out.String()
}

func fallbackWorker(ctx context.Context, d *Device, ip string, interval time.Duration, jitter float64) {
	timeout := probeTimeout(interval)
	valid := net.ParseIP(ip) != nil

	for {
		start := time.Now()

		d.mu.Lock()
		d.Loading = true
		d.mu.Unlock()
		signalUpdate()

		if valid {
			rtt, ok := parseRTT(runPing(ctx, ip, timeout))
			if ctx.Err() != nil {
				return
			}
			d.UpdateStats(rtt, ok, false)
		} else {
			d.UpdateStats(0, false, false)
		}

		baseWait := max(float64(interval-time.Since(start)), 0)
		// #nosec G404 -- 只用於錯開時間，不涉及安全
		wait := time.Duration(baseWait * (1 + (rand.Float64()*2-1)*jitter))
		if !sleepCtx(ctx, wait) {
			return
		}
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
