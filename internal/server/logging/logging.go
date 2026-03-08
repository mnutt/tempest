// Package logging adds helpers for logging
package logging

import (
	"os"
	"strings"

	"golang.org/x/exp/slog"
)

const LevelTrace slog.Level = -8

// Create a new logger with our preferred settings.
func NewLogger() *slog.Logger {
	opts := slog.HandlerOptions{
		Level: parseLevel(os.Getenv("LOG_LEVEL")),
	}
	return slog.New(opts.NewTextHandler(os.Stdout))
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "trace":
		return LevelTrace
	case "debug", "":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

// Log a message and then panic
func Panic(l *slog.Logger, msg string, args ...any) {
	msg = "FATAL: " + msg
	l.Error(msg, args...)
	panic(msg)
}
