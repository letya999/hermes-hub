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
			if len(groups) > 0 {
				return slog.Attr{}
			}
			if attr.Key == slog.MessageKey {
				if event := lifecycleEvent(attr.Value.String()); event != "" {
					return slog.String("event", event)
				}
				return slog.Attr{}
			}
			if !allowedDiagnosticField(attr.Key) {
				return slog.Attr{}
			}
			if attr.Key == slog.LevelKey {
				if attr.Value.Kind() == slog.KindAny {
					level, ok := attr.Value.Any().(slog.Level)
					if !ok {
						return slog.Attr{}
					}
					return slog.String(slog.LevelKey, level.String())
				}
				value, ok := diagnosticString(slog.LevelKey, attr.Value.String())
				if !ok {
					return slog.Attr{}
				}
				return slog.String(slog.LevelKey, value)
			}
			if attr.Value.Kind() == slog.KindAny || attr.Value.Kind() == slog.KindGroup {
				return slog.Attr{}
			}
			if attr.Value.Kind() == slog.KindString {
				value, ok := diagnosticString(attr.Key, attr.Value.String())
				if !ok {
					return slog.Attr{}
				}
				attr.Value = slog.StringValue(value)
			}
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
