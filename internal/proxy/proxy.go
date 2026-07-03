// Package proxy is leash's enforcement engine: an HTTP reverse proxy that
// governs every model call. For each incoming request it resolves a run, folds
// that run's durable journal into state, evaluates the boundaries in a fixed
// order, and either refuses the call with a machine-readable 429 or forwards it
// upstream, streaming the response through untouched while metering usage on
// the side, then records the call in the ledger before the next one is judged.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/meter"
	"github.com/sylvester-francis/leash/internal/policy"
)

// governanceLeaseKey is the run id leash leases to claim governance of a ledger.
// The SQLite lease is store-wide, so a single key claims the whole database for
// this process; a distributed backend would lease per run.
const governanceLeaseKey = "leash-governor"

// defaultRunID is used when a request carries no X-Loop-Id and no wrapper
// default was configured.
const defaultRunID = "default"

// defaultMaxBodyBytes caps an incoming request body at 10 MiB when the caller
// does not set Config.MaxBodyBytes. The cap only guards the request read; a
// streamed response is never buffered and never capped.
const defaultMaxBodyBytes = 10 * 1024 * 1024

// Default upstreams inferred per provider when no --upstream override is set.
var (
	openAIUpstream    = &url.URL{Scheme: "https", Host: "api.openai.com"}
	anthropicUpstream = &url.URL{Scheme: "https", Host: "api.anthropic.com"}
)

// Config configures a Proxy. Governor and Ledger are required; the rest have
// safe defaults.
type Config struct {
	// Ledger is the durable account. Required.
	Ledger *ledger.Ledger
	// Governor holds the boundaries and meters. Required.
	Governor *policy.Governor
	// DefaultRun is the run id used when a request carries no X-Loop-Id. Empty
	// means fall back to "default".
	DefaultRun string
	// Upstream overrides per-provider inference; nil infers from the provider.
	Upstream *url.URL
	// Inject enables rewriting streaming OpenAI requests to ask for a usage
	// chunk (stream_options.include_usage). The CLI's --no-inject clears it.
	Inject bool
	// Client is the upstream HTTP client; nil builds a hardened client from
	// UpstreamHeaderTimeout with no overall timeout so long streams are not cut
	// off.
	Client *http.Client
	// UpstreamHeaderTimeout bounds how long the upstream may take to send
	// response headers on the default client (zero disables it). It is ignored
	// when Client is set explicitly.
	UpstreamHeaderTimeout time.Duration
	// MaxBodyBytes caps the request body read; zero uses defaultMaxBodyBytes. A
	// request over the cap is refused 413. It never touches response streaming.
	MaxBodyBytes int64
	// RequireRunID refuses requests that carry no X-Loop-Id with a 400 instead
	// of pooling them into the default run. It closes the shared-gateway footgun
	// where one stopped default run would 419 all untagged traffic forever.
	RequireRunID bool
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Logger receives redacted operational logs; nil discards them.
	Logger *log.Logger
	// OnStop, if set, is called once with the final state when a run stops. It
	// is the observer seam for the CLI to print the stop line.
	OnStop func(*policy.State)
}

// Proxy governs model calls for one ledger. It is safe for concurrent use:
// requests for the same run serialize, requests for different runs proceed in
// parallel.
type Proxy struct {
	cfg   Config
	lease *ledger.Lease

	mu          sync.Mutex
	runs        map[string]*runState
	warnedBlind map[string]bool
}

// runState serializes access to one run and remembers whether it has been
// created in the ledger yet.
type runState struct {
	mu      sync.Mutex
	ensured bool
}

// New builds a Proxy and claims the ledger's governance lease. It returns an
// error if the lease is already held in this process, which means another
// governor is running against the same ledger.
func New(cfg Config) (*Proxy, error) {
	if cfg.Governor == nil {
		return nil, errors.New("proxy: Governor is required")
	}
	if cfg.Ledger == nil {
		return nil, errors.New("proxy: Ledger is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		cfg.Client = newUpstreamClient(cfg.UpstreamHeaderTimeout)
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	p := &Proxy{
		cfg:         cfg,
		runs:        make(map[string]*runState),
		warnedBlind: make(map[string]bool),
	}
	lease, acquired, err := cfg.Ledger.Acquire(context.Background(), governanceLeaseKey)
	if err != nil {
		return nil, fmt.Errorf("proxy: acquire governance lease: %w", err)
	}
	if !acquired {
		return nil, errors.New("proxy: another governor already holds this ledger in this process")
	}
	p.lease = lease
	return p, nil
}

// Shutdown releases the governance lease. The Proxy must not be used after.
func (p *Proxy) Shutdown() error {
	if p.lease != nil {
		return p.lease.Release()
	}
	return nil
}

// ServeHTTP governs one request. It recovers from any panic in the request
// path (belt and braces: the path has no panics today) so a single bad request
// can never take the whole gateway down, logging the stack with no request data
// and returning a 500 leash_gateway error.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			// Log the panic and stack only; never the request, headers, or body.
			p.cfg.Logger.Printf("panic recovered in request path: %v\n%s", rec, debug.Stack())
			p.writeGatewayError(w, http.StatusInternalServerError, "internal error")
		}
	}()
	p.serve(w, r)
}

// serve governs one request without the panic guard.
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID, ok := p.resolveRunID(w, r)
	if !ok {
		return
	}
	provider := meter.DetectProvider(r.URL.Path, r.Header)

	rs := p.runStateFor(runID)
	rs.mu.Lock()
	defer rs.mu.Unlock()

	now := p.cfg.Now()
	if !rs.ensured {
		if err := p.cfg.Ledger.EnsureRun(ctx, runID, now); err != nil {
			p.writeGatewayError(w, http.StatusInternalServerError, "ledger unavailable")
			p.cfg.Logger.Printf("run %s: ensure failed: %v", runID, err)
			return
		}
		rs.ensured = true
	}

	state, err := p.cfg.Ledger.Load(ctx, runID, p.cfg.Governor)
	if err != nil {
		p.writeGatewayError(w, http.StatusInternalServerError, "ledger unavailable")
		p.cfg.Logger.Printf("run %s: load failed: %v", runID, err)
		return
	}

	// A run already stopped stays stopped: every later call gets the same answer.
	if state.StopReason != "" {
		writeBoundary(w, state)
		return
	}

	if reason, tripped := p.cfg.Governor.Evaluate(state, now); tripped {
		state.StopReason = reason
		if err := p.cfg.Ledger.AppendStop(ctx, runID, state, now); err != nil {
			p.cfg.Logger.Printf("run %s: record stop failed: %v", runID, err)
		}
		if p.cfg.OnStop != nil {
			p.cfg.OnStop(state)
		}
		writeBoundary(w, state)
		return
	}

	p.forward(ctx, w, r, runID, provider, state)
}

// forward sends the request upstream, streams the response to the client, meters
// usage, and records the call in the ledger.
func (p *Proxy) forward(ctx context.Context, w http.ResponseWriter, r *http.Request, runID string, provider meter.Provider, state *policy.State) {
	base := p.upstreamFor(provider)
	if base == nil {
		p.writeGatewayError(w, http.StatusBadGateway,
			"leash could not determine an upstream for this endpoint; set --upstream")
		p.cfg.Logger.Printf("run %s: no upstream for path %s", runID, r.URL.Path)
		return
	}

	// Cap the request read so a hostile or buggy client cannot exhaust memory.
	// MaxBytesReader guards only this read; the response stream below is never
	// wrapped and never buffered.
	r.Body = http.MaxBytesReader(w, r.Body, p.cfg.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			p.writeGatewayError(w, http.StatusRequestEntityTooLarge,
				"request body exceeds the --max-body-bytes limit")
			p.cfg.Logger.Printf("run %s: request body over the %d byte cap", runID, p.cfg.MaxBodyBytes)
			return
		}
		p.writeGatewayError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if p.cfg.Inject && provider == meter.OpenAI {
		if out, changed, injErr := meter.InjectIncludeUsage(body); injErr != nil {
			p.cfg.Logger.Printf("run %s: include_usage injection skipped: %v", runID, injErr)
		} else if changed {
			body = out
		}
	}

	outReq, err := p.buildUpstreamRequest(ctx, r, base, body)
	if err != nil {
		p.writeGatewayError(w, http.StatusInternalServerError, "could not build upstream request")
		p.cfg.Logger.Printf("run %s: build upstream request: %v", runID, err)
		return
	}

	resp, err := p.cfg.Client.Do(outReq)
	if err != nil {
		p.writeGatewayError(w, http.StatusBadGateway, "upstream request failed")
		p.cfg.Logger.Printf("run %s: upstream error: %v", runID, err)
		return
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	var result meter.Result
	if meter.IsSSE(resp.Header.Get("Content-Type")) {
		sm := meter.NewStreamMeter(provider)
		if teeErr := sm.Tee(newFlushWriter(w), resp.Body); teeErr != nil {
			p.cfg.Logger.Printf("run %s: stream tee error: %v", runID, teeErr)
		}
		result = sm.Result()
	} else {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			p.cfg.Logger.Printf("run %s: read response error: %v", runID, readErr)
		}
		if _, wErr := w.Write(respBody); wErr != nil {
			p.cfg.Logger.Printf("run %s: write response error: %v", runID, wErr)
		}
		result, err = meter.ParseUsageJSON(provider, respBody)
		if err != nil {
			p.cfg.Logger.Printf("run %s: parse usage error: %v", runID, err)
		}
	}

	// Record the call after the response is delivered. A crash before this point
	// undercounts by one call (safe) but can never double count.
	at := p.cfg.Now()
	rec := policy.CallRecord{Usage: result.Usage, Fingerprint: result.Fingerprint, At: at}
	if appendErr := p.cfg.Ledger.AppendCall(ctx, runID, state.Calls, rec); appendErr != nil {
		p.cfg.Logger.Printf("run %s: record call failed: %v", runID, appendErr)
	}
	if !result.HasUsage && provider != meter.Unknown {
		p.warnBlind(runID)
	}
}

// runStateFor returns the per-run serializer, creating it on first touch.
func (p *Proxy) runStateFor(runID string) *runState {
	p.mu.Lock()
	defer p.mu.Unlock()
	rs, ok := p.runs[runID]
	if !ok {
		rs = &runState{}
		p.runs[runID] = rs
	}
	return rs
}

// warnBlind logs the blind-token-meter warning once per run.
func (p *Proxy) warnBlind(runID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warnedBlind[runID] {
		return
	}
	p.warnedBlind[runID] = true
	p.cfg.Logger.Printf("run %s: token meter blind (no usage on the wire); relying on other boundaries", runID)
}

// upstreamFor returns the base URL for a provider, preferring the override.
func (p *Proxy) upstreamFor(provider meter.Provider) *url.URL {
	if p.cfg.Upstream != nil {
		return p.cfg.Upstream
	}
	switch provider {
	case meter.OpenAI:
		return openAIUpstream
	case meter.Anthropic:
		return anthropicUpstream
	default:
		return nil
	}
}

// buildUpstreamRequest constructs the outbound request: rebased URL, forwarded
// headers (secrets untouched, hop-by-hop and leash-internal headers dropped),
// and a fresh Content-Length for the possibly-rewritten body.
func (p *Proxy) buildUpstreamRequest(ctx context.Context, r *http.Request, base *url.URL, body []byte) (*http.Request, error) {
	target := *base
	target.Path = singleJoiningSlash(base.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyRequestHeader(req.Header, r.Header)
	req.ContentLength = int64(len(body))
	return req, nil
}

// resolveRunID resolves the run from the request, validating any client-supplied
// id at the door. A present but malformed X-Loop-Id is refused 400 (this also
// blocks log injection via header newlines). A missing X-Loop-Id under
// RequireRunID is refused 400; otherwise it falls back to the wrapper default or
// "default", both of which are already well formed. It returns ok false when it
// has written an error and the caller must stop.
func (p *Proxy) resolveRunID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if id := r.Header.Get("X-Loop-Id"); id != "" {
		if !policy.ValidRunID(id) {
			p.writeGatewayError(w, http.StatusBadRequest, "invalid X-Loop-Id run id")
			return "", false
		}
		return id, true
	}
	if p.cfg.RequireRunID {
		p.writeGatewayError(w, http.StatusBadRequest,
			"missing X-Loop-Id and this gateway requires one (--require-run-id)")
		return "", false
	}
	if p.cfg.DefaultRun != "" {
		return p.cfg.DefaultRun, true
	}
	return defaultRunID, true
}

// boundaryBody is the 429 JSON leash returns when a boundary trips.
type boundaryBody struct {
	Error struct {
		Type        string  `json:"type"`
		Reason      string  `json:"reason"`
		Run         string  `json:"run"`
		Calls       int64   `json:"calls"`
		TokenCost   float64 `json:"token_cost"`
		ComputeCost float64 `json:"compute_cost"`
		TotalCost   float64 `json:"total_cost"`
	} `json:"error"`
}

// writeBoundary writes the machine-readable 429 for a stopped run.
func writeBoundary(w http.ResponseWriter, s *policy.State) {
	var b boundaryBody
	b.Error.Type = "leash_boundary"
	b.Error.Reason = s.StopReason
	b.Error.Run = s.RunID
	b.Error.Calls = s.Calls
	b.Error.TokenCost = round2(s.TokenCost)
	b.Error.ComputeCost = round2(s.ComputeCost)
	b.Error.TotalCost = round2(s.TotalCost)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(b)
}

// writeGatewayError writes a small JSON error for proxy-level failures.
func (p *Proxy) writeGatewayError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"type": "leash_gateway", "message": msg},
	})
}

// round2 rounds a dollar amount to the cent for reporting.
func round2(x float64) float64 {
	return math.Round(x*100) / 100
}
