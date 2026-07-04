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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

func TestMaxCostPerCallStopsRun(t *testing.T) {
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	// One call reports 2,000,000 input tokens at $1/M = $2.00.
	up := &upstreamRecorder{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":2000000,"completion_tokens":0}}`)
	}}
	upSrv := httptest.NewServer(up)
	upURL, _ := url.Parse(upSrv.URL)
	p, err := New(Config{
		Ledger:         l,
		Governor:       policy.NewGovernor(policy.Limits{MaxCalls: 100}, policy.PriceTable{"gpt-4o": {InputPerM: 1}}, 0),
		Upstream:       upURL,
		MaxCostPerCall: 1.00, // the $2 call exceeds this
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(func() {
		front.Close()
		_ = p.Shutdown()
		upSrv.Close()
		_ = l.Close()
	})

	// The over-cap call is delivered (metering is post-response), then the run stops.
	if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("first call status = %d, want 200 (already billed)", code)
	}
	code, body := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429 (run stopped)", code)
	}
	if !strings.Contains(body, "max_cost_per_call") {
		t.Fatalf("stop reason not max_cost_per_call: %s", body)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1 (run stopped after the over-cap call)", up.count())
	}
}
