package main

import (
	"slices"
	"testing"
	"time"
)

func TestParseRTT(t *testing.T) {
	cases := []struct {
		out  string
		rtt  float64
		want bool
	}{
		{"64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=12.3 ms", 12.3, true},
		{"Reply from 8.8.8.8: bytes=32 time=15ms TTL=117", 15, true},
		{"Reply from 192.168.1.1: bytes=32 time<1ms TTL=64", 1, true},
		{"回覆自 8.8.8.8: 位元組=32 時間=12ms TTL=117", 12, true},
		{"回覆自 192.168.1.1: 位元組=32 時間<1ms TTL=64", 1, true},
		{"Request timed out.", 0, false},
		{"要求等候逾時。", 0, false},
		{"time=abc ms", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		rtt, ok := parseRTT(c.out)
		if ok != c.want || rtt != c.rtt {
			t.Errorf("parseRTT(%q) = %v, %v; want %v, %v", c.out, rtt, ok, c.rtt, c.want)
		}
	}
}

func TestProbeTracker(t *testing.T) {
	tr := newProbeTracker()
	t0 := time.Unix(0, 0)
	timeout := time.Second

	tr.sent(0, t0)
	tr.sent(1, t0)
	tr.sent(2, t0.Add(500*time.Millisecond))

	if !tr.recv(1) {
		t.Fatal("in-flight reply should count")
	}
	if tr.recv(1) {
		t.Error("duplicate reply must not count twice")
	}
	if tr.recv(7) {
		t.Error("reply for unknown seq must not count")
	}
	if n := tr.expire(t0.Add(900*time.Millisecond), timeout); n != 0 {
		t.Errorf("expired %d before timeout", n)
	}
	if n := tr.expire(t0.Add(timeout), timeout); n != 1 {
		t.Errorf("expired %d, want 1 (seq 0)", n)
	}
	if tr.recv(0) {
		t.Error("late reply after timeout must not count")
	}
	if !tr.recv(2) {
		t.Error("seq 2 still in flight")
	}
	if n := tr.expire(t0.Add(time.Hour), timeout); n != 0 {
		t.Errorf("nothing should remain, expired %d", n)
	}
}

func TestProbeTrackerSeqWrap(t *testing.T) {
	tr := newProbeTracker()
	now := time.Now()
	tr.sent(65535, now)
	tr.sent(0, now)
	if !tr.recv(0) || !tr.recv(65535) {
		t.Error("wrapped sequence numbers should be tracked independently")
	}
}

func TestProbeTimeout(t *testing.T) {
	if probeTimeout(200*time.Millisecond) != time.Second || probeTimeout(3*time.Second) != 3*time.Second {
		t.Error("probeTimeout should be max(interval, 1s)")
	}
}

func TestPingArgs(t *testing.T) {
	ip := "192.0.2.1"
	cases := map[string][]string{
		"windows": {"-n", "1", "-w", "1500", ip},
		"darwin":  {"-c", "1", "-W", "1500", ip},
		"linux":   {"-c", "1", "-W", "2", ip},
	}
	for goos, want := range cases {
		if got := pingArgs(goos, ip, 1500*time.Millisecond); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", goos, got, want)
		}
	}
}
