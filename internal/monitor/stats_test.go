package monitor

import (
	"math"
	"testing"
)

func TestUpdateStats(t *testing.T) {
	d := &Device{}
	for _, p := range []struct {
		rtt float64
		ok  bool
	}{{10, true}, {20, true}, {0, false}, {50, true}, {40, true}} {
		d.UpdateStats(p.rtt, p.ok, true)
	}

	if d.Snt != 5 || d.Loss != 1 || d.LossRate != 20 {
		t.Errorf("Snt/Loss/LossRate = %d/%d/%v", d.Snt, d.Loss, d.LossRate)
	}
	if d.AvgRTT != 30 {
		t.Errorf("AvgRTT = %v, want 30", d.AvgRTT)
	}
	// 只配對 10→20 與 50→40，不跨越中間的丟包（否則會含 |50-20|）
	if d.Jitter != 10 {
		t.Errorf("Jitter = %v, want 10", d.Jitter)
	}
	if !d.LastOK || d.LastRTT != 40 {
		t.Errorf("LastOK/LastRTT = %v/%v", d.LastOK, d.LastRTT)
	}

	d.UpdateStats(0, false, false)
	if d.LastOK || d.LastRTT != 40 || d.IsNative {
		t.Errorf("after failure LastOK/LastRTT/IsNative = %v/%v/%v", d.LastOK, d.LastRTT, d.IsNative)
	}
	if d.HistCount != 6 || d.History[(d.HistoryIdx-1+HistSize)%HistSize].Success {
		t.Errorf("history not updated: count=%d", d.HistCount)
	}
}

func TestUpdateStatsWindowSlides(t *testing.T) {
	d := &Device{}
	for i := 0; i < WindowSize+10; i++ {
		d.UpdateStats(float64(i%2)*10+5, true, true) // 5,15,5,15...
	}
	if len(d.Window) != WindowSize || len(d.JitWindow) != WindowSize {
		t.Fatalf("window sizes = %d/%d", len(d.Window), len(d.JitWindow))
	}
	if math.Abs(d.AvgRTT-10) > 1e-9 || math.Abs(d.Jitter-10) > 1e-9 {
		t.Errorf("AvgRTT/Jitter = %v/%v, want 10/10", d.AvgRTT, d.Jitter)
	}
}

func TestGetLogChar(t *testing.T) {
	if getLogChar(0, false) != "·" || getLogChar(10, false) != "·" {
		t.Error("failure should render as ·")
	}
	if getLogChar(0.5, true) != "▁" || getLogChar(10000, true) != "█" {
		t.Error("scale bounds wrong")
	}
}
