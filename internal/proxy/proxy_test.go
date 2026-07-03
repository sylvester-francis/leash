package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// testClock is a controllable clock so time-based boundaries are deterministic.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordedReq captures what the upstream received.
type recordedReq struct {
	Path   string
	Header http.Header
	Body   []byte
}

// upstreamRecorder is a fake upstream that records requests and delegates to a
// handler for the response.
type upstreamRecorder struct {
	mu       sync.Mutex
	requests []recordedReq
	handler  http.HandlerFunc
}

func (u *upstreamRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.requests = append(u.requests, recordedReq{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	u.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	u.handler(w, r)
}

func (u *upstreamRecorder) last() recordedReq {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[len(u.requests)-1]
}

func (u *upstreamRecorder) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

type harness struct {
	t      *testing.T
	ledger *ledger.Ledger
	front  *httptest.Server
	up     *upstreamRecorder
	logs   *bytes.Buffer
	clock  *testClock
	limits policy.Limits
	prices policy.PriceTable
}

func newHarness(t *testing.T, limits policy.Limits, prices policy.PriceTable, inject bool, handler http.HandlerFunc) *harness {
	t.Helper()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	up := &upstreamRecorder{handler: handler}
	upSrv := httptest.NewServer(up)
	upURL, _ := url.Parse(upSrv.URL)
	logs := &bytes.Buffer{}
	clock := &testClock{t: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)}

	g := policy.NewGovernor(limits, prices, 0)
	p, err := New(Config{
		Ledger:   l,
		Governor: g,
		Upstream: upURL,
		Inject:   inject,
		Logger:   log.New(logs, "", 0),
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	front := httptest.NewServer(p)

	t.Cleanup(func() {
		front.Close()
		_ = p.Shutdown()
		upSrv.Close()
		_ = l.Close()
	})
	return &harness{t: t, ledger: l, front: front, up: up, logs: logs, clock: clock, limits: limits, prices: prices}
}

func (h *harness) do(path string, header http.Header, body string) (*http.Response, string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.front.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func (h *harness) loadState(runID string) *policy.State {
	g := policy.NewGovernor(h.limits, h.prices, 0)
	s, err := h.ledger.Load(context.Background(), runID, g)
	if err != nil {
		h.t.Fatalf("load state: %v", err)
	}
	return s
}

func decodeBoundary(t *testing.T, body string) boundaryBody {
	t.Helper()
	var b boundaryBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("decode boundary body %q: %v", body, err)
	}
	return b
}

// openAIJSON responds with a non-streaming OpenAI response of fixed usage.
func openAIJSON(model, content string, in, out int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"content":%q}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
			model, content, in, out)
	}
}

const chatPath = "/v1/chat/completions"

func TestMaxCallsTripsEndToEnd(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 2}, nil, false, openAIJSON("gpt-4o", "hi", 10, 5))

	for i := range 2 {
		resp, _ := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", i, resp.StatusCode)
		}
	}
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third call status = %d, want 429", resp.StatusCode)
	}
	b := decodeBoundary(t, body)
	if b.Error.Type != "leash_boundary" || b.Error.Reason != "max_calls" {
		t.Fatalf("boundary body = %+v, want type leash_boundary reason max_calls", b.Error)
	}
	if b.Error.Calls != 2 {
		t.Fatalf("calls = %d, want 2", b.Error.Calls)
	}
	if h.up.count() != 2 {
		t.Fatalf("upstream saw %d calls, want 2 (blocked call must not forward)", h.up.count())
	}
}

func TestCostBudgetTripsEndToEnd(t *testing.T) {
	prices := policy.PriceTable{"gpt-4o": {InputPerM: 3.0}} // $3 per million input tokens
	h := newHarness(t, policy.Limits{MaxCost: 5.0}, prices, false, openAIJSON("gpt-4o", "hi", 1_000_000, 0))

	// Each call costs $3; budget is $5, so the third call is refused.
	h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	b := decodeBoundary(t, body)
	if b.Error.Reason != "cost_budget" {
		t.Fatalf("reason = %q, want cost_budget", b.Error.Reason)
	}
	if b.Error.TokenCost < 5.999 || b.Error.TokenCost > 6.001 {
		t.Fatalf("token_cost = %v, want 6.00", b.Error.TokenCost)
	}
}

func TestDeadlineTripsEndToEnd(t *testing.T) {
	h := newHarness(t, policy.Limits{Deadline: 10 * time.Minute}, nil, false, openAIJSON("gpt-4o", "hi", 1, 1))

	h.do(chatPath, nil, `{"model":"gpt-4o"}`) // first call pins StartedAt
	h.clock.advance(11 * time.Minute)         // now past the deadline
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if decodeBoundary(t, body).Error.Reason != "deadline" {
		t.Fatalf("reason = %q, want deadline", decodeBoundary(t, body).Error.Reason)
	}
}

func TestRateLimitTripsEndToEnd(t *testing.T) {
	h := newHarness(t, policy.Limits{RateTokens: 100, RateWindow: time.Minute}, nil, false,
		openAIJSON("gpt-4o", "hi", 60, 0)) // 60 tokens per call

	h.do(chatPath, nil, `{"model":"gpt-4o"}`) // cumulative 60
	h.do(chatPath, nil, `{"model":"gpt-4o"}`) // cumulative 120, still under before this call
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if decodeBoundary(t, body).Error.Reason != "rate_limit" {
		t.Fatalf("reason = %q, want rate_limit", decodeBoundary(t, body).Error.Reason)
	}
}

func TestStallTripsEndToEnd(t *testing.T) {
	// Identical response content every call yields identical fingerprints.
	h := newHarness(t, policy.Limits{Stall: 2}, nil, false, openAIJSON("gpt-4o", "same answer", 1, 1))

	h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if decodeBoundary(t, body).Error.Reason != "stall" {
		t.Fatalf("reason = %q, want stall", decodeBoundary(t, body).Error.Reason)
	}
}

func TestKillSwitchTripsEndToEnd(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, false, openAIJSON("gpt-4o", "hi", 1, 1))

	// First call establishes the run under the default id.
	h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	// A durable kill written straight to the ledger (as `leash kill` would).
	if err := h.ledger.AppendKill(context.Background(), "default", h.clock.now()); err != nil {
		t.Fatalf("append kill: %v", err)
	}
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if decodeBoundary(t, body).Error.Reason != "kill_switch" {
		t.Fatalf("reason = %q, want kill_switch", decodeBoundary(t, body).Error.Reason)
	}
}

func TestStoppedRunKeepsReturningSameAnswer(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 1}, nil, false, openAIJSON("gpt-4o", "hi", 1, 1))
	h.do(chatPath, nil, `{"model":"gpt-4o"}`) // uses the one allowed call
	r1, b1 := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	r2, b2 := h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	if r1.StatusCode != http.StatusTooManyRequests || r2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d, %d, want both 429", r1.StatusCode, r2.StatusCode)
	}
	if b1 != b2 {
		t.Fatalf("stopped run gave different answers:\n%s\n%s", b1, b2)
	}
	if h.up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1", h.up.count())
	}
}

func TestOpenAINonStreamingUsageRecorded(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, false, openAIJSON("gpt-4o", "hi", 42, 7))
	h.do(chatPath, nil, `{"model":"gpt-4o"}`)
	s := h.loadState("default")
	if s.InputTokens != 42 || s.OutputTokens != 7 {
		t.Fatalf("recorded tokens = (%d,%d), want (42,7)", s.InputTokens, s.OutputTokens)
	}
}

const openAISSE = `data: {"model":"gpt-4o","choices":[{"delta":{"content":"Hel"}}]}

data: {"model":"gpt-4o","choices":[{"delta":{"content":"lo"}}]}

data: {"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}

data: [DONE]

`

func sseHandler(payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func TestOpenAIStreamingTeesUnmodifiedAndMeters(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, true, sseHandler(openAISSE))
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o","stream":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body != openAISSE {
		t.Fatalf("client stream was modified:\n got %q\nwant %q", body, openAISSE)
	}
	// Injection made the upstream request ask for usage.
	var reqObj map[string]any
	_ = json.Unmarshal(h.up.last().Body, &reqObj)
	opts, _ := reqObj["stream_options"].(map[string]any)
	if opts == nil || opts["include_usage"] != true {
		t.Fatalf("upstream request did not carry include_usage: %s", h.up.last().Body)
	}
	// Usage from the final chunk was recorded.
	s := h.loadState("default")
	if s.InputTokens != 11 || s.OutputTokens != 3 {
		t.Fatalf("streamed usage = (%d,%d), want (11,3)", s.InputTokens, s.OutputTokens)
	}
}

const openAISSENoUsage = `data: {"model":"gpt-4o","choices":[{"delta":{"content":"Hello"}}]}

data: [DONE]

`

func TestNoInjectBlindPath(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, false, sseHandler(openAISSENoUsage))
	resp, body := h.do(chatPath, nil, `{"model":"gpt-4o","stream":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body != openAISSENoUsage {
		t.Fatalf("client stream was modified on the blind path")
	}
	// With injection off, the upstream request must be untouched (no stream_options).
	if strings.Contains(string(h.up.last().Body), "stream_options") {
		t.Fatalf("--no-inject still injected stream_options: %s", h.up.last().Body)
	}
	// The call is recorded blind (zero tokens) and a warning is logged.
	s := h.loadState("default")
	if s.InputTokens != 0 || s.OutputTokens != 0 {
		t.Fatalf("blind call recorded nonzero tokens: %+v", s)
	}
	if !strings.Contains(h.logs.String(), "token meter blind") {
		t.Fatalf("expected a blind-meter warning in logs, got: %s", h.logs.String())
	}
}

const anthropicSSE = `event: message_start
data: {"type":"message_start","message":{"model":"claude-3-5-sonnet","usage":{"input_tokens":9,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`

func TestAnthropicStreamingMeters(t *testing.T) {
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, true, sseHandler(anthropicSSE))
	resp, body := h.do("/v1/messages", http.Header{"Anthropic-Version": {"2023-06-01"}}, `{"model":"claude-3-5-sonnet","stream":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body != anthropicSSE {
		t.Fatalf("anthropic stream was modified")
	}
	// Anthropic reports usage natively, so leash must NOT inject stream_options.
	if strings.Contains(string(h.up.last().Body), "stream_options") {
		t.Fatalf("leash injected stream_options into an Anthropic request")
	}
	s := h.loadState("default")
	if s.InputTokens != 9 || s.OutputTokens != 4 {
		t.Fatalf("anthropic streamed usage = (%d,%d), want (9,4)", s.InputTokens, s.OutputTokens)
	}
}

func TestSecretsForwardedButNeverLogged(t *testing.T) {
	const secret = "sk-super-secret-key-12345"
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, false, openAIJSON("gpt-4o", "hi", 1, 1))
	header := http.Header{
		"Authorization": {"Bearer " + secret},
		"X-Api-Key":     {secret},
		"X-Loop-Id":     {"secretrun"},
	}
	h.do(chatPath, header, `{"model":"gpt-4o"}`)

	// The upstream must receive the secret untouched.
	got := h.up.last()
	if got.Header.Get("Authorization") != "Bearer "+secret {
		t.Fatalf("Authorization not forwarded untouched: %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("X-Api-Key") != secret {
		t.Fatalf("X-Api-Key not forwarded untouched")
	}
	// The leash-internal routing header must NOT be forwarded upstream.
	if got.Header.Get("X-Loop-Id") != "" {
		t.Fatalf("X-Loop-Id leaked upstream")
	}
	// The secret must never appear in logs.
	if strings.Contains(h.logs.String(), secret) {
		t.Fatalf("secret leaked into logs: %s", h.logs.String())
	}
	// The run id from X-Loop-Id must have been used.
	runs, _ := h.ledger.Incomplete(context.Background())
	found := false
	for _, r := range runs {
		if r.ID == "secretrun" {
			found = true
		}
	}
	if !found {
		t.Fatalf("X-Loop-Id run id was not used; runs = %+v", runs)
	}
}

func TestSecretsNeverPersistedInLedger(t *testing.T) {
	const secret = "sk-persist-secret-98765"
	h := newHarness(t, policy.Limits{MaxCalls: 100}, nil, false, openAIJSON("gpt-4o", "hi", 1, 1))
	h.do(chatPath, http.Header{"Authorization": {"Bearer " + secret}}, `{"model":"gpt-4o","messages":[{"role":"user","content":"`+secret+`"}]}`)

	// Read the raw journal payloads back and assert no secret is present.
	logs, err := h.ledger.RawLogs(context.Background(), "default")
	if err != nil {
		t.Fatalf("raw logs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("expected at least one journal entry")
	}
	for _, l := range logs {
		if strings.Contains(string(l.Payload), secret) {
			t.Fatalf("secret persisted in journal payload: %s", l.Payload)
		}
	}
}
