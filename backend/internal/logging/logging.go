// Package logging slog 日志初始化：按级别/格式构建 *slog.Logger。
// 默认输出到 stderr（与原标准库 log 的默认去向一致）。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options 日志初始化选项
type Options struct {
	Level  string // debug|info|warn|error（大小写不敏感），空为 info
	Format string // text|json，空为 text

	out io.Writer // 输出目标；测试注入用，nil 为 stderr
}

// New 按选项构建 *slog.Logger；级别或格式非法时返回错误（由调用方决定是否致命）。
func New(opts Options) (*slog.Logger, error) {
	level := slog.LevelInfo
	if opts.Level != "" {
		if err := level.UnmarshalText([]byte(opts.Level)); err != nil {
			return nil, fmt.Errorf("非法日志级别 %q（可选 debug/info/warn/error）", opts.Level)
		}
	}
	out := opts.out
	if out == nil {
		out = os.Stderr
	}
	var h slog.Handler
	switch strings.ToLower(opts.Format) {
	case "", "text":
		h = slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})
	case "json":
		h = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level})
	default:
		return nil, fmt.Errorf("非法日志格式 %q（可选 text/json）", opts.Format)
	}
	return slog.New(h), nil
}
