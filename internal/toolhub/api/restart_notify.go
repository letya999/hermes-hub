package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RuntimeRestartNotifier watches projection revisions for every principal
// served by this ToolHub and asks the supervisor to mark the owning managed
// runtime for a controlled restart. It complements the MCP list_changed push
// (which only reaches clients holding an open stream): after the marker, the
// runtime restarts Hermes between runs and the next session carries the new
// tools. Managed deployments persist applied revisions in ToolHub's store.
type RuntimeRestartNotifier struct {
	// URL is the supervisor base (HUB_RUNTIME_SUPERVISOR_URL); Token is the
	// control-plane bearer (HUB_COMMUNICATION_AUTH, rendered as the
	// supervisor's own auth). HTTP defaults to a 3s-timeout client.
	URL, Token string
	HTTP       *http.Client
	State      interface {
		AppliedRestartRevision(ProjectionTarget) uint64
		MarkRestartApplied(ProjectionTarget) error
	}

	mu     sync.Mutex
	seen   map[string]uint64
	primed bool
}

// RuntimeRestartNotifierFromEnv returns nil when the supervisor control plane
// is not configured (unmanaged deployments keep the file-marker contract).
func RuntimeRestartNotifierFromEnv() *RuntimeRestartNotifier {
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")), "/")
	token := strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_AUTH"))
	if url == "" || token == "" {
		return nil
	}
	return &RuntimeRestartNotifier{URL: url, Token: token}
}

// Notify diffs the snapshot against the last acknowledged revisions and POSTs
// /v1/restart-request for each changed target. Failures are returned so the
// caller retries on its next tick; acknowledged revisions are not resent.
func (n *RuntimeRestartNotifier) Notify(targets []ProjectionTarget) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = make(map[string]uint64)
	}
	if n.State == nil && !n.primed {
		for _, target := range targets {
			key := n.key(target)
			n.seen[key] = max(n.seen[key], target.Revision)
		}
		n.primed = true
		return nil
	}
	latest := make(map[string]int, len(targets))
	coalesced := make([]ProjectionTarget, 0, len(targets))
	for _, target := range targets {
		key := n.key(target)
		if index, ok := latest[key]; ok {
			if target.Revision > coalesced[index].Revision {
				coalesced[index] = target
			}
			continue
		}
		latest[key] = len(coalesced)
		coalesced = append(coalesced, target)
	}
	var errs []error
	for _, target := range coalesced {
		key := n.key(target)
		applied := n.seen[key]
		if n.State != nil {
			applied = n.State.AppliedRestartRevision(target)
		}
		if target.Revision <= applied {
			continue
		}
		if target.Digest != "" && n.State != nil {
			if state, ok := n.State.(interface{ AppliedRestartDigest(ProjectionTarget) string }); ok && state.AppliedRestartDigest(target) == target.Digest {
				if err := n.State.MarkRestartApplied(target); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", target.PrincipalID, err))
				}
				continue
			}
		}
		if err := n.post(target); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", target.PrincipalID, err))
			continue
		}
		if n.State != nil {
			if err := n.State.MarkRestartApplied(target); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", target.PrincipalID, err))
				continue
			}
		} else {
			n.seen[key] = target.Revision
		}
	}
	return errors.Join(errs...)
}

func (n *RuntimeRestartNotifier) key(target ProjectionTarget) string {
	return target.PrincipalID + "\x00" + target.ContextID + "\x00" + target.RuntimeID
}

func (n *RuntimeRestartNotifier) post(target ProjectionTarget) error {
	sum := sha256.Sum256([]byte(n.key(target) + "\x00" + strconv.FormatUint(target.Revision, 10) + "\x00" + target.Digest))
	body, err := json.Marshal(struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		RuntimeID   string `json:"runtime_id"`
		Revision    uint64 `json:"revision"`
		OperationID string `json:"operation_id"`
		Reason      string `json:"reason"`
	}{target.PrincipalID, target.ContextID, target.RuntimeID, target.Revision, "projection-" + hex.EncodeToString(sum[:12]), "tool_projection_change"})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, n.URL+"/v1/restart-request", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.Token)
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("restart-request returned %d", resp.StatusCode)
	}
	return nil
}
