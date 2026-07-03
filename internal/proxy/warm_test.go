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
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// warmFixture builds a proxy over a known db path and a varying-usage upstream,
// so a test can open a second ledger handle on the same db.
type warmFixture struct {
	db    string
	front *httptest.Server
	proxy *Proxy
	l     *ledger.Ledger
}

func newWarmFixture(t *testing.T, limits policy.Limits, prices policy.PriceTable) *warmFixture {
	t.Helper()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	var n atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":"gpt-4o","choices":[{"message":{"content":"reply %d"}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, i, 10*i, i)
	}))
	upURL, _ := url.Parse(up.URL)
	p, err := New(Config{
		Ledger:   l,
		Governor: policy.NewGovernor(limits, prices, 0),
		Upstream: upURL,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(func() {
		front.Close()
		up.Close()
		_ = p.Shutdown()
		_ = l.Close()
	})
	return &warmFixture{db: db, front: front, proxy: p, l: l}
}

func (f *warmFixture) call(t *testing.T, run string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.front.URL+chatPath, strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("X-Loop-Id", run)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestKillFromSecondHandleSeenWithinOneCallWarm(t *testing.T) {
	f := newWarmFixture(t, policy.Limits{MaxCalls: 100}, nil)
	const run = "warmkill"

	if code := f.call(t, run); code != http.StatusOK {
		t.Fatalf("first call = %d, want 200", code)
	}
	// The run is now warm in memory. A separate handle records the kill (which
	// also sets the fast cancel flag), as a second `leash kill` process would.
	killer, err := ledger.Open(f.db)
	if err != nil {
		t.Fatalf("open killer: %v", err)
	}
	if err := killer.AppendKill(context.Background(), run, time.Now()); err != nil {
		t.Fatalf("append kill: %v", err)
	}
	_ = killer.Close()

	// The very next call on the warm path must observe the kill, with no
	// eviction or restart in between.
	if code := f.call(t, run); code != http.StatusTooManyRequests {
		t.Fatalf("warm path did not observe the kill within one call: status %d", code)
	}
}

func TestCachedStateEqualsColdFold(t *testing.T) {
	prices := policy.PriceTable{"gpt-4o": {InputPerM: 2.0, OutputPerM: 8.0}}
	f := newWarmFixture(t, policy.Limits{MaxCalls: 1000}, prices)
	const run = "property"

	for i := range 25 {
		if code := f.call(t, run); code != http.StatusOK {
			t.Fatalf("call %d = %d, want 200", i, code)
		}
	}

	// The warm cache the proxy holds.
	f.proxy.mu.Lock()
	rs := f.proxy.runs[run]
	f.proxy.mu.Unlock()
	if rs == nil || rs.state == nil {
		t.Fatalf("no warm state cached for run %q", run)
	}
	cached := rs.state

	// A fresh, independent cold fold of the journal.
	cold, err := f.l.Load(context.Background(), run, policy.NewGovernor(policy.Limits{MaxCalls: 1000}, prices, 0))
	if err != nil {
		t.Fatalf("cold load: %v", err)
	}

	if cached.Calls != cold.Calls {
		t.Fatalf("Calls: warm %d, cold %d", cached.Calls, cold.Calls)
	}
	if cached.InputTokens != cold.InputTokens || cached.OutputTokens != cold.OutputTokens {
		t.Fatalf("tokens: warm (%d,%d), cold (%d,%d)",
			cached.InputTokens, cached.OutputTokens, cold.InputTokens, cold.OutputTokens)
	}
	if cached.TokenCost != cold.TokenCost {
		t.Fatalf("TokenCost: warm %v, cold %v", cached.TokenCost, cold.TokenCost)
	}
	if !cached.StartedAt.Equal(cold.StartedAt) {
		t.Fatalf("StartedAt: warm %v, cold %v", cached.StartedAt, cold.StartedAt)
	}
	if len(cached.Samples) != len(cold.Samples) {
		t.Fatalf("Samples length: warm %d, cold %d", len(cached.Samples), len(cold.Samples))
	}
}
