package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestToolHubReconnectMarkerAndAppliedRevision(t *testing.T) {
	state := t.TempDir()
	if _, err := readToolHubReconnectMarker(state); err == nil {
		t.Fatal("missing marker accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "toolhub-reconnect.request"), []byte(`{"revision":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	marker, err := readToolHubReconnectMarker(state)
	if err != nil || marker.Revision != 7 {
		t.Fatalf("marker=%+v err=%v", marker, err)
	}
	if got, err := readAppliedToolHubRevision(state); err != nil || got != 0 {
		t.Fatalf("missing applied revision: %d %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(state, "toolhub-reconnect.request"), []byte(`{"revision":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToolHubReconnectMarker(state); err == nil {
		t.Fatal("zero revision accepted")
	}
}

func TestToolHubReconnectRestartsOnceWithDurableRevision(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "toolhub-reconnect.request"), []byte(`{"revision":9}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_ENDPOINT", "http://toolhub:8090/mcp")
	t.Setenv("HUB_TOOLHUB_RECONNECT", "true")
	called := make(chan struct{}, 2)
	oldSignal := signalRuntimeProcess
	signalRuntimeProcess = func() { called <- struct{}{} }
	defer func() { signalRuntimeProcess = oldSignal }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startToolHubReconnectWatcher(ctx, state)
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("projection did not schedule runtime reconnect")
	}
	if _, err := os.Stat(filepath.Join(state, "restart.request")); err != nil {
		t.Fatal("runtime restart was not requested", err)
	}
	if got, err := readAppliedToolHubRevision(state); err != nil || got != 9 {
		t.Fatalf("durable revision=%d err=%v", got, err)
	}
	if err := os.Remove(filepath.Join(state, "restart.request")); err != nil {
		t.Fatal(err)
	}
	// A fresh watcher represents the post-restart runtime. It must not loop.
	startToolHubReconnectWatcher(ctx, state)
	select {
	case <-called:
		t.Fatal("unchanged projection restarted Hermes twice")
	case <-time.After(700 * time.Millisecond):
	}
}

func TestToolHubReconnectDisabled(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_ENDPOINT", "http://toolhub:8090/mcp")
	t.Setenv("HUB_TOOLHUB_RECONNECT", "false")
	oldSignal := signalRuntimeProcess
	signalRuntimeProcess = func() { t.Error("disabled watcher signaled runtime") }
	defer func() { signalRuntimeProcess = oldSignal }()
	startToolHubReconnectWatcher(context.Background(), t.TempDir())
}
