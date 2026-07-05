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
)

const m = 1_000_000.0

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// TestTokenCostDimensions prices each opt-in dimension and their combinations,
// checking that every token is counted exactly once and unset refinements fall
// back to the coarser rate.
func TestTokenCostDimensions(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		price Price
		want  float64
	}{
		{
			name:  "backward compat: input/output/reasoning unchanged",
			usage: Usage{Model: "x", InputTokens: 1000, OutputTokens: 500, ReasoningTokens: 100},
			price: Price{InputPerM: 1, OutputPerM: 10, ReasoningPerM: 5},
			want:  1000/m*1 + 400/m*10 + 100/m*5,
		},
		{
			name:  "audio output priced once as a subset of output",
			usage: Usage{Model: "x", OutputTokens: 1000, ReasoningTokens: 300, AudioOutputTokens: 200},
			price: Price{OutputPerM: 10, ReasoningPerM: 5, AudioOutputPerM: 8},
			want:  500/m*10 + 300/m*5 + 200/m*8,
		},
		{
			name:  "audio input priced as a subset of input",
			usage: Usage{Model: "x", InputTokens: 1000, AudioInputTokens: 400},
			price: Price{InputPerM: 1, AudioInputPerM: 5},
			want:  600/m*1 + 400/m*5,
		},
		{
			name: "cache-write TTL tiers",
			usage: Usage{
				Model: "x", InputTokens: 1000, CacheWriteTokens: 500,
				CacheWrite5mTokens: 300, CacheWrite1hTokens: 200,
			},
			price: Price{InputPerM: 1, CacheWritePerM: 1.25, CacheWrite5mPerM: 1.25, CacheWrite1hPerM: 2},
			want:  500/m*1 + 300/m*1.25 + 200/m*2,
		},
		{
			name:  "per-request tool charges",
			usage: Usage{Model: "x", WebSearchRequests: 3, WebFetchRequests: 2},
			price: Price{WebSearchPerRequest: 0.01, WebFetchPerRequest: 0.005},
			want:  3*0.01 + 2*0.005,
		},
		{
			name:  "service tier override replaces base rates",
			usage: Usage{Model: "x", InputTokens: 1000, OutputTokens: 1000, ServiceTier: "priority"},
			price: Price{InputPerM: 1, OutputPerM: 10, Tiers: map[string]Price{"priority": {InputPerM: 2, OutputPerM: 20}}},
			want:  1000/m*2 + 1000/m*20,
		},
		{
			name:  "unknown tier falls back to base",
			usage: Usage{Model: "x", InputTokens: 1000, OutputTokens: 1000, ServiceTier: "nope"},
			price: Price{InputPerM: 1, OutputPerM: 10, Tiers: map[string]Price{"priority": {InputPerM: 2, OutputPerM: 20}}},
			want:  1000/m*1 + 1000/m*10,
		},
		{
			name:  "audio rate unset falls back to base output rate",
			usage: Usage{Model: "x", OutputTokens: 1000, AudioOutputTokens: 400},
			price: Price{OutputPerM: 10}, // no AudioOutputPerM
			want:  1000 / m * 10,          // all output at the output rate, counted once
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TokenCost(tt.usage, PriceTable{"x": tt.price})
			if !approx(got, tt.want) {
				t.Fatalf("TokenCost = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnpricedToolActivity(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		table PriceTable
		want  bool
	}{
		{"tool used, no price", Usage{Model: "x", WebSearchRequests: 1}, PriceTable{"x": {InputPerM: 1}}, true},
		{"tool used, priced", Usage{Model: "x", WebSearchRequests: 1}, PriceTable{"x": {WebSearchPerRequest: 0.01}}, false},
		{"no tool", Usage{Model: "x", OutputTokens: 10}, PriceTable{"x": {OutputPerM: 1}}, false},
		{"unknown model, tool used", Usage{Model: "y", WebFetchRequests: 1}, PriceTable{"x": {WebFetchPerRequest: 1}}, true},
		{
			"priced only in the call's tier",
			Usage{Model: "x", WebSearchRequests: 1, ServiceTier: "priority"},
			PriceTable{"x": {Tiers: map[string]Price{"priority": {WebSearchPerRequest: 0.01}}}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnpricedToolActivity(tt.usage, tt.table); got != tt.want {
				t.Fatalf("UnpricedToolActivity = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadPriceTableNewDimensions(t *testing.T) {
	doc := `{"claude-x": {
		"input": 3, "output": 15, "reasoning": 15,
		"cache_write_1h": 6, "audio_input": 40,
		"web_search_per_request": 0.01,
		"tiers": {"batch": {"input": 1.5, "output": 7.5}}
	}}`
	table, err := LoadPriceTable(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("LoadPriceTable: %v", err)
	}
	p := table["claude-x"]
	if p.WebSearchPerRequest != 0.01 || p.CacheWrite1hPerM != 6 || p.AudioInputPerM != 40 {
		t.Fatalf("new fields not decoded: %+v", p)
	}
	if p.Tiers["batch"].InputPerM != 1.5 {
		t.Fatalf("tier not decoded: %+v", p.Tiers)
	}
}
