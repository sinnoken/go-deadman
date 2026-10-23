package main

//go:generate go-winres make --product-version=git-tag --file-version=git-tag

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// version 於 CI 建置時以 -ldflags "-X main.version=..." 覆寫為 tag 名稱
var version = "v1.12.0"

func main() {
	configPath := flag.String("config", "", "外部 YAML 設定檔路徑；未指定時使用內嵌的 "+presetName+" preset")
	showVersion := flag.Bool("version", false, "顯示版本後結束")
	flag.Parse()

	if *showVersion {
		fmt.Printf("go-deadman %s (preset: %s)\n", version, presetName)
		return
	}

	data := embeddedYaml
	source := "內嵌 preset " + presetName
	if *configPath != "" {
		// #nosec G304 -- 路徑由執行者以 -config 明確指定
		b, err := os.ReadFile(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "讀取設定檔 %s 失敗: %v\n", *configPath, err)
			os.Exit(1)
		}
		data, source = b, *configPath
	}

	cfg, err := loadConfig(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "設定檔（%s）無效: %v\n", source, err)
		os.Exit(1)
	}

	hostname, _ := os.Hostname()

	p := tea.NewProgram(model{
		cfg:      cfg,
		devices:  cfg.Devices,
		hostname: hostname,
	}, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
