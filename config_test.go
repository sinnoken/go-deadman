package main

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigPresets(t *testing.T) {
	for _, path := range []string{"presets/internet.yaml", "presets/tasks.yaml"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if cfg.interval != time.Second {
			t.Errorf("%s: interval = %v, want 1s", path, cfg.interval)
		}
	}
}

func TestLoadConfigEmbedded(t *testing.T) {
	if _, err := loadConfig(embeddedYaml); err != nil {
		t.Fatalf("embedded preset %s: %v", presetName, err)
	}
}

func TestLoadConfigDefaultsAndSeparator(t *testing.T) {
	cfg, err := loadConfig([]byte(`
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
		if _, err := loadConfig([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadConfigIgnoresUnknownKeys(t *testing.T) {
	if _, err := loadConfig([]byte("scale: 10\ndevices: [{ip: 192.0.2.1}]")); err != nil {
		t.Fatal(err)
	}
}
