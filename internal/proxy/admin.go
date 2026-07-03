package proxy

import (
	"context"
	"io"
	"net/http"
	"time"
)

// readyTimeout bounds the ledger probe behind /readyz: readiness must be a
// quick, cheap check, not a place a slow database can hang a load balancer.
const readyTimeout = time.Second

// LedgerPinger is the readiness dependency: a cheap durable read that succeeds
// when the ledger is reachable. *ledger.Ledger satisfies it.
type LedgerPinger interface {
	Ping(ctx context.Context) error
}

// ActiveRunsSource supplies the live active-run count for the metrics gauge.
// *Proxy satisfies it.
type ActiveRunsSource interface {
	ActiveRuns() int
}

// NewAdminServer builds the admin HTTP server: GET /healthz (liveness, always
// 200), GET /readyz (200 when a ledger probe succeeds within readyTimeout, else
// 503), and GET /metrics (Prometheus text exposition). It is a separate server
// from the proxy listener so it never collides with proxied API paths and can be
// placed on its own network segment. metrics may be nil, in which case /metrics
// returns an empty body.
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
		// The exposition version is part of the historical Prometheus text
		// format contract; scrapers key off it.
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if metrics != nil {
			metrics.WriteTo(w, active.ActiveRuns())
		}
	})

	return HardenedServer(addr, mux)
}
