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

// Property-based tests for the fold. These assert the invariants the rest of
// leash relies on (see ARCHITECTURE.md) across randomly generated inputs, using
// the standard library's testing/quick so no test dependency is added.
package policy

import (
	"testing"
	"testing/quick"
	"time"
)

var propBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
var propPrices = PriceTable{"m": {InputPerM: 3, OutputPerM: 7, ReasoningPerM: 7, CachedInputPerM: 1}}

// recsFrom zips two generated token slices into call records.
func recsFrom(in, out []uint16) []CallRecord {
	n := min(len(in), len(out))
	recs := make([]CallRecord, n)
	for i := range n {
		recs[i] = CallRecord{
			Usage: Usage{Model: "m", InputTokens: int64(in[i]), OutputTokens: int64(out[i])},
			At:    propBase.Add(time.Duration(i) * time.Second),
		}
	}
	return recs
}

// Invariant: the fold is deterministic. Replaying the same records reaches the
// same state, which is what makes a journal a reliable source of truth.
func TestPropFoldDeterministic(t *testing.T) {
	f := func(in, out []uint16) bool {
		recs := recsFrom(in, out)
		g := NewGovernor(Limits{}, propPrices, 0)
		a, b := &State{}, &State{}
		for _, r := range recs {
			g.Fold(a, r)
		}
		for _, r := range recs {
			g.Fold(b, r)
		}
		return a.Calls == b.Calls && a.InputTokens == b.InputTokens &&
			a.OutputTokens == b.OutputTokens && a.TokenCost == b.TokenCost
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 400}); err != nil {
		t.Fatal(err)
	}
}

// Invariant: token cost never decreases as calls are folded (each call adds a
// non-negative cost), and the call count equals the number of records folded.
func TestPropCostMonotonicAndCallsCounted(t *testing.T) {
	f := func(in, out []uint16) bool {
		recs := recsFrom(in, out)
		g := NewGovernor(Limits{}, propPrices, 0)
		s := &State{}
		prev := -1.0
		for _, r := range recs {
			g.Fold(s, r)
			if s.TokenCost < prev {
				return false
			}
			prev = s.TokenCost
		}
		return s.Calls == int64(len(recs))
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 400}); err != nil {
		t.Fatal(err)
	}
}

// Invariant: with a rate limit active, the retained sample slice stays bounded
// (it is pruned to the window), so a long run does not accumulate samples without
// limit. The window here is small relative to the one-second spacing.
func TestPropRateSamplesBounded(t *testing.T) {
	f := func(in []uint16) bool {
		g := NewGovernor(Limits{RateTokens: 1_000_000, RateWindow: 3 * time.Second}, propPrices, 0)
		s := &State{}
		for i, tok := range in {
			g.Fold(s, CallRecord{
				Usage: Usage{Model: "m", InputTokens: int64(tok)},
				At:    propBase.Add(time.Duration(i) * time.Second),
			})
			// A 3s window at 1s spacing needs at most the baseline plus a few
			// in-window samples; it must never grow with the run length.
			if len(s.Samples) > 6 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}
