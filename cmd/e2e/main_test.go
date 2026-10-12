package main

import "testing"

func TestCommandRejectsInvalidSelectionBeforeAccountAccess(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"mock", "--select", "E2E-999"}, {"telegram", "--bad"}, {"telegram", "extra"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestManualModeNeverPassesWithoutObservation(t *testing.T) {
	if err := run([]string{"telegram", "--interactive", "--select", "E2E-064"}); err == nil {
		t.Fatal("unobserved live action passed")
	}
}
