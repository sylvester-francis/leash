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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// faultLedger wraps a real ledger and injects durable-write failures on a
// schedule, so a test can break the ledger at any boundary. When the disk is
// "full", both the append and the write probe (Ping) fail, as they would on a
// real full or read-only disk.
type faultLedger struct {
	*ledger.Ledger
	mu   sync.Mutex
	full bool
}

func (f *faultLedger) setFull(v bool) { f.mu.Lock(); f.full = v; f.mu.Unlock() }
func (f *faultLedger) isFull() bool   { f.mu.Lock(); defer f.mu.Unlock(); return f.full }

func (f *faultLedger) AppendCallAt(ctx context.Context, runID string, i int64, rec policy.CallRecord, hint int) (int, error) {
	if f.isFull() {
		return 0, errors.New("injected: no space left on device")
	}
	return f.Ledger.AppendCallAt(ctx, runID, i, rec, hint)
}

func (f *faultLedger) Ping(ctx context.Context) error {
	if f.isFull() {
		return errors.New("injected: no space left on device")
	}
	return f.Ledger.Ping(ctx)
}

func buildFaultProxy(t *testing.T) (*httptest.Server, *upstreamRecorder, *faultLedger) {
	t.Helper()
	real, err := ledger.Open(filepath.Join(t.TempDir(), "leash.db"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	fl := &faultLedger{Ledger: real}
	up := &upstreamRecorder{handler: openAIJSON("gpt-4o", "hi", 1, 1)}
	upSrv := httptest.NewServer(up)
	upURL, _ := url.Parse(upSrv.URL)
	p, err := New(Config{
		Ledger:   fl,
		Governor: policy.NewGovernor(policy.Limits{MaxCalls: 100}, nil, 0),
		Upstream: upURL,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(func() { front.Close(); _ = p.Shutdown(); upSrv.Close(); _ = real.Close() })
	return front, up, fl
}

// TestFaultInjectionFailsClosedAtAnyBoundary drives a write outage that starts at
// a different call each time and asserts the invariant holds regardless of when
// it hits: at most one call slips through unmetered (the one whose response was
// already delivered when the write failed), and every later call is refused 503
// until the disk recovers, after which the run resumes.
func TestFaultInjectionFailsClosedAtAnyBoundary(t *testing.T) {
	for _, outageAt := range []int{1, 2, 5} {
		t.Run("outage_starts_at_call_"+string(rune('0'+outageAt)), func(t *testing.T) {
			front, up, fl := buildFaultProxy(t)

			// Healthy calls before the outage.
			for i := 1; i < outageAt; i++ {
				if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
					t.Fatalf("pre-outage call %d = %d, want 200", i, code)
				}
			}

			// The disk fills. The next call is delivered (already billed) but its
			// record cannot be written, flagging the run.
			fl.setFull(true)
			if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
				t.Fatalf("first call during outage = %d, want 200 (already billed)", code)
			}
			forwardsAtOutage := up.count()

			// Every subsequent call fails closed while the disk stays full.
			for range 3 {
				if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusServiceUnavailable {
					t.Fatalf("call during outage = %d, want 503 (fail closed)", code)
				}
			}
			if up.count() != forwardsAtOutage {
				t.Fatalf("forwarded %d unmetered calls during the outage, want at most 1",
					up.count()-forwardsAtOutage+1)
			}

			// The disk recovers; the run resumes.
			fl.setFull(false)
			if code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`); code != http.StatusOK {
				t.Fatalf("post-recovery call = %d, want 200", code)
			}
		})
	}
}
