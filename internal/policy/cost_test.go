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
	"math"
	"strings"
	"testing"
	"time"
)

// approxEqual reports whether two dollar amounts are equal to the cent.
func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestTokenCost(t *testing.T) {
	prices := PriceTable{
		"gpt-4": {InputPerM: 30, OutputPerM: 60, ReasoningPerM: 0},
		"o1":    {InputPerM: 15, OutputPerM: 60, ReasoningPerM: 60},
	}
	tests := []struct {
		name  string
		usage Usage
		want  float64
	}{
		{
			name:  "known model input and output at one million each",
			usage: Usage{Model: "gpt-4", InputTokens: 1_000_000, OutputTokens: 1_000_000},
			want:  90,
		},
		{
			// Reasoning tokens are a subset of output. A response that is all
			// reasoning (output == reasoning) is priced once, at the reasoning rate.
			name:  "all-reasoning output is priced at the reasoning rate, once",
			usage: Usage{Model: "o1", OutputTokens: 1_000_000, ReasoningTokens: 1_000_000},
			want:  60,
		},
		{
			// Reasoning must not be double-charged: output rate on the
			// non-reasoning output, reasoning rate on the reasoning subset.
			name:  "reasoning is not double-charged",
			usage: Usage{Model: "o1", InputTokens: 1_000_000, OutputTokens: 1_000_000, ReasoningTokens: 500_000},
			want:  15 + 30 + 30, // input 15, output 500k@60=30, reasoning 500k@60=30
		},
		{
			// With no reasoning rate set (gpt-4 prices reasoning at 0), reasoning
			// falls under the output rate: all output priced once, never free.
			name:  "reasoning without a reasoning rate uses the output rate",
			usage: Usage{Model: "gpt-4", OutputTokens: 1_000_000, ReasoningTokens: 400_000},
			want:  60, // all 1M output at gpt-4 output rate 60
		},
		{
			name:  "cost scales linearly below one million",
			usage: Usage{Model: "gpt-4", InputTokens: 500_000, OutputTokens: 250_000},
			want:  15 + 15,
		},
		{
			name:  "unknown model costs zero, never a hardcoded price",
			usage: Usage{Model: "mystery-model", InputTokens: 1_000_000, OutputTokens: 1_000_000},
			want:  0,
		},
		{
			name:  "empty model costs zero",
			usage: Usage{Model: "", InputTokens: 1_000_000},
			want:  0,
		},
		{
			name:  "zero tokens cost zero",
			usage: Usage{Model: "gpt-4"},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TokenCost(tt.usage, prices)
			if !approxEqual(got, tt.want) {
				t.Fatalf("TokenCost(%+v) = %v, want %v", tt.usage, got, tt.want)
			}
		})
	}
}

func TestTokenCostNilTableIsZero(t *testing.T) {
	// An absent price table means the token meter is blind: cost is zero.
	got := TokenCost(Usage{Model: "gpt-4", InputTokens: 1_000_000}, nil)
	if got != 0 {
		t.Fatalf("TokenCost with nil table = %v, want 0", got)
	}
}

func TestComputeCost(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		rate    float64
		want    float64
	}{
		{"one hour at one dollar per hour", time.Hour, 1.0, 1.0},
		{"half hour at two dollars per hour", 30 * time.Minute, 2.0, 1.0},
		{"quarter hour at four dollars per hour", 15 * time.Minute, 4.0, 1.0},
		{"zero rate is free", time.Hour, 0, 0},
		{"zero elapsed is free", 0, 5.0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeCost(tt.elapsed, tt.rate)
			if !approxEqual(got, tt.want) {
				t.Fatalf("ComputeCost(%v, %v) = %v, want %v", tt.elapsed, tt.rate, got, tt.want)
			}
		})
	}
}

func TestLoadPriceTable(t *testing.T) {
	const doc = `{
		"gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0},
		"o1": {"input": 15, "output": 60, "reasoning": 60}
	}`
	table, err := LoadPriceTable(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("LoadPriceTable returned error: %v", err)
	}
	if got := table["gpt-4o"].InputPerM; got != 2.5 {
		t.Fatalf("gpt-4o input price = %v, want 2.5", got)
	}
	if got := table["o1"].ReasoningPerM; got != 60 {
		t.Fatalf("o1 reasoning price = %v, want 60", got)
	}
	if _, ok := table["absent"]; ok {
		t.Fatalf("absent model should not be present")
	}
}

func TestLoadPriceTableRejectsGarbage(t *testing.T) {
	if _, err := LoadPriceTable(strings.NewReader("not json")); err == nil {
		t.Fatalf("LoadPriceTable accepted invalid JSON, want error")
	}
}

func TestUsageTotalTokens(t *testing.T) {
	// Reasoning is a subset of output, so it is not summed again.
	u := Usage{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 3}
	if got := u.TotalTokens(); got != 120 {
		t.Fatalf("TotalTokens = %d, want 120 (input+output; reasoning is within output)", got)
	}
}
