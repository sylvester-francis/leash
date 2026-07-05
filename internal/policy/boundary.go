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

package policy

import "time"

// Boundary is one stopping condition. Check reports whether the boundary is
// tripped given the current state and wall-clock time; the state's time-derived
// fields are expected to have been refreshed first. Reason names the boundary
// for the stop line and the 429 body.
type Boundary interface {
	// Check reports whether this boundary is tripped.
	Check(s *State, now time.Time) bool
	// Reason is the stable machine-readable name of this boundary.
	Reason() string
}

// KillSwitch trips when a durable kill has been folded into the run. It is
// always active and always evaluated first.
type KillSwitch struct{}

// Check reports whether the run has been killed.
func (KillSwitch) Check(s *State, _ time.Time) bool { return s.Killed }

// Reason returns "kill_switch".
func (KillSwitch) Reason() string { return "kill_switch" }

// Deadline trips when elapsed wall-clock since the first call reaches D.
type Deadline struct {
	// D is the wall-clock budget measured from the first governed call.
	D time.Duration
}

// Check reports whether the refreshed elapsed time has reached the deadline.
func (b Deadline) Check(s *State, _ time.Time) bool { return s.Elapsed >= b.D }

// Reason returns "deadline".
func (b Deadline) Reason() string { return "deadline" }

// CostBudget trips when total cost (token cost plus compute cost) reaches USD.
type CostBudget struct {
	// USD is the dollar ceiling for the run.
	USD float64
}

// Check reports whether the refreshed total cost has reached the budget.
func (b CostBudget) Check(s *State, _ time.Time) bool { return s.TotalCost >= b.USD }

// Reason returns "cost_budget".
func (b CostBudget) Reason() string { return "cost_budget" }

// MaxCalls trips when the run has made N governed calls.
type MaxCalls struct {
	// N is the maximum number of governed calls.
	N int64
}

// Check reports whether the call count has reached the maximum.
func (b MaxCalls) Check(s *State, _ time.Time) bool { return s.Calls >= b.N }

// Reason returns "max_calls".
func (b MaxCalls) Reason() string { return "max_calls" }

// RateLimit trips when the tokens consumed within a trailing Window exceed
// MaxTokens. The window delta is computed from the recorded rate samples, so it
// decays to zero after a quiet period.
type RateLimit struct {
	// MaxTokens is the most tokens allowed within the trailing window.
	MaxTokens int64
	// Window is the trailing duration the rate is measured over.
	Window time.Duration
}

// Check reports whether the trailing-window token delta exceeds the maximum. It
// finds the cumulative token count as of the window's start (the last sample at
// or before now-Window, or zero) and compares the delta to MaxTokens.
func (b RateLimit) Check(s *State, now time.Time) bool {
	if len(s.Samples) == 0 {
		return false
	}
	cutoff := now.Add(-b.Window)
	var baseline int64
	for _, sm := range s.Samples {
		if sm.At.After(cutoff) {
			break
		}
		baseline = sm.CumulativeTokens
	}
	current := s.Samples[len(s.Samples)-1].CumulativeTokens
	return current-baseline > b.MaxTokens
}

// ReasonRateLimit is the reason string for the rate limit. It is the one
// transient boundary: a refusal, not a terminal stop.
const ReasonRateLimit = "rate_limit"

// Reason returns "rate_limit".
func (b RateLimit) Reason() string { return ReasonRateLimit }

// Stall trips when Patience consecutive responses carried the same fingerprint,
// which is how leash notices an agent redoing identical work.
type Stall struct {
	// Patience is the number of identical responses tolerated.
	Patience int
}

// Check reports whether the stall streak has reached the patience.
func (b Stall) Check(s *State, _ time.Time) bool { return s.StallCount >= b.Patience }

// Reason returns "stall".
func (b Stall) Reason() string { return "stall" }

// Limits is the caller-supplied configuration of the boundaries. A zero value
// for a limit disables that boundary; the kill switch is always active. This is
// what the CLI flags populate.
type Limits struct {
	// MaxCost is the dollar budget; zero disables the cost budget.
	MaxCost float64
	// MaxCalls is the call ceiling; zero disables it.
	MaxCalls int64
	// Deadline is the wall-clock budget; zero disables it.
	Deadline time.Duration
	// RateTokens with RateWindow bound the trailing token rate; either zero
	// disables the rate limit.
	RateTokens int64
	// RateWindow is the trailing window for the rate limit.
	RateWindow time.Duration
	// Stall is the identical-response patience; zero disables stall detection.
	Stall int
}

// Governor holds the active boundaries in their fixed evaluation order together
// with the meters (prices and compute rate) used to refresh cost before each
// evaluation. It is the single object the proxy consults per call.
//
// A Governor is immutable after NewGovernor returns, so it is safe for
// concurrent use: many runs share one Governor and evaluate against it in
// parallel. The mutable per-call state lives in State, not here.
type Governor struct {
	// ComputeRate is dollars per hour for the compute meter.
	ComputeRate float64
	// Prices is the token price table; nil means the token meter is blind.
	Prices PriceTable
	// Boundaries are evaluated in order; the first to trip stops the run.
	Boundaries []Boundary
}

// NewGovernor assembles a Governor from limits, prices, and a compute rate. The
// boundaries are appended in the fixed documented order (kill, deadline, cost,
// calls, rate, stall) so the order is a property of the policy core and not of
// how a caller happens to configure it. Disabled limits are omitted.
func NewGovernor(l Limits, prices PriceTable, computeRate float64) *Governor {
	g := &Governor{ComputeRate: computeRate, Prices: prices}
	g.Boundaries = append(g.Boundaries, KillSwitch{})
	if l.Deadline > 0 {
		g.Boundaries = append(g.Boundaries, Deadline{D: l.Deadline})
	}
	if l.MaxCost > 0 {
		g.Boundaries = append(g.Boundaries, CostBudget{USD: l.MaxCost})
	}
	if l.MaxCalls > 0 {
		g.Boundaries = append(g.Boundaries, MaxCalls{N: l.MaxCalls})
	}
	if l.RateTokens > 0 && l.RateWindow > 0 {
		g.Boundaries = append(g.Boundaries, RateLimit{MaxTokens: l.RateTokens, Window: l.RateWindow})
	}
	if l.Stall > 0 {
		g.Boundaries = append(g.Boundaries, Stall{Patience: l.Stall})
	}
	return g
}

// RateWindow returns the rate limiter's trailing window, or 0 when no rate
// limit is active. It sizes the Retry-After hint on a rate-limited refusal.
func (g *Governor) RateWindow() time.Duration {
	for _, b := range g.Boundaries {
		if rl, ok := b.(RateLimit); ok {
			return rl.Window
		}
	}
	return 0
}

// MetersCost reports whether a cost budget is active, i.e. whether the governor
// relies on token cost to stop the run. The proxy uses this to decide whether an
// unmeterable call is a fail-closed concern.
func (g *Governor) MetersCost() bool {
	for _, b := range g.Boundaries {
		if _, ok := b.(CostBudget); ok {
			return true
		}
	}
	return false
}

// BudgetStatus is a run's utilization of one bounded budget.
type BudgetStatus struct {
	// Reason is the boundary reason this budget stops on (cost_budget,
	// max_calls, deadline).
	Reason string
	// Used and Limit are in the budget's own unit: dollars, calls, or seconds.
	Used  float64
	Limit float64
	// Fraction is Used/Limit; it can exceed 1 on the call that overshoots.
	Fraction float64
}

// BudgetStatuses reports the run's utilization of each budget that has a fixed
// ceiling: the cost budget, the call cap, and the deadline. Boundaries without a
// ceiling (rate, stall, kill) are omitted. Callers should Refresh or Evaluate
// first so the time-derived fields are current.
func (g *Governor) BudgetStatuses(s *State) []BudgetStatus {
	var out []BudgetStatus
	for _, b := range g.Boundaries {
		switch t := b.(type) {
		case CostBudget:
			out = append(out, budgetStatus("cost_budget", s.TotalCost, t.USD))
		case MaxCalls:
			out = append(out, budgetStatus("max_calls", float64(s.Calls), float64(t.N)))
		case Deadline:
			out = append(out, budgetStatus("deadline", s.Elapsed.Seconds(), t.D.Seconds()))
		}
	}
	return out
}

func budgetStatus(reason string, used, limit float64) BudgetStatus {
	f := 0.0
	if limit > 0 {
		f = used / limit
	}
	return BudgetStatus{Reason: reason, Used: used, Limit: limit, Fraction: f}
}

// Evaluate refreshes the state's cost and time fields, then checks every active
// boundary in order. It returns the reason of the first tripped boundary and
// true, or the empty string and false when the run may proceed. Evaluate does
// not mutate the stop reason; the caller records the stop exactly once.
func (g *Governor) Evaluate(s *State, now time.Time) (reason string, tripped bool) {
	s.Refresh(now, g.ComputeRate)
	for _, b := range g.Boundaries {
		if b.Check(s, now) {
			return b.Reason(), true
		}
	}
	return "", false
}

// Fold applies one recorded call to the state under the governor's prices, then
// prunes rate samples to what the rate limiter still needs. Pruning here (not in
// State.Fold) keeps the warm and cold fold paths identical, since both fold
// through the governor.
func (g *Governor) Fold(s *State, rec CallRecord) {
	s.Fold(rec.Usage, rec.Fingerprint, rec.At, g.Prices)
	s.pruneSamples(g.sampleWindow(), rec.At)
}

// sampleWindow is the rate limiter's trailing window, or 0 when no rate limit is
// active (in which case rate samples are not needed at all).
func (g *Governor) sampleWindow() time.Duration {
	for _, b := range g.Boundaries {
		if rl, ok := b.(RateLimit); ok {
			return rl.Window
		}
	}
	return 0
}
