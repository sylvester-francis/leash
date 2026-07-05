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
	// InputTokens is the total prompt token count, including any cached or
	// cache-written tokens.
	InputTokens int64 `json:"input"`
	// CachedReadTokens is the portion of InputTokens served from the provider's
	// prompt cache, billed at the cache-read rate. A subset of InputTokens.
	CachedReadTokens int64 `json:"cached_read,omitempty"`
	// CacheWriteTokens is the portion of InputTokens written to the prompt cache,
	// billed at the cache-write rate. A subset of InputTokens.
	CacheWriteTokens int64 `json:"cache_write,omitempty"`
	// CacheWrite5mTokens and CacheWrite1hTokens are the 5-minute and 1-hour TTL
	// portions of CacheWriteTokens, priced at their own rates when set. Subsets of
	// CacheWriteTokens.
	CacheWrite5mTokens int64 `json:"cache_write_5m,omitempty"`
	CacheWrite1hTokens int64 `json:"cache_write_1h,omitempty"`
	// AudioInputTokens is the audio portion of InputTokens, priced at the audio
	// input rate when set. A subset of InputTokens.
	AudioInputTokens int64 `json:"audio_input,omitempty"`
	// OutputTokens is the completion token count.
	OutputTokens int64 `json:"output"`
	// ReasoningTokens is the reasoning/thinking token count when the provider
	// reports it separately; zero otherwise. A subset of OutputTokens.
	ReasoningTokens int64 `json:"reasoning"`
	// AudioOutputTokens is the audio portion of OutputTokens, priced at the audio
	// output rate when set. A subset of OutputTokens.
	AudioOutputTokens int64 `json:"audio_output,omitempty"`
	// WebSearchRequests and WebFetchRequests are provider-side tool requests the
	// call billed. These are per-request charges, not tokens. Priced from the
	// table's per-request rates when set; when a request has no rate, it is billed
	// activity leash cannot account for and the call is failed closed.
	WebSearchRequests int64 `json:"web_search,omitempty"`
	WebFetchRequests  int64 `json:"web_fetch,omitempty"`
	// ServiceTier names the provider service tier (for example "priority" or
	// "batch"); when set and present in a Price's Tiers, that tier's rates apply.
	ServiceTier string `json:"service_tier,omitempty"`
}

// ServerToolRequests is the total count of billed provider-side tool requests.
func (u Usage) ServerToolRequests() int64 {
	return u.WebSearchRequests + u.WebFetchRequests
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
	// CachedInputPerM is dollars per million cache-read input tokens; when zero,
	// cached input is billed at the input rate.
	CachedInputPerM float64 `json:"cached_input"`
	// CacheWritePerM is dollars per million cache-write input tokens; when zero,
	// cache writes are billed at the input rate.
	CacheWritePerM float64 `json:"cache_write"`

	// The fields below are opt-in refinements. Each unset (zero) field falls back
	// to a coarser rate, so a table that sets only input/output/reasoning behaves
	// exactly as before.

	// CacheWrite5mPerM and CacheWrite1hPerM price the 5-minute and 1-hour TTL
	// portions of cache-write tokens; each falls back to CacheWritePerM.
	CacheWrite5mPerM float64 `json:"cache_write_5m"`
	CacheWrite1hPerM float64 `json:"cache_write_1h"`
	// AudioInputPerM and AudioOutputPerM price audio tokens; they fall back to the
	// input and output rates.
	AudioInputPerM  float64 `json:"audio_input"`
	AudioOutputPerM float64 `json:"audio_output"`
	// WebSearchPerRequest and WebFetchPerRequest are dollars per provider-side
	// tool request. Unset (zero) means the tool is unpriced, so a call that uses it
	// fails closed under a cost budget rather than billing at zero.
	WebSearchPerRequest float64 `json:"web_search_per_request"`
	WebFetchPerRequest  float64 `json:"web_fetch_per_request"`
	// Tiers holds per-service-tier price overrides. When a call's ServiceTier
	// matches a key, that tier's Price replaces this one for the call (a tier's own
	// Tiers is ignored).
	Tiers map[string]Price `json:"tiers,omitempty"`
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
	e := effectivePrice(p, u.ServiceTier)
	return tokenSpend(u, e) + toolSpend(u, e)
}

// effectivePrice returns the tier override when the call names a tier that the
// price defines, otherwise the base price.
func effectivePrice(base Price, tier string) Price {
	if tier != "" {
		if tp, ok := base.Tiers[tier]; ok {
			return tp
		}
	}
	return base
}

// rate returns v, or fallback when v is zero, so an unset refinement never prices
// at zero (never accidentally free) and never double counts.
func rate(v, fallback float64) float64 {
	if v == 0 {
		return fallback
	}
	return v
}

// perM is tokens priced at ratePerM dollars per million.
func perM(tokens int64, ratePerM float64) float64 {
	return float64(tokens) / tokensPerMillion * ratePerM
}

// tokenSpend prices a call's tokens under p. Reasoning and audio are subsets of
// output; cache-read, cache-write, and audio are subsets of input; each is priced
// at its own rate (falling back to a coarser one) and the remainder at the base
// rate, so every token is counted exactly once.
func tokenSpend(u Usage, p Price) float64 {
	// Output side.
	reasoning := min(u.ReasoningTokens, u.OutputTokens)
	audioOut := max(0, min(u.AudioOutputTokens, u.OutputTokens-reasoning))
	billableOutput := max(0, u.OutputTokens-reasoning-audioOut)
	out := perM(billableOutput, p.OutputPerM) +
		perM(reasoning, rate(p.ReasoningPerM, p.OutputPerM)) +
		perM(audioOut, rate(p.AudioOutputPerM, p.OutputPerM))

	// Input side. Cache-write splits into TTL tiers; the untiered remainder pays
	// the base cache-write rate.
	cw5m := min(u.CacheWrite5mTokens, u.CacheWriteTokens)
	cw1h := max(0, min(u.CacheWrite1hTokens, u.CacheWriteTokens-cw5m))
	cwBase := max(0, u.CacheWriteTokens-cw5m-cw1h)
	writeRate := rate(p.CacheWritePerM, p.InputPerM)
	fullInput := max(0, u.InputTokens-u.CachedReadTokens-u.CacheWriteTokens-u.AudioInputTokens)
	in := perM(fullInput, p.InputPerM) +
		perM(u.CachedReadTokens, rate(p.CachedInputPerM, p.InputPerM)) +
		perM(u.AudioInputTokens, rate(p.AudioInputPerM, p.InputPerM)) +
		perM(cwBase, writeRate) +
		perM(cw5m, rate(p.CacheWrite5mPerM, writeRate)) +
		perM(cw1h, rate(p.CacheWrite1hPerM, writeRate))
	return in + out
}

// toolSpend prices per-request provider-side tool charges. An unpriced tool
// (rate zero) contributes nothing here; UnpricedToolActivity flags it so the
// caller fails closed instead of billing it at zero.
func toolSpend(u Usage, p Price) float64 {
	return float64(u.WebSearchRequests)*p.WebSearchPerRequest +
		float64(u.WebFetchRequests)*p.WebFetchPerRequest
}

// UnpricedToolActivity reports whether a call billed provider-side tool requests
// the model's effective price does not cover. That spend cannot be metered from
// the table, so the caller fails closed on it (see the proxy's --on-blind path).
func UnpricedToolActivity(u Usage, table PriceTable) bool {
	var p Price
	if table != nil {
		p = effectivePrice(table[u.Model], u.ServiceTier)
	}
	return (u.WebSearchRequests > 0 && p.WebSearchPerRequest == 0) ||
		(u.WebFetchRequests > 0 && p.WebFetchPerRequest == 0)
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
// mapping model name to a price. The common case is input, output, and reasoning
// dollars per million tokens:
//
//	{"gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0}}
//
// Optional fields refine that when you need them, and unset ones fall back to a
// coarser rate, so the simple case above is unchanged: cache-read/write rates,
// per-TTL cache-write rates (cache_write_5m / cache_write_1h), audio rates
// (audio_input / audio_output), per-request tool rates
// (web_search_per_request / web_fetch_per_request), and per-service-tier
// overrides under "tiers". It returns an error wrapping any decode failure.
func LoadPriceTable(r io.Reader) (PriceTable, error) {
	var table PriceTable
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&table); err != nil {
		return nil, fmt.Errorf("decode price table: %w", err)
	}
	return table, nil
}
