//go:build windows

package monitor

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWindowsSessionLoopback(t *testing.T) {
	var mu sync.Mutex
	var sent, recv []int
	var rtts []time.Duration
	sess, err := newWindowsSession("127.0.0.1", 100*time.Millisecond, echoHandlers{
		onSend: func(seq int) { mu.Lock(); sent = append(sent, seq); mu.Unlock() },
		onRecv: func(seq int, rtt time.Duration) {
			mu.Lock()
			recv = append(recv, seq)
			rtts = append(rtts, rtt)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sess.(*icmpSession); !ok {
		t.Fatalf("IPv4 should use IcmpSendEcho, got %T", sess)
	}

	done := make(chan error, 1)
	go func() { done <- sess.Run() }()
	time.Sleep(450 * time.Millisecond)
	sess.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Stop")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) < 3 || len(recv) < 3 {
		t.Fatalf("sent=%v recv=%v", sent, recv)
	}
	for i, seq := range sent {
		if seq != i {
			t.Fatalf("sequence not consecutive: %v", sent)
		}
	}
	for _, r := range rtts {
		if r <= 0 || r > 100*time.Millisecond {
			t.Errorf("loopback rtt out of range: %v", r)
		}
	}
}

func TestWindowsSessionIPv6UsesProBing(t *testing.T) {
	sess, err := newWindowsSession("::1", time.Second, echoHandlers{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sess.(*icmpSession); ok {
		t.Error("IPv6 should not use the IPv4 IcmpSendEcho session")
	}
}

func TestWindowsNativeWorkerEndToEnd(t *testing.T) {
	d := &Device{}
	goWorker(t, func(ctx context.Context) { runNative(ctx, d, "127.0.0.1", 100*time.Millisecond) })
	waitFor(t, d, 3*time.Second, func(d *Device) bool { return d.Snt >= 3 && d.IsNative && d.Loss == 0 })
}
