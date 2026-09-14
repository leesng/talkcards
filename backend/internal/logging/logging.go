// Package logging builds slog loggers from level/format options.
// Defaults to stderr, matching the standard library log default.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type Options struct {
	Level  string // debug|info|warn|error (case-insensitive), empty = info
	Format string // text|json, empty = text

	out io.Writer // injected by tests; nil = stderr
}

// New builds a *slog.Logger; invalid level or format returns an error
// (fatality is the caller's decision).
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
