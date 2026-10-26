package monitor

import (
	"testing"
	"time"

	"go-deadman/presets"
)

func TestLoadConfigPresets(t *testing.T) {
	for _, name := range presets.Names() {
		data, err := presets.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(data)
		if err != nil {
			t.Errorf("preset %s: %v", name, err)
			continue
		}
		if cfg.interval != time.Second {
			t.Errorf("preset %s: interval = %v, want 1s", name, cfg.interval)
		}
		checkPresetContent(t, name, cfg)
	}
}

// 專題 preset 的目標數上限；總覽 internet 與區網範本 tasks 不受限
const maxThemedTargets = 16

func checkPresetContent(t *testing.T, name string, cfg Config) {
	t.Helper()
	if cfg.Description == "" {
		t.Errorf("preset %s: missing description", name)
	}
	seen := map[string]bool{}
	targets := 0
	for _, d := range cfg.Devices {
		if d.IsSeparator() {
			continue
		}
		targets++
		key := d.IP + d.Host
		if key == "" {
			key = d.Name
		}
		if seen[key] {
			t.Errorf("preset %s: duplicate target %s", name, key)
		}
		seen[key] = true
	}
	if targets == 0 || (name != presets.Default && name != "tasks" && targets > maxThemedTargets) {
		t.Errorf("preset %s: %d targets, want 1-%d", name, targets, maxThemedTargets)
	}
}

func TestLoadConfigDefaultsAndSeparator(t *testing.T) {
	cfg, err := LoadConfig([]byte(`
devices:
  - name: " router "
  - ip: "192.0.2.1"
  - name: "---"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.interval != time.Second || cfg.Jitter != defaultJitter {
		t.Errorf("defaults = %v / %v", cfg.interval, cfg.Jitter)
	}
	d := cfg.Devices
	if d[0].Name != "router" || d[0].isSep || !d[0].Loading {
		t.Errorf("name-only device = %+v", d[0])
	}
	if d[1].IP != "192.0.2.1" || d[1].isSep {
		t.Errorf("ip-only device = %+v", d[1])
	}
	if !d[2].isSep || d[2].Loading {
		t.Errorf("separator = %+v", d[2])
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := map[string]string{
		"bad interval":   "interval: soon\ndevices: [{ip: 192.0.2.1}]",
		"tiny interval":  "interval: 10ms\ndevices: [{ip: 192.0.2.1}]",
		"bad jitter":     "jitter: 1.5\ndevices: [{ip: 192.0.2.1}]",
		"no devices":     "interval: 1s",
		"empty device":   "devices: [{name: ''}]",
		"invalid ip":     "devices: [{name: a, ip: 999.1.1.1}]",
		"malformed yaml": "devices: [",
	}
	for name, in := range cases {
		if _, err := LoadConfig([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadConfigIgnoresUnknownKeys(t *testing.T) {
	if _, err := LoadConfig([]byte("scale: 10\ndevices: [{ip: 192.0.2.1}]")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigHost(t *testing.T) {
	cfg, err := LoadConfig([]byte(`
devices:
  - {name: "Steam store", host: " store.example.com "}
  - {host: "only-host.example.com"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.Devices[0]; d.Name != "Steam store" || d.Host != "store.example.com" {
		t.Errorf("name+host = %q / %q", d.Name, d.Host)
	}
	if d := cfg.Devices[1]; d.Name != "only-host.example.com" {
		t.Errorf("host-only name = %q", d.Name)
	}
	for _, bad := range []string{
		`devices: [{name: x, ip: 192.0.2.1, host: x.example}]`, // ip 與 host 擇一
		`devices: [{name: "---", host: x.example}]`,            // 分隔列不可帶目標
		`devices: [{host: "---"}]`,                             // 分隔列不可寫在 host
		`devices: [{name: "---", ip: 192.0.2.1}]`,
	} {
		if _, err := LoadConfig([]byte(bad)); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}
