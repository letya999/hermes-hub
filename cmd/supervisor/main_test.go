package main

import (
	"os"
	"testing"
)

func TestSupervisorCLIAuthAndDefaults(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_AUTH", "supervisor-secret")
	if supervisorAuthFromEnv() != "supervisor-secret" || envOr("MISSING_SUPERVISOR_ENV", "fallback") != "fallback" {
		t.Fatal("supervisor environment defaults are wrong")
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	oldArgs := os.Args
	os.Args = []string{"hub-supervisor"}
	t.Cleanup(func() { os.Args = oldArgs })
	if err := run(); err == nil {
		t.Fatal("supervisor started without auth")
	}
}
