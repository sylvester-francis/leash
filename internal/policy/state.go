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

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Sample is one point in the trailing token-rate history: the cumulative token
// count observed at a moment in time. RateLimit reads a window of these.
type Sample struct {
	// At is when the sample was recorded.
	At time.Time `json:"at"`
	// CumulativeTokens is the run's total tokens as of At.
	CumulativeTokens int64 `json:"cumulative_tokens"`
}

// State is the running account for one governed run. It is a cache: the ledger
// journal is the source of truth, and State is rebuilt from it by folding each
// recorded call in order. Time-derived fields (Elapsed, ComputeCost, TotalCost)
// are set by Refresh; everything else is set by Fold.
type State struct {
	// RunID is the run this state accounts for.
	RunID string `json:"run_id"`
	// Calls is the number of governed calls folded so far.
	Calls int64 `json:"calls"`
	// InputTokens is the cumulative prompt token count.
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens is the cumulative completion token count.
	OutputTokens int64 `json:"output_tokens"`
	// ReasoningTokens is the cumulative reasoning token count.
	ReasoningTokens int64 `json:"reasoning_tokens"`
	// TokenCost is the cumulative dollar cost of tokens under the run's prices.
	TokenCost float64 `json:"token_cost"`
	// ComputeCost is the dollar cost of elapsed wall-clock time as of the last
	// Refresh.
	ComputeCost float64 `json:"compute_cost"`
	// TotalCost is TokenCost plus ComputeCost as of the last Refresh.
	TotalCost float64 `json:"total_cost"`
	// StartedAt is the timestamp of the first folded call; the Deadline is
	// measured from it.
	StartedAt time.Time `json:"started_at"`
	// Elapsed is wall-clock since StartedAt as of the last Refresh.
	Elapsed time.Duration `json:"elapsed"`
	// Samples is the trailing token-rate history.
	Samples []Sample `json:"samples"`
	// Fingerprint is the last non-blind response fingerprint, for stall
	// detection.
	Fingerprint string `json:"fingerprint"`
	// StallCount is how many consecutive folds carried the current Fingerprint.
	StallCount int `json:"stall_count"`
	// Killed is set once a durable kill has been folded.
	Killed bool `json:"killed"`
	// StopReason names the boundary that stopped the run, empty while running.
	StopReason string `json:"stop_reason"`
}

// CallRecord is the durable payload for one governed call. It carries only
// accounting: token counts, a content fingerprint hash, and a timestamp. It
// never carries request or response bodies. It is what the ledger stores and
// what Fold consumes on replay.
type CallRecord struct {
	// Usage is the token accounting the provider reported.
	Usage Usage `json:"usage"`
	// Fingerprint is the normalized-content hash, or empty when blind.
	Fingerprint string `json:"fingerprint"`
	// At is when the call completed.
	At time.Time `json:"at"`
}

// Fold applies one governed call to the state: it advances the call count,
// accumulates tokens and token cost under prices, records a rate sample, and
// updates stall tracking. The first fold pins StartedAt. Fold is deterministic
// in its inputs, so replaying a journal reproduces the exact same State.
func (s *State) Fold(u Usage, fingerprint string, at time.Time, prices PriceTable) {
	if s.Calls == 0 {
		s.StartedAt = at
	}
	s.Calls++
	s.InputTokens += u.InputTokens
	s.OutputTokens += u.OutputTokens
	s.ReasoningTokens += u.ReasoningTokens
	s.TokenCost += TokenCost(u, prices)
	s.Samples = append(s.Samples, Sample{
		At: at,
		// Reasoning tokens are within output; do not add them again.
		CumulativeTokens: s.InputTokens + s.OutputTokens,
	})
	s.foldStall(fingerprint)
	// Keep TotalCost coherent even before a Refresh sets ComputeCost.
	s.TotalCost = s.TokenCost + s.ComputeCost
}

// pruneSamples bounds the rate-sample slice so it cannot grow with the run. With
// no rate window it clears the slice (the samples are read only by the rate
// limiter). Otherwise it keeps the last sample at or before now-window - the
// rate baseline - and every sample after it, dropping the older ones in place.
func (s *State) pruneSamples(window time.Duration, now time.Time) {
	if window <= 0 {
		s.Samples = nil
		return
	}
	cutoff := now.Add(-window)
	keepFrom := 0
	for i, sm := range s.Samples {
		if sm.At.After(cutoff) {
			break
		}
		keepFrom = i
	}
	if keepFrom > 0 {
		s.Samples = append(s.Samples[:0], s.Samples[keepFrom:]...)
	}
}

// foldStall updates the stall streak. A blind (empty) fingerprint cannot be a
// repeat and resets the streak; a matching fingerprint extends it; any other
// fingerprint starts a new streak of one.
func (s *State) foldStall(fingerprint string) {
	switch fingerprint {
	case "":
		s.Fingerprint = ""
		s.StallCount = 0
	case s.Fingerprint:
		s.StallCount++
	default:
		s.Fingerprint = fingerprint
		s.StallCount = 1
	}
}

// Refresh sets the time-derived fields from the current wall-clock time and the
// compute rate. Elapsed is measured from StartedAt (zero before the first
// call), ComputeCost from Elapsed, and TotalCost from both meters.
func (s *State) Refresh(now time.Time, computeRate float64) {
	if s.StartedAt.IsZero() {
		s.Elapsed = 0
	} else {
		s.Elapsed = now.Sub(s.StartedAt)
	}
	s.ComputeCost = ComputeCost(s.Elapsed, computeRate)
	s.TotalCost = s.TokenCost + s.ComputeCost
}

// Fingerprint returns a hex SHA-256 of the whitespace-normalized text, or the
// empty string when the text is blank. Normalization collapses every run of
// whitespace to a single space so that cosmetically different but identical
// responses fingerprint the same, which is what stall detection needs.
func Fingerprint(text string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
