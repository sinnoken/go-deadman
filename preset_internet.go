//go:build !tasks

package main

import _ "embed"

// 預設內嵌 internet preset；以 -tags tasks 建置時改用 preset_tasks.go
//
//go:embed presets/internet.yaml
var embeddedYaml []byte

const presetName = "internet"
