// Package presets 內嵌所有監控目標清單（本目錄下的 *.yaml），執行時以名稱選擇。
package presets

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.yaml
var files embed.FS

// Default 是未指定 -preset 時使用的清單
const Default = "internet"

// Names 回傳所有內嵌 preset 的名稱（不含副檔名），依字母排序
func Names() []string {
	entries, _ := files.ReadDir(".")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names
}

// Get 回傳指定 preset 的 YAML 內容
func Get(name string) ([]byte, error) {
	data, err := files.ReadFile(name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("找不到 preset %q（可用：%s）", name, strings.Join(Names(), ", "))
	}
	return data, nil
}
