package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobResolvePostsOperatorVerdict(t *testing.T) {
	var got struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	conflict := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/jobs/resolve" || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Errorf("bad request: %s %s auth=%v", r.Method, r.URL.Path, r.Header.Get("Authorization") != "")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if conflict != "" {
			http.Error(w, conflict, http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	opts := jobResolveOptions{Dir: t.TempDir(), Environment: "dev", JobID: "job-1", Status: "failed", GatewayURL: server.URL, GatewayAuth: "service-token"}
	var out strings.Builder
	if err := runJobResolve(context.Background(), &out, opts); err != nil {
		t.Fatal(err)
	}
	if got.JobID != "job-1" || got.Status != "failed" || !strings.Contains(out.String(), "job-1") {
		t.Fatalf("body=%+v out=%q", got, out.String())
	}
	// Two distinct 409 reasons must reach the operator as different localized
	// causes, not an indistinguishable status code.
	conflict = "job is not uncertain"
	if err := runJobResolve(context.Background(), &out, opts); err == nil || !strings.Contains(err.Error(), "not uncertain") {
		t.Fatalf("not-uncertain conflict must name its cause: %v", err)
	}
	conflict = "job is being observed"
	if err := runJobResolve(context.Background(), &out, opts); err == nil || !strings.Contains(err.Error(), "observ") {
		t.Fatalf("observing conflict must name its cause: %v", err)
	}
	conflict = "upstream exploded"
	if err := runJobResolve(context.Background(), &out, opts); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("unrecognized conflict keeps the status code: %v", err)
	}
}

func TestJobResolveRequiresBearerAndJob(t *testing.T) {
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	if err := runJobResolve(context.Background(), &strings.Builder{}, jobResolveOptions{Dir: t.TempDir(), Environment: "dev", JobID: "job-1", Status: "interrupted"}); err == nil {
		t.Fatal("resolve ran without a service-control bearer")
	}
	for _, args := range [][]string{nil, {"cancel"}, {"resolve", "--dir", t.TempDir()}} {
		if err := runJob(context.Background(), args); err == nil {
			t.Fatalf("args %v accepted", args)
		}
	}
}
