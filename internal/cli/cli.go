// Package cli is what the lovkilde commands share: the one writer logs and progress bars go
// through, the logger, and the profiler.
package cli

import (
	"log/slog"
	"os"

	"github.com/lkarlslund/lovkilde/internal/progress"
)

// Gate serializes progress frames and log records, which share stderr. It is
// package-level because slog's default logger is: every writer in the process
// has to go through the same gate or the guarantee is void.
var Gate = progress.NewGate(os.Stderr)

// SetLogger sends logs at level and above through the gate.
func SetLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	// Through the gate, so a record never lands in the middle of a bar frame.
	slog.SetDefault(slog.New(slog.NewTextHandler(Gate.LogWriter(), &slog.HandlerOptions{Level: lv})))
}
