package diagnostics

import (
	"encoding/json"
	"log/slog"
	"strings"
)

var diagnosticFields = []string{"level", "source_position", "diagnostic_gap", "component", "event", "job_id", "run_id", "session_id", "principal", "context_id", "status", "from", "to", "duration_ms", "count", "error_class", "reason_code", "method", "route"}

func allowedDiagnosticField(key string) bool {
	for _, field := range diagnosticFields {
		if key == field {
			return true
		}
	}
	return key == slog.TimeKey || key == slog.LevelKey
}

func lifecycleEvent(message string) string {
	switch message {
	case "http":
		return "http"
	case "job admitted":
		return "job.admitted"
	case "job outcome":
		return "job.outcome"
	case "job finished":
		return "job.finished"
	case "rejected phase transition":
		return "phase.rejected"
	}
	return ""
}

// ProjectDiagnosticLine selects known scalar metadata before redaction.
// Content is included only by an explicit trusted operator opt-in.
func ProjectDiagnosticLine(message string, content bool) string {
	if content {
		return Redact(message)
	}
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(message), &input) != nil {
		first, last := strings.IndexByte(message, '{'), strings.LastIndexByte(message, '}')
		if first < 0 || last < first || json.Unmarshal([]byte(message[first:last+1]), &input) != nil {
			input = map[string]json.RawMessage{}
		}
	}
	for _, field := range strings.Fields(message) {
		key, value, ok := strings.Cut(field, "=")
		if ok && allowedDiagnosticField(key) {
			encoded, _ := json.Marshal(strings.Trim(value, `"`))
			input[key] = encoded
		}
	}
	output := map[string]any{}
	for _, key := range diagnosticFields {
		raw, ok := input[key]
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		switch v := value.(type) {
		case string:
			if safe, ok := diagnosticString(key, v); ok {
				output[key] = safe
			}
		case float64:
			if key == "count" || key == "duration_ms" {
				output[key] = v
			}
		}
	}
	if raw, ok := input["msg"]; ok {
		var msg string
		if json.Unmarshal(raw, &msg) == nil {
			if event := lifecycleEvent(msg); event != "" {
				output["event"] = event
			}
		}
	}
	if len(output) == 0 {
		level := "INFO"
		for _, field := range strings.Fields(strings.ToLower(message)) {
			switch strings.Trim(field, `"',.;:()[]{}<>`) {
			case "error", "panic", "fatal", "failed", "denied", "refused", "unauthorized", "forbidden":
				level = "ERROR"
			case "warn", "warning", "timeout", "retry", "backoff", "unhealthy":
				if level != "ERROR" {
					level = "WARN"
				}
			}
		}
		output["event"] = "unstructured_log"
		output["level"] = level
	}
	body, err := json.Marshal(output)
	if err != nil {
		return "content_omitted"
	}
	return string(body)
}

func diagnosticString(key, value string) (string, bool) {
	if len(value) > 256 {
		return "", false
	}
	if key == "level" {
		value = strings.ToUpper(value)
		switch value {
		case "TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "FATAL", "PANIC":
			return value, true
		}
		return "", false
	}
	if key == "method" {
		switch value {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
			return value, true
		}
		return "", false
	}
	if key == "route" {
		if !strings.HasPrefix(value, "/") {
			return "", false
		}
		value, _, _ = strings.Cut(value, "?")
		value, _, _ = strings.Cut(value, "#")
		value = safeRoute(value)
	}
	if Redact(value) != value {
		return "", false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-:@/", r)) {
			return "", false
		}
	}
	return value, value != ""
}
