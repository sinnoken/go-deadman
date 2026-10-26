package main

import (
	"strings"
	"testing"

	"go-deadman/internal/monitor"
	"go-deadman/presets"
)

func TestChoosePreset(t *testing.T) {
	cases := []struct {
		flag, exe, want string
	}{
		{"", `C:\tools\go-deadman.exe`, presets.Default},
		{"", `C:\tools\go-deadman-tasks.exe`, "tasks"},
		{"", "/usr/local/bin/go-deadman-TASKS", "tasks"},
		{"", "/usr/local/bin/go-deadman-internet", "internet"},
		{"", "/usr/local/bin/tasks", presets.Default}, // 必須是 -<preset> 結尾
		{"internet", `C:\tools\go-deadman-tasks.exe`, "internet"},
		{"custom", "go-deadman", "custom"}, // 不存在的名稱交給 presets.Get 回報錯誤
	}
	for _, c := range cases {
		if got := choosePreset(c.flag, c.exe); got != c.want {
			t.Errorf("choosePreset(%q, %q) = %q, want %q", c.flag, c.exe, got, c.want)
		}
	}
}

func TestWritePresetList(t *testing.T) {
	var b strings.Builder
	if err := writePresetList(&b); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	names := presets.Names()
	if len(lines) != len(names) {
		t.Fatalf("%d lines for %d presets", len(lines), len(names))
	}
	// 每行依序對應一個 preset，且說明都從同一欄開始
	col := -1
	for i, l := range lines {
		data, _ := presets.Get(names[i])
		cfg, err := monitor.LoadConfig(data)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(l, names[i]+" ") || cfg.Description == "" || !strings.HasSuffix(l, cfg.Description) {
			t.Fatalf("line %d = %q, want preset %s with its description", i, l, names[i])
		}
		at := len(l) - len(cfg.Description)
		if col == -1 {
			col = at
		} else if at != col {
			t.Errorf("description of %s starts at column %d, want %d", names[i], at, col)
		}
	}
}
