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

//go:build unix

package reactions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

// TestCommandHookRunsWithEnv runs a real command hook and checks that event data
// arrives in the LEASH_* environment. Unix-only because it uses a shell script.
func TestCommandHookRunsWithEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s %s %s' \"$LEASH_EVENT\" \"$LEASH_RUN\" \"$LEASH_REASON\" > "+out+"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	d, err := NewDispatcher(Config{DSN: filepath.Join(dir, "r.db"), Command: script, Logger: silentLogger(), RetryPolicy: fastPolicy()})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer closeDispatcher(d)

	d.RunStopped(&policy.State{RunID: "run-77", StopReason: "cost_budget"})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
			if got := string(b); got != "stopped run-77 cost_budget" {
				t.Fatalf("hook wrote %q, want %q", got, "stopped run-77 cost_budget")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("command hook did not run within 5s")
}
