package agenttools

import (
	"testing"
)

// The terminal family uses ToolHub job control and disposable agent-image
// workloads; hub-side model and web backends retain their runtime executors.
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
		t.Error("terminal_exec must route through ToolHub job control")
	}
	if exec.executor != "handler:terminal" {
		t.Errorf("terminal_exec executor = %q, want handler:terminal", exec.executor)
	}
	job, ok := specs["terminal_job"]
	if !ok {
		t.Fatal("terminal_job missing from the catalog")
	}
	if job.sandboxed || job.executor != "handler:terminal" {
		t.Errorf("terminal_job = %+v, want ToolHub handler:terminal", job)
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
