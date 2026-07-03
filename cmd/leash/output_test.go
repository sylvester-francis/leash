package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// captureStdout runs f with os.Stdout redirected to a pipe and returns what it
// wrote.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// seedRun creates a ledger at db with one run holding a single call.
func seedRun(t *testing.T, db, runID string) {
	t.Helper()
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer l.Close()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	if err := l.EnsureRun(context.Background(), runID, now); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	rec := policy.CallRecord{Usage: policy.Usage{Model: "gpt-4o", InputTokens: 42, OutputTokens: 7}, At: now}
	if err := l.AppendCall(context.Background(), runID, 0, rec); err != nil {
		t.Fatalf("append call: %v", err)
	}
}

func TestPsJSON(t *testing.T) {
	db := filepath.Join(t.TempDir(), "leash.db")
	seedRun(t, db, "runA")
	out := captureStdout(t, func() {
		if code := cmdPs([]string{"--db", db, "--json"}); code != 0 {
			t.Fatalf("cmdPs exit = %d, want 0", code)
		}
	})
	var runs []runJSON
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		t.Fatalf("ps --json not valid JSON: %v\n%s", err, out)
	}
	if len(runs) != 1 || runs[0].Run != "runA" {
		t.Fatalf("ps --json = %+v, want one run runA", runs)
	}
	if runs[0].InputTokens != 42 || runs[0].OutputTokens != 7 {
		t.Fatalf("ps --json tokens = (%d,%d), want (42,7)", runs[0].InputTokens, runs[0].OutputTokens)
	}
	if runs[0].Status != "running" {
		t.Fatalf("ps --json status = %q, want running", runs[0].Status)
	}
}

func TestPsJSONEmptyIsArray(t *testing.T) {
	db := filepath.Join(t.TempDir(), "leash.db")
	out := captureStdout(t, func() { cmdPs([]string{"--db", db, "--json"}) })
	var runs []runJSON
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		t.Fatalf("empty ps --json not valid JSON: %v\n%s", err, out)
	}
	if len(runs) != 0 {
		t.Fatalf("empty ps --json = %+v, want []", runs)
	}
}

func TestInspectJSON(t *testing.T) {
	db := filepath.Join(t.TempDir(), "leash.db")
	seedRun(t, db, "runB")
	out := captureStdout(t, func() {
		if code := cmdInspect([]string{"--db", db, "--json", "runB"}); code != 0 {
			t.Fatalf("cmdInspect exit = %d, want 0", code)
		}
	})
	var got inspectJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("inspect --json not valid JSON: %v\n%s", err, out)
	}
	if got.Run != "runB" || got.Calls != 1 {
		t.Fatalf("inspect --json summary = %+v, want run runB calls 1", got.runJSON)
	}
	if len(got.Entries) != 1 || got.Entries[0].Kind != "call" {
		t.Fatalf("inspect --json entries = %+v, want one call entry", got.Entries)
	}
	if got.Entries[0].InputTokens != 42 {
		t.Fatalf("inspect --json entry input = %d, want 42", got.Entries[0].InputTokens)
	}
}

func TestVersionSubcommand(t *testing.T) {
	out := captureStdout(t, func() {
		if code := dispatch([]string{"version"}); code != 0 {
			t.Fatalf("version exit = %d, want 0", code)
		}
	})
	if !strings.HasPrefix(out, "leash ") {
		t.Fatalf("version output = %q, want it to start with 'leash '", out)
	}
	// Shape: "leash <version> <goversion> <os>/<arch>".
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 4 {
		t.Fatalf("version output = %q, want 4 fields", out)
	}
	if !strings.HasPrefix(fields[2], "go") {
		t.Fatalf("version go field = %q, want it to start with 'go'", fields[2])
	}
	if !strings.Contains(fields[3], "/") {
		t.Fatalf("version platform field = %q, want os/arch", fields[3])
	}
}
