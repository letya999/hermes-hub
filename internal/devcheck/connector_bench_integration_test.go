//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

// One full cycle against the local daemon: the fixture MCP server must answer
// initialize/tools/list/tools/call inside the workload profile, and the
// sample must carry sane timings for every phase.
func TestConnectorBenchSingleRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sample, err := connectorBenchRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sample.CreateMS <= 0 || sample.HandshakeMS <= 0 || sample.InstallMS <= 0 || sample.FirstCallMS <= 0 || sample.RemoveMS <= 0 {
		t.Fatalf("phase timings missing: %+v", sample)
	}
	if sample.InstallMS < sample.CreateMS || sample.FirstCallMS > sample.InstallMS {
		t.Fatalf("implausible timings: %+v", sample)
	}
}

// The full bench emits one JSON report covering install, first call and
// remove with p50/p95, and passes its own budget verdict on this host.
func TestConnectorBenchReport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	if err := ConnectorBench(ctx, 5, &out); err != nil {
		t.Fatalf("%v\nreport: %s", err, out.String())
	}
	var report connectorBenchReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("report: %v\n%s", err, out.String())
	}
	if report.Install.Runs != 5 || report.Install.P95MS <= 0 || report.FirstCall.P95MS <= 0 || report.Remove.P95MS <= 0 {
		t.Fatalf("incomplete report: %+v", report)
	}
}
