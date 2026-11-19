// Package log is the one logger setup of every v1 service: JSON lines on the
// given writer (stdout for services), the service name on every record and the
// level from HUB_LOG_LEVEL (debug, info, warn, error; default info). Callers
// log safe fields only; never tokens, credentials or message bodies.
package log

import (
	"io"
	"log/slog"
	"os"
)

// LevelEnv selects the minimum level.
const LevelEnv = "HUB_LOG_LEVEL"

// New returns a JSON logger for service writing to w.
func New(w io.Writer, service string) *slog.Logger {
	level := slog.LevelInfo
	if raw := os.Getenv(LevelEnv); raw != "" {
		if err := level.UnmarshalText([]byte(raw)); err != nil {
			level = slog.LevelInfo
		}
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})).With("service", service)
}
