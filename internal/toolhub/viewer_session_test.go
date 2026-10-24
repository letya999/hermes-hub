package toolhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A restarted process must spend a persisted viewer link: the ledger file
// survives on disk while the in-memory map does not, so the lazy load has to
// initialize the map before filling it — otherwise the first post-restart
// spend panics on a nil-map write and the viewer can never open again.
func TestSpendViewerLinkAfterRestartLoadsLedger(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/store.json"
	if err := NewStore().Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nonce, _, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-1", now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.SpendViewerLink("conn-browser", nonce, now); err != nil {
		t.Fatalf("post-restart spend: %v", err)
	}
}

// S8.3: spending a viewer link opens a short-lived owner session; the session
// forwards only the bounded browser-viewer toolset against the owner's own
// binding — eval, network and file tools are never reachable through it.
func TestViewerSessionToolAuthorization(t *testing.T) {
	store := NewStore()
	definition := browserDefinition()
	definition.Tools = []api.ToolSpec{
		{Name: "browser_take_screenshot", Effect: api.ReadEffect},
		{Name: "browser_mouse_click_xy", Effect: api.WriteEffect},
		{Name: "browser_evaluate", Effect: api.WriteEffect},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	connection := browserConnection()
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{
		Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision,
		PolicyVersion: "policy-1", WorkloadClass: definition.Workload.Class,
		Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1,
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	// Another principal's binding on their own connection must stay out of
	// alice's session reach.
	bobConn := browserConnection()
	bobConn.ConnectionID = "conn-bob"
	bobConn.Owner = api.OwnerRef{Type: api.PrincipalOwner, ID: "bob"}
	if err := store.PutConnection(bobConn); err != nil {
		t.Fatal(err)
	}
	bobBinding := binding
	bobBinding.PrincipalID = "bob"
	bobBinding.ContextID = "bob"
	bobBinding.RuntimeID = "runtime-bob"
	bobBinding.ConnectionID = "conn-bob"
	bobBinding.ToolBindingID = api.DeterministicBindingID("bob", "bob", "runtime-bob", binding.DefinitionID, binding.DefinitionVersion, "conn-bob", "")
	if err := store.PutBinding(bobBinding); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	nonce, _, err := store.MintViewerLink(aliceAuth(), connection.ConnectionID, "op-1", now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := store.OpenViewerSession(connection.ConnectionID, nonce, now)
	if err != nil || sessionID == "" {
		t.Fatalf("open session: id=%q err=%v", sessionID, err)
	}
	// The link was spent opening the session — a second open fails gone.
	if _, err := store.OpenViewerSession(connection.ConnectionID, nonce, now); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("reopen: %v", err)
	}
	var admitted []string
	admit := func(spec api.ToolSpec, effective api.EffectiveBinding) error {
		admitted = append(admitted, spec.Name)
		if effective.Binding.ToolBindingID != binding.ToolBindingID {
			t.Fatalf("viewer resolved foreign binding %s", effective.Binding.ToolBindingID)
		}
		if effective.WorkloadID == "" {
			t.Fatal("viewer forward lost workload identity")
		}
		return nil
	}
	if err := store.CallViewerTool(sessionID, "browser_take_screenshot", now.Add(time.Minute), admit); err != nil {
		t.Fatalf("screenshot forward: %v", err)
	}
	if err := store.CallViewerTool(sessionID, "browser_mouse_click_xy", now.Add(2*time.Minute), admit); err != nil {
		t.Fatalf("input forward: %v", err)
	}
	if !slices.Equal(admitted, []string{"browser_take_screenshot", "browser_mouse_click_xy"}) {
		t.Fatalf("admitted=%v", admitted)
	}
	// eval never forwards through the viewer, whitelisted or not.
	if err := store.CallViewerTool(sessionID, "browser_evaluate", now.Add(3*time.Minute), admit); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("eval forward: %v", err)
	}
	if err := store.CallViewerTool(sessionID, "navigate", now.Add(3*time.Minute), admit); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("non-viewer tool forward: %v", err)
	}
	// Unknown and expired sessions fail closed.
	if err := store.CallViewerTool("forged", "browser_take_screenshot", now.Add(3*time.Minute), admit); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("forged session: %v", err)
	}
	if err := store.CallViewerTool(sessionID, "browser_take_screenshot", now.Add(3*time.Hour), admit); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("expired session: %v", err)
	}
	// Disabling the connection mid-session cuts the next forward.
	if err := store.SetConnectionStatus(connection.ConnectionID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.CallViewerTool(sessionID, "browser_take_screenshot", now.Add(4*time.Minute), admit); err == nil {
		t.Fatal("disabled connection still served the viewer")
	}
}

// S8.3: the rendered page polls screenshot frames and posts input through the
// session — the frame endpoint returns image bytes from the workload's own
// screenshot tool, the input endpoint forwards only allowlisted calls.
func TestServeViewerSessionForwardsCalls(t *testing.T) {
	store := NewStore()
	definition := browserDefinition()
	definition.Tools = []api.ToolSpec{
		{Name: "browser_take_screenshot", Effect: api.ReadEffect},
		{Name: "browser_mouse_click_xy", Effect: api.WriteEffect},
		{Name: "browser_evaluate", Effect: api.WriteEffect},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	connection := browserConnection()
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{
		Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision,
		PolicyVersion: "policy-1", WorkloadClass: definition.Workload.Class,
		Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1,
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	var calls []string
	frame := []byte("jpeg-frame-bytes")
	backend := backendFunc(func(ctx context.Context, effective api.EffectiveBinding, spec api.ToolSpec, args map[string]any) (api.BackendResult, error) {
		calls = append(calls, spec.Name)
		if spec.Name == "browser_take_screenshot" {
			return api.BackendResult{Content: []mcp.Content{&mcp.ImageContent{Data: frame, MIMEType: "image/jpeg"}}}, nil
		}
		return api.BackendResult{Text: "ok"}, nil
	})
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	nonce, _, err := store.MintViewerLink(aliceAuth(), connection.ConnectionID, "op-1", time.Now(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	public := (&Gateway{Control: control, Backend: backend}).PublicMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://hub.example.com/viewer/conn-browser?t="+nonce, nil)
	req.Host = "hub.example.com"
	public.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer page status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	sessionID := ""
	for _, line := range strings.Split(body, "\"") {
		if strings.HasPrefix(line, "session=") {
			sessionID = strings.TrimPrefix(line, "session=")
		}
	}
	if sessionID == "" {
		t.Fatalf("page carried no session id: %s", body)
	}
	// Frame poll: the workload's own screenshot tool answers as image bytes.
	frameRec := httptest.NewRecorder()
	frameReq := httptest.NewRequest(http.MethodGet, "https://hub.example.com/viewer/conn-browser/frame?session="+sessionID, nil)
	frameReq.Host = "hub.example.com"
	public.ServeHTTP(frameRec, frameReq)
	if frameRec.Code != http.StatusOK || !strings.Contains(frameRec.Header().Get("Content-Type"), "image/") || frameRec.Body.String() != string(frame) {
		t.Fatalf("frame status=%d ct=%s", frameRec.Code, frameRec.Header().Get("Content-Type"))
	}
	// Input forward reaches the workload as a tool call.
	inputRec := httptest.NewRecorder()
	inputReq := httptest.NewRequest(http.MethodPost, "https://hub.example.com/viewer/conn-browser/input?session="+sessionID,
		strings.NewReader(`{"tool":"browser_mouse_click_xy","arguments":{"x":10,"y":20}}`))
	inputReq.Host = "hub.example.com"
	public.ServeHTTP(inputRec, inputReq)
	if inputRec.Code != http.StatusOK {
		t.Fatalf("input status=%d body=%s", inputRec.Code, inputRec.Body.String())
	}
	if !slices.Equal(calls, []string{"browser_take_screenshot", "browser_mouse_click_xy"}) {
		t.Fatalf("backend calls=%v", calls)
	}
	// eval and forged sessions never reach the backend.
	evalRec := httptest.NewRecorder()
	evalReq := httptest.NewRequest(http.MethodPost, "https://hub.example.com/viewer/conn-browser/input?session="+sessionID,
		strings.NewReader(`{"tool":"browser_evaluate","arguments":{"function":"()=>1"}}`))
	evalReq.Host = "hub.example.com"
	public.ServeHTTP(evalRec, evalReq)
	if evalRec.Code != http.StatusForbidden {
		t.Fatalf("eval input status=%d", evalRec.Code)
	}
	forgedRec := httptest.NewRecorder()
	forgedReq := httptest.NewRequest(http.MethodGet, "https://hub.example.com/viewer/conn-browser/frame?session=forged", nil)
	forgedReq.Host = "hub.example.com"
	public.ServeHTTP(forgedRec, forgedReq)
	if forgedRec.Code != http.StatusForbidden && forgedRec.Code != http.StatusGone {
		t.Fatalf("forged session status=%d", forgedRec.Code)
	}
}
