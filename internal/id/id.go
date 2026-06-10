// Package id 提供统一的短 ID 生成工具（零依赖，纯 stdlib）。
//
// 重构前（v1.8）有 4 处几乎相同的 base36 时间戳 ID 实现，分散在
// agent / server / permission / cmd 包里。本包消除重复（R2）。
//
// 生成策略：UnixNano + 单调递增 counter，避免同一纳秒内的碰撞；
// 用 base36（小写字母 + 数字）编码。非加密强度，仅用于会话内对象标识。
package id

import (
	"sync/atomic"
	"time"
)

const chars = "abcdefghijklmnopqrstuvwxyz0123456789"

// counter 防同纳秒冲突：每次生成自增，混入时间戳。
var counter atomic.Uint64

// encode 把 (UnixNano + counter) 编成 n 字符的 base36 串。
func encode(n int) string {
	seed := time.Now().UnixNano() + int64(counter.Add(1))
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[seed%int64(len(chars))]
		seed /= int64(len(chars))
		if seed == 0 {
			seed = time.Now().UnixNano() + int64(counter.Add(1))
		}
	}
	return string(b)
}

// Short 生成 8 字符短 ID（无前缀）。
func Short() string {
	return encode(8)
}

// New 生成带前缀的 16 字符 ID，形如 "msg_a1b2c3...".
func New(prefix string) string {
	return prefix + "_" + encode(16)
}
