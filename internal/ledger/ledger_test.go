package ledger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

var ledgerPrices = policy.PriceTable{
	"gpt-4": {InputPerM: 30, OutputPerM: 60},
}

func testGovernor() *policy.Governor {
	return policy.NewGovernor(policy.Limits{MaxCost: 1000, MaxCalls: 100000}, ledgerPrices, 0)
}

func call(model string, in, out int64, fp string, at time.Time) policy.CallRecord {
	return policy.CallRecord{
		Usage:       policy.Usage{Model: model, InputTokens: in, OutputTokens: out},
		Fingerprint: fp,
		At:          at,
	}
}

func TestAppendAndLoadRebuildsTotals(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	if err := l.EnsureRun(ctx, "r1", base); err != nil {
		t.Fatalf("EnsureRun: %v", err)
	}
	if err := l.AppendCall(ctx, "r1", 0, call("gpt-4", 1_000_000, 500_000, "fp1", base)); err != nil {
		t.Fatalf("AppendCall 0: %v", err)
	}
	if err := l.AppendCall(ctx, "r1", 1, call("gpt-4", 1_000_000, 0, "fp2", base.Add(time.Minute))); err != nil {
		t.Fatalf("AppendCall 1: %v", err)
	}

	s, err := l.Load(ctx, "r1", testGovernor())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Calls != 2 {
		t.Fatalf("Calls = %d, want 2", s.Calls)
	}
	if s.InputTokens != 2_000_000 || s.OutputTokens != 500_000 {
		t.Fatalf("tokens = (%d,%d), want (2000000,500000)", s.InputTokens, s.OutputTokens)
	}
	// 30 + 30 for input millions, 30 for the half million output = 90.
	if s.TokenCost < 89.999 || s.TokenCost > 90.001 {
		t.Fatalf("TokenCost = %v, want 90", s.TokenCost)
	}
	if !s.StartedAt.Equal(base) {
		t.Fatalf("StartedAt = %v, want %v", s.StartedAt, base)
	}
}

func TestResumeAcrossReopenPreservesBudget(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	l1, err := Open(db)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := l1.EnsureRun(ctx, "r1", base); err != nil {
		t.Fatalf("EnsureRun: %v", err)
	}
	_ = l1.AppendCall(ctx, "r1", 0, call("gpt-4", 1_000_000, 0, "fp", base))
	l1.Close() // simulate the process going away

	// A fresh ledger over the same db must resume with totals intact, and
	// EnsureRun on an existing run must not be an error.
	l2, err := Open(db)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer l2.Close()
	if err := l2.EnsureRun(ctx, "r1", base); err != nil {
		t.Fatalf("EnsureRun on resume must not error: %v", err)
	}
	s, err := l2.Load(ctx, "r1", testGovernor())
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	if s.Calls != 1 || s.InputTokens != 1_000_000 {
		t.Fatalf("resumed state wrong: Calls=%d Input=%d", s.Calls, s.InputTokens)
	}
}

func TestLoadIsIdempotentNoDoubleCount(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, _ := Open(db)
	defer l.Close()
	base := time.Now()
	_ = l.EnsureRun(ctx, "r1", base)
	_ = l.AppendCall(ctx, "r1", 0, call("gpt-4", 1_000_000, 0, "fp", base))

	a, _ := l.Load(ctx, "r1", testGovernor())
	b, _ := l.Load(ctx, "r1", testGovernor())
	if a.Calls != b.Calls || a.TokenCost != b.TokenCost || a.InputTokens != b.InputTokens {
		t.Fatalf("repeated Load produced different totals: %+v vs %+v", a, b)
	}
	if a.Calls != 1 {
		t.Fatalf("Calls = %d, want 1 (folding a journal twice must not double count)", a.Calls)
	}
}

func TestKillIsDurable(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, _ := Open(db)
	defer l.Close()
	now := time.Now()
	_ = l.EnsureRun(ctx, "r1", now)
	if err := l.AppendKill(ctx, "r1", now); err != nil {
		t.Fatalf("AppendKill: %v", err)
	}
	s, _ := l.Load(ctx, "r1", testGovernor())
	if !s.Killed {
		t.Fatalf("Killed = false after AppendKill, want true")
	}
}

func TestKillFromSecondLedgerOnSameDB(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	now := time.Now()

	governor, err := Open(db)
	if err != nil {
		t.Fatalf("Open governor: %v", err)
	}
	defer governor.Close()
	_ = governor.EnsureRun(ctx, "r1", now)
	_ = governor.AppendCall(ctx, "r1", 0, call("gpt-4", 1000, 10, "fp", now))

	// A separate ledger handle on the same file stands in for a second process
	// running `leash kill`.
	killer, err := Open(db)
	if err != nil {
		t.Fatalf("Open killer: %v", err)
	}
	if err := killer.AppendKill(ctx, "r1", now); err != nil {
		t.Fatalf("AppendKill from second handle: %v", err)
	}
	killer.Close()

	s, _ := governor.Load(ctx, "r1", testGovernor())
	if !s.Killed {
		t.Fatalf("governor did not observe a kill written by a second handle")
	}
}

func TestStopIsRecordedOnceAndFrozen(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, _ := Open(db)
	defer l.Close()
	now := time.Now()
	_ = l.EnsureRun(ctx, "r1", now)
	_ = l.AppendCall(ctx, "r1", 0, call("gpt-4", 1_000_000, 0, "fp", now))

	s, _ := l.Load(ctx, "r1", testGovernor())
	s.StopReason = "cost_budget"
	s.ComputeCost = 0.91
	s.TotalCost = s.TokenCost + 0.91
	if err := l.AppendStop(ctx, "r1", s, now); err != nil {
		t.Fatalf("AppendStop: %v", err)
	}

	reloaded, _ := l.Load(ctx, "r1", testGovernor())
	if reloaded.StopReason != "cost_budget" {
		t.Fatalf("StopReason = %q, want cost_budget", reloaded.StopReason)
	}
	if reloaded.ComputeCost < 0.9099 || reloaded.ComputeCost > 0.9101 {
		t.Fatalf("frozen ComputeCost = %v, want 0.91", reloaded.ComputeCost)
	}
}

func TestIncompleteListsRunningRetiresFinished(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, _ := Open(db)
	defer l.Close()
	now := time.Now()
	_ = l.EnsureRun(ctx, "active", now)
	_ = l.EnsureRun(ctx, "retired", now)

	runs, err := l.Incomplete(ctx)
	if err != nil {
		t.Fatalf("Incomplete: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("Incomplete count = %d, want 2", len(runs))
	}

	if err := l.Finish(ctx, "retired", true); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	runs, _ = l.Incomplete(ctx)
	if len(runs) != 1 || runs[0].ID != "active" {
		t.Fatalf("after Finish, Incomplete = %+v, want just [active]", runs)
	}
}

func TestLeaseIsExclusiveWithinProcess(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, _ := Open(db)
	defer l.Close()

	lease, acquired, err := l.Acquire(ctx, "r1")
	if err != nil || !acquired {
		t.Fatalf("first Acquire = (%v,%v), want acquired", acquired, err)
	}
	_, acquired2, _ := l.Acquire(ctx, "r1")
	if acquired2 {
		t.Fatalf("second Acquire on a held lease returned acquired=true")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	_, acquired3, _ := l.Acquire(ctx, "r1")
	if !acquired3 {
		t.Fatalf("Acquire after Release did not reacquire")
	}
}
