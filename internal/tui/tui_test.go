package tui

import (
	"strings"
	"testing"

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
