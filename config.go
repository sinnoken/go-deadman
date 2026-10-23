package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultInterval = "1s"
	defaultJitter   = 0.1
	minInterval     = 100 * time.Millisecond
	separatorName   = "---"
)

type Config struct {
	Interval string    `yaml:"interval"`
	Jitter   float64   `yaml:"jitter"`
	Devices  []*Device `yaml:"devices"`

	interval time.Duration // 由 Interval 解析而來，經 loadConfig 驗證
}

// loadConfig 解析 YAML、套用預設值並驗證；通過後每個 Device 都已可交給 worker。
// 未知的鍵（例如 preset 中的 scale）會被忽略。
func loadConfig(data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("YAML 格式錯誤: %w", err)
	}

	if cfg.Interval == "" {
		cfg.Interval = defaultInterval
	}
	itv, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return cfg, fmt.Errorf("interval %q 格式錯誤: %w", cfg.Interval, err)
	}
	if itv < minInterval {
		return cfg, fmt.Errorf("interval %s 小於下限 %s", itv, minInterval)
	}
	cfg.interval = itv

	if cfg.Jitter == 0 {
		cfg.Jitter = defaultJitter
	}
	if cfg.Jitter < 0 || cfg.Jitter >= 1 {
		return cfg, fmt.Errorf("jitter %v 必須介於 0 與 1 之間", cfg.Jitter)
	}

	if len(cfg.Devices) == 0 {
		return cfg, errors.New("devices 不可為空")
	}
	for i, d := range cfg.Devices {
		if d == nil {
			return cfg, fmt.Errorf("第 %d 個 device 是空項目", i+1)
		}
		d.Name = strings.TrimSpace(d.Name)
		d.IP = strings.TrimSpace(d.IP)
		if d.Name == separatorName {
			d.isSep = true
			continue
		}
		if d.Name == "" && d.IP == "" {
			return cfg, fmt.Errorf("第 %d 個 device 需要 name 或 ip", i+1)
		}
		if d.IP != "" && net.ParseIP(d.IP) == nil {
			return cfg, fmt.Errorf("第 %d 個 device（%s）的 ip %q 不是合法 IP", i+1, d.Name, d.IP)
		}
		d.Loading = true
	}
	return cfg, nil
}
