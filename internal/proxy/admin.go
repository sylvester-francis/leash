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
	"io"
	"net/http"
	"time"
)

// readyTimeout bounds the /readyz ledger probe.
const readyTimeout = time.Second

// LedgerPinger is the readiness dependency; *ledger.Ledger satisfies it.
type LedgerPinger interface {
	Ping(ctx context.Context) error
}

// ActiveRunsSource supplies the active-run gauge; *Proxy satisfies it.
type ActiveRunsSource interface {
	ActiveRuns() int
}

// NewAdminServer builds the admin server: GET /healthz (always 200), /readyz
// (200 when the ledger probe succeeds within readyTimeout, else 503), and
// /metrics. It is separate from the proxy listener so it never collides with
// proxied paths and can be network-segmented. metrics may be nil.
func NewAdminServer(addr string, pinger LedgerPinger, active ActiveRunsSource, metrics *Metrics) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := pinger.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "not ready\n")
			return
		}
		_, _ = io.WriteString(w, "ready\n")
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// version=0.0.4 is the Prometheus text-format contract scrapers key off.
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if metrics != nil {
			metrics.WriteTo(w, active.ActiveRuns())
		}
	})

	return HardenedServer(addr, mux)
}
