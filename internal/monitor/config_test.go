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
