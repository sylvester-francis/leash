package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/leash/internal/proxy"
)

// TestStandbyTakeover exercises active/passive HA in one process over a single
// sqlite ledger handle, whose lease has the same one-at-a-time acquire/release
// semantics as the postgres advisory lock. Instance A governs; instance B waits
// in standby; when A steps down, B acquires and serves.
func TestStandbyTakeover(t *testing.T) {
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer l.Close()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	cfg := func() proxy.Config {
		return proxy.Config{
			Ledger:   l,
			Governor: policy.NewGovernor(policy.Limits{MaxCalls: 100}, nil, 0),
			Upstream: upURL,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Instance A acquires the lease.
	a, err := acquireProxy(cfg(), false, 10*time.Millisecond, logger)
	if err != nil {
		t.Fatalf("A acquire: %v", err)
	}

	// Instance B, in standby, must block until A steps down.
	bCh := make(chan *proxy.Proxy, 1)
	go func() {
		b, err := acquireProxy(cfg(), true, 10*time.Millisecond, logger)
		if err != nil {
			t.Errorf("B acquire: %v", err)
			bCh <- nil
			return
		}
		bCh <- b
	}()

	select {
	case <-bCh:
		t.Fatalf("B acquired the lease while A still held it")
	case <-time.After(150 * time.Millisecond):
		// Expected: B is still standing by.
	}

	// A steps down; B must take over.
	if err := a.Shutdown(); err != nil {
		t.Fatalf("A shutdown: %v", err)
	}
	var b *proxy.Proxy
	select {
	case b = <-bCh:
		if b == nil {
			t.Fatalf("B failed to acquire after takeover")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("B did not take over within 2s of A stepping down")
	}
	defer b.Shutdown()

	// B now governs: a call through B is served.
	front := httptest.NewServer(b)
	defer front.Close()
	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("call through B: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("B serve status = %d, want 200", resp.StatusCode)
	}
}
