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
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/meter"
	"github.com/sylvester-francis/leash/internal/policy"
)

// governanceLeaseKey is the run id leash leases to claim governance of a ledger.
// The SQLite lease is store-wide, so a single key claims the whole database for
// this process; a distributed backend would lease per run.
const governanceLeaseKey = "leash-governor"

// ErrGovernorHeld is returned by New when the ledger's governance lease is
// already held, meaning another governor is running against the same ledger.
// serve --standby waits on this error and retries until the lease frees.
var ErrGovernorHeld = errors.New("proxy: another governor already holds this ledger")

// defaultRunID is used when a request carries no X-Loop-Id and no wrapper
// default was configured.
const defaultRunID = "default"

// authHeader carries the shared-secret credential when Config.AuthToken is set.
// It is leash-internal and never forwarded upstream.
const authHeader = "X-Leash-Token"

// blindStopReason is the stop reason recorded when a call cannot be metered
// under a cost budget and Config.OnBlind is BlindRefuse.
const blindStopReason = "meter_blind"

// unpricedToolStopReason is recorded when a call billed provider-side tool
// requests (e.g. web search) that leash cannot price from the token table, so
// spend went uncounted. Governed by the same --on-blind policy as blindStopReason.
const unpricedToolStopReason = "server_tool_unpriced"

// BlindPolicy decides what leash does when it cannot meter a call's cost (an
// unrecognized provider, or a response whose usage it cannot read) while a cost
// budget is active. The zero value, BlindRefuse, fails closed.
type BlindPolicy int

const (
	// BlindRefuse fails closed: an unmeterable endpoint is refused before it is
	// forwarded, and a run whose forwarded call comes back unmeterable is stopped
	// so no further spend goes uncounted.
	BlindRefuse BlindPolicy = iota
	// BlindWarn forwards the call and warns once per run (the pre-v0.2 behavior).
	BlindWarn
	// BlindAllow forwards the call silently.
	BlindAllow
)

// defaultMaxBodyBytes caps a request body at 10 MiB when Config.MaxBodyBytes is
// unset. It guards the request read only; responses are never capped.
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
	Ledger Ledger
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
	// MaxBodyBytes caps the request body read (zero uses defaultMaxBodyBytes);
	// over-cap requests get 413. It never touches response streaming.
	MaxBodyBytes int64
	// RequireRunID refuses untagged requests with 400 instead of pooling them
	// into the default run.
	RequireRunID bool
	// AuthTokens, when non-empty, requires every request to present an
	// X-Leash-Token header matching one of them; an empty slice disables
	// authentication. Configuring more than one allows zero-downtime rotation:
	// accept the old and new token during an overlap, roll clients over, then
	// drop the old one.
	AuthTokens []string
	// MaxRuns caps the number of runs the proxy will track in memory at once;
	// zero is unlimited. A request for a new run beyond the cap is refused 503.
	MaxRuns int
	// OnBlind decides how leash handles a call it cannot meter while a cost
	// budget is active. The zero value (BlindRefuse) fails closed.
	OnBlind BlindPolicy
	// MaxCostPerCall caps a single call's token cost; zero disables it. A call
	// over the cap stops the run so a runaway large call cannot repeat. Because
	// metering is post-response, the overshooting call itself still happens.
	MaxCostPerCall float64
	// WarnAt fires a one-time BudgetWarning per run per budget when utilization
	// reaches this fraction (e.g. 0.8 for 80%). Zero disables warnings. It is a
	// soft signal only; enforcement is unchanged.
	WarnAt float64
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Logger receives redacted structured logs; nil discards them. Header values
	// and bodies are never logged, at any level.
	Logger *slog.Logger
	// Observer receives governance events (forwarded, refused, stopped, upstream
	// error); nil installs NopObserver. It subsumes the former OnStop callback.
	Observer Observer
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

	// authDigests holds the SHA-256 of each configured token; authOn gates their
	// use. Hashing both sides gives a constant-length, constant-time comparison.
	authDigests [][32]byte
	authOn      bool

	// stopSweep stops the idle-run eviction goroutine on Shutdown.
	stopSweep chan struct{}
	sweepOnce sync.Once
}

// runState serializes access to one run and caches its folded state (the warm
// path). state is nil until the first touch cold-loads it. The eviction fields
// are atomics so the sweeper can read them without taking mu (avoiding a
// lock-order inversion with warnBlind).
type runState struct {
	mu      sync.Mutex
	ensured bool
	state   *policy.State
	lastSeq int // highest journal seq known; -1 until cold-loaded

	lastActiveNanos atomic.Int64
	stopped         atomic.Bool
	// appendFailed is set when a durable write for this run failed; the next call
	// is refused (fail closed) until a write probe succeeds.
	appendFailed atomic.Bool
	// warned records which budgets have already fired a warning for this run, so
	// each fires at most once. Guarded by the run's mu.
	warned map[string]bool
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
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Observer == nil {
		cfg.Observer = NopObserver{}
	}
	p := &Proxy{
		cfg:         cfg,
		runs:        make(map[string]*runState),
		warnedBlind: make(map[string]bool),
		stopSweep:   make(chan struct{}),
	}
	for _, tok := range cfg.AuthTokens {
		if tok != "" {
			p.authDigests = append(p.authDigests, sha256.Sum256([]byte(tok)))
		}
	}
	p.authOn = len(p.authDigests) > 0
	lease, acquired, err := cfg.Ledger.Acquire(context.Background(), governanceLeaseKey)
	if err != nil {
		return nil, fmt.Errorf("proxy: acquire governance lease: %w", err)
	}
	if !acquired {
		return nil, ErrGovernorHeld
	}
	p.lease = lease
	go p.sweepLoop()
	return p, nil
}

// Shutdown releases the governance lease and stops the eviction sweeper. The
// Proxy must not be used after.
func (p *Proxy) Shutdown() error {
	p.sweepOnce.Do(func() { close(p.stopSweep) })
	if p.lease != nil {
		return p.lease.Release()
	}
	return nil
}

// ServeHTTP governs one request. It stamps a request id (echoed as X-Request-Id
// and logged), meters latency and status for the metrics, and recovers any panic
// in the request path into a 500 (logging the stack with no request data) so one
// bad request cannot take the gateway down.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := p.cfg.Now()
	reqID := requestID(r)
	w.Header().Set("X-Request-Id", reqID)
	rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	p.cfg.Observer.RequestStarted()
	defer func() {
		if rec := recover(); rec != nil {
			// Log the panic and stack only; never the request, headers, or body.
			p.cfg.Logger.Error("panic recovered in request path",
				"panic", rec, "stack", string(debug.Stack()), "request_id", reqID)
			p.writeGatewayError(rw, http.StatusInternalServerError, "internal error")
		}
		dur := p.cfg.Now().Sub(start)
		p.cfg.Observer.RequestFinished(rw.status, dur)
		p.cfg.Logger.Debug("request served", "method", r.Method, "path", r.URL.Path,
			"status", rw.status, "duration_ms", dur.Milliseconds(), "request_id", reqID)
	}()
	p.serve(rw, r)
}

// statusWriter wraps a ResponseWriter to capture the response status for the
// metrics while preserving streaming: it forwards Flush so SSE responses still
// stream through untouched. The status defaults to 200 when WriteHeader is never
// called explicitly.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestID echoes a safe incoming X-Request-Id or mints a fresh random one, so
// a client can correlate its request with leash's logs and metrics.
func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); validRequestID(id) {
		return id
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// validRequestID accepts a bounded, printable token so a reflected id cannot
// inject into logs or response headers.
func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// serve governs one request without the panic guard.
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		p.writeGatewayError(w, http.StatusUnauthorized, "missing or invalid credential")
		return
	}
	ctx := r.Context()
	runID, ok := p.resolveRunID(w, r)
	if !ok {
		return
	}
	// Scope the run to the caller's credential so one tenant cannot touch or read
	// another tenant's run by naming its id.
	runID = namespaceRun(p.tenantKey(r), runID)
	provider := meter.DetectProvider(r.URL.Path, r.Header)

	rs, ok := p.runStateFor(runID)
	if !ok {
		p.writeGatewayError(w, http.StatusServiceUnavailable, "run capacity reached; the governor is at its --max-runs limit")
		p.cfg.Observer.CallRefused(provider, "capacity")
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()

	now := p.cfg.Now()
	rs.lastActiveNanos.Store(now.UnixNano())
	if !rs.ensured {
		if err := p.cfg.Ledger.EnsureRun(ctx, runID, now); err != nil {
			p.writeGatewayError(w, http.StatusInternalServerError, "ledger unavailable")
			p.cfg.Logger.Error("ensure run failed", "run", runID, "err", err)
			return
		}
		rs.ensured = true
	}

	state, err := p.stateFor(ctx, rs, runID)
	if err != nil {
		p.writeGatewayError(w, http.StatusInternalServerError, "ledger unavailable")
		p.cfg.Logger.Error("load run failed", "run", runID, "err", err)
		return
	}

	// A run already stopped stays stopped: every later call gets the same answer.
	if state.StopReason != "" {
		rs.stopped.Store(true)
		p.cfg.Observer.CallRefused(provider, state.StopReason)
		writeBoundary(w, state)
		return
	}

	if reason, tripped := p.cfg.Governor.Evaluate(state, now); tripped {
		// The rate limit is backpressure, not a terminal stop: refuse this call
		// with a Retry-After and leave the run running, so it resumes once the
		// trailing window decays. Every other boundary stops the run for good.
		if reason == policy.ReasonRateLimit {
			if secs := int(p.cfg.Governor.RateWindow().Seconds()); secs > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(secs))
			}
			p.cfg.Observer.CallRefused(provider, reason)
			writeBoundaryStatus(w, state, reason, http.StatusTooManyRequests)
			return
		}
		state.StopReason = reason
		rs.stopped.Store(true)
		if err := p.cfg.Ledger.AppendStop(ctx, runID, state, now); err != nil {
			p.cfg.Logger.Error("record stop failed", "run", runID, "err", err)
			p.cfg.Observer.LedgerError()
		}
		p.cfg.Observer.RunStopped(state)
		p.cfg.Observer.CallRefused(provider, reason)
		p.cfg.Logger.Info("run stopped", "run", runID, "reason", reason, "calls", state.Calls)
		writeBoundary(w, state)
		return
	}

	// Fail closed on a run whose durable writes are failing: a prior call could
	// not be recorded, so refuse to forward (and bill) another unmetered one
	// until a write probe confirms the ledger recovered.
	if rs.appendFailed.Load() {
		if err := p.cfg.Ledger.Ping(ctx); err != nil {
			p.writeGatewayError(w, http.StatusServiceUnavailable,
				"ledger write is failing; refusing to forward an unmetered call")
			p.cfg.Observer.CallRefused(provider, "ledger_unavailable")
			p.cfg.Observer.LedgerError()
			p.cfg.Logger.Error("refusing to forward: ledger writes failing", "run", runID, "err", err)
			return
		}
		rs.appendFailed.Store(false)
	}

	// Fail closed on an endpoint leash cannot meter under a cost budget: an
	// Unknown provider is refused before it is forwarded, so unpriced spend never
	// slips past the cost boundary. (A known provider that returns unreadable
	// usage is caught after the fact in forward, which then stops the run.)
	if provider == meter.Unknown && p.blindRefuses() {
		p.writeGatewayError(w, http.StatusPaymentRequired,
			"leash cannot meter this endpoint under a cost budget (--on-blind=refuse)")
		p.cfg.Observer.CallRefused(provider, blindStopReason)
		p.cfg.Logger.Warn("refused unmeterable endpoint under a cost budget", "run", runID, "path", r.URL.Path)
		return
	}

	p.forward(ctx, w, r, runID, provider, rs)
}

// blindRefuses reports whether an unmeterable call should fail closed: the
// policy is BlindRefuse and a cost budget is actually active.
func (p *Proxy) blindRefuses() bool {
	return p.cfg.OnBlind == BlindRefuse && p.cfg.Governor.MetersCost()
}

// stateFor returns the run's folded state. On the cold path (first touch or
// after eviction) it folds the whole journal once and caches it. On the warm
// path it reuses the cache and only checks the durable cancel flag (an O(1)
// indexed read) to catch a kill, folding new calls in as they happen. When the
// store cannot serve the flag, or the probe fails, it falls back to a full
// reload. Correct because one governor owns the ledger: no other writer appends
// calls out of band, and kills also set the flag.
func (p *Proxy) stateFor(ctx context.Context, rs *runState, runID string) (*policy.State, error) {
	if rs.state == nil {
		return p.coldLoad(ctx, rs, runID)
	}
	s := rs.state
	if s.StopReason == "" && !s.Killed {
		requested, supported, err := p.cfg.Ledger.CancelRequested(ctx, runID)
		switch {
		case !supported || err != nil:
			return p.coldLoad(ctx, rs, runID)
		case requested:
			s.Killed = true
		}
	}
	return s, nil
}

// coldLoad folds the whole journal and seeds the cache and append hint.
func (p *Proxy) coldLoad(ctx context.Context, rs *runState, runID string) (*policy.State, error) {
	s, lastSeq, err := p.cfg.Ledger.LoadAt(ctx, runID, p.cfg.Governor)
	if err != nil {
		return nil, err
	}
	rs.state = s
	rs.lastSeq = lastSeq
	return s, nil
}

// forward sends the request upstream, streams the response to the client, meters
// usage, and records the call in the ledger.
func (p *Proxy) forward(ctx context.Context, w http.ResponseWriter, r *http.Request, runID string, provider meter.Provider, rs *runState) {
	state := rs.state
	base := p.upstreamFor(provider)
	if base == nil {
		p.writeGatewayError(w, http.StatusBadGateway,
			"leash could not determine an upstream for this endpoint; set --upstream")
		p.cfg.Logger.Error("no upstream for endpoint", "run", runID, "path", r.URL.Path)
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
			p.cfg.Logger.Warn("request body over cap", "run", runID, "cap_bytes", p.cfg.MaxBodyBytes)
			return
		}
		p.writeGatewayError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if p.cfg.Inject && provider == meter.OpenAI {
		if out, changed, injErr := meter.InjectIncludeUsage(body); injErr != nil {
			p.cfg.Logger.Debug("include_usage injection skipped", "run", runID, "err", injErr)
		} else if changed {
			body = out
		}
	}

	outReq, err := p.buildUpstreamRequest(ctx, r, base, body)
	if err != nil {
		p.writeGatewayError(w, http.StatusInternalServerError, "could not build upstream request")
		p.cfg.Logger.Error("build upstream request failed", "run", runID, "err", err)
		return
	}

	resp, err := p.cfg.Client.Do(outReq)
	if err != nil {
		p.writeGatewayError(w, http.StatusBadGateway, "upstream request failed")
		p.cfg.Logger.Error("upstream request failed", "run", runID, "err", err)
		p.cfg.Observer.UpstreamError()
		return
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	var result meter.Result
	if meter.IsStreamed(resp.Header.Get("Content-Type")) {
		sm := meter.NewStreamMeter(provider)
		if teeErr := sm.Tee(newFlushWriter(w), resp.Body); teeErr != nil {
			p.cfg.Logger.Error("stream tee error", "run", runID, "err", teeErr)
			p.cfg.Observer.UpstreamError()
		}
		result = sm.Result()
	} else {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			p.cfg.Logger.Error("read response error", "run", runID, "err", readErr)
			p.cfg.Observer.UpstreamError()
		}
		if _, wErr := w.Write(respBody); wErr != nil {
			p.cfg.Logger.Error("write response error", "run", runID, "err", wErr)
		}
		result, err = meter.ParseUsageJSON(provider, respBody)
		if err != nil {
			p.cfg.Logger.Warn("parse usage error", "run", runID, "err", err)
		}
	}

	// Record the call after the response is delivered. A crash before this point
	// undercounts by one call (safe) but can never double count. Record with a
	// detached context: the upstream already billed this call, so a client that
	// disconnects mid-stream must not cancel the ledger write.
	at := p.cfg.Now()
	recCtx := context.WithoutCancel(ctx)
	rec := policy.CallRecord{Usage: result.Usage, Fingerprint: result.Fingerprint, At: at}
	if usedSeq, appendErr := p.cfg.Ledger.AppendCallAt(recCtx, runID, state.Calls, rec, rs.lastSeq+1); appendErr != nil {
		// The call could not be recorded. Flag the run so the next call fails
		// closed rather than forwarding more unmetered spend.
		p.cfg.Logger.Error("record call failed", "run", runID, "err", appendErr)
		rs.appendFailed.Store(true)
		p.cfg.Observer.LedgerError()
	} else {
		// Fold into the warm cache and advance the seq hint so the next call needs
		// neither a reload nor a re-read. state is the cached pointer, so the cache
		// stays equal to a cold fold of the journal.
		rs.lastSeq = usedSeq
		rs.appendFailed.Store(false)
		p.cfg.Governor.Fold(state, rec)
	}

	blind := !result.HasUsage
	// Provider-side tool requests (e.g. web search) are per-request charges. When
	// the table prices them, TokenCost includes them; when it does not, they are
	// billed spend leash cannot account for, handled under the same --on-blind
	// policy as a blind meter.
	unpriced := policy.UnpricedToolActivity(result.Usage, p.cfg.Governor.Prices)
	callCost := policy.TokenCost(result.Usage, p.cfg.Governor.Prices)
	switch {
	case p.cfg.MaxCostPerCall > 0 && callCost > p.cfg.MaxCostPerCall && state.StopReason == "":
		// A single call exceeded the per-call cap. The call already happened;
		// stop the run so it cannot repeat.
		p.stopRun(recCtx, rs, runID, state, "max_cost_per_call", at)
	case blind && p.blindRefuses() && state.StopReason == "":
		// Fail closed: a forwarded call under a cost budget came back unmeterable.
		// The response already reached the client, so stop the run to bound the
		// damage - no further spend goes uncounted.
		p.stopRun(recCtx, rs, runID, state, blindStopReason, at)
	case blind && p.cfg.OnBlind != BlindAllow:
		p.warnBlind(runID)
	case unpriced && p.blindRefuses() && state.StopReason == "":
		// Fail closed: the call billed server-side tool requests leash cannot
		// price, so spend went uncounted. Stop the run, same as a blind meter.
		p.stopRun(recCtx, rs, runID, state, unpricedToolStopReason, at)
	case unpriced && p.cfg.OnBlind != BlindAllow:
		p.warnUnpricedTool(runID, result.Usage.ServerToolRequests())
	}
	if state.StopReason == "" {
		p.maybeWarn(rs, state)
	}
	p.cfg.Observer.CallForwarded(provider, result.Usage, blind)
}

// maybeWarn fires a one-time BudgetWarning per run per budget once utilization
// crosses Config.WarnAt. It is a soft signal - it never stops the run. The
// caller holds the run's mu, so rs.warned needs no further locking.
func (p *Proxy) maybeWarn(rs *runState, state *policy.State) {
	if p.cfg.WarnAt <= 0 {
		return
	}
	for _, st := range p.cfg.Governor.BudgetStatuses(state) {
		if st.Fraction < p.cfg.WarnAt || rs.warned[st.Reason] {
			continue
		}
		if rs.warned == nil {
			rs.warned = map[string]bool{}
		}
		rs.warned[st.Reason] = true
		p.cfg.Observer.BudgetWarning(state, st)
		p.cfg.Logger.Warn("run approaching budget", "run", state.RunID, "budget", st.Reason,
			"used", st.Used, "limit", st.Limit, "fraction", st.Fraction)
	}
}

// stopRun records a post-forward stop (a blind or per-call-cost stop) with the
// given reason and notifies observers.
func (p *Proxy) stopRun(ctx context.Context, rs *runState, runID string, state *policy.State, reason string, at time.Time) {
	state.StopReason = reason
	rs.stopped.Store(true)
	if err := p.cfg.Ledger.AppendStop(ctx, runID, state, at); err != nil {
		p.cfg.Logger.Error("record stop failed", "run", runID, "err", err)
		p.cfg.Observer.LedgerError()
	}
	p.cfg.Observer.RunStopped(state)
	p.cfg.Logger.Warn("run stopped", "run", runID, "reason", reason, "calls", state.Calls)
}

// authorized reports whether a request carries a valid credential. It compares
// the SHA-256 of the presented token against every configured token in constant
// time, without early return, so timing reveals neither which token matched nor
// how many are configured. It always returns true when authentication is off.
func (p *Proxy) authorized(r *http.Request) bool {
	if !p.authOn {
		return true
	}
	got := sha256.Sum256([]byte(r.Header.Get(authHeader)))
	match := 0
	for i := range p.authDigests {
		match |= subtle.ConstantTimeCompare(got[:], p.authDigests[i][:])
	}
	return match == 1
}

// tenantKey derives a stable, opaque namespace from the caller's credential, so
// the same run id presented with two different tokens maps to two isolated runs.
// It is empty when auth is off (a single shared namespace). Call only after
// authorized has passed; the token itself is never logged or stored.
func (p *Proxy) tenantKey(r *http.Request) string {
	if !p.authOn {
		return ""
	}
	sum := sha256.Sum256([]byte(r.Header.Get(authHeader)))
	return hex.EncodeToString(sum[:4])
}

// namespaceRun scopes a client run id to its tenant. With auth off (empty
// tenant) the id is unchanged, preserving single-tenant behavior.
func namespaceRun(tenant, runID string) string {
	if tenant == "" {
		return runID
	}
	return tenant + "-" + runID
}

// runStateFor returns the per-run serializer, creating it on first touch. ok is
// false when a new run would exceed MaxRuns; an already-tracked run is always
// returned so its durable answer (a stop, a kill) is never shed.
func (p *Proxy) runStateFor(runID string) (*runState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if rs, ok := p.runs[runID]; ok {
		return rs, true
	}
	if p.cfg.MaxRuns > 0 && len(p.runs) >= p.cfg.MaxRuns {
		return nil, false
	}
	rs := &runState{}
	p.runs[runID] = rs
	return rs, true
}

// Eviction bounds the per-run memory a long-lived serve holds.
const (
	evictionIdleWindow    = 10 * time.Minute // stopped-and-idle before eviction
	evictionSweepInterval = time.Minute
)

// sweepLoop evicts idle runs on a fixed interval until Shutdown.
func (p *Proxy) sweepLoop() {
	t := time.NewTicker(evictionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stopSweep:
			return
		case <-t.C:
			p.evictIdle(p.cfg.Now())
		}
	}
}

// evictIdle drops in-memory entries for any run idle at least
// evictionIdleWindow, stopped or not, returning the count. Evicting a still-open
// run is safe because the journal is the source of truth: its next call
// cold-reloads and folds to exactly the same state. This bounds memory to the
// set of recently active runs, so a flood of distinct run ids (or a rotated
// X-Loop-Id) cannot grow the map without limit.
func (p *Proxy) evictIdle(now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	evicted := 0
	for id, rs := range p.runs {
		if now.Sub(time.Unix(0, rs.lastActiveNanos.Load())) >= evictionIdleWindow {
			delete(p.runs, id)
			delete(p.warnedBlind, id)
			evicted++
		}
	}
	if evicted > 0 {
		p.cfg.Logger.Debug("evicted idle runs from memory", "count", evicted)
	}
	return evicted
}

// ActiveRuns reports the in-memory runs that are not stopped, backing the
// leash_active_runs gauge.
func (p *Proxy) ActiveRuns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, rs := range p.runs {
		if !rs.stopped.Load() {
			n++
		}
	}
	return n
}

// warnBlind logs the blind-token-meter warning once per run.
func (p *Proxy) warnBlind(runID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warnedBlind[runID] {
		return
	}
	p.warnedBlind[runID] = true
	p.cfg.Logger.Warn("token meter blind (no usage on the wire); relying on other boundaries", "run", runID)
}

// warnUnpricedTool logs, once per run, that a call billed server-side tool
// requests leash cannot price, so its spend is under-counted. The "tool:" prefix
// keeps it distinct from the blind warning in the same per-run map.
func (p *Proxy) warnUnpricedTool(runID string, requests int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := "tool:" + runID
	if p.warnedBlind[key] {
		return
	}
	p.warnedBlind[key] = true
	p.cfg.Logger.Warn("call billed server-side tool requests leash cannot price; spend under-counted",
		"run", runID, "requests", requests)
}

// upstreamFor returns the base URL for a provider, preferring the override.
// Ollama has no default upstream; pass --upstream with a host:port to point
// leash at a running Ollama instance.
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

// resolveRunID resolves and validates the run for a request. A malformed
// X-Loop-Id is refused 400; a missing one under RequireRunID is refused 400;
// otherwise it falls back to the wrapper default or "default". ok is false when
// an error was written and the caller must stop.
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
	writeBoundaryStatus(w, s, s.StopReason, http.StatusTooManyRequests)
}

// writeBoundaryStatus writes the boundary body with an explicit reason and
// status. It backs the transient rate-limit refusal, where the run is not
// stopped (State.StopReason is empty) but the call is still refused.
func writeBoundaryStatus(w http.ResponseWriter, s *policy.State, reason string, status int) {
	var b boundaryBody
	b.Error.Type = "leash_boundary"
	b.Error.Reason = reason
	b.Error.Run = s.RunID
	b.Error.Calls = s.Calls
	b.Error.TokenCost = round2(s.TokenCost)
	b.Error.ComputeCost = round2(s.ComputeCost)
	b.Error.TotalCost = round2(s.TotalCost)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}

// gatewayBody is the JSON leash returns for a proxy-level failure (413, 400,
// 5xx), mirroring boundaryBody's shape.
type gatewayBody struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeGatewayError writes a small JSON error for proxy-level failures.
func (p *Proxy) writeGatewayError(w http.ResponseWriter, status int, msg string) {
	var b gatewayBody
	b.Error.Type = "leash_gateway"
	b.Error.Message = msg

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}

// round2 rounds a dollar amount to the cent for reporting.
func round2(x float64) float64 {
	return math.Round(x*100) / 100
}
