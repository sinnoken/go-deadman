package main

import (
	"testing"

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
