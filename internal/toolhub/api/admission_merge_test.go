package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAdmissionReceiptValidateContract(t *testing.T) {
	exec := ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1024, CPUMillis: 100, MemoryMiB: 32, MaxPIDs: 4, Egress: []string{"h"}}
	cliDef := ToolDefinition{Transport: BoundedCLI, Source: DefinitionSource{Image: "img", Digest: "sha256:" + strings.Repeat("a", 64)}, Execution: exec}
	effective := EffectiveBinding{WorkloadID: "w-1", Definition: cliDef}
	good := AdmissionReceipt{
		WorkloadID: "w-1", State: "running", Enforced: true,
		CellID: "cell-1", Runtime: "runsc", ImageDigest: cliDef.Source.Digest,
		Isolation: []string{"filesystem", "network", "pid"}, Execution: exec,
	}
	if err := good.Validate(effective); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(r *AdmissionReceipt){
		"wrong workload":    func(r *AdmissionReceipt) { r.WorkloadID = "w-2" },
		"not running":       func(r *AdmissionReceipt) { r.State = "pending" },
		"not enforced":      func(r *AdmissionReceipt) { r.Enforced = false },
		"container fields":  func(r *AdmissionReceipt) { r.Endpoint = "http://x" },
		"no cell":           func(r *AdmissionReceipt) { r.CellID = "" },
		"bad cell chars":    func(r *AdmissionReceipt) { r.CellID = "cell 1" },
		"no runtime":        func(r *AdmissionReceipt) { r.Runtime = "custom" },
		"digest mismatch":   func(r *AdmissionReceipt) { r.ImageDigest = "sha256:" + strings.Repeat("b", 64) },
		"missing isolation": func(r *AdmissionReceipt) { r.Isolation = []string{"filesystem"} },
		"exec mismatch":     func(r *AdmissionReceipt) { r.Execution.TimeoutSeconds = 99 },
	}
	for name, mutate := range cases {
		r := good
		mutate(&r)
		if err := r.Validate(effective); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// stray digest on a source-less definition
	noImage := effective
	noImage.Definition.Source = DefinitionSource{}
	r := good
	r.ImageDigest = ""
	if err := r.Validate(noImage); err != nil {
		t.Fatalf("source-less cli: %v", err)
	}
	// container path: endpoint + digest + sidecars must match
	cont := ToolDefinition{Transport: ContainerMCP, Source: DefinitionSource{Image: "img", Digest: "sha256:" + strings.Repeat("a", 64)}, Execution: exec}
	contEff := EffectiveBinding{WorkloadID: "w-1", Definition: cont}
	rc := AdmissionReceipt{WorkloadID: "w-1", State: "running", Enforced: true,
		ImageDigest: cont.Source.Digest, Endpoint: "http://toolhive:8080", Execution: exec}
	if err := rc.Validate(contEff); err != nil {
		t.Fatalf("container receipt: %v", err)
	}
	rc.Endpoint = "https://8.8.8.8/evil"
	if err := rc.Validate(contEff); err == nil {
		t.Fatal("public endpoint admitted in receipt")
	}
	rc.Endpoint = "http://toolhive:8080"
	rc.ImageDigest = "sha256:" + strings.Repeat("c", 64)
	if err := rc.Validate(contEff); err == nil {
		t.Fatal("digest drift admitted")
	}
}

func TestSubmitPlanHTTPContract(t *testing.T) {
	exec := ExecutionPolicy{TimeoutSeconds: 30}
	def := ToolDefinition{Transport: ContainerMCP, Execution: exec}
	effective := EffectiveBinding{WorkloadID: "w-1", Definition: def}
	plan := ControllerPlan{WorkloadID: "w-1"}

	var sawAuth, sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		sawBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AdmissionReceipt{WorkloadID: "w-1", State: "running", Enforced: true, Execution: exec})
	}))
	defer srv.Close()
	receipt, err := SubmitPlan(context.Background(), srv.Client(), srv.URL, "tok", plan, effective)
	if err != nil || receipt.WorkloadID != "w-1" {
		t.Fatalf("submit: %v %+v", err, receipt)
	}
	if sawAuth != "Bearer tok" || !strings.Contains(sawBody, `"workload_id":"w-1"`) {
		t.Fatalf("request: auth=%q body=%q", sawAuth, sawBody)
	}
	// 204 without receipt → attempt, not proof
	srv204 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv204.Close()
	if _, err := SubmitPlan(context.Background(), srv204.Client(), srv204.URL, "", plan, effective); err == nil {
		t.Fatal("empty 204 accepted as proof")
	}
	// non-2xx
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("boom"))
	}))
	defer srvErr.Close()
	if _, err := SubmitPlan(context.Background(), srvErr.Client(), srvErr.URL, "", plan, effective); err == nil {
		t.Fatal("500 accepted")
	}
	// invalid JSON receipt
	srvBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{bad")) }))
	defer srvBad.Close()
	if _, err := SubmitPlan(context.Background(), srvBad.Client(), srvBad.URL, "", plan, effective); err == nil {
		t.Fatal("malformed receipt accepted")
	}
	// valid JSON but failed validation
	srvWeak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(AdmissionReceipt{WorkloadID: "other"})
	}))
	defer srvWeak.Close()
	if _, err := SubmitPlan(context.Background(), srvWeak.Client(), srvWeak.URL, "", plan, effective); err == nil {
		t.Fatal("mismatched receipt accepted")
	}
}

func TestControllerEnvSeams(t *testing.T) {
	// no env → nil seams, no error
	for _, f := range []func() (any, error){
		func() (any, error) { return ControllerAdmissionFromEnv() },
		func() (any, error) { return ControllerAdmissionVerifierFromEnv() },
		func() (any, error) { return CLIExecFromEnv() },
		func() (any, error) { return CLIReleaseFromEnv() },
		func() (any, error) { return ControllerAdmissionReleaserFromEnv() },
	} {
		got, err := f()
		if err != nil || !reflect.ValueOf(got).IsNil() {
			t.Fatalf("env seam without env: %v %v", got, err)
		}
	}
	// public endpoint fails closed before any wiring
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "https://8.8.8.8/admit")
	if _, err := CLIExecFromEnv(); err == nil {
		t.Fatal("public admission endpoint accepted")
	}
	// private endpoint wires release + exec against derived URLs
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "http://127.0.0.1:9/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "secret")
	release, err := CLIReleaseFromEnv()
	if err != nil || release == nil {
		t.Fatalf("release seam: %v", err)
	}
	// connection refused is fine — proves wiring reached the dial
	if err := release(context.Background(), CliReleaseRequest{}); err == nil {
		t.Fatal("release to dead endpoint succeeded")
	}
	releaser, err := ControllerAdmissionReleaserFromEnv()
	if err != nil || releaser == nil {
		t.Fatalf("releaser seam: %v", err)
	}
	if err := releaser(context.Background(), ControllerRelease{}); err == nil {
		t.Fatal("empty workload release accepted")
	}
	if err := releaser(context.Background(), ControllerRelease{WorkloadID: "w-1"}); err == nil {
		t.Fatal("release to dead endpoint succeeded")
	}
}
