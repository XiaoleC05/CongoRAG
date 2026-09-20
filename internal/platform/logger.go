package platform

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger 建一个结构化日志器。
//
// 用标准库的 log/slog，不引第三方——本项目只需要"键值对 + 级别 + 输出到 stdout"，
// slog 全都有。
//
// 方案 §2 要求日志里带 request_id / agent_run_id / token_usage。
// 那些不是在这里配的——request_id 从 context 里取（中间件注入），
// 另外两个由调用方在打日志时显式带上。
func NewLogger(cfg *Config) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
