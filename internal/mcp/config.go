// Package mcp - config.go：从 acorncode.json 读取 MCP server 配置。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// FileConfig 是 acorncode.json 中与 MCP 相关的部分。
// 只反序列化 "mcpServers" 段，其余字段（如 permissions）忽略。
type FileConfig struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// ServerSpec 是单个 MCP server 的声明（兼容主流 MCP 客户端的配置格式）。
type ServerSpec struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// LoadFileConfig 从 path 读取 MCP server 配置。
//
// 文件不存在 → 返 nil（无 MCP server）
// 文件存在但 JSON 错 → 返 error
func LoadFileConfig(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c FileConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// ToConfigs 把文件里的 server 声明转成 []Config（跳过 disabled）。
func (f *FileConfig) ToConfigs() []Config {
	if f == nil {
		return nil
	}
	out := make([]Config, 0, len(f.MCPServers))
	for name, spec := range f.MCPServers {
		if spec.Disabled {
			continue
		}
		out = append(out, Config{
			Name:    name,
			Command: spec.Command,
			Args:    spec.Args,
			Env:     spec.Env,
		})
	}
	return out
}
