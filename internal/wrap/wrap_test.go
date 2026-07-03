package wrap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/leash/internal/proxy"
)

// TestMain doubles as the stub child: when LEASH_WRAP_CHILD is set the test
// binary re-executes itself in that role instead of running the tests. This
// keeps the wrapper end-to-end test portable, with no external script.
func TestMain(m *testing.M) {
	switch os.Getenv("LEASH_WRAP_CHILD") {
	case "echoenv":
		fmt.Println(os.Getenv("OPENAI_BASE_URL"))
		fmt.Println(os.Getenv("OPENAI_API_BASE"))
		fmt.Println(os.Getenv("ANTHROPIC_BASE_URL"))
		os.Exit(0)
	case "exit7":
		os.Exit(7)
	case "loopcall":
		loopCall()
	default:
		os.Exit(m.Run())
	}
}

// loopCall makes model calls through the injected base URL until one is refused.
func loopCall() {
	base := os.Getenv("OPENAI_BASE_URL")
	for i := range 50 {
		resp, err := http.Post(base+"/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
		if err != nil {
			fmt.Fprintln(os.Stderr, "child error:", err)
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		fmt.Printf("call %d status %d\n", i, resp.StatusCode)
		if resp.StatusCode != http.StatusOK {
			os.Exit(0) // the child itself exits cleanly; leash decides the code
		}
	}
	os.Exit(0)
}

type wrapFixture struct {
	proxy    *proxy.Proxy
	ledger   *ledger.Ledger
	governor *policy.Governor
}

func newWrapFixture(t *testing.T, runID string, limits policy.Limits) *wrapFixture {
	t.Helper()
	db := filepath.Join(t.TempDir(), "leash.db")
	l, err := ledger.Open(db)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	upURL, _ := url.Parse(up.URL)
	g := policy.NewGovernor(limits, nil, 0)
	p, err := proxy.New(proxy.Config{Ledger: l, Governor: g, Upstream: upURL, DefaultRun: runID})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	t.Cleanup(func() {
		up.Close()
		_ = p.Shutdown()
		_ = l.Close()
	})
	return &wrapFixture{proxy: p, ledger: l, governor: g}
}

func TestInjectsProviderBaseURLs(t *testing.T) {
	f := newWrapFixture(t, "envrun", policy.Limits{MaxCalls: 100})
	var stdout bytes.Buffer
	res, err := Run(context.Background(), Options{
		Handler:  f.proxy,
		Ledger:   f.ledger,
		Governor: f.governor,
		RunID:    "envrun",
		Command:  []string{os.Args[0]},
		Env:      append(os.Environ(), "LEASH_WRAP_CHILD=echoenv"),
		Stdout:   &stdout,
		Stderr:   io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped || res.ExitCode != 0 {
		t.Fatalf("clean child: Stopped=%v ExitCode=%d, want false/0", res.Stopped, res.ExitCode)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 env lines, got %q", stdout.String())
	}
	openaiBase, openaiLegacy, anthropicBase := lines[0], lines[1], lines[2]
	if !strings.HasPrefix(openaiBase, "http://127.0.0.1:") || !strings.HasSuffix(openaiBase, "/v1") {
		t.Fatalf("OPENAI_BASE_URL = %q, want a localhost /v1 base", openaiBase)
	}
	if openaiLegacy != openaiBase {
		t.Fatalf("OPENAI_API_BASE = %q, want it to match OPENAI_BASE_URL", openaiLegacy)
	}
	if !strings.HasPrefix(anthropicBase, "http://127.0.0.1:") || strings.HasSuffix(anthropicBase, "/v1") {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want a localhost base without /v1", anthropicBase)
	}
}

func TestChildExitCodePassesThrough(t *testing.T) {
	f := newWrapFixture(t, "exitrun", policy.Limits{MaxCalls: 100})
	res, err := Run(context.Background(), Options{
		Handler:  f.proxy,
		Ledger:   f.ledger,
		Governor: f.governor,
		RunID:    "exitrun",
		Command:  []string{os.Args[0]},
		Env:      append(os.Environ(), "LEASH_WRAP_CHILD=exit7"),
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped {
		t.Fatalf("Stopped = true, want false for a clean child")
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7 (child's own code)", res.ExitCode)
	}
}

func TestWrapperEndToEndBoundaryExits3(t *testing.T) {
	f := newWrapFixture(t, "loop", policy.Limits{MaxCalls: 3})
	var stdout, stderr bytes.Buffer
	res, err := Run(context.Background(), Options{
		Handler:  f.proxy,
		Ledger:   f.ledger,
		Governor: f.governor,
		RunID:    "loop",
		Command:  []string{os.Args[0]},
		Env:      append(os.Environ(), "LEASH_WRAP_CHILD=loopcall"),
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Stopped {
		t.Fatalf("Stopped = false, want true (a boundary should have fired)")
	}
	if res.ExitCode != BoundaryExitCode {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, BoundaryExitCode)
	}
	// The child observed successful calls and then a refusal.
	if !strings.Contains(stdout.String(), "status 200") {
		t.Fatalf("child never saw a successful call:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "status 429") {
		t.Fatalf("child never saw the boundary refusal:\n%s", stdout.String())
	}
	// The exact stop line went to stderr.
	if !strings.Contains(stderr.String(), "leash: stopped run loop after 3 calls") {
		t.Fatalf("stop line missing from stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "(max_calls)") {
		t.Fatalf("stop line missing the boundary reason:\n%s", stderr.String())
	}
}
