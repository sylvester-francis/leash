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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// blindOpenAI answers an OpenAI request with content but no usage block (a blind
// call: leash cannot read what it cost).
func blindOpenAI() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}]}`)
	}
}

// buildBlind stands up a proxy with a cost budget and a chosen blind policy,
// capturing logs. handler is the upstream response.
func buildBlind(t *testing.T, onBlind BlindPolicy, handler http.HandlerFunc) (*httptest.Server, *upstreamRecorder, *bytes.Buffer) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	up := &upstreamRecorder{handler: handler}
	upSrv := httptest.NewServer(up)
	upURL, _ := url.Parse(upSrv.URL)
	logs := &bytes.Buffer{}
	p, err := New(Config{
		Ledger:   l,
		Governor: policy.NewGovernor(policy.Limits{MaxCost: 5}, policy.PriceTable{"gpt-4o": {InputPerM: 1}}, 0),
		Upstream: upURL,
		OnBlind:  onBlind,
		Logger:   slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
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
	return front, up, logs
}

func TestBlindRefuseUnknownProviderRefusedBeforeForward(t *testing.T) {
	// An unrecognized path is Unknown; with a cost budget and the default refuse
	// policy it must be rejected before forwarding.
	front, up, _ := buildBlind(t, BlindRefuse, blindOpenAI())
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/embeddings", strings.NewReader(`{"model":"x"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("Unknown-provider status = %d, want 402", resp.StatusCode)
	}
	if up.count() != 0 {
		t.Fatalf("unmeterable call was forwarded %d times, want 0", up.count())
	}
}

func TestBlindRefuseStopsRunAfterBlindForward(t *testing.T) {
	// A known provider that returns unreadable usage under a cost budget: the
	// call is delivered (already billed), then the run stops so nothing further
	// spends uncounted.
	front, up, _ := buildBlind(t, BlindRefuse, blindOpenAI())
	if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("first blind call status = %d, want 200 (already billed)", code)
	}
	code, body := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429 (run stopped)", code)
	}
	if !strings.Contains(body, blindStopReason) {
		t.Fatalf("stop reason not %q: %s", blindStopReason, body)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1 (run stopped after the first)", up.count())
	}
}

func TestBlindWarnForwardsAndWarns(t *testing.T) {
	front, up, logs := buildBlind(t, BlindWarn, blindOpenAI())
	for range 3 {
		if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
			t.Fatalf("warn-mode blind call refused, want 200")
		}
	}
	if up.count() != 3 {
		t.Fatalf("warn mode forwarded %d, want 3 (no stop)", up.count())
	}
	if !strings.Contains(logs.String(), "token meter blind") {
		t.Fatalf("warn mode did not warn: %s", logs.String())
	}
}

func TestBlindAllowIsSilent(t *testing.T) {
	front, up, logs := buildBlind(t, BlindAllow, blindOpenAI())
	postBody(t, front, nil, `{"model":"gpt-4o"}`)
	postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if up.count() != 2 {
		t.Fatalf("allow mode forwarded %d, want 2", up.count())
	}
	if strings.Contains(logs.String(), "token meter blind") {
		t.Fatalf("allow mode should not warn: %s", logs.String())
	}
}
