package main

import "testing"

func TestRunFlagRejectsInvalidRunID(t *testing.T) {
	// An invalid --run must fail fast with exit 1, before any ledger work.
	code := cmdRun([]string{"--run", "bad id", "--", "true"})
	if code != 1 {
		t.Fatalf("cmdRun with invalid --run exited %d, want 1", code)
	}
}

func TestRunFlagAcceptsValidRunIDShape(t *testing.T) {
	// A well-formed --run must get past validation. We give it a command that
	// does not exist so it fails later (exit 1 from the launch), but crucially
	// not with the validation message; a valid id must not be rejected outright.
	// Using an empty command instead isolates the validation gate: no command
	// yields exit 2, proving we passed the run-id check (which would be exit 1).
	code := cmdRun([]string{"--run", "good.run_1-2"})
	if code != 2 {
		t.Fatalf("cmdRun with valid --run and no command exited %d, want 2 (past run-id validation)", code)
	}
}
