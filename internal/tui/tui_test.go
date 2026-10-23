package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go-deadman/internal/monitor"
)

func testModel(t *testing.T, width int) model {
	t.Helper()
	cfg, err := monitor.LoadConfig([]byte(`
devices:
  - name: "a-very-long-hostname-that-will-not-fit-anywhere.example.net"
    ip: "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff"
  - name: "中華電信-HiNet-骨幹"
    ip: "168.95.1.1"
  - name: "---"
  - name: "dns-fail"
`))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Devices
	for i := 0; i < 150; i++ {
		d[0].UpdateStats(float64(i), i%7 != 0, true)
	}
	d[1].UpdateStats(12345.678, true, false)
	d[1].UpdateStats(0, false, false)
	d[3].IsDNSFail = true
	d[3].Loading = false
	return model{cfg: cfg, width: width, height: 40, hostname: "test-host", version: "v0.0.0-test"}
}

func TestViewFitsWidth(t *testing.T) {
	for width := 20; width <= 200; width++ {
		view := testModel(t, width).View()
		for i, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: line %d is %d cells: %q", width, i, w, line)
			}
		}
		if !strings.Contains(view, "HOSTNAME") || !strings.Contains(view, "RTT(ms)") {
			t.Fatalf("width %d: HOSTNAME/RTT column missing", width)
		}
		if !strings.Contains(view, "FAIL") {
			t.Fatalf("width %d: failed latest probe not shown", width)
		}
		if width >= 60 && !strings.Contains(view, "12345.7") {
			t.Fatalf("width %d: large RTT truncated", width)
		}
	}
}

func TestComputeLayoutPriority(t *testing.T) {
	narrow := computeLayout(40)
	if narrow.name == 0 || narrow.hist != 0 || narrow.snt {
		t.Errorf("narrow layout = %+v", narrow)
	}
	wide := computeLayout(200)
	if !wide.loss || wide.addr == 0 || !wide.avg || !wide.jit || !wide.snt || wide.hist != monitor.HistSize {
		t.Errorf("wide layout = %+v", wide)
	}
	if wide.name > colMax || wide.addr > colMax {
		t.Errorf("columns exceed max: %+v", wide)
	}
}

func TestFitWidthCJK(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 5, "abc.."},
		{"中華電信", 5, "中.. "},
		{"中華", 4, "中華"},
		{"abcdef", 2, "ab"},
		{"abc", 0, ""},
	}
	for _, c := range cases {
		if got := fitWidth(c.in, c.w); got != c.want {
			t.Errorf("fitWidth(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

func TestFormatLoss(t *testing.T) {
	cases := map[float64]string{
		0:     "0%",
		0.001: "<0.1%",
		0.05:  "<0.1%",
		0.15:  "0.1%",
		0.5:   "0.5%",
		9.99:  "9.9%",
		10:    "10%",
		99.6:  "99%",
		100:   "100%",
	}
	for rate, want := range cases {
		got := formatLoss(rate)
		if got != want {
			t.Errorf("formatLoss(%v) = %q, want %q", rate, got, want)
		}
		if len(got) > lossW {
			t.Errorf("formatLoss(%v) = %q exceeds %d chars", rate, got, lossW)
		}
	}
}

func presetModel(t *testing.T, width, height int) model {
	t.Helper()
	data, err := os.ReadFile("../../presets/internet.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := monitor.LoadConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	return model{cfg: cfg, width: width, height: height, hostname: "h", version: "v"}
}

func press(m model, key string) model {
	var msg tea.KeyMsg
	switch key {
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "pgdown":
		msg = tea.KeyMsg{Type: tea.KeyPgDown}
	case "end":
		msg = tea.KeyMsg{Type: tea.KeyEnd}
	case "home":
		msg = tea.KeyMsg{Type: tea.KeyHome}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, _ := m.Update(msg)
	return next.(model)
}

func TestViewFitsHeight(t *testing.T) {
	for height := 8; height <= 60; height++ {
		m := presetModel(t, 120, height)
		for _, key := range []string{"", "pgdown", "end"} {
			if key != "" {
				m = press(m, key)
			}
			if n := strings.Count(m.View(), "\n") + 1; n > height {
				t.Fatalf("height %d after %q: %d lines", height, key, n)
			}
		}
	}
}

func TestScrolling(t *testing.T) {
	m := presetModel(t, 120, 20)
	first, last := "Lumen-Level3", "Google-IDC-Taiwan"
	if v := m.View(); !strings.Contains(v, first) || strings.Contains(v, last) || !strings.Contains(v, "Rows 1-13/") {
		t.Fatalf("initial view wrong:\n%s", v)
	}
	m = press(m, "end")
	if v := m.View(); !strings.Contains(v, last) || strings.Contains(v, first) {
		t.Fatalf("end should show last target:\n%s", v)
	}
	if press(m, "down").offset != m.offset {
		t.Error("scrolling past the end should be clamped")
	}
	m = press(m, "home")
	if m.offset != 0 || press(m, "up").offset != 0 {
		t.Error("home/up should clamp at 0")
	}
	if press(m, "j").offset != 1 || press(press(m, "j"), "k").offset != 0 {
		t.Error("j/k should scroll by one row")
	}

	// 視窗放大到能顯示全部時，offset 歸零且不顯示 Rows 提示
	m = press(m, "end")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
	if v := next.(model).View(); strings.Contains(v, "Rows ") || !strings.Contains(v, first) {
		t.Error("full-height view should not scroll")
	}
}

func TestQuitKey(t *testing.T) {
	_, cmd := presetModel(t, 80, 20).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return tea.Quit")
	}
}
