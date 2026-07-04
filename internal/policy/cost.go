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

// Package policy is the pure, deterministic core of leash: cost math, run
// state, and the boundaries that stop a run. It has no dependency on the
// network, the ledger, or the clock beyond the timestamps passed into it, so
// every decision it makes can be reproduced from recorded inputs.
package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// tokensPerMillion is the denominator for per-million-token pricing.
const tokensPerMillion = 1_000_000.0

// Usage is the token accounting reported by one governed model call. Counts are
// only ever what the provider wire reports; leash never estimates tokens.
type Usage struct {
	// Model is the model name the provider billed, used to select a Price.
	Model string `json:"model"`
	// InputTokens is the prompt token count.
	InputTokens int64 `json:"input"`
	// OutputTokens is the completion token count.
	OutputTokens int64 `json:"output"`
	// ReasoningTokens is the reasoning/thinking token count when the provider
	// reports it separately; zero otherwise.
	ReasoningTokens int64 `json:"reasoning"`
}

// TotalTokens is the number of tokens the rate limiter meters. Reasoning tokens
// are a subset of the reported output tokens, so they are not added again.
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}

// Price is the dollar cost per one million tokens for a single model. All
// prices are caller-supplied; leash ships no price table of its own.
type Price struct {
	// InputPerM is dollars per million input tokens.
	InputPerM float64 `json:"input"`
	// OutputPerM is dollars per million output tokens.
	OutputPerM float64 `json:"output"`
	// ReasoningPerM is dollars per million reasoning tokens.
	ReasoningPerM float64 `json:"reasoning"`
}

// PriceTable maps a model name to its Price. A nil or missing entry means the
// token meter is blind for that model and its token cost is zero.
type PriceTable map[string]Price

// TokenCost returns the dollar cost of one usage record under the given price
// table. An unknown model or a nil table yields zero: leash never invents a
// price it was not given.
func TokenCost(u Usage, table PriceTable) float64 {
	if table == nil {
		return 0
	}
	p, ok := table[u.Model]
	if !ok {
		return 0
	}
	// Reasoning tokens are a subset of the reported output tokens (OpenAI's
	// completion_tokens includes them), so charge the non-reasoning output at the
	// output rate and reasoning at the reasoning rate. When no reasoning rate is
	// set, reasoning falls under the output rate - so it is priced once, never
	// twice, and never for free.
	reasoning := u.ReasoningTokens
	billableOutput := u.OutputTokens - reasoning
	if billableOutput < 0 {
		billableOutput = 0
		reasoning = u.OutputTokens
	}
	reasoningRate := p.ReasoningPerM
	if reasoningRate == 0 {
		reasoningRate = p.OutputPerM
	}
	return float64(u.InputTokens)/tokensPerMillion*p.InputPerM +
		float64(billableOutput)/tokensPerMillion*p.OutputPerM +
		float64(reasoning)/tokensPerMillion*reasoningRate
}

// ComputeCost returns the dollar cost of elapsed wall-clock time at ratePerHour
// dollars per hour. A zero rate (the default) makes compute free, which is the
// honest default when leash cannot see the machine bill.
func ComputeCost(elapsed time.Duration, ratePerHour float64) float64 {
	if ratePerHour == 0 || elapsed <= 0 {
		return 0
	}
	return elapsed.Hours() * ratePerHour
}

// LoadPriceTable decodes a JSON price table from r. The document is an object
// mapping model name to an object with input, output, and reasoning dollars per
// million tokens, for example:
//
//	{"gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0}}
//
// It returns an error wrapping any decode failure.
func LoadPriceTable(r io.Reader) (PriceTable, error) {
	var table PriceTable
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&table); err != nil {
		return nil, fmt.Errorf("decode price table: %w", err)
	}
	return table, nil
}
