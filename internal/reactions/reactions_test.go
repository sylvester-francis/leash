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

package reactions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/rerun"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fastPolicy() rerun.RetryPolicy {
	return rerun.RetryPolicy{MaxAttempts: 100, Backoff: rerun.FixedBackoff(100 * time.Millisecond)}
}

func closeDispatcher(d *Dispatcher) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = d.Close(ctx)
}

func TestEventID(t *testing.T) {
	if got := (Event{Kind: "stopped", Run: "r1"}).id(); got != "stop:r1" {
		t.Fatalf("stop id = %q, want stop:r1", got)
	}
	if got := (Event{Kind: "warning", Run: "r1"}).id(); got != "warn:r1" {
		t.Fatalf("warn id = %q, want warn:r1", got)
	}
}

func TestReactionDeliversWebhook(t *testing.T) {
	got := make(chan Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		select {
		case got <- ev:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, err := NewDispatcher(Config{DSN: filepath.Join(t.TempDir(), "r.db"), WebhookURL: srv.URL, Logger: silentLogger(), RetryPolicy: fastPolicy()})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer closeDispatcher(d)

	d.RunStopped(&policy.State{RunID: "run-1", StopReason: "cost_budget", Calls: 7, TotalCost: 5.01})
	select {
	case ev := <-got:
		if ev.Run != "run-1" || ev.Kind != "stopped" || ev.Reason != "cost_budget" || ev.Calls != 7 {
			t.Fatalf("webhook got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not called within 5s")
	}
}

func TestReactionDedupByEventID(t *testing.T) {
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, err := NewDispatcher(Config{DSN: filepath.Join(t.TempDir(), "r.db"), WebhookURL: srv.URL, Logger: silentLogger(), RetryPolicy: fastPolicy()})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer closeDispatcher(d)

	// The same stop enqueued three times shares one event id, so it fires once.
	st := &policy.State{RunID: "run-9", StopReason: "cost_budget"}
	d.RunStopped(st)
	d.RunStopped(st)
	d.RunStopped(st)
	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	c := count
	mu.Unlock()
	if c != 1 {
		t.Fatalf("webhook called %d times, want 1 (dedup by event id)", c)
	}
}

// TestEnqueueDropsWhenFull proves the enforcement-path guarantee directly: an
// enqueue onto a full queue returns at once rather than blocking the caller.
func TestEnqueueDropsWhenFull(t *testing.T) {
	d := &Dispatcher{ch: make(chan Event, 1), logger: silentLogger(), now: time.Now}
	d.ch <- Event{} // fill the single slot

	done := make(chan struct{})
	go func() {
		d.RunStopped(&policy.State{RunID: "x", StopReason: "cost_budget"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunStopped blocked on a full queue; the enforcement path must never wait")
	}
}

// TestReactionRecoverResumesAfterRestart is the durability crux: a reaction left
// in flight when a process dies is resumed by a fresh dispatcher over the same
// store, and delivered. Dispatcher A's webhook always fails, so the reaction is
// mid-retry when A is shut down; dispatcher B, with a working webhook, recovers
// and delivers it.
func TestReactionRecoverResumesAfterRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "r.db")

	aHit := make(chan struct{}, 16)
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case aHit <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	dA, err := NewDispatcher(Config{DSN: db, WebhookURL: srvA.URL, Logger: silentLogger(), RetryPolicy: fastPolicy()})
	if err != nil {
		t.Fatalf("dispatcher A: %v", err)
	}
	dA.RunStopped(&policy.State{RunID: "run-42", StopReason: "cost_budget", Calls: 3, TotalCost: 9.0})

	// Wait until A has attempted at least once: the run exists and is mid-retry.
	select {
	case <-aHit:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher A never attempted the webhook")
	}
	srvA.Close()        // A's endpoint is now dead; the reaction keeps failing
	closeDispatcher(dA) // park the in-flight reaction for recovery

	// Dispatcher B over the same store, with a working webhook.
	bHit := make(chan Event, 1)
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		select {
		case bHit <- ev:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	dB, err := NewDispatcher(Config{DSN: db, WebhookURL: srvB.URL, Logger: silentLogger(), RetryPolicy: fastPolicy()})
	if err != nil {
		t.Fatalf("dispatcher B: %v", err)
	}
	defer closeDispatcher(dB)
	if err := dB.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	select {
	case ev := <-bHit:
		if ev.Run != "run-42" {
			t.Fatalf("recovered reaction run=%q, want run-42", ev.Run)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the recovered reaction never reached the working webhook")
	}
}
