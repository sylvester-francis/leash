package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// postCall makes one governed call to a front server and returns its status.
func postCall(t *testing.T, front *httptest.Server) int {
	t.Helper()
	resp, err := http.Post(front.URL+chatPath, "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("post call: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestCrashResumePreservesBudgetNoDoubleCount(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")

	up := &upstreamRecorder{handler: openAIJSON("gpt-4o", "hi", 100, 50)}
	upSrv := httptest.NewServer(up)
	defer upSrv.Close()
	upURL, _ := url.Parse(upSrv.URL)

	// Process 1: two calls allowed, the third trips max_calls and records a stop.
	l1, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger 1: %v", err)
	}
	g1 := policy.NewGovernor(policy.Limits{MaxCalls: 2}, nil, 0)
	p1, err := New(Config{Ledger: l1, Governor: g1, Upstream: upURL})
	if err != nil {
		t.Fatalf("new proxy 1: %v", err)
	}
	front1 := httptest.NewServer(p1)

	if postCall(t, front1) != http.StatusOK {
		t.Fatalf("first call should pass")
	}
	if postCall(t, front1) != http.StatusOK {
		t.Fatalf("second call should pass")
	}
	if postCall(t, front1) != http.StatusTooManyRequests {
		t.Fatalf("third call should trip max_calls")
	}
	// Simulate an unclean crash: abandon the proxy and ledger without shutdown.
	front1.Close()

	// Process 2: a fresh ledger and proxy over the same database.
	l2, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	defer l2.Close()
	g2 := policy.NewGovernor(policy.Limits{MaxCalls: 2}, nil, 0)
	p2, err := New(Config{Ledger: l2, Governor: g2, Upstream: upURL})
	if err != nil {
		t.Fatalf("new proxy 2 over same db: %v", err)
	}
	defer p2.Shutdown()
	front2 := httptest.NewServer(p2)
	defer front2.Close()

	// The over-budget run stays stopped after the restart.
	if code := postCall(t, front2); code != http.StatusTooManyRequests {
		t.Fatalf("resumed run should stay stopped, got status %d", code)
	}

	// Totals are intact and nothing was double counted.
	s, err := l2.Load(ctx, "default", g2)
	if err != nil {
		t.Fatalf("load resumed state: %v", err)
	}
	if s.Calls != 2 {
		t.Fatalf("Calls = %d, want 2 (a restart must not double count)", s.Calls)
	}
	if s.StopReason != "max_calls" {
		t.Fatalf("StopReason = %q, want max_calls", s.StopReason)
	}
	if up.count() != 2 {
		t.Fatalf("upstream forwarded %d calls, want 2 (blocked calls must never forward)", up.count())
	}
}

func TestKillFromSecondHandleStopsGovernor(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")

	up := &upstreamRecorder{handler: openAIJSON("gpt-4o", "hi", 1, 1)}
	upSrv := httptest.NewServer(up)
	defer upSrv.Close()
	upURL, _ := url.Parse(upSrv.URL)

	l1, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer l1.Close()
	g := policy.NewGovernor(policy.Limits{MaxCalls: 100}, nil, 0)
	p1, err := New(Config{Ledger: l1, Governor: g, Upstream: upURL})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	defer p1.Shutdown()
	front := httptest.NewServer(p1)
	defer front.Close()

	if postCall(t, front) != http.StatusOK {
		t.Fatalf("first call should pass")
	}

	// A second ledger handle stands in for a separate `leash kill` process.
	killer, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open killer ledger: %v", err)
	}
	if err := killer.AppendKill(ctx, "default", time.Now()); err != nil {
		t.Fatalf("append kill: %v", err)
	}
	_ = killer.Close()

	if postCall(t, front) != http.StatusTooManyRequests {
		t.Fatalf("governor did not observe the kill written by a second process")
	}
	s, _ := l1.Load(ctx, "default", g)
	if s.StopReason != "kill_switch" {
		t.Fatalf("StopReason = %q, want kill_switch", s.StopReason)
	}
}
