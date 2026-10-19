package api

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func exportTar(t *testing.T, members map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range members {
		if err := w.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	w.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestApplyExportsFenceContract(t *testing.T) {
	eff := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	apply := func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{}, nil
	}
	// empty export: no fence needed, nothing applied
	if a, s, err := ApplyExports(context.Background(), apply, nil, eff, AgentExecRequest{}, "rel", ""); err != nil || a != nil || s != nil {
		t.Fatalf("empty export: %v", err)
	}
	// export without an authority recheck is an isolation violation
	if _, _, err := ApplyExports(context.Background(), apply, nil, eff, AgentExecRequest{}, "rel", "eA=="); !errors.Is(err, ErrIsolation) {
		t.Fatalf("unfenced export: %v", err)
	}
	// a fence rejection propagates and nothing is applied
	fenceErr := errors.New("revoked")
	if _, _, err := ApplyExports(context.Background(), apply, func(context.Context, EffectiveBinding) error { return fenceErr }, eff, AgentExecRequest{}, "rel", "eA=="); !errors.Is(err, fenceErr) {
		t.Fatalf("fence error: %v", err)
	}
	// malformed payload is rejected before any apply call
	if _, _, err := ApplyExports(context.Background(), apply, func(context.Context, EffectiveBinding) error { return nil }, eff, AgentExecRequest{}, "rel", "%%%"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad b64: %v", err)
	}
}

func TestApplyExportsCreateOnly(t *testing.T) {
	eff := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-1"}}
	fence := func(context.Context, EffectiveBinding) error { return nil }
	var calls []AgentExecRequest
	apply := func(_ context.Context, _ EffectiveBinding, req AgentExecRequest) (AgentExecResult, error) {
		calls = append(calls, req)
		return AgentExecResult{}, nil
	}
	// regular members land under <path>/exports/ via file_write
	b64 := exportTar(t, map[string]string{"out.txt": "data"})
	applied, skipped, err := ApplyExports(context.Background(), apply, fence, eff, AgentExecRequest{}, "docs", b64)
	if err != nil || len(applied) != 1 || applied[0] != "docs/exports/out.txt" || len(skipped) != 0 {
		t.Fatalf("apply: %v %v %v", applied, skipped, err)
	}
	if len(calls) != 1 || calls[0].Tool != "file_write" {
		t.Fatalf("calls: %+v", calls)
	}
	// unsafe members (dirs, traversal, oversized) are skipped, never applied
	calls = nil
	var dirTar bytes.Buffer
	dw := tar.NewWriter(&dirTar)
	dw.WriteHeader(&tar.Header{Name: "dir", Typeflag: tar.TypeDir, Mode: 0o755})
	dw.WriteHeader(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644})
	dw.Write([]byte("x"))
	dw.WriteHeader(&tar.Header{Name: "big", Typeflag: tar.TypeReg, Size: scratchExportMaxOne + 1, Mode: 0o644})
	dw.Write(make([]byte, scratchExportMaxOne+1))
	dw.Close()
	applied, skipped, err = ApplyExports(context.Background(), apply, fence, eff, AgentExecRequest{}, "rel",
		base64.StdEncoding.EncodeToString(dirTar.Bytes()))
	if err != nil || len(applied) != 0 || len(skipped) != 3 || len(calls) != 0 {
		t.Fatalf("unsafe members: %v %v %v", applied, skipped, err)
	}
	// executor-level error marks the member skipped; transport error aborts
	failApply := func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{Error: "exists"}, nil
	}
	if applied, skipped, err = ApplyExports(context.Background(), failApply, fence, eff, AgentExecRequest{}, "rel", exportTar(t, map[string]string{"dup": "x"})); err != nil || len(applied) != 0 || len(skipped) != 1 {
		t.Fatalf("skipped: %v %v %v", applied, skipped, err)
	}
	boom := func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{}, errors.New("transport")
	}
	if _, _, err := ApplyExports(context.Background(), boom, fence, eff, AgentExecRequest{}, "rel", exportTar(t, map[string]string{"a": "x"})); err == nil {
		t.Fatal("transport error swallowed")
	}
}

func TestScratchLeasePersistence(t *testing.T) {
	// package-global quarantine; isolate mutations for this test
	path := filepath.Join(t.TempDir(), "leases.json")
	ScratchLeaseMu.Lock()
	old := ScratchLeasePath
	ScratchLeasePath = ""
	ScratchLeaseMu.Unlock()
	defer func() {
		ScratchLeaseMu.Lock()
		ScratchLeasePath = old
		ScratchLeaseMu.Unlock()
		ScratchPending.Delete("bind-a")
		ScratchPending.Delete("bind-b")
	}()

	if err := SetScratchLeasePath(path); err != nil {
		t.Fatal(err)
	}
	scratchLeaseSet("bind-a", "hermes-scratch-aaa")
	scratchLeaseSet("bind-b", "hermes-scratch-bbb")
	// mutations persist atomically to the file
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var leases map[string]string
	if err := json.Unmarshal(body, &leases); err != nil || len(leases) != 2 {
		t.Fatalf("persisted: %v %v", leases, err)
	}
	// clearing a lease rewrites the file
	scratchLeaseClear("bind-a")
	body, _ = os.ReadFile(path)
	leases = map[string]string{}
	json.Unmarshal(body, &leases)
	if _, ok := leases["bind-a"]; ok || len(leases) != 1 {
		t.Fatalf("after clear: %v", leases)
	}
	// a corrupt file fails closed: path not enabled, quarantine untouched
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{"), 0o600)
	if err := SetScratchLeasePath(bad); err == nil {
		t.Fatal("corrupt lease accepted")
	}
	// a restart loads the persisted quarantine into memory
	path2 := filepath.Join(t.TempDir(), "leases.json")
	os.WriteFile(path2, []byte(`{"bind-c":"hermes-scratch-ccc"}`), 0o600)
	if err := SetScratchLeasePath(path2); err != nil {
		t.Fatal(err)
	}
	if name, ok := ScratchPending.Load("bind-c"); !ok || name != "hermes-scratch-ccc" {
		t.Fatalf("reload: %v", name)
	}
	ScratchPending.Delete("bind-c")
}
