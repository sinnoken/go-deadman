// go-deadman：多目標 ICMP 延遲、jitter 與丟包監控 TUI
package main

//go:generate go-winres make --in ../../winres/winres.json --product-version=git-tag --file-version=git-tag

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"go-deadman/internal/monitor"
	"go-deadman/internal/tui"
	"go-deadman/presets"
)

// version 於 CI 建置時以 -ldflags "-X main.version=..." 覆寫為 tag 名稱
var version = "v1.15.0"

// presetFromExe 讓改名為 go-deadman-<preset>(.exe) 的執行檔預設使用該 preset，
// 方便在 Windows 上直接雙擊執行不同清單
func presetFromExe(exe string, names []string) string {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe)))
	for _, n := range names {
		if strings.HasSuffix(base, "-"+n) {
			return n
		}
	}
	return ""
}

// choosePreset 的優先順序：-preset 參數 > 執行檔名稱 > presets.Default
func choosePreset(flagValue, exe string) string {
	if flagValue != "" {
		return flagValue
	}
	if n := presetFromExe(exe, presets.Names()); n != "" {
		return n
	}
	return presets.Default
}

func main() {
	presetFlag := flag.String("preset", "", "內嵌 preset 名稱（"+strings.Join(presets.Names(), ", ")+"）；預設 "+presets.Default)
	configPath := flag.String("config", "", "外部 YAML 設定檔路徑；指定時忽略 -preset")
	listPresets := flag.Bool("list-presets", false, "列出內嵌 preset 後結束")
	showVersion := flag.Bool("version", false, "顯示版本後結束")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Printf("go-deadman %s\n", version)
		return
	case *listPresets:
		for _, n := range presets.Names() {
			fmt.Println(n)
		}
		return
	}

	var data []byte
	var source string
	if *configPath != "" {
		// #nosec G304 -- 路徑由執行者以 -config 明確指定
		b, err := os.ReadFile(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "讀取設定檔 %s 失敗: %v\n", *configPath, err)
			os.Exit(1)
		}
		data, source = b, *configPath
	} else {
		name := choosePreset(*presetFlag, os.Args[0])
		b, err := presets.Get(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		data, source = b, "preset "+name
	}

	cfg, err := monitor.LoadConfig(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "設定檔（%s）無效: %v\n", source, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	wait := cfg.Start(ctx)

	hostname, _ := os.Hostname()
	p := tea.NewProgram(tui.New(cfg, hostname, version), tea.WithAltScreen())
	_, runErr := p.Run()

	// 停止所有 worker（含 pinger 與 ping 子行程）；最多等 2 秒
	cancel()
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", runErr)
		os.Exit(1)
	}
}
