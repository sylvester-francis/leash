package policy

import (
	"testing"
	"time"
)

func TestKillSwitch(t *testing.T) {
	now := time.Now()
	if (KillSwitch{}).Check(&State{}, now) {
		t.Fatalf("KillSwitch tripped on a live run")
	}
	if !(KillSwitch{}).Check(&State{Killed: true}, now) {
		t.Fatalf("KillSwitch did not trip on a killed run")
	}
	if (KillSwitch{}).Reason() != "kill_switch" {
		t.Fatalf("KillSwitch reason = %q, want kill_switch", (KillSwitch{}).Reason())
	}
}

func TestDeadline(t *testing.T) {
	now := time.Now()
	b := Deadline{D: 30 * time.Minute}
	if b.Check(&State{Elapsed: 29 * time.Minute}, now) {
		t.Fatalf("Deadline tripped before the limit")
	}
	if !b.Check(&State{Elapsed: 30 * time.Minute}, now) {
		t.Fatalf("Deadline did not trip at the limit")
	}
	if !b.Check(&State{Elapsed: 31 * time.Minute}, now) {
		t.Fatalf("Deadline did not trip past the limit")
	}
	if b.Reason() != "deadline" {
		t.Fatalf("Deadline reason = %q, want deadline", b.Reason())
	}
}

func TestCostBudget(t *testing.T) {
	now := time.Now()
	b := CostBudget{USD: 5.00}
	if b.Check(&State{TotalCost: 4.99}, now) {
		t.Fatalf("CostBudget tripped under budget")
	}
	if !b.Check(&State{TotalCost: 5.00}, now) {
		t.Fatalf("CostBudget did not trip at budget")
	}
	if !b.Check(&State{TotalCost: 5.01}, now) {
		t.Fatalf("CostBudget did not trip over budget")
	}
	if b.Reason() != "cost_budget" {
		t.Fatalf("CostBudget reason = %q, want cost_budget", b.Reason())
	}
}

func TestMaxCalls(t *testing.T) {
	now := time.Now()
	b := MaxCalls{N: 100}
	if b.Check(&State{Calls: 99}, now) {
		t.Fatalf("MaxCalls tripped under the limit")
	}
	if !b.Check(&State{Calls: 100}, now) {
		t.Fatalf("MaxCalls did not trip at the limit")
	}
	if b.Reason() != "max_calls" {
		t.Fatalf("MaxCalls reason = %q, want max_calls", b.Reason())
	}
}

func TestStall(t *testing.T) {
	now := time.Now()
	b := Stall{Patience: 3}
	if b.Check(&State{StallCount: 2}, now) {
		t.Fatalf("Stall tripped under patience")
	}
	if !b.Check(&State{StallCount: 3}, now) {
		t.Fatalf("Stall did not trip at patience")
	}
	if b.Reason() != "stall" {
		t.Fatalf("Stall reason = %q, want stall", b.Reason())
	}
}

func TestRateLimit(t *testing.T) {
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	// Cumulative tokens: 100 at 0s, 200 at 1s, 300 at 2s.
	samples := []Sample{
		{At: at(0), CumulativeTokens: 100},
		{At: at(1), CumulativeTokens: 200},
		{At: at(2), CumulativeTokens: 300},
	}
	tests := []struct {
		name      string
		maxTokens int64
		window    time.Duration
		now       time.Time
		want      bool
	}{
		{"whole history over the max trips", 250, 5 * time.Second, at(2), true},
		{"whole history under the max is fine", 350, 5 * time.Second, at(2), false},
		{"short window counts only recent delta", 150, 1 * time.Second, at(2), false},
		{"quiet period lets the rate decay", 50, 5 * time.Second, at(10), false},
		{"exact max does not trip (strictly greater)", 300, 5 * time.Second, at(2), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := RateLimit{MaxTokens: tt.maxTokens, Window: tt.window}
			s := &State{Samples: samples}
			if got := b.Check(s, tt.now); got != tt.want {
				t.Fatalf("RateLimit.Check = %v, want %v", got, tt.want)
			}
		})
	}
	if (RateLimit{}).Reason() != "rate_limit" {
		t.Fatalf("RateLimit reason wrong")
	}
	// No samples: nothing to rate limit.
	if (RateLimit{MaxTokens: 1, Window: time.Second}).Check(&State{}, base) {
		t.Fatalf("RateLimit tripped with no samples")
	}
}

func TestGovernorEvaluatesInFixedOrder(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	limits := Limits{
		MaxCost:  5.00,
		MaxCalls: 100,
		Deadline: 30 * time.Minute,
		Stall:    3,
	}
	g := NewGovernor(limits, nil, 0)

	// A killed, over-deadline, over-budget, over-calls, stalled state: kill wins.
	s := &State{
		Killed:     true,
		StartedAt:  now.Add(-time.Hour),
		Calls:      200,
		TokenCost:  10,
		StallCount: 5,
	}
	reason, tripped := g.Evaluate(s, now)
	if !tripped || reason != "kill_switch" {
		t.Fatalf("Evaluate = (%q,%v), want (kill_switch,true)", reason, tripped)
	}

	// Not killed: deadline is next in order and beats cost and calls.
	s.Killed = false
	reason, _ = g.Evaluate(s, now)
	if reason != "deadline" {
		t.Fatalf("Evaluate = %q, want deadline before cost/calls", reason)
	}

	// Within deadline: cost beats calls.
	s.StartedAt = now.Add(-time.Minute)
	reason, _ = g.Evaluate(s, now)
	if reason != "cost_budget" {
		t.Fatalf("Evaluate = %q, want cost_budget before max_calls", reason)
	}
}

func TestGovernorRefreshesBeforeChecks(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	// Compute meter alone must be able to trip the cost budget: no prices, but a
	// compute rate that accrues past the budget over the elapsed hour.
	g := NewGovernor(Limits{MaxCost: 1.00}, nil, 2.00) // $2/hr
	s := &State{StartedAt: now.Add(-time.Hour), Calls: 1}
	reason, tripped := g.Evaluate(s, now)
	if !tripped || reason != "cost_budget" {
		t.Fatalf("Evaluate = (%q,%v), want (cost_budget,true) from compute meter", reason, tripped)
	}
	if !approxEqual(s.ComputeCost, 2.00) {
		t.Fatalf("Evaluate did not refresh ComputeCost: got %v, want 2.00", s.ComputeCost)
	}
}

func TestGovernorZeroLimitsDisable(t *testing.T) {
	now := time.Now()
	// All zero limits: only the kill switch is active, nothing else trips.
	g := NewGovernor(Limits{}, nil, 0)
	s := &State{Calls: 1_000_000, TokenCost: 1_000_000}
	if _, tripped := g.Evaluate(s, now); tripped {
		t.Fatalf("zero limits tripped a boundary; only kill should be active")
	}
	s.Killed = true
	if reason, tripped := g.Evaluate(s, now); !tripped || reason != "kill_switch" {
		t.Fatalf("kill switch inactive under zero limits")
	}
}

func TestGovernorRateLimitActive(t *testing.T) {
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	g := NewGovernor(Limits{RateTokens: 100, RateWindow: time.Minute}, nil, 0)
	s := &State{Samples: []Sample{{At: base, CumulativeTokens: 150}}}
	reason, tripped := g.Evaluate(s, base)
	if !tripped || reason != "rate_limit" {
		t.Fatalf("Evaluate = (%q,%v), want (rate_limit,true)", reason, tripped)
	}
}

func TestGovernorRateLimitNeedsBothTokensAndWindow(t *testing.T) {
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	heavy := &State{Samples: []Sample{{At: base, CumulativeTokens: 1_000_000}}}

	// A window without a token budget must not activate the rate limit.
	if _, tripped := NewGovernor(Limits{RateWindow: time.Minute}, nil, 0).Evaluate(heavy, base); tripped {
		t.Fatalf("rate limit tripped with no token budget configured")
	}
	// A token budget without a window must not activate the rate limit.
	if _, tripped := NewGovernor(Limits{RateTokens: 1}, nil, 0).Evaluate(heavy, base); tripped {
		t.Fatalf("rate limit tripped with no window configured")
	}
}

func TestStopLine(t *testing.T) {
	s := &State{
		RunID:       "a3f9",
		Calls:       18,
		TokenCost:   4.10,
		ComputeCost: 0.91,
		TotalCost:   5.01,
		StopReason:  "cost_budget",
	}
	got := StopLine(s)
	want := "leash: stopped run a3f9 after 18 calls, $4.10 tokens + $0.91 compute = $5.01 (cost_budget)"
	if got != want {
		t.Fatalf("StopLine =\n  %q\nwant\n  %q", got, want)
	}
}
