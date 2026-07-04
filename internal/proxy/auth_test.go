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
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/policy"
)

const testToken = "sk-leash-77a1c0ffee0000deadbeef"

func TestAuthRejectsMissingAndWrongToken(t *testing.T) {
	front, up, _ := buildProxy(t, func(c *Config) { c.AuthTokens = []string{testToken} })

	// No token.
	code, body := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", code)
	}
	if !strings.Contains(body, "leash_gateway") {
		t.Fatalf("401 body missing leash_gateway shape: %s", body)
	}
	// Wrong token.
	code, _ = postBody(t, front, http.Header{"X-Leash-Token": {"wrong"}}, `{"model":"gpt-4o"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", code)
	}
	if up.count() != 0 {
		t.Fatalf("unauthorized requests forwarded upstream %d times, want 0", up.count())
	}
}

func TestAuthAcceptsCorrectTokenAndStripsIt(t *testing.T) {
	front, up, _ := buildProxy(t, func(c *Config) { c.AuthTokens = []string{testToken} })
	code, _ := postBody(t, front, http.Header{"X-Leash-Token": {testToken}}, `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("correct token status = %d, want 200", code)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1", up.count())
	}
	// The credential must not leak upstream.
	if up.last().Header.Get("X-Leash-Token") != "" {
		t.Fatalf("X-Leash-Token was forwarded upstream")
	}
}

func TestAuthAcceptsAnyConfiguredTokenForRotation(t *testing.T) {
	const oldTok, newTok = "old-token-aaaa", "new-token-bbbb"
	// During an overlap window the server accepts both, so clients can roll from
	// old to new with no downtime.
	front, _, _ := buildProxy(t, func(c *Config) { c.AuthTokens = []string{oldTok, newTok} })
	for _, tok := range []string{oldTok, newTok} {
		if code, _ := postBody(t, front, http.Header{"X-Leash-Token": {tok}}, `{"model":"gpt-4o"}`); code != http.StatusOK {
			t.Fatalf("token %q status = %d, want 200", tok, code)
		}
	}
	if code, _ := postBody(t, front, http.Header{"X-Leash-Token": {"retired-token"}}, `{"model":"gpt-4o"}`); code != http.StatusUnauthorized {
		t.Fatalf("a token not in the set was accepted, status = %d", code)
	}
}

func TestAuthOffAllowsNoToken(t *testing.T) {
	front, _, _ := buildProxy(t, nil) // no AuthToken
	code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when auth is off", code)
	}
}

func TestAuthTokenNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	front, _, _ := buildProxy(t, func(c *Config) {
		c.AuthTokens = []string{testToken}
		c.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	postBody(t, front, http.Header{"X-Leash-Token": {testToken}}, `{"model":"gpt-4o"}`)
	postBody(t, front, http.Header{"X-Leash-Token": {"wrong"}}, `{"model":"gpt-4o"}`)
	if strings.Contains(logs.String(), testToken) {
		t.Fatalf("auth token leaked into logs:\n%s", logs.String())
	}
}

func TestMaxRunsRefusesNewRunsAtCapacity(t *testing.T) {
	front, up, p := buildProxy(t, func(c *Config) { c.MaxRuns = 2 })
	hdr := func(id string) http.Header { return http.Header{"X-Loop-Id": {id}} }

	// Two distinct runs fit.
	for _, id := range []string{"run-a", "run-b"} {
		if code, _ := postBody(t, front, hdr(id), `{"model":"gpt-4o"}`); code != http.StatusOK {
			t.Fatalf("run %s status = %d, want 200", id, code)
		}
	}
	// A third distinct run is refused with 503.
	code, body := postBody(t, front, hdr("run-c"), `{"model":"gpt-4o"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("over-capacity run status = %d, want 503", code)
	}
	if !strings.Contains(body, "leash_gateway") {
		t.Fatalf("503 body missing leash_gateway shape: %s", body)
	}
	// An already-tracked run still works.
	if code, _ := postBody(t, front, hdr("run-a"), `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("existing run refused at capacity, status = %d", code)
	}
	if up.count() != 3 { // a, b, then a again; c never forwarded
		t.Fatalf("upstream saw %d calls, want 3", up.count())
	}
	if got := p.ActiveRuns(); got != 2 {
		t.Fatalf("ActiveRuns = %d, want 2 (capacity held)", got)
	}
}

func TestMetricsRequiresTokenWhenAuthOn(t *testing.T) {
	metrics := NewMetrics("v", policy.PriceTable{})
	_, _, p := buildProxy(t, nil)
	srv := NewAdminServer("", p.cfg.Ledger, p, metrics, []string{testToken}, nil)
	rec := func(h http.Header) int {
		r, _ := http.NewRequest(http.MethodGet, "/metrics", nil)
		maps.Copy(r.Header, h)
		w := &statusRecorder{header: http.Header{}}
		srv.Handler.ServeHTTP(w, r)
		return w.status
	}
	if s := rec(nil); s != http.StatusUnauthorized {
		t.Fatalf("/metrics without token = %d, want 401", s)
	}
	if s := rec(http.Header{"X-Leash-Token": {testToken}}); s != http.StatusOK {
		t.Fatalf("/metrics with token = %d, want 200", s)
	}
	// Health stays open so probes need no credential.
	rq, _ := http.NewRequest(http.MethodGet, "/healthz", nil)
	hw := &statusRecorder{header: http.Header{}}
	srv.Handler.ServeHTTP(hw, rq)
	if hw.status != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (unauthenticated)", hw.status)
	}
}

// statusRecorder captures the status code and discards the body.
type statusRecorder struct {
	header http.Header
	status int
}

func (s *statusRecorder) Header() http.Header { return s.header }
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return len(b), nil
}
func (s *statusRecorder) WriteHeader(code int) { s.status = code }
