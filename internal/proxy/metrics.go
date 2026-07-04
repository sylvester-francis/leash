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

package proxy

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sylvester-francis/leash/internal/meter"
	"github.com/sylvester-francis/leash/internal/policy"
)

// Metrics is a hand-rolled Prometheus registry and an Observer, with no
// third-party dependency. Cardinality is bounded by construction: the only
// labels are the fixed decision, provider, token-kind, and reason sets. There is
// no run-id label anywhere; run ids are unbounded and per-run data lives in the
// ledger.
type Metrics struct {
	version string
	prices  policy.PriceTable

	mu             sync.Mutex
	callsForwarded map[string]int64 // provider -> count
	callsRefused   map[string]int64 // provider -> count
	stops          map[string]int64 // reason -> count
	tokensInput    int64
	tokensOutput   int64
	tokensReason   int64
	tokenCostUSD   float64
	blindCalls     int64
	upstreamErrors int64
	ledgerErrors   int64
	budgetWarnings map[string]int64 // reason -> count
	inFlight       int64
	responses      map[int]int64 // status code -> count
	durSum         float64       // sum of request latencies, seconds
	durCount       int64
	durBuckets     []int64 // cumulative counts aligned to latencyBuckets
}

// latencyBuckets are the request-duration histogram upper bounds, in seconds.
// They span a fast refusal to a long streamed completion.
var latencyBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// NewMetrics returns an empty registry stamped with version. The price table
// attributes a dollar cost to forwarded tokens; a nil table leaves the cost
// counter at zero (leash never invents a price).
func NewMetrics(version string, prices policy.PriceTable) *Metrics {
	return &Metrics{
		version:        version,
		prices:         prices,
		callsForwarded: map[string]int64{},
		callsRefused:   map[string]int64{},
		stops:          map[string]int64{},
		budgetWarnings: map[string]int64{},
		responses:      map[int]int64{},
		durBuckets:     make([]int64, len(latencyBuckets)),
	}
}

// RequestStarted marks a request entering the proxy.
func (m *Metrics) RequestStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight++
}

// RequestFinished records a completed request's status and latency.
func (m *Metrics) RequestFinished(status int, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight--
	m.responses[status]++
	secs := dur.Seconds()
	m.durSum += secs
	m.durCount++
	for i, bound := range latencyBuckets {
		if secs <= bound {
			m.durBuckets[i]++
		}
	}
}

// CallForwarded records a forwarded call's provider, token usage, and blindness.
func (m *Metrics) CallForwarded(p meter.Provider, u policy.Usage, blind bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callsForwarded[p.String()]++
	m.tokensInput += u.InputTokens
	m.tokensOutput += u.OutputTokens
	m.tokensReason += u.ReasoningTokens
	m.tokenCostUSD += policy.TokenCost(u, m.prices)
	if blind {
		m.blindCalls++
	}
}

// CallRefused records a refused call. The reason is not labelled here; it drives
// leash_stops_total via RunStopped instead.
func (m *Metrics) CallRefused(p meter.Provider, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callsRefused[p.String()]++
}

// RunStopped records a run's transition to stopped, keyed by boundary reason.
func (m *Metrics) RunStopped(s *policy.State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops[s.StopReason]++
}

// BudgetWarning records a run approaching a budget, keyed by which budget. The
// run id is not a label (unbounded cardinality); it is in the log and webhook.
func (m *Metrics) BudgetWarning(_ *policy.State, status policy.BudgetStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.budgetWarnings[status.Reason]++
}

// UpstreamError records one upstream failure.
func (m *Metrics) UpstreamError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upstreamErrors++
}

// LedgerError records one durable-write failure.
func (m *Metrics) LedgerError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ledgerErrors++
}

// WriteTo renders the metrics in Prometheus text format. activeRuns is passed in
// because that live count is owned by the Proxy, not accumulated here.
func (m *Metrics) WriteTo(w io.Writer, activeRuns int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder

	b.WriteString("# HELP leash_calls_total Governed model calls by decision and provider.\n")
	b.WriteString("# TYPE leash_calls_total counter\n")
	for _, prov := range sortedKeys(m.callsForwarded) {
		fmt.Fprintf(&b, "leash_calls_total{decision=\"forwarded\",provider=%q} %d\n", prov, m.callsForwarded[prov])
	}
	for _, prov := range sortedKeys(m.callsRefused) {
		fmt.Fprintf(&b, "leash_calls_total{decision=\"refused\",provider=%q} %d\n", prov, m.callsRefused[prov])
	}

	b.WriteString("# HELP leash_stops_total Runs stopped by boundary reason.\n")
	b.WriteString("# TYPE leash_stops_total counter\n")
	for _, reason := range sortedKeys(m.stops) {
		fmt.Fprintf(&b, "leash_stops_total{reason=%q} %d\n", reason, m.stops[reason])
	}

	b.WriteString("# HELP leash_tokens_total Tokens metered off the wire by kind.\n")
	b.WriteString("# TYPE leash_tokens_total counter\n")
	fmt.Fprintf(&b, "leash_tokens_total{kind=\"input\"} %d\n", m.tokensInput)
	fmt.Fprintf(&b, "leash_tokens_total{kind=\"output\"} %d\n", m.tokensOutput)
	fmt.Fprintf(&b, "leash_tokens_total{kind=\"reasoning\"} %d\n", m.tokensReason)

	b.WriteString("# HELP leash_token_cost_usd_total Dollar cost of metered tokens under the run prices.\n")
	b.WriteString("# TYPE leash_token_cost_usd_total counter\n")
	fmt.Fprintf(&b, "leash_token_cost_usd_total %s\n", strconv.FormatFloat(m.tokenCostUSD, 'g', -1, 64))

	b.WriteString("# HELP leash_blind_calls_total Forwarded calls that reported no usage on the wire.\n")
	b.WriteString("# TYPE leash_blind_calls_total counter\n")
	fmt.Fprintf(&b, "leash_blind_calls_total %d\n", m.blindCalls)

	b.WriteString("# HELP leash_upstream_errors_total Upstream request or read failures.\n")
	b.WriteString("# TYPE leash_upstream_errors_total counter\n")
	fmt.Fprintf(&b, "leash_upstream_errors_total %d\n", m.upstreamErrors)

	b.WriteString("# HELP leash_ledger_errors_total Durable-write failures (a call or stop record).\n")
	b.WriteString("# TYPE leash_ledger_errors_total counter\n")
	fmt.Fprintf(&b, "leash_ledger_errors_total %d\n", m.ledgerErrors)

	b.WriteString("# HELP leash_budget_warnings_total Runs that crossed a warn threshold, by budget.\n")
	b.WriteString("# TYPE leash_budget_warnings_total counter\n")
	for _, reason := range sortedKeys(m.budgetWarnings) {
		fmt.Fprintf(&b, "leash_budget_warnings_total{reason=%q} %d\n", reason, m.budgetWarnings[reason])
	}

	b.WriteString("# HELP leash_build_info Build version, always 1.\n")
	b.WriteString("# TYPE leash_build_info gauge\n")
	fmt.Fprintf(&b, "leash_build_info{version=\"%s\"} 1\n", escapeLabel(m.version))

	b.WriteString("# HELP leash_active_runs Runs currently governed in memory and not stopped.\n")
	b.WriteString("# TYPE leash_active_runs gauge\n")
	fmt.Fprintf(&b, "leash_active_runs %d\n", activeRuns)

	b.WriteString("# HELP leash_requests_in_flight HTTP requests currently being served.\n")
	b.WriteString("# TYPE leash_requests_in_flight gauge\n")
	fmt.Fprintf(&b, "leash_requests_in_flight %d\n", m.inFlight)

	b.WriteString("# HELP leash_responses_total HTTP responses by status code.\n")
	b.WriteString("# TYPE leash_responses_total counter\n")
	for _, code := range sortedIntKeys(m.responses) {
		fmt.Fprintf(&b, "leash_responses_total{code=\"%d\"} %d\n", code, m.responses[code])
	}

	b.WriteString("# HELP leash_request_duration_seconds Request latency in seconds.\n")
	b.WriteString("# TYPE leash_request_duration_seconds histogram\n")
	for i, bound := range latencyBuckets {
		fmt.Fprintf(&b, "leash_request_duration_seconds_bucket{le=%q} %d\n",
			strconv.FormatFloat(bound, 'g', -1, 64), m.durBuckets[i])
	}
	fmt.Fprintf(&b, "leash_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.durCount)
	fmt.Fprintf(&b, "leash_request_duration_seconds_sum %s\n", strconv.FormatFloat(m.durSum, 'g', -1, 64))
	fmt.Fprintf(&b, "leash_request_duration_seconds_count %d\n", m.durCount)

	_, _ = io.WriteString(w, b.String())
}

// sortedKeys returns a map's keys sorted, so the exposition output is stable.
func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedIntKeys returns an int-keyed map's keys sorted ascending.
func sortedIntKeys(m map[int]int64) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// labelEscaper escapes a Prometheus label value: backslash, quote, newline.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// escapeLabel escapes a Prometheus label value. Only the build-supplied version
// string needs it; the other labels are fixed enums.
func escapeLabel(v string) string { return labelEscaper.Replace(v) }
