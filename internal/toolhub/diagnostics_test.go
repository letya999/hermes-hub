package toolhub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// diagFixture registers a credential-free per-user definition and binds it for
// auth, returning the binding ID the projection should expose.
func diagFixture(t *testing.T, store *Store, auth identity.Envelope, workloadID string) string {
	t.Helper()
	definition := catalogReadDefinition()
	definition.DefinitionID = "diag-" + workloadID[len("work-"):len("work-")+8]
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatalf("register definition: %v", err)
	}
	binding := ToolBinding{
		Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: PerUser, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1,
	}
	if err := store.PutBinding(binding); err != nil {
		t.Fatalf("put binding: %v", err)
	}
	bindingID := DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	workload := WorkloadInstance{
		Schema: SchemaVersion, WorkloadID: workloadID, BindingID: bindingID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Class: PerUser,
		Owner: &OwnerRef{Type: ContextOwner, ID: auth.ContextID}, RuntimeID: auth.RuntimeID,
		Generation: 1, Status: RunningStatus, StartedAt: time.Now().UTC(),
	}
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatalf("put workload: %v", err)
	}
	return bindingID
}

func diagWrite(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, diagFileName), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatalf("write diagnostics: %v", err)
	}
}

func diagLine(stamp, container, message string) string {
	return stamp + " [" + container + "] " + message
}

func TestDiagnosticsScopeIsolation(t *testing.T) {
	store := NewStore()
	alice, bob := aliceAuth(), bobAuth()
	diagFixture(t, store, alice, "work-aaaaaaaaaaaaaaaaaaaaaaaa")
	diagFixture(t, store, bob, "work-bbbbbbbbbbbbbbbbbbbbbbbb")
	aliceWork, bobWork := "work-aaaaaaaaaaaaaaaaaaaaaaaa", "work-bbbbbbbbbbbbbbbbbbbbbbbb"
	aliceRuntime := runtimeContainerName(alice.PrincipalID, alice.ContextID)
	bobRuntime := runtimeContainerName(bob.PrincipalID, bob.ContextID)
	dir := t.TempDir()
	diagWrite(t, dir,
		diagLine("2026-09-28T10:00:00Z", aliceWork, "alice workload ready"),
		diagLine("2026-09-28T10:00:01Z", bobWork, "bob workload secret contents"),
		diagLine("2026-09-28T10:00:02Z", aliceRuntime, "alice runtime started"),
		diagLine("2026-09-28T10:00:03Z", bobRuntime, "bob runtime started"),
		diagLine("2026-09-28T10:00:04Z", "hermes-hub-local-dev-toolhub-1", "platform line"),
		"[2026-09-28T10:00:05Z] [host-supervisor] spawned "+aliceRuntime+" generation 1",
		"[2026-09-28T10:00:06Z] [host-supervisor] spawned "+bobRuntime+" generation 1",
	)
	control := &ControlPlane{Store: store, DiagnosticsDir: dir}
	body, err := control.diagnostics(t.Context(), alice, map[string]any{})
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	lines, ok := body["lines"].([]map[string]any)
	if !ok {
		t.Fatalf("lines: %#v", body["lines"])
	}
	var containers []string
	for _, entry := range lines {
		containers = append(containers, entry["workload"].(string))
		if msg := entry["message"].(string); strings.Contains(msg, "bob") {
			t.Fatalf("foreign line leaked: %q", msg)
		}
	}
	joined := strings.Join(containers, ",")
	for _, want := range []string{aliceWork, aliceRuntime, "host-supervisor"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, containers)
		}
	}
	for _, banned := range []string{bobWork, bobRuntime, "hermes-hub-local-dev"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("foreign container %s visible in %v", banned, containers)
		}
	}
	workloads, _ := body["workloads"].([]diagWorkload)
	if len(workloads) != 1 || workloads[0].WorkloadID != aliceWork {
		t.Fatalf("workloads: %#v", body["workloads"])
	}
	// Bob sees only his own material.
	bobBody, err := control.diagnostics(t.Context(), bob, map[string]any{})
	if err != nil {
		t.Fatalf("bob diagnostics: %v", err)
	}
	for _, entry := range bobBody["lines"].([]map[string]any) {
		if w := entry["workload"].(string); w == aliceWork || w == aliceRuntime {
			t.Fatalf("alice workload %s leaked to bob", w)
		}
	}
}

func TestDiagnosticsRedaction(t *testing.T) {
	store := NewStore()
	alice := aliceAuth()
	work := "work-cccccccccccccccccccccccc"
	diagFixture(t, store, alice, work)
	dir := t.TempDir()
	diagWrite(t, dir,
		diagLine("2026-09-28T10:00:00Z", work, `connect token=ghp_1234567890abcdef failed`),
		diagLine("2026-09-28T10:00:01Z", work, `Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdef012345`),
		diagLine("2026-09-28T10:00:02Z", work, `dial postgres://alice:hunter2@db.internal:5432/app`),
		diagLine("2026-09-28T10:00:03Z", work, `bot 8275678764:AAE1234567890abcdefghijklmnopqrstuv refused`),
		diagLine("2026-09-28T10:00:04Z", work, `set-cookie: sessionid=abcdef1234567890`),
	)
	control := &ControlPlane{Store: store, DiagnosticsDir: dir}
	body, err := control.diagnostics(t.Context(), alice, map[string]any{})
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	for _, entry := range body["lines"].([]map[string]any) {
		msg := entry["message"].(string)
		for _, secret := range []string{"ghp_1234567890abcdef", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIi", "hunter2", "AAE1234567890", "abcdef1234567890"} {
			if strings.Contains(msg, secret) {
				t.Fatalf("secret leaked in %q", msg)
			}
		}
	}
}

func TestDiagnosticsBoundsAndFilters(t *testing.T) {
	store := NewStore()
	alice := aliceAuth()
	work := "work-dddddddddddddddddddddddd"
	diagFixture(t, store, alice, work)
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, diagLine(fmt.Sprintf("2026-09-28T10:00:%02dZ", i), work, fmt.Sprintf("line %d noise", i)))
	}
	lines = append(lines, diagLine("2026-09-28T10:01:00Z", work, "ERROR crash loop detected"))
	diagWrite(t, dir, lines...)
	control := &ControlPlane{Store: store, DiagnosticsDir: dir}
	body, err := control.diagnostics(t.Context(), alice, map[string]any{"tail": float64(5)})
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	if got := len(body["lines"].([]map[string]any)); got != 5 {
		t.Fatalf("tail bound ignored, got %d lines", got)
	}
	// Severity filter keeps only error-class lines.
	body, err = control.diagnostics(t.Context(), alice, map[string]any{"severity": "error"})
	if err != nil {
		t.Fatalf("diagnostics severity: %v", err)
	}
	got := body["lines"].([]map[string]any)
	if len(got) != 1 || !strings.Contains(got[0]["message"].(string), "crash loop") {
		t.Fatalf("severity filter: %#v", got)
	}
	// Search substring.
	body, err = control.diagnostics(t.Context(), alice, map[string]any{"search": "line 42"})
	if err != nil {
		t.Fatalf("diagnostics search: %v", err)
	}
	if got := body["lines"].([]map[string]any); len(got) != 1 {
		t.Fatalf("search filter: %#v", got)
	}
	// since/until window.
	body, err = control.diagnostics(t.Context(), alice, map[string]any{"since": "2026-09-28T10:00:30Z", "until": "2026-09-28T10:00:35Z"})
	if err != nil {
		t.Fatalf("diagnostics window: %v", err)
	}
	if got := body["lines"].([]map[string]any); len(got) != 6 {
		t.Fatalf("time window: %#v", got)
	}
	// Invalid args fail closed.
	if _, err = control.diagnostics(t.Context(), alice, map[string]any{"severity": "chatty"}); err == nil {
		t.Fatal("invalid severity accepted")
	}
	if _, err = control.diagnostics(t.Context(), alice, map[string]any{"since": "yesterday"}); err == nil {
		t.Fatal("invalid since accepted")
	}
	if _, err = control.diagnostics(t.Context(), alice, map[string]any{"tail": "lots"}); err == nil {
		t.Fatal("invalid tail accepted")
	}
}

func TestDiagnosticsSelectorAndRevocation(t *testing.T) {
	store := NewStore()
	alice := aliceAuth()
	bindingID := diagFixture(t, store, alice, "work-eeeeeeeeeeeeeeeeeeeeeeee")
	dir := t.TempDir()
	diagWrite(t, dir, diagLine("2026-09-28T10:00:00Z", "work-eeeeeeeeeeeeeeeeeeeeeeee", "visible"))
	control := &ControlPlane{Store: store, DiagnosticsDir: dir}
	// Selector narrows to the workload family.
	body, err := control.diagnostics(t.Context(), alice, map[string]any{"workload": "work-eeeeeeeeeeeeeeeeeeeeeeee"})
	if err != nil || len(body["lines"].([]map[string]any)) != 1 {
		t.Fatalf("selector: %v %#v", err, body)
	}
	// Selector by binding_id resolves too.
	if body, err = control.diagnostics(t.Context(), alice, map[string]any{"workload": bindingID}); err != nil || len(body["lines"].([]map[string]any)) != 1 {
		t.Fatalf("binding selector: %v %#v", err, body)
	}
	// A foreign workload selector yields nothing rather than foreign data.
	body, err = control.diagnostics(t.Context(), alice, map[string]any{"workload": "work-ffffffffffffffffffffffff"})
	if err != nil {
		t.Fatalf("foreign selector: %v", err)
	}
	if len(body["lines"].([]map[string]any)) != 0 || len(body["workloads"].([]diagWorkload)) != 0 {
		t.Fatalf("foreign selector exposed data: %#v", body)
	}
	// Revoking the binding removes the workload from diagnostics scope.
	if err := store.SetBindingStatus(bindingID, RevokedStatus); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	body, err = control.diagnostics(t.Context(), alice, map[string]any{})
	if err != nil {
		t.Fatalf("post-revoke: %v", err)
	}
	if len(body["workloads"].([]diagWorkload)) != 0 || len(body["lines"].([]map[string]any)) != 0 {
		t.Fatalf("revoked workload still visible: %#v", body)
	}
}

func TestDiagnosticsDisabledAndMissing(t *testing.T) {
	store := NewStore()
	alice := aliceAuth()
	control := &ControlPlane{Store: store}
	body, err := control.diagnostics(t.Context(), alice, map[string]any{})
	if err != nil || body["enabled"] != false {
		t.Fatalf("disabled diagnostics: %v %#v", err, body)
	}
	control.DiagnosticsDir = t.TempDir()
	body, err = control.diagnostics(t.Context(), alice, map[string]any{})
	if err != nil || body["enabled"] != true {
		t.Fatalf("empty diagnostics: %v %#v", err, body)
	}
	if len(body["lines"].([]map[string]any)) != 0 {
		t.Fatalf("expected no lines: %#v", body)
	}
}

func TestDiagnosticsCancellationAndConcurrency(t *testing.T) {
	store := NewStore()
	alice := aliceAuth()
	work := "work-999999999999999999999999"
	diagFixture(t, store, alice, work)
	dir := t.TempDir()
	diagWrite(t, dir, diagLine("2026-09-28T10:00:00Z", work, "x"))
	control := &ControlPlane{Store: store, DiagnosticsDir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := control.diagnostics(ctx, alice, map[string]any{}); err == nil {
		t.Fatal("canceled context accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := control.diagnostics(t.Context(), alice, map[string]any{}); err != nil {
				t.Errorf("concurrent diagnostics: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestDiagnosticsRejectsAuthorityArgs(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, DiagnosticsDir: t.TempDir()}
	if _, err := control.Invoke(t.Context(), aliceAuth(), "diagnostics", map[string]any{"owner": "root"}); err == nil {
		t.Fatal("authority argument accepted")
	}
}
