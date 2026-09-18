package toolhub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControllerAdmissionSendsReviewedDefinitionWithoutAuthorityOrSecretValues(t *testing.T) {
	var received string
	definition := statefulContainerDefinition()
	effective := EffectiveBinding{Definition: definition, WorkloadID: "fixture-workload"}
	effective.Binding = ToolBinding{ToolBindingID: "fixture-binding", PrincipalID: "alice", ContextID: "alice"}
	t.Setenv("HUB_STATE", t.TempDir())
	receipt, _ := json.Marshal(AdmissionReceipt{WorkloadID: effective.WorkloadID, State: "running", Enforced: true, ImageDigest: definition.Source.Digest, SidecarImages: definition.Workload.SidecarImages, Execution: definition.Execution})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		_, _ = w.Write(receipt)
	}))
	defer server.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL)
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", strings.Repeat("s", 32))
	admit, err := ControllerAdmissionVerifierFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admit(context.Background(), effective); err != nil {
		t.Fatal(err)
	}
	if _, err := admit(context.Background(), EffectiveBinding{Definition: remoteDefinition()}); err != nil {
		t.Fatalf("remote admission unexpectedly used controller: %v", err)
	}
	legacyAdmit, err := ControllerAdmissionFromEnv()
	if err != nil || legacyAdmit == nil {
		t.Fatalf("legacy admission wrapper unavailable: %v", err)
	}
	if err := legacyAdmit(context.Background(), effective); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, definition.DefinitionID) || !strings.Contains(received, "SERVICE_TOKEN") || strings.Contains(received, "principal_id") || strings.Contains(received, "credential_locator") || strings.Contains(received, "private-value") {
		t.Fatalf("controller plan leaked authority or credentials: %s", received)
	}
}

func TestControllerAdmissionDeniesNonSuccess(t *testing.T) {
	effective := EffectiveBinding{Definition: statefulContainerDefinition(), WorkloadID: "fixture-workload"}
	for _, receipt := range []AdmissionReceipt{
		{WorkloadID: "wrong", State: "running", Enforced: true, ImageDigest: effective.Definition.Source.Digest, SidecarImages: effective.Definition.Workload.SidecarImages, Execution: effective.Definition.Execution},
		{WorkloadID: effective.WorkloadID, State: "stopped", Enforced: true, ImageDigest: effective.Definition.Source.Digest, SidecarImages: effective.Definition.Workload.SidecarImages, Execution: effective.Definition.Execution},
		{WorkloadID: effective.WorkloadID, State: "running", Enforced: true, ImageDigest: "sha256:" + strings.Repeat("0", 64), SidecarImages: effective.Definition.Workload.SidecarImages, Execution: effective.Definition.Execution},
	} {
		if err := receipt.validate(effective); err == nil {
			t.Fatal("invalid controller receipt accepted")
		}
	}
	unsafe := AdmissionReceipt{WorkloadID: effective.WorkloadID, State: "running", Enforced: true, Endpoint: "https://public.example/mcp", ImageDigest: effective.Definition.Source.Digest, SidecarImages: effective.Definition.Workload.SidecarImages, Execution: effective.Definition.Execution}
	if err := unsafe.validate(effective); err == nil {
		t.Fatal("public receipt endpoint accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "denied", http.StatusForbidden) }))
	defer server.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL)
	admit, err := ControllerAdmissionVerifierFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admit(context.Background(), EffectiveBinding{Definition: statefulContainerDefinition(), WorkloadID: "fixture-workload"}); err == nil {
		t.Fatal("controller denial was accepted")
	}
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server2.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server2.URL)
	admit, err = ControllerAdmissionVerifierFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admit(context.Background(), EffectiveBinding{Definition: statefulContainerDefinition(), WorkloadID: "fixture-workload"}); err == nil {
		t.Fatal("empty 204 admission was accepted as enforcement proof")
	}
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "https://public.example/admit")
	if _, err := ControllerAdmissionFromEnv(); err == nil {
		t.Fatal("public controller endpoint accepted")
	}
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	admit, err = ControllerAdmissionVerifierFromEnv()
	if err != nil || admit != nil {
		t.Fatalf("unset controller endpoint did not disable admission: %v", err)
	}
}

func TestControllerAdmissionReleaserUsesAuthenticatedReleaseEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", strings.Repeat("r", 32))
	release, err := ControllerAdmissionReleaserFromEnv()
	if err != nil || release == nil {
		t.Fatalf("release endpoint was not configured: %v", err)
	}
	if err := release(context.Background(), "fixture-workload"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admit/release" || gotAuth != "Bearer "+strings.Repeat("r", 32) {
		t.Fatalf("unexpected release request path=%q auth=%q", gotPath, gotAuth)
	}
	if err := release(context.Background(), ""); err == nil {
		t.Fatal("empty workload release accepted")
	}
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "https://public.example/admit")
	if _, err := ControllerAdmissionReleaserFromEnv(); err == nil {
		t.Fatal("public release endpoint accepted")
	}
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	disabled, err := ControllerAdmissionReleaserFromEnv()
	if err != nil || disabled != nil {
		t.Fatalf("unset release endpoint did not disable release: %v", err)
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "denied", http.StatusForbidden) }))
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", denied.URL)
	release, err = ControllerAdmissionReleaserFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background(), "fixture-workload"); err == nil {
		t.Fatal("failed release response accepted")
	}
	denied.Close()
	server.Close()
	failed, err := ControllerAdmissionReleaserFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := failed(context.Background(), "fixture-workload"); err == nil {
		t.Fatal("network release failure accepted")
	}
}
