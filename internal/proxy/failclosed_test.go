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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// flakyLedger wraps a real ledger and can be made to fail its writes while its
// reads keep working - the disk-full / read-only-remount failure mode.
type flakyLedger struct {
	*ledger.Ledger
	failWrites atomic.Bool
}

func (f *flakyLedger) AppendCallAt(ctx context.Context, runID string, i int64, rec policy.CallRecord, hint int) (int, error) {
	if f.failWrites.Load() {
		return 0, errors.New("no space left on device")
	}
	return f.Ledger.AppendCallAt(ctx, runID, i, rec, hint)
}

func (f *flakyLedger) Ping(ctx context.Context) error {
	if f.failWrites.Load() {
		return errors.New("no space left on device")
	}
	return f.Ledger.Ping(ctx)
}

func TestLedgerWriteFailureFailsClosed(t *testing.T) {
	db := filepath.Join(t.TempDir(), "leash.db")
	real, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	fl := &flakyLedger{Ledger: real}
	up := &upstreamRecorder{handler: openAIJSON("gpt-4o", "hi", 1, 1)}
	upSrv := httptest.NewServer(up)
	upURL, _ := url.Parse(upSrv.URL)
	metrics := NewMetrics("t", nil)
	p, err := New(Config{
		Ledger:   fl,
		Governor: policy.NewGovernor(policy.Limits{MaxCalls: 100}, nil, 0),
		Upstream: upURL,
		Observer: metrics,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(func() {
		front.Close()
		_ = p.Shutdown()
		upSrv.Close()
		_ = real.Close()
	})

	// Writes start failing (reads still work).
	fl.failWrites.Store(true)

	// Call 1: forwards (already billed), but recording fails -> the run is flagged.
	if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("call 1 status = %d, want 200 (upstream already billed)", code)
	}
	// Call 2: the write probe still fails, so leash refuses to forward again.
	code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("call 2 status = %d, want 503 (fail closed on write outage)", code)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1 (only the first forwarded)", up.count())
	}

	// The ledger recovers; the next call proceeds and records.
	fl.failWrites.Store(false)
	if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("call 3 after recovery status = %d, want 200", code)
	}
	if up.count() != 2 {
		t.Fatalf("upstream saw %d calls, want 2 after recovery", up.count())
	}

	var out bytes.Buffer
	metrics.WriteTo(&out, 0)
	if !strings.Contains(out.String(), "leash_ledger_errors_total") ||
		strings.Contains(out.String(), "leash_ledger_errors_total 0\n") {
		t.Fatalf("expected a nonzero leash_ledger_errors_total:\n%s", out.String())
	}
}

// failingPinger reports the ledger as unwritable.
type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("no space left on device") }

func TestReadyzFailsWhenLedgerUnwritable(t *testing.T) {
	_, _, p := buildProxy(t, nil)
	srv := NewAdminServer("", failingPinger{}, p, nil, nil)
	rec := &statusRecorder{header: http.Header{}}
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	srv.Handler.ServeHTTP(rec, req)
	if rec.status != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503 when the ledger cannot be written", rec.status)
	}
}
