package monitor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// 這些測試會替換套件層級的 newEchoSession / runPing / lookup*，因此不可平行執行

const testInterval = 100 * time.Millisecond

// fakeSession 依 script 呼叫 handler，之後阻塞到 Stop；runErr 非 nil 時 Run 立即回傳該錯誤
type fakeSession struct {
	h       echoHandlers
	script  func(h echoHandlers)
	runErr  error
	stop    chan struct{}
	stopped sync.Once
}

func (f *fakeSession) Run() error {
	if f.runErr != nil {
		return f.runErr
	}
	if f.script != nil {
		f.script(f.h)
	}
	<-f.stop
	return nil
}

func (f *fakeSession) Stop() { f.stopped.Do(func() { close(f.stop) }) }

func useFakeSession(t *testing.T, script func(h echoHandlers), runErr error) {
	t.Helper()
	orig := newEchoSession
	t.Cleanup(func() { newEchoSession = orig })
	newEchoSession = func(_ string, _ time.Duration, h echoHandlers) (echoSession, error) {
		return &fakeSession{h: h, script: script, runErr: runErr, stop: make(chan struct{})}, nil
	}
}

func useFakePing(t *testing.T, out string) {
	t.Helper()
	orig := runPing
	t.Cleanup(func() { runPing = orig })
	runPing = func(ctx context.Context, _ string, _ time.Duration) string {
		if ctx.Err() != nil {
			return ""
		}
		return out
	}
}

// goWorker 在背景執行 fn；cleanup 時先取消並等它結束，之後才還原被替換的全域函式
// （t.Cleanup 為後進先出，所以必須在 useFake* 之後呼叫）
func goWorker(t *testing.T, fn func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		fn(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("worker did not stop after cancel")
		}
	})
}

// waitFor 輪詢直到 cond 成立或逾時
func waitFor(t *testing.T, d *Device, timeout time.Duration, cond func(*Device) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.mu.RLock()
		ok := cond(d)
		d.mu.RUnlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	t.Fatalf("condition not met: Snt=%d Loss=%d LastOK=%v IsNative=%v IsDNSFail=%v Name=%q IP=%q",
		d.Snt, d.Loss, d.LastOK, d.IsNative, d.IsDNSFail, d.Name, d.IP)
}

func TestNativeSettlesEachSequenceOnce(t *testing.T) {
	useFakeSession(t, func(h echoHandlers) {
		h.onSend(0)
		h.onRecv(0, 10*time.Millisecond)
		h.onSend(1) // 永遠沒有回覆 → 逾時丟包
		h.onSend(2)
		h.onRecv(2, 30*time.Millisecond)
		h.onRecv(2, 30*time.Millisecond) // 重複回覆不可再計
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Device{}
	done := make(chan bool)
	go func() { done <- runNative(ctx, d, "192.0.2.1", testInterval) }()

	waitFor(t, d, 3*time.Second, func(d *Device) bool { return d.Snt == 3 })
	d.mu.RLock()
	if d.Loss != 1 || !d.IsNative || d.AvgRTT != 20 {
		t.Errorf("Loss=%d IsNative=%v AvgRTT=%v", d.Loss, d.IsNative, d.AvgRTT)
	}
	d.mu.RUnlock()

	cancel()
	if !<-done {
		t.Error("runNative should report clean shutdown after cancel")
	}
}

func TestNativeIgnoresLateReply(t *testing.T) {
	useFakeSession(t, func(h echoHandlers) {
		h.onSend(0)
		time.Sleep(probeTimeout(testInterval) + 300*time.Millisecond)
		h.onRecv(0, 1500*time.Millisecond)
	}, nil)

	d := &Device{}
	goWorker(t, func(ctx context.Context) { runNative(ctx, d, "192.0.2.1", testInterval) })

	waitFor(t, d, 3*time.Second, func(d *Device) bool { return d.Snt == 1 })
	time.Sleep(500 * time.Millisecond) // 晚到的回覆送達後，數字不應改變
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.Snt != 1 || d.Loss != 1 || d.LastOK {
		t.Errorf("late reply counted: Snt=%d Loss=%d LastOK=%v", d.Snt, d.Loss, d.LastOK)
	}
}

func TestNativeFailureFallsBackToSystemPing(t *testing.T) {
	useFakeSession(t, nil, errors.New("socket: permission denied"))
	useFakePing(t, "Reply from 192.0.2.1: bytes=32 time=7ms TTL=64")

	d := &Device{}
	goWorker(t, func(ctx context.Context) { deviceWorker(ctx, d, "192.0.2.1", testInterval, 0.1) })

	waitFor(t, d, 2*time.Second, func(d *Device) bool { return d.Snt >= 2 })
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.IsNative || d.LastRTT != 7 || d.Loss != 0 {
		t.Errorf("IsNative=%v LastRTT=%v Loss=%d", d.IsNative, d.LastRTT, d.Loss)
	}
}

func TestSessionCreateErrorFallsBack(t *testing.T) {
	orig := newEchoSession
	t.Cleanup(func() { newEchoSession = orig })
	newEchoSession = func(string, time.Duration, echoHandlers) (echoSession, error) {
		return nil, errors.New("no icmp")
	}
	useFakePing(t, "Request timed out.")

	d := &Device{}
	goWorker(t, func(ctx context.Context) { deviceWorker(ctx, d, "192.0.2.1", testInterval, 0.1) })

	waitFor(t, d, 2*time.Second, func(d *Device) bool { return d.Snt >= 1 && d.Loss == d.Snt && !d.IsNative })
}

func TestFallbackInvalidIPCountsAsLoss(t *testing.T) {
	useFakePing(t, "time=1ms")
	d := &Device{}
	goWorker(t, func(ctx context.Context) { fallbackWorker(ctx, d, "not-an-ip", testInterval, 0.1) })
	waitFor(t, d, 2*time.Second, func(d *Device) bool { return d.Snt >= 1 && d.Loss == d.Snt })
}

func TestDNSRetryUntilResolved(t *testing.T) {
	origMin, origMax, origLookup := dnsRetryMin, dnsRetryMax, lookupIP
	t.Cleanup(func() { dnsRetryMin, dnsRetryMax, lookupIP = origMin, origMax, origLookup })
	dnsRetryMin, dnsRetryMax = 20*time.Millisecond, 40*time.Millisecond

	var mu sync.Mutex
	calls := 0
	lookupIP = func(context.Context, string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return nil, errors.New("no such host")
		}
		return []net.IP{net.ParseIP("192.0.2.9")}, nil
	}
	useFakeSession(t, func(h echoHandlers) {
		h.onSend(0)
		h.onRecv(0, time.Millisecond)
	}, nil)

	d := &Device{Name: "nas.lan"}
	goWorker(t, func(ctx context.Context) { runDevice(ctx, d, testInterval, 0.1) })

	waitFor(t, d, 2*time.Second, func(d *Device) bool { return d.IsDNSFail && d.IP == "DNS_FAIL" })
	waitFor(t, d, 2*time.Second, func(d *Device) bool { return !d.IsDNSFail && d.IP == "192.0.2.9" && d.Snt == 1 })
}

func TestReverseLookupFillsName(t *testing.T) {
	origAddr := lookupAddr
	t.Cleanup(func() { lookupAddr = origAddr })
	lookupAddr = func(context.Context, string) ([]string, error) { return []string{"router.lan."}, nil }
	useFakeSession(t, nil, nil)

	d := &Device{IP: "192.0.2.1"}
	goWorker(t, func(ctx context.Context) { runDevice(ctx, d, testInterval, 0.1) })
	waitFor(t, d, time.Second, func(d *Device) bool { return d.Name == "router.lan" && d.Loading })
}

func TestReverseLookupFailureUsesIP(t *testing.T) {
	origAddr := lookupAddr
	t.Cleanup(func() { lookupAddr = origAddr })
	lookupAddr = func(context.Context, string) ([]string, error) { return nil, errors.New("nxdomain") }
	useFakeSession(t, nil, nil)

	d2 := &Device{IP: "192.0.2.2"}
	goWorker(t, func(ctx context.Context) { runDevice(ctx, d2, testInterval, 0.1) })
	waitFor(t, d2, time.Second, func(d *Device) bool { return d.Name == "192.0.2.2" })
}

func TestStartStopsAllWorkers(t *testing.T) {
	useFakeSession(t, nil, nil)
	cfg, err := LoadConfig([]byte(`
interval: 100ms
devices:
  - {name: a, ip: 192.0.2.1}
  - name: "---"
  - {name: b, ip: 192.0.2.2}
`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := cfg.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not stop after cancel")
	}
}

func TestFallbackStopsOnCancel(t *testing.T) {
	orig := runPing
	t.Cleanup(func() { runPing = orig })
	runPing = func(ctx context.Context, _ string, _ time.Duration) string {
		<-ctx.Done() // 模擬卡住的 ping 子行程，只能靠取消結束
		return ""
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		fallbackWorker(ctx, &Device{}, "192.0.2.1", testInterval, 0.1)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fallbackWorker did not stop")
	}
}

func TestBackoffAndSleep(t *testing.T) {
	if nextBackoff(0) != dnsRetryMin || nextBackoff(dnsRetryMin) != 2*dnsRetryMin || nextBackoff(dnsRetryMax) != dnsRetryMax {
		t.Error("backoff sequence wrong")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !sleepCtx(ctx, time.Millisecond) {
		t.Error("sleepCtx should complete")
	}
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Error("sleepCtx should abort on cancel")
	}
	if off := randOffset(time.Second); off < 0 || off >= time.Second {
		t.Errorf("randOffset = %v", off)
	}
}

func TestNewProBingSessionRejectsBadAddress(t *testing.T) {
	if _, err := newProBingSession("not a host name!", time.Second, echoHandlers{}); err == nil {
		t.Error("expected error for unresolvable address")
	}
}
