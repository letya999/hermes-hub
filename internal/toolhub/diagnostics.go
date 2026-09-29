package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	diagcollect "github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/identity"
)

// User-facing runtime diagnostics (issue #125). The bounded operator collector
// in internal/diagnostics keeps appending container stdout/stderr to one local
// file; this file projects that material back to a principal through the same
// authenticated control plane as every other ToolHub operation. Callers never
// see host paths, Docker IDs, or another principal's containers: visibility is
// derived here from store bindings, not from model-supplied arguments.

const (
	diagFileName      = "hermes-diagnostics.txt"
	diagScanCap       = 8 << 20  // only the file tail is ever scanned
	diagMaxLine       = 32 << 10 // a single stored line beyond this is cut
	diagMaxTail       = 500
	diagDefaultTail   = 200
	diagMaxPayload    = 192 << 10 // redacted response budget
	diagMaxConcurrent = 4
)

var (
	// Global bound plus a small per-principal bound so one caller's heavy
	// scans cannot starve diagnostics for everyone else.
	diagLimiter     = make(chan struct{}, diagMaxConcurrent)
	diagUserLimiter sync.Map // principalID -> chan struct{} (cap 2)
)

// runtimeContainerName mirrors supervisor.containerName: the runtime
// contract pins RuntimeMode to "gateway" (see supervisor normalize/pins).
func runtimeContainerName(principalID, contextID string) string {
	sum := sha256.Sum256([]byte(principalID + "\x00" + contextID + "\x00" + "gateway"))
	return "hermes-context-" + hex.EncodeToString(sum[:8])
}

type diagScope struct {
	runtime   string
	workloads []diagWorkload
}

type diagWorkload struct {
	WorkloadID   string `json:"workload_id"`
	BindingID    string `json:"binding_id"`
	DefinitionID string `json:"definition_id"`
	Status       string `json:"status"`
	StartedAt    string `json:"started_at,omitempty"`
}

// allowed reports whether container name is in scope: the caller's own runtime
// container or a workload family (work-<id> plus work-<id>-* sidecars).
func (s diagScope) allowed(name string) bool {
	if name == s.runtime {
		return true
	}
	for _, w := range s.workloads {
		if name == w.WorkloadID || strings.HasPrefix(name, w.WorkloadID+"-") {
			return true
		}
	}
	return false
}

func (c *ControlPlane) diagnosticsScope(auth identity.Envelope) diagScope {
	scope := diagScope{runtime: runtimeContainerName(auth.PrincipalID, auth.ContextID)}
	c.Store.mu.RLock()
	defer c.Store.mu.RUnlock()
	seen := map[string]bool{}
	for _, binding := range c.Store.bindings {
		if binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID || binding.PolicyVersion != auth.PolicyVersion || binding.Status == RevokedStatus {
			continue
		}
		for _, workload := range c.Store.workloads {
			if workload.BindingID != binding.ToolBindingID {
				continue
			}
			entry := diagWorkload{WorkloadID: workload.WorkloadID, BindingID: binding.ToolBindingID, DefinitionID: workload.DefinitionID, Status: string(workload.Status)}
			if !workload.StartedAt.IsZero() {
				entry.StartedAt = workload.StartedAt.UTC().Format(time.RFC3339)
			}
			scope.workloads = append(scope.workloads, entry)
			seen[workload.WorkloadID] = true
		}
		// Controller records are not always persisted; the workload ID is
		// deterministic so it can be derived from the binding the same way
		// resolveLocked does at invocation time.
		ownerID := auth.ContextID
		switch binding.WorkloadClass {
		case Shared:
			ownerID = ""
		case PerUser:
			ownerID = binding.ToolBindingID
			if binding.ConnectionID != "" {
				ownerID = auth.ContextID + ":" + auth.PrincipalID + ":" + binding.ConnectionID
			}
		}
		workloadID := WorkloadInstanceID(binding.DefinitionID, binding.WorkloadClass, ownerID, "")
		if !seen[workloadID] {
			seen[workloadID] = true
			scope.workloads = append(scope.workloads, diagWorkload{WorkloadID: workloadID, BindingID: binding.ToolBindingID, DefinitionID: binding.DefinitionID, Status: "inferred"})
		}
	}
	return scope
}

var (
	diagLinePattern = regexp.MustCompile(`^(\S+)\s+\[([^\]]+)\]\s+(.*)$`)
	// Host-side supervisor lines are logged with bracketed stamps.
	diagHostPattern  = regexp.MustCompile(`^\[(\S+)\]\s+\[host-supervisor\]\s+(.*)$`)
	diagErrorPattern = regexp.MustCompile(`(?i)\b(error|panic|fatal|failed|denied|refused|unauthorized|forbidden)\b`)
	diagWarnPattern  = regexp.MustCompile(`(?i)\b(warn|timeout|timed out|retry|restarting|backoff|unhealthy)\b`)
)

// diagRedact delegates to the shared collector redaction; it is applied again
// at projection time so lines collected before ingest-time redaction existed
// still get masked.
func diagRedact(line string) string {
	return diagcollect.Redact(line)
}

func diagSeverityMatch(severity, message string) bool {
	switch severity {
	case "error":
		return diagErrorPattern.MatchString(message)
	case "warn":
		return diagWarnPattern.MatchString(message) || diagErrorPattern.MatchString(message)
	default:
		return true
	}
}

type diagQuery struct {
	tail     int
	since    time.Time
	until    time.Time
	search   string
	severity string
	selector string
}

func diagParseQuery(args map[string]any) (diagQuery, error) {
	q := diagQuery{tail: diagDefaultTail}
	if raw, ok := args["tail"]; ok {
		value, ok := raw.(float64)
		if !ok || value != float64(int(value)) {
			return q, fmt.Errorf("%w: diagnostics tail must be an integer", ErrInvalid)
		}
		q.tail = int(value)
	}
	if q.tail < 1 {
		q.tail = 1
	}
	if q.tail > diagMaxTail {
		q.tail = diagMaxTail
	}
	if raw := strings.TrimSpace(argString(args, "since")); raw != "" {
		stamp, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return q, fmt.Errorf("%w: diagnostics since must be RFC3339", ErrInvalid)
		}
		q.since = stamp.UTC()
	}
	if raw := strings.TrimSpace(argString(args, "until")); raw != "" {
		stamp, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return q, fmt.Errorf("%w: diagnostics until must be RFC3339", ErrInvalid)
		}
		q.until = stamp.UTC()
	}
	if !q.since.IsZero() && !q.until.IsZero() && q.until.Before(q.since) {
		return q, fmt.Errorf("%w: diagnostics until precedes since", ErrInvalid)
	}
	if search := strings.TrimSpace(argString(args, "search")); search != "" {
		if len(search) > 256 {
			return q, fmt.Errorf("%w: diagnostics search exceeds 256 bytes", ErrInvalid)
		}
		q.search = strings.ToLower(search)
	}
	if severity := strings.ToLower(strings.TrimSpace(argString(args, "severity"))); severity != "" {
		switch severity {
		case "info", "warn", "error":
			q.severity = severity
		default:
			return q, fmt.Errorf("%w: diagnostics severity must be info, warn or error", ErrInvalid)
		}
	}
	q.selector = strings.TrimSpace(argString(args, "workload"))
	return q, nil
}

// selectorMatch narrows the scope when the caller passes a workload selector;
// it matches only entities already inside the caller's authorized scope, so a
// foreign ID just yields an empty set rather than another user's data.
func (s diagScope) selectorMatch(selector string) diagScope {
	if selector == "" {
		return s
	}
	out := diagScope{runtime: s.runtime}
	if selector == "runtime" || selector == s.runtime {
		return diagScope{runtime: s.runtime}
	}
	for _, w := range s.workloads {
		if w.WorkloadID == selector || w.BindingID == selector || w.DefinitionID == selector {
			out.workloads = append(out.workloads, w)
		}
	}
	out.runtime = ""
	return out
}

func (c *ControlPlane) diagnostics(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	query, err := diagParseQuery(args)
	if err != nil {
		return nil, err
	}
	if c.DiagnosticsDir == "" {
		return map[string]any{"enabled": false, "detail": "diagnostics are not enabled on this hub"}, nil
	}
	raw, _ := diagUserLimiter.LoadOrStore(auth.PrincipalID, make(chan struct{}, 2))
	userSem := raw.(chan struct{})
	select {
	case userSem <- struct{}{}:
		defer func() { <-userSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case diagLimiter <- struct{}{}:
		defer func() { <-diagLimiter }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	scope := c.diagnosticsScope(auth).selectorMatch(query.selector)
	path := filepath.Join(c.DiagnosticsDir, diagFileName)
	handle, err := os.Open(path) // #nosec G304 -- fixed file inside configured diagnostics dir.
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"enabled": true, "workloads": scope.workloads, "lines": []map[string]any{}, "detail": "no diagnostics collected yet"}, nil
		}
		return nil, fmt.Errorf("%w: diagnostics store", ErrInvalid)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: diagnostics store", ErrInvalid)
	}
	offset := int64(0)
	if size := info.Size(); size > diagScanCap {
		offset = size - diagScanCap
	}
	if _, err := handle.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(handle, diagScanCap))
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if cut := strings.IndexByte(string(data[:min(len(data), 4096)]), '\n'); cut >= 0 {
			data = data[cut+1:]
		}
	}
	matched := make([]map[string]any, 0, query.tail)
	payload := 0
	truncated := false
	stoppedEarly := false
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := lines[i]
		if line == "" {
			continue
		}
		stamp, name, message, ok := diagParseLine(line, scope)
		if !ok {
			continue
		}
		if !query.since.IsZero() && stamp.Before(query.since) {
			continue
		}
		if !query.until.IsZero() && stamp.After(query.until) {
			continue
		}
		if query.search != "" && !strings.Contains(strings.ToLower(message), query.search) {
			continue
		}
		if !diagSeverityMatch(query.severity, message) {
			continue
		}
		message = diagRedact(message)
		if len(message) > diagMaxLine {
			message = message[:diagMaxLine] + "…"
		}
		entry := map[string]any{"ts": stamp.Format(time.RFC3339Nano), "workload": name, "message": message}
		if len(matched) >= query.tail || payload+len(message)+len(name)+48 > diagMaxPayload {
			stoppedEarly = true
			break
		}
		payload += len(message) + len(name) + 48
		matched = append(matched, entry)
	}
	truncated = stoppedEarly
	for left, right := 0, len(matched)-1; left < right; left, right = left+1, right-1 {
		matched[left], matched[right] = matched[right], matched[left]
	}
	return map[string]any{
		"enabled":   true,
		"workloads": scope.workloads,
		"runtime":   scope.runtime != "",
		"lines":     matched,
		"truncated": truncated,
	}, nil
}

// diagParseLine recognizes collector lines "<ts> [container] msg" and
// host-supervisor lines "[<ts>] [host-supervisor] msg". Supervisor lines enter
// scope only when they mention the caller's own runtime container, so restart
// and reaping diagnostics stay useful without exposing other users' runtimes.
func diagParseLine(line string, scope diagScope) (time.Time, string, string, bool) {
	if match := diagHostPattern.FindStringSubmatch(line); match != nil {
		if scope.runtime == "" || !mentionsContainer(match[2], scope.runtime) {
			return time.Time{}, "", "", false
		}
		stamp, err := time.Parse(time.RFC3339Nano, match[1])
		if err != nil {
			return time.Time{}, "", "", false
		}
		return stamp, "host-supervisor", match[2], true
	}
	if match := diagLinePattern.FindStringSubmatch(line); match != nil {
		if !scope.allowed(match[2]) {
			return time.Time{}, "", "", false
		}
		stamp, err := time.Parse(time.RFC3339Nano, match[1])
		if err != nil {
			return time.Time{}, "", "", false
		}
		return stamp, match[2], match[3], true
	}
	return time.Time{}, "", "", false
}

// mentionsContainer reports whether the line contains name as a whole token:
// a substring match could leak lines about foreign containers that merely
// extend the caller's container prefix (hermes-context-<hash>-extra).
func mentionsContainer(line, name string) bool {
	for _, field := range strings.Fields(line) {
		if strings.Trim(field, `"',.;:()[]{}<>`) == name {
			return true
		}
	}
	return false
}
