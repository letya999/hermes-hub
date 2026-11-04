package agenttools

import (
	"testing"
)

// SPEC-0093 R3.2/R3.3 + issue #441 S1: the terminal family runs on the
// persistent in-runtime daemon executor — a disposable scratch cell cannot
// hold a background job across calls — while hub-side backends (models
// passport, web provider relay) declare runtime: executors instead of
// in-runtime handlers that do not exist.
func TestExecutorRoutingContract(t *testing.T) {
	specs := map[string]struct {
		sandboxed bool
		executor  string
	}{}
	for _, spec := range HubToolsDefinition().Tools {
		specs[spec.Name] = struct {
			sandboxed bool
			executor  string
		}{spec.Sandboxed, spec.Executor}
	}
	exec, ok := specs["terminal_exec"]
	if !ok {
		t.Fatal("terminal_exec missing from the catalog")
	}
	if exec.sandboxed {
		t.Error("terminal_exec is sandboxed: a disposable cell cannot host persistent jobs")
	}
	if exec.executor != "handler:terminal" {
		t.Errorf("terminal_exec executor = %q, want handler:terminal", exec.executor)
	}
	job, ok := specs["terminal_job"]
	if !ok {
		t.Fatal("terminal_job missing from the catalog")
	}
	if job.sandboxed || job.executor != "handler:terminal" {
		t.Errorf("terminal_job = %+v, want non-sandboxed handler:terminal", job)
	}
	for name, executor := range map[string]string{
		"model_list":   "runtime:models",
		"model_switch": "runtime:models",
		"web_search":   "runtime:web",
		"web_fetch":    "runtime:web",
	} {
		spec, ok := specs[name]
		if !ok {
			t.Errorf("%s missing from the catalog", name)
			continue
		}
		if spec.executor != executor {
			t.Errorf("%s executor = %q, want %q", name, spec.executor, executor)
		}
	}
}
