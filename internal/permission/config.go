// Package permission - 配置加载
package permission

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Config 是 acorncode.json 的 permissions 段
type Config struct {
	Permissions Permissions `json:"permissions"`
}

// Permissions 包含权限规则
type Permissions struct {
	Rules []Rule `json:"rules"`
}

// LoadConfig 从 path 加载 acorncode.json
//
// 文件不存在 → 返 nil（用默认：所有 ask 都允许）
// 文件存在但 JSON 错 → 返 error
// 文件存在且合法 → 返 *Config
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // 文件不存在不报错
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}
