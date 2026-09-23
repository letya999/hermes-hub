package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type toolHubReconnectMarker struct {
	Revision uint64 `json:"revision"`
}

// The installed Hermes API treats /reload-mcp as model input. A controlled
// runtime restart reloads MCP discovery from the same owner home and session DB.
func startToolHubReconnectWatcher(ctx context.Context, stateDir string) {
	if toolHubEndpoint() == "" || strings.EqualFold(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_RECONNECT")), "false") {
		return
	}
	go watchToolHubReconnect(ctx, stateDir)
}

func watchToolHubReconnect(ctx context.Context, stateDir string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			change, err := readToolHubReconnectMarker(stateDir)
			if err != nil {
				continue
			}
			applied, err := readAppliedToolHubRevision(stateDir)
			if err != nil || change.Revision <= applied {
				continue
			}
			if err := scheduleToolHubReconnect(stateDir, change.Revision); err != nil {
				fmt.Fprintf(os.Stderr, "ToolHub MCP reconnect revision %d pending: %v\n", change.Revision, err)
				continue
			}
			return
		}
	}
}

func readToolHubReconnectMarker(stateDir string) (toolHubReconnectMarker, error) {
	body, err := os.ReadFile(filepath.Join(stateDir, "toolhub-reconnect.request"))
	if err != nil {
		return toolHubReconnectMarker{}, err
	}
	var marker toolHubReconnectMarker
	if err := json.Unmarshal(body, &marker); err != nil || marker.Revision == 0 {
		return toolHubReconnectMarker{}, errors.New("invalid ToolHub reconnect marker")
	}
	return marker, nil
}

func readAppliedToolHubRevision(stateDir string) (uint64, error) {
	body, err := os.ReadFile(filepath.Join(stateDir, "toolhub-reconnect.applied"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	revision, err := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return 0, errors.New("invalid applied ToolHub revision")
	}
	return revision, nil
}

func scheduleToolHubReconnect(stateDir string, revision uint64) error {
	if revision == 0 {
		return errors.New("invalid ToolHub reconnect revision")
	}
	// The runtime's existing restart loop drains Hermes, retains /state and starts
	// a new gateway process. The durable revision prevents a restart loop.
	tmp, err := os.CreateTemp(stateDir, ".toolhub-applied-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strconv.FormatUint(revision, 10)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "restart.request"), nil, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(stateDir, "toolhub-reconnect.applied")); err != nil {
		return err
	}
	signalRuntimeProcess()
	return nil
}
