package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIExecFromEnvContract(t *testing.T) {
	var gotRequest CliExecRequest
	var gotAuth string
	status := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte("busy"))
			return
		}
		if r.URL.Path != "/cli-exec" {
			w.WriteHeader(404)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotRequest)
		json.NewEncoder(w).Encode(CliExecResponse{Output: "hi", ExitCode: 0})
	}))
	defer server.Close()

	// no endpoint configured: nil exec, every call fails closed
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	execFn, err := CLIExecFromEnv()
	if err != nil || execFn != nil {
		t.Fatalf("no endpoint: %v", execFn)
	}
	// public endpoint is refused outright
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "https://169.254.169.254/x")
	if _, err := CLIExecFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("public endpoint: %v", err)
	}
	// private controller endpoint posts the verbatim request to /cli-exec
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "tok")
	execFn, err = CLIExecFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := execFn(context.Background(), CliExecRequest{Plan: ControllerPlan{WorkloadID: "wl-1"}, Command: "ls"})
	if err != nil || resp.Output != "hi" {
		t.Fatalf("exec: %v %+v", err, resp)
	}
	if gotRequest.Command != "ls" || gotRequest.Plan.WorkloadID != "wl-1" || gotAuth != "Bearer tok" {
		t.Fatalf("request: %+v auth %q", gotRequest, gotAuth)
	}
	// non-200 responses surface the controller's status and detail
	status = 409
	if _, err := execFn(context.Background(), CliExecRequest{}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("409: %v", err)
	}
	status = 0
}

// CLIExecClient is the host-side constructor hubctl uses: explicit
// endpoint+token instead of the container env pair; the /admit -> /cli-exec
// derivation and privacy validation are the same contract.
func TestCLIExecClientContract(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-exec" {
			w.WriteHeader(404)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(CliExecResponse{Output: "hi", ExitCode: 0})
	}))
	defer server.Close()
	if _, err := CLIExecClient("https://169.254.169.254/x", "tok"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("public endpoint: %v", err)
	}
	execFn, err := CLIExecClient(server.URL+"/admit", "tok")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := execFn(context.Background(), CliExecRequest{Command: "git"})
	if err != nil || resp.Output != "hi" || gotAuth != "Bearer tok" {
		t.Fatalf("exec: %v %+v auth %q", err, resp, gotAuth)
	}
}

func TestCLIReleaseFromEnvContract(t *testing.T) {
	var released CliReleaseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-release" {
			w.WriteHeader(404)
			return
		}
		json.NewDecoder(r.Body).Decode(&released)
		w.WriteHeader(204)
	}))
	defer server.Close()

	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	if fn, err := CLIReleaseFromEnv(); err != nil || fn != nil {
		t.Fatalf("no endpoint: err=%v", err)
	}
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "tok")
	fn, err := CLIReleaseFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(context.Background(), CliReleaseRequest{BindingID: "bind-1"}); err != nil {
		t.Fatal(err)
	}
	if released.BindingID != "bind-1" {
		t.Fatalf("release: %+v", released)
	}
}
