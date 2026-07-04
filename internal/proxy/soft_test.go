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
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

// TestRateLimitIsTransientWithRetryAfter proves the rate limit is backpressure,
// not a terminal stop: the refused call carries Retry-After, and once the window
// decays the run resumes.
func TestRateLimitIsTransientWithRetryAfter(t *testing.T) {
	h := newHarness(t, policy.Limits{RateTokens: 100, RateWindow: time.Minute}, nil, false,
		openAIJSON("gpt-4o", "hi", 60, 0)) // 60 tokens per call

	h.do(chatPath, nil, `{"model":"gpt-4o"}`) // cumulative 60
	h.do(chatPath, nil, `{"model":"gpt-4o"}`) // cumulative 120
	resp, _ := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After = %q, want 60", resp.Header.Get("Retry-After"))
	}

	// After the trailing window passes, the run recovers - it was never stopped.
	h.clock.advance(61 * time.Second)
	resp, _ = h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-window status = %d, want 200 (rate limit is transient)", resp.StatusCode)
	}
}

// capturingObserver records BudgetWarning events.
type capturingObserver struct {
	NopObserver
	mu       sync.Mutex
	warnings []policy.BudgetStatus
}

func (c *capturingObserver) BudgetWarning(_ *policy.State, st policy.BudgetStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warnings = append(c.warnings, st)
}

func TestWarnAtFiresOncePerBudget(t *testing.T) {
	obs := &capturingObserver{}
	front, _, _ := buildProxy(t, func(c *Config) {
		c.Governor = policy.NewGovernor(policy.Limits{MaxCalls: 4}, nil, 0)
		c.WarnAt = 0.75 // warn when calls reach 3 of 4
		c.Observer = obs
	})

	for i := 0; i < 3; i++ {
		if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
			t.Fatalf("call %d refused early", i+1)
		}
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.warnings) != 1 {
		t.Fatalf("got %d warnings, want exactly 1", len(obs.warnings))
	}
	if obs.warnings[0].Reason != "max_calls" {
		t.Fatalf("warning reason = %q, want max_calls", obs.warnings[0].Reason)
	}
	if obs.warnings[0].Fraction < 0.75 {
		t.Fatalf("warning fraction = %v, want >= 0.75", obs.warnings[0].Fraction)
	}
}

func TestWebhookNotifierPostsEvents(t *testing.T) {
	got := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		_, _ = io.Copy(io.Discard, r.Body)
		got <- m
	}))
	defer srv.Close()

	clk := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	n := NewWebhookNotifier(srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return clk })

	st := &policy.State{RunID: "r1", Calls: 3, TotalCost: 4.2}
	n.BudgetWarning(st, policy.BudgetStatus{Reason: "cost_budget", Used: 4.2, Limit: 5, Fraction: 0.84})
	st.StopReason = "cost_budget"
	n.RunStopped(st)

	want := map[string]bool{"warning": true, "stopped": true}
	for range 2 {
		select {
		case m := <-got:
			ev, _ := m["event"].(string)
			if !want[ev] {
				t.Fatalf("unexpected or duplicate event %q", ev)
			}
			delete(want, ev)
			if m["run"] != "r1" {
				t.Fatalf("event %q run = %v, want r1", ev, m["run"])
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for webhook events; still missing %v", want)
		}
	}
}
