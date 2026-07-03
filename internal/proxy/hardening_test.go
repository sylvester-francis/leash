package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// buildProxy stands up a Proxy over a temp ledger and a fake upstream, applying
// tweak to the Config before New. It returns a front server the test drives.
func buildProxy(t *testing.T, tweak func(*Config)) (*httptest.Server, *upstreamRecorder) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	up := &upstreamRecorder{handler: openAIJSON("gpt-4o", "hi", 1, 1)}
	upSrv := httptest.NewServer(up)
	upURL, _ := url.Parse(upSrv.URL)

	cfg := Config{
		Ledger:   l,
		Governor: policy.NewGovernor(policy.Limits{MaxCalls: 100}, nil, 0),
		Upstream: upURL,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	p, err := New(cfg)
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
	return front, up
}

// postBody posts a raw body with optional headers and returns status and body.
func postBody(t *testing.T, front *httptest.Server, header http.Header, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, front.URL+chatPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, string(out)
}

func TestMaxBodyBytesOverCapRefused413(t *testing.T) {
	front, up := buildProxy(t, func(c *Config) { c.MaxBodyBytes = 64 })
	big := `{"model":"gpt-4o","pad":"` + strings.Repeat("x", 200) + `"}`
	code, body := postBody(t, front, nil, big)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", code)
	}
	if !strings.Contains(body, "leash_gateway") {
		t.Fatalf("413 body missing leash_gateway shape: %s", body)
	}
	if up.count() != 0 {
		t.Fatalf("over-cap request forwarded upstream %d times, want 0", up.count())
	}
}

func TestMaxBodyBytesJustUnderCapSucceeds(t *testing.T) {
	body := `{"model":"gpt-4o"}` // 18 bytes
	front, up := buildProxy(t, func(c *Config) { c.MaxBodyBytes = int64(len(body)) })
	code, _ := postBody(t, front, nil, body)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a body at the cap", code)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1", up.count())
	}
}

func TestInvalidRunIDHeaderRefused400(t *testing.T) {
	front, up := buildProxy(t, nil)
	// A newline is rejected client- and server-side before it ever reaches the
	// handler, so the transmittable-but-invalid cases here are what the handler
	// itself must reject; ValidRunID's unit test covers newline rejection.
	// Transmittable-but-invalid header values (a newline is blocked before it
	// reaches the handler; non-ASCII rejection is covered by ValidRunID's test).
	for _, bad := range []string{"has space", "../traversal", "a;b", "a+b"} {
		code, body := postBody(t, front, http.Header{"X-Loop-Id": {bad}}, `{"model":"gpt-4o"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("X-Loop-Id %q status = %d, want 400", bad, code)
		}
		if !strings.Contains(body, "leash_gateway") {
			t.Fatalf("400 body missing leash_gateway shape: %s", body)
		}
	}
	if up.count() != 0 {
		t.Fatalf("invalid run ids forwarded upstream %d times, want 0", up.count())
	}
}

func TestValidRunIDHeaderAccepted(t *testing.T) {
	front, up := buildProxy(t, nil)
	code, _ := postBody(t, front, http.Header{"X-Loop-Id": {"good.run_1-2"}}, `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("valid run id status = %d, want 200", code)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1", up.count())
	}
}

func TestRequireRunIDRefusesUntagged(t *testing.T) {
	front, up := buildProxy(t, func(c *Config) { c.RequireRunID = true })
	// No X-Loop-Id: refused.
	code, body := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("untagged request status = %d, want 400 under --require-run-id", code)
	}
	if !strings.Contains(body, "leash_gateway") {
		t.Fatalf("body missing leash_gateway shape: %s", body)
	}
	if up.count() != 0 {
		t.Fatalf("untagged request forwarded, want refused")
	}
	// With a run id: allowed.
	code, _ = postBody(t, front, http.Header{"X-Loop-Id": {"tagged"}}, `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("tagged request status = %d, want 200", code)
	}
}

func TestRequireRunIDOffPoolsUntagged(t *testing.T) {
	front, _ := buildProxy(t, nil) // default: require-run-id off
	code, _ := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusOK {
		t.Fatalf("untagged request status = %d, want 200 when require-run-id is off", code)
	}
}

// panicRoundTripper panics on every request, standing in for an unexpected
// panic anywhere in the request path.
type panicRoundTripper struct{}

func (panicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("injected panic in request path")
}

func TestPanicRecoveryReturns500(t *testing.T) {
	front, _ := buildProxy(t, func(c *Config) {
		c.Client = &http.Client{Transport: panicRoundTripper{}}
	})
	code, body := postBody(t, front, nil, `{"model":"gpt-4o"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 after a recovered panic", code)
	}
	if !strings.Contains(body, "leash_gateway") {
		t.Fatalf("500 body missing leash_gateway shape: %s", body)
	}
	if strings.Contains(body, "injected panic") {
		t.Fatalf("500 body leaked the panic detail to the client: %s", body)
	}
}
