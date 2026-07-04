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
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

func TestSecretNeverLoggedAtDebugJSON(t *testing.T) {
	const secret = "sk-debug-json-secret-abcdef"
	var logs bytes.Buffer
	front, _, _ := buildProxy(t, func(c *Config) {
		// The strictest case: debug level, JSON format, everything captured.
		c.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	header := http.Header{
		"Authorization": {"Bearer " + secret},
		"X-Api-Key":     {secret},
	}
	code, _ := postBody(t, front, header, `{"model":"gpt-4o","messages":[{"role":"user","content":"`+secret+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("secret leaked into debug/json logs:\n%s", logs.String())
	}
}

func TestMetricsExpositionNoRunIDLabels(t *testing.T) {
	const canaryRun = "cardinality-canary-run"
	metrics := NewMetrics("v-test", policy.PriceTable{"gpt-4o": {InputPerM: 1.0}})
	front, _, p := buildProxy(t, func(c *Config) {
		c.Governor = policy.NewGovernor(policy.Limits{MaxCalls: 1}, policy.PriceTable{"gpt-4o": {InputPerM: 1.0}}, 0)
		c.Observer = metrics
	})
	// One forwarded call, then the run trips max_calls and is refused.
	postBody(t, front, http.Header{"X-Loop-Id": {canaryRun}}, `{"model":"gpt-4o"}`)
	postBody(t, front, http.Header{"X-Loop-Id": {canaryRun}}, `{"model":"gpt-4o"}`)

	var out bytes.Buffer
	metrics.WriteTo(&out, p.ActiveRuns())
	got := out.String()

	for _, want := range []string{
		`leash_calls_total{decision="forwarded",provider="openai"} 1`,
		`leash_calls_total{decision="refused",provider="openai"} 1`,
		`leash_stops_total{reason="max_calls"} 1`,
		`leash_tokens_total{kind="input"} 1`,
		`leash_build_info{version="v-test"} 1`,
		"leash_upstream_errors_total 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metrics missing %q in:\n%s", want, got)
		}
	}
	// The unbounded run id must never appear as a label or anywhere else.
	if strings.Contains(got, canaryRun) {
		t.Fatalf("run id leaked into metrics (unbounded cardinality):\n%s", got)
	}
	// Every exposed series must carry HELP/TYPE metadata.
	if !strings.Contains(got, "# TYPE leash_calls_total counter") {
		t.Fatalf("missing TYPE metadata:\n%s", got)
	}
}

func TestAdminEndpoints(t *testing.T) {
	metrics := NewMetrics("v-admin", nil)
	_, _, p := buildProxy(t, func(c *Config) { c.Observer = metrics })
	admin := httptest.NewServer(NewAdminServer("", p.cfg.Ledger, p, metrics).Handler)
	defer admin.Close()

	// The ledger under test is the one the proxy governs; expose it for readyz.
	cases := []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{"/healthz", 200, "ok"},
		{"/readyz", 200, "ready"},
		{"/metrics", 200, "leash_build_info"},
	}
	for _, tc := range cases {
		resp, err := http.Get(admin.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body := readAll(t, resp)
		if resp.StatusCode != tc.wantStatus {
			t.Fatalf("GET %s status = %d, want %d", tc.path, resp.StatusCode, tc.wantStatus)
		}
		if !strings.Contains(body, tc.wantBody) {
			t.Fatalf("GET %s body = %q, want to contain %q", tc.path, body, tc.wantBody)
		}
	}
	// The metrics content type carries the exposition version scrapers expect.
	resp, _ := http.Get(admin.URL + "/metrics")
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "version=0.0.4") {
		t.Fatalf("/metrics Content-Type = %q, want version=0.0.4", ct)
	}
	_ = readAll(t, resp)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return b.String()
}

func TestEvictStoppedIdleRunColdReloadsSame429(t *testing.T) {
	front, up, p := buildProxy(t, func(c *Config) {
		c.Governor = policy.NewGovernor(policy.Limits{MaxCalls: 1}, nil, 0)
	})
	const run = "evictme"
	hdr := http.Header{"X-Loop-Id": {run}}

	postBody(t, front, hdr, `{"model":"gpt-4o"}`)                   // uses the one call
	_, stoppedBody := postBody(t, front, hdr, `{"model":"gpt-4o"}`) // 429 stopped
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1", up.count())
	}

	// The run is stopped in memory; evict it as if 10m idle had passed.
	if n := p.evictIdle(time.Now().Add(11 * time.Minute)); n != 1 {
		t.Fatalf("evictIdle removed %d runs, want 1", n)
	}
	p.mu.Lock()
	_, present := p.runs[run]
	p.mu.Unlock()
	if present {
		t.Fatalf("run %q still in memory after eviction", run)
	}

	// A later call cold-reloads from the journal and returns the identical 429.
	code, coldBody := postBody(t, front, hdr, `{"model":"gpt-4o"}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("post-eviction status = %d, want 429", code)
	}
	if coldBody != stoppedBody {
		t.Fatalf("cold-reload 429 body differs from warm:\nwarm %s\ncold %s", stoppedBody, coldBody)
	}
}
