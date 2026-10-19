package diagnostics

import (
	"log"
	"log/slog"
	"time"
)

// Logger returns the structured lifecycle logger (SPEC-0053 R3.5): JSON lines
// with UTC timestamps. It writes through the standard log writer so existing
// mirroring (HostLog) captures the records untouched.
func Logger(component string) *slog.Logger {
	handler := slog.NewJSONHandler(log.Writer(), &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				attr.Value = slog.StringValue(attr.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			return attr
		},
	})
	return slog.New(handler).With("component", component)
}

// JobFields are the common correlation fields a `hubctl trace` joins on.
func JobFields(jobID, runID, principal string) []any {
	return []any{"job_id", jobID, "run_id", runID, "principal", principal}
}
