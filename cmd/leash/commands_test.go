// Copyright 2026 Sylvester Francis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestCmdHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()

	if code := cmdHealthcheck([]string{"--url", ok.URL}); code != 0 {
		t.Fatalf("healthcheck of a 200 = %d, want 0", code)
	}
	if code := cmdHealthcheck([]string{"--url", bad.URL}); code != 1 {
		t.Fatalf("healthcheck of a 503 = %d, want 1", code)
	}
}
