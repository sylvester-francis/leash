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

// Package wrap is leash's Tier 1 front door: it launches a child process with
// its provider base URLs pointed at an embedded proxy, forwards the child's
// standard streams and signals, and after the child exits reports whether a
// boundary stopped the run. Zero code change to the agent: it just reads the
// base-url environment variables its SDK already honors.
package wrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/leash/internal/proxy"
)

// BoundaryExitCode is the exit status leash uses when a boundary stopped the
// run, regardless of the child's own exit code. It is documented so scripts can
// distinguish a governed stop from an ordinary child failure.
const BoundaryExitCode = 3

// serverShutdownTimeout bounds the graceful shutdown of the embedded proxy.
const serverShutdownTimeout = 5 * time.Second

// Options configures a wrapped run.
type Options struct {
	// Handler is the enforcement proxy, served on a local port for the child.
	Handler http.Handler
	// Ledger is queried after the child exits for the run's final state.
	Ledger *ledger.Ledger
	// Governor supplies prices and the compute rate for the final state.
	Governor *policy.Governor
	// RunID is the run the child's calls are governed under.
	RunID string
	// Command is the child command and its arguments.
	Command []string
	// Stdin, Stdout, Stderr are the child's streams; nil uses the process's own.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Env is the base environment; nil uses os.Environ. The provider base-url
	// variables are appended so they take effect.
	Env []string
}

// Result reports the outcome of a wrapped run.
type Result struct {
	// ExitCode is the status leash should exit with.
	ExitCode int
	// Stopped reports whether a boundary stopped the run.
	Stopped bool
	// State is the run's final folded state.
	State *policy.State
}

// Run launches the child under governance and blocks until it exits. It starts
// the embedded proxy on a free localhost port, injects the provider base URLs,
// forwards signals, waits for the child, then reads the run's final state to
// decide the exit code: BoundaryExitCode when a boundary stopped the run, the
// child's own code otherwise.
func Run(ctx context.Context, opts Options) (Result, error) {
	if len(opts.Command) == 0 {
		return Result{}, errors.New("wrap: no command to run")
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Result{}, fmt.Errorf("wrap: listen: %w", err)
	}
	baseURL := "http://" + ln.Addr().String()
	// Even the embedded, loopback-only server gets the request-hardening timeouts,
	// so the two server construction sites cannot drift; the empty address is
	// unused because it is driven by Serve on an existing listener.
	srv := proxy.HardenedServer("", opts.Handler)
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	baseEnv := opts.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}

	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	cmd.Env = injectBaseURLs(baseEnv, baseURL)
	cmd.Stdin = orStdin(opts.Stdin)
	cmd.Stdout = orStdout(opts.Stdout)
	cmd.Stderr = orStderr(opts.Stderr)

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("wrap: start %s: %w", opts.Command[0], err)
	}

	stopForwarding := forwardSignals(cmd)
	waitErr := cmd.Wait()
	stopForwarding()

	childCode, runErr := exitCodeFrom(waitErr)
	if runErr != nil {
		return Result{}, fmt.Errorf("wrap: wait for child: %w", runErr)
	}

	state, err := opts.Ledger.Load(context.Background(), opts.RunID, opts.Governor)
	if err != nil {
		return Result{}, fmt.Errorf("wrap: load final state: %w", err)
	}

	res := Result{State: state}
	if state.StopReason != "" {
		res.Stopped = true
		res.ExitCode = BoundaryExitCode
		fmt.Fprintln(stderr, policy.StopLine(state))
		return res, nil
	}

	// Clean completion: refresh the compute meter to the moment of exit, retire
	// the run, and print a completion summary.
	state.Refresh(time.Now(), opts.Governor.ComputeRate)
	if err := opts.Ledger.Finish(context.Background(), opts.RunID, true); err != nil {
		fmt.Fprintf(stderr, "leash: warning: could not retire run %s: %v\n", opts.RunID, err)
	}
	res.ExitCode = childCode
	fmt.Fprintf(stderr, "leash: run %s finished after %d calls, $%.2f tokens + $%.2f compute = $%.2f (child_exited)\n",
		state.RunID, state.Calls, state.TokenCost, state.ComputeCost, state.TotalCost)
	return res, nil
}

// injectBaseURLs appends the provider base-url variables so the child's SDKs
// send their traffic to leash. OpenAI's base includes /v1; Anthropic's does not
// (its SDK appends /v1/messages itself).
func injectBaseURLs(env []string, baseURL string) []string {
	out := make([]string, len(env), len(env)+3)
	copy(out, env)
	return append(out,
		"OPENAI_BASE_URL="+baseURL+"/v1",
		"OPENAI_API_BASE="+baseURL+"/v1",
		"ANTHROPIC_BASE_URL="+baseURL,
	)
}

// exitCodeFrom extracts the child's exit code. A process killed by a signal
// reports -1, which is mapped to 1. A non-exit error is a leash-side failure.
func exitCodeFrom(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code, nil
		}
		return 1, nil
	}
	return 0, err
}

func orStdin(r io.Reader) io.Reader {
	if r == nil {
		return os.Stdin
	}
	return r
}

func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

func orStderr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}
