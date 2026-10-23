//go:build !tasks

// Package presets 內嵌監控目標清單。預設為 internet；以 -tags tasks 建置時改用 tasks。
package presets

import _ "embed"

//go:embed internet.yaml
var Data []byte

const Name = "internet"
