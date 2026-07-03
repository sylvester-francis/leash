package policy

import (
	"testing"
	"time"
)

var testPrices = PriceTable{
	"gpt-4": {InputPerM: 30, OutputPerM: 60, ReasoningPerM: 0},
}

func TestFoldAccumulates(t *testing.T) {
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	s := &State{RunID: "r1"}

	s.Fold(Usage{Model: "gpt-4", InputTokens: 1_000_000, OutputTokens: 500_000}, "fp1", base, testPrices)
	if s.Calls != 1 {
		t.Fatalf("after one fold Calls = %d, want 1", s.Calls)
	}
	if !s.StartedAt.Equal(base) {
		t.Fatalf("StartedAt = %v, want %v (first fold sets it)", s.StartedAt, base)
	}
	if s.InputTokens != 1_000_000 || s.OutputTokens != 500_000 {
		t.Fatalf("token totals = (%d,%d), want (1000000,500000)", s.InputTokens, s.OutputTokens)
	}
	// 30 for a million input + 30 for half a million output = 60.
	if !approxEqual(s.TokenCost, 60) {
		t.Fatalf("TokenCost = %v, want 60", s.TokenCost)
	}

	s.Fold(Usage{Model: "gpt-4", InputTokens: 1_000_000}, "fp2", base.Add(time.Minute), testPrices)
	if s.Calls != 2 {
		t.Fatalf("after two folds Calls = %d, want 2", s.Calls)
	}
	if !s.StartedAt.Equal(base) {
		t.Fatalf("StartedAt moved to %v, want it pinned to the first fold %v", s.StartedAt, base)
	}
	if !approxEqual(s.TokenCost, 90) {
		t.Fatalf("TokenCost = %v, want 90", s.TokenCost)
	}
	if len(s.Samples) != 2 {
		t.Fatalf("Samples length = %d, want 2", len(s.Samples))
	}
	if s.Samples[1].CumulativeTokens != 2_500_000 {
		t.Fatalf("second sample cumulative tokens = %d, want 2500000", s.Samples[1].CumulativeTokens)
	}
}

func TestFoldStallTracking(t *testing.T) {
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	s := &State{RunID: "r1"}
	at := func(i int) time.Time { return base.Add(time.Duration(i) * time.Second) }

	s.Fold(Usage{Model: "gpt-4"}, "same", at(0), testPrices)
	if s.StallCount != 1 {
		t.Fatalf("first fingerprint StallCount = %d, want 1", s.StallCount)
	}
	s.Fold(Usage{Model: "gpt-4"}, "same", at(1), testPrices)
	if s.StallCount != 2 {
		t.Fatalf("repeated fingerprint StallCount = %d, want 2", s.StallCount)
	}
	s.Fold(Usage{Model: "gpt-4"}, "different", at(2), testPrices)
	if s.StallCount != 1 {
		t.Fatalf("changed fingerprint StallCount = %d, want 1 (reset)", s.StallCount)
	}
	if s.Fingerprint != "different" {
		t.Fatalf("Fingerprint = %q, want %q", s.Fingerprint, "different")
	}
	// A blind call (empty fingerprint) breaks the streak and cannot be a stall.
	s.Fold(Usage{Model: "gpt-4"}, "", at(3), testPrices)
	if s.StallCount != 0 {
		t.Fatalf("blind fingerprint StallCount = %d, want 0", s.StallCount)
	}
	if s.Fingerprint != "" {
		t.Fatalf("Fingerprint = %q, want empty after blind call", s.Fingerprint)
	}
}

func TestRefresh(t *testing.T) {
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	s := &State{RunID: "r1"}
	s.Fold(Usage{Model: "gpt-4", InputTokens: 1_000_000}, "fp", base, testPrices) // TokenCost 30

	s.Refresh(base.Add(30*time.Minute), 2.0) // half hour at $2/hr = $1 compute
	if s.Elapsed != 30*time.Minute {
		t.Fatalf("Elapsed = %v, want 30m", s.Elapsed)
	}
	if !approxEqual(s.ComputeCost, 1.0) {
		t.Fatalf("ComputeCost = %v, want 1.0", s.ComputeCost)
	}
	if !approxEqual(s.TotalCost, 31.0) {
		t.Fatalf("TotalCost = %v, want 31.0 (30 token + 1 compute)", s.TotalCost)
	}
}

func TestRefreshZeroStartIsZeroElapsed(t *testing.T) {
	s := &State{RunID: "r1"} // no folds, StartedAt is zero
	s.Refresh(time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC), 100.0)
	if s.Elapsed != 0 {
		t.Fatalf("Elapsed = %v, want 0 before the first call", s.Elapsed)
	}
	if s.ComputeCost != 0 {
		t.Fatalf("ComputeCost = %v, want 0 before the first call", s.ComputeCost)
	}
}

func TestFoldSampleIncludesReasoning(t *testing.T) {
	s := &State{}
	s.Fold(Usage{Model: "gpt-4", InputTokens: 10, OutputTokens: 20, ReasoningTokens: 30}, "fp", time.Now(), nil)
	// The rate sample must sum all three token kinds.
	if s.Samples[0].CumulativeTokens != 60 {
		t.Fatalf("sample cumulative = %d, want 60 (input+output+reasoning)", s.Samples[0].CumulativeTokens)
	}
}

func TestFoldKeepsTotalCostCoherent(t *testing.T) {
	// A compute cost from a prior refresh must still be reflected after a fold,
	// before the next refresh runs.
	s := &State{ComputeCost: 5}
	s.Fold(Usage{Model: "gpt-4", InputTokens: 1_000_000}, "fp", time.Now(), testPrices) // token cost 30
	if !approxEqual(s.TotalCost, 35) {
		t.Fatalf("TotalCost = %v, want 35 (30 token + 5 compute)", s.TotalCost)
	}
}

func TestFingerprint(t *testing.T) {
	// Normalization is whitespace-insensitive: these must all hash the same.
	a := Fingerprint("hello world")
	b := Fingerprint("hello   world")
	c := Fingerprint("  hello world  \n")
	if a == "" {
		t.Fatalf("Fingerprint of real text should not be empty")
	}
	if a != b || b != c {
		t.Fatalf("normalized fingerprints differ: %q %q %q", a, b, c)
	}
	// Distinct content hashes differently.
	if Fingerprint("hello world") == Fingerprint("goodbye world") {
		t.Fatalf("distinct text produced identical fingerprints")
	}
	// Empty or whitespace-only content is blind: the empty fingerprint.
	for _, blank := range []string{"", "   ", "\n\t "} {
		if got := Fingerprint(blank); got != "" {
			t.Fatalf("Fingerprint(%q) = %q, want empty", blank, got)
		}
	}
}
