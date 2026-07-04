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

package ledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// backend names a ledger backend and how to produce a data-source string for it
// that a single subtest shares across handles.
type backend struct {
	name string
	dsn  func(t *testing.T) string
}

// backends returns the backends to run the suite against: SQLite always, and
// PostgreSQL when LEASH_TEST_POSTGRES_DSN is set (skipped otherwise, so the
// suite is green with no database to hand and exercised for real in CI).
func backends() []backend {
	bs := []backend{{
		name: "sqlite",
		dsn:  func(t *testing.T) string { return filepath.Join(t.TempDir(), "leash.db") },
	}}
	if dsn := os.Getenv("LEASH_TEST_POSTGRES_DSN"); dsn != "" {
		bs = append(bs, backend{
			name: "postgres",
			dsn:  func(t *testing.T) string { return dsn },
		})
	}
	return bs
}

// runCounter makes run ids unique so a persistent PostgreSQL database shared
// across subtests and repeated CI runs never collides on a run id.
var runCounter atomic.Int64

func uniqueRun() string {
	return fmt.Sprintf("t-%d-%d", time.Now().UnixNano(), runCounter.Add(1))
}

// openLedger opens a ledger from dsn and closes it at test end.
func openLedger(t *testing.T, dsn string) *Ledger {
	t.Helper()
	l, err := Open(dsn)
	if err != nil {
		t.Fatalf("open ledger %q: %v", dsn, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestBackendAppendAndLoadRebuilds(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			l := openLedger(t, b.dsn(t))
			run := uniqueRun()
			base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
			if err := l.EnsureRun(ctx, run, base); err != nil {
				t.Fatalf("EnsureRun: %v", err)
			}
			if err := l.AppendCall(ctx, run, 0, call("gpt-4", 1_000_000, 500_000, "fp1", base)); err != nil {
				t.Fatalf("AppendCall: %v", err)
			}
			s, err := l.Load(ctx, run, testGovernor())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if s.Calls != 1 || s.InputTokens != 1_000_000 || s.OutputTokens != 500_000 {
				t.Fatalf("state = %+v, want 1 call (1000000,500000)", s)
			}
		})
	}
}

func TestBackendKillIsDurable(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			l := openLedger(t, b.dsn(t))
			run := uniqueRun()
			now := time.Now()
			if err := l.EnsureRun(ctx, run, now); err != nil {
				t.Fatalf("EnsureRun: %v", err)
			}
			if err := l.AppendKill(ctx, run, now); err != nil {
				t.Fatalf("AppendKill: %v", err)
			}
			s, _ := l.Load(ctx, run, testGovernor())
			if !s.Killed {
				t.Fatalf("Killed = false after AppendKill")
			}
		})
	}
}

func TestBackendKillFromSecondHandle(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			dsn := b.dsn(t)
			run := uniqueRun()
			now := time.Now()

			governor := openLedger(t, dsn)
			if err := governor.EnsureRun(ctx, run, now); err != nil {
				t.Fatalf("EnsureRun: %v", err)
			}
			if err := governor.AppendCall(ctx, run, 0, call("gpt-4", 1000, 10, "fp", now)); err != nil {
				t.Fatalf("AppendCall: %v", err)
			}

			// A separate handle stands in for a second `leash kill` process.
			killer, err := Open(dsn)
			if err != nil {
				t.Fatalf("open killer: %v", err)
			}
			if err := killer.AppendKill(ctx, run, now); err != nil {
				t.Fatalf("AppendKill from second handle: %v", err)
			}
			_ = killer.Close()

			s, _ := governor.Load(ctx, run, testGovernor())
			if !s.Killed {
				t.Fatalf("governor did not observe a kill written by a second handle")
			}
		})
	}
}

func TestBackendStopRecordedOnceAndFrozen(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends() {
		t.Run(b.name, func(t *testing.T) {
			l := openLedger(t, b.dsn(t))
			run := uniqueRun()
			now := time.Now()
			if err := l.EnsureRun(ctx, run, now); err != nil {
				t.Fatalf("EnsureRun: %v", err)
			}
			if err := l.AppendCall(ctx, run, 0, call("gpt-4", 1_000_000, 0, "fp", now)); err != nil {
				t.Fatalf("AppendCall: %v", err)
			}
			s, _ := l.Load(ctx, run, testGovernor())
			s.StopReason = "cost_budget"
			s.ComputeCost = 0.91
			s.TotalCost = s.TokenCost + 0.91
			if err := l.AppendStop(ctx, run, s, now); err != nil {
				t.Fatalf("AppendStop: %v", err)
			}
			reloaded, _ := l.Load(ctx, run, testGovernor())
			if reloaded.StopReason != "cost_budget" {
				t.Fatalf("StopReason = %q, want cost_budget", reloaded.StopReason)
			}
			if reloaded.ComputeCost < 0.9099 || reloaded.ComputeCost > 0.9101 {
				t.Fatalf("frozen ComputeCost = %v, want 0.91", reloaded.ComputeCost)
			}
		})
	}
}
