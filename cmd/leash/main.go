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

// Command leash puts a durable spend governor in front of an AI agent. It has
// three surfaces over one engine: `leash -- <command>` wraps an agent with zero
// code change, `leash serve` runs a standalone gateway, and `leash ps`,
// `leash inspect`, and `leash kill` operate on the durable ledger.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

// version is the leash build version. It is "dev" for an unstamped local build
// and is set at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch routes to a subcommand. Anything that is not an explicit subcommand
// is treated as a wrapper invocation: leash [flags] -- <command> [args...].
func dispatch(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "ps":
		return cmdPs(args[1:])
	case "inspect":
		return cmdInspect(args[1:])
	case "kill":
		return cmdKill(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "version", "--version":
		return cmdVersion()
	case "gen-token":
		return cmdGenToken()
	case "healthcheck":
		return cmdHealthcheck(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		return cmdRun(args)
	}
}

// cmdVersion prints the build version, Go version, and platform on one line, for
// example "leash v0.1.0 go1.25.0 linux/amd64".
func cmdVersion() int {
	fmt.Printf("leash %s %s %s/%s\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return 0
}

// cmdGenToken prints a cryptographically strong token for --auth-token, so an
// operator does not have to invent one. Feed it to the server as LEASH_AUTH_TOKEN
// and to clients as the X-Leash-Token header.
func cmdGenToken() int {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		fmt.Fprintf(os.Stderr, "leash: generate token: %v\n", err)
		return 1
	}
	fmt.Println(hex.EncodeToString(b[:]))
	return 0
}

// cmdHealthcheck probes a URL and exits 0 on a 2xx, 1 otherwise. It gives a
// distroless image - which has no shell or curl - a usable HEALTHCHECK against
// the admin listener's /healthz.
func cmdHealthcheck(args []string) int {
	fs := flag.NewFlagSet("leash healthcheck", flag.ContinueOnError)
	url := fs.String("url", envStr("LEASH_HEALTHCHECK_URL", "http://127.0.0.1:9090/healthz"),
		"URL to probe (typically the admin listener's /healthz)")
	timeout := fs.Duration("timeout", 2*time.Second, "probe timeout")
	setUsage(fs, "leash healthcheck - probe a health URL for container HEALTHCHECK.",
		"leash healthcheck [--url URL]",
		"leash healthcheck --url http://127.0.0.1:9090/healthz")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: healthcheck %s: %v\n", *url, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0
	}
	fmt.Fprintf(os.Stderr, "leash: healthcheck %s: status %d\n", *url, resp.StatusCode)
	return 1
}

// usage prints the top-level help.
func usage(w *os.File) {
	fmt.Fprint(w, `leash - durable agent spend governor

Usage:
  leash [flags] -- <command> [args...]   wrap an agent (Tier 1)
  leash serve --listen :8088 [flags]     standalone gateway (Tier 2)
  leash ps [--json]                      list runs from the ledger
  leash inspect [--json] <run>           show one run's folded journal
  leash kill <run>                       durably stop a run on its next call
  leash version                          print the build version
  leash gen-token                        print a random token for --auth-token

Flags are per-subcommand (pass -h after a subcommand); see the README for the full list.
Every shared flag also reads a LEASH_-prefixed environment variable (for example
--max-cost reads LEASH_MAX_COST); an explicit flag beats the environment.
`)
}

// commonFlags are the governance flags shared by run and serve (and reused,
// where they make sense, by ps and inspect).
type commonFlags struct {
	maxCost               float64
	maxCalls              int64
	deadline              time.Duration
	rate                  string
	stall                 int
	prices                string
	computeRate           float64
	upstream              string
	db                    string
	run                   string
	noInject              bool
	maxBodyBytes          int64
	upstreamHeaderTimeout time.Duration
	logLevel              string
	logFormat             string
	onBlind               string
	maxCostPerCall        float64
}

// registerCommon binds the shared flags with their documented defaults.
func registerCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.Float64Var(&c.maxCost, "max-cost", envFloat("LEASH_MAX_COST", 5.00), "dollar budget over token + compute cost (0 disables)")
	fs.Float64Var(&c.maxCostPerCall, "max-cost-per-call", envFloat("LEASH_MAX_COST_PER_CALL", 0), "dollar cap on a single call's token cost; over it stops the run (0 disables)")
	fs.Int64Var(&c.maxCalls, "max-calls", envInt64("LEASH_MAX_CALLS", 100), "maximum governed calls (0 disables)")
	fs.DurationVar(&c.deadline, "deadline", envDuration("LEASH_DEADLINE", 30*time.Minute), "wall-clock budget from the first call (0 disables)")
	fs.StringVar(&c.rate, "rate", envStr("LEASH_RATE", ""), "trailing token rate as tokens/window, e.g. 100000/1m (empty disables)")
	fs.IntVar(&c.stall, "stall", envInt("LEASH_STALL", 0), "consecutive identical responses tolerated (0 disables)")
	fs.StringVar(&c.prices, "prices", envStr("LEASH_PRICES", ""), "path to a JSON price table (model -> input/output/reasoning per million)")
	fs.Float64Var(&c.computeRate, "compute-rate", envFloat("LEASH_COMPUTE_RATE", 0), "compute meter in dollars per hour")
	fs.StringVar(&c.upstream, "upstream", envStr("LEASH_UPSTREAM", ""), "upstream base URL override (empty infers per provider)")
	fs.StringVar(&c.db, "db", envStr("LEASH_DB", defaultDBPath()), "ledger database path")
	fs.StringVar(&c.run, "run", envStr("LEASH_RUN", ""), "run name; reusing it on a later invocation resumes that budget")
	fs.BoolVar(&c.noInject, "no-inject", envBool("LEASH_NO_INJECT", false), "do not add stream_options.include_usage to streaming requests")
	fs.Int64Var(&c.maxBodyBytes, "max-body-bytes", envInt64("LEASH_MAX_BODY_BYTES", 10485760), "cap on an incoming request body in bytes (10 MiB default)")
	fs.DurationVar(&c.upstreamHeaderTimeout, "upstream-header-timeout", envDuration("LEASH_UPSTREAM_HEADER_TIMEOUT", 5*time.Minute),
		"how long the upstream may take to send response headers (0 disables; the body stream is never capped)")
	fs.StringVar(&c.logLevel, "log-level", envStr("LEASH_LOG_LEVEL", "info"), "log level: debug, info, warn, or error")
	fs.StringVar(&c.logFormat, "log-format", envStr("LEASH_LOG_FORMAT", "text"), "log format: text or json")
	fs.StringVar(&c.onBlind, "on-blind", envStr("LEASH_ON_BLIND", "refuse"),
		"when a call can't be metered under a cost budget: refuse (fail closed), warn, or allow")
	return c
}

// defaultDBPath is $HOME/.leash/leash.db, or leash.db when there is no home.
func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "leash.db"
	}
	return filepath.Join(home, ".leash", "leash.db")
}

// parseRate parses a "tokens/window" rate limit. An empty string disables it.
func parseRate(s string) (tokens int64, window time.Duration, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("rate %q must be tokens/window, e.g. 100000/1m", s)
	}
	tokens, err = strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("rate tokens %q: %w", parts[0], err)
	}
	window, err = time.ParseDuration(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("rate window %q: %w", parts[1], err)
	}
	if window <= 0 {
		return 0, 0, fmt.Errorf("rate window %q must be positive", parts[1])
	}
	return tokens, window, nil
}

// loadPrices reads a price table from the flag path, or nil when unset.
func loadPrices(path string) (policy.PriceTable, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open price table: %w", err)
	}
	defer f.Close()
	table, err := policy.LoadPriceTable(f)
	if err != nil {
		return nil, err
	}
	return table, nil
}

// buildGovernor assembles the limits, prices, and governor from common flags.
func buildGovernor(c *commonFlags) (*policy.Governor, policy.Limits, policy.PriceTable, error) {
	prices, err := loadPrices(c.prices)
	if err != nil {
		return nil, policy.Limits{}, nil, err
	}
	rateTokens, rateWindow, err := parseRate(c.rate)
	if err != nil {
		return nil, policy.Limits{}, nil, err
	}
	limits := policy.Limits{
		MaxCost:    c.maxCost,
		MaxCalls:   c.maxCalls,
		Deadline:   c.deadline,
		RateTokens: rateTokens,
		RateWindow: rateWindow,
		Stall:      c.stall,
	}
	return policy.NewGovernor(limits, prices, c.computeRate), limits, prices, nil
}

// warnIfBlind logs one loud warning when a cost budget is set but nothing can
// make the token meter live: no prices and no compute rate. It goes through the
// structured logger so it does not emit a stray non-JSON line under
// --log-format json.
func warnIfBlind(logger *slog.Logger, c *commonFlags, limits policy.Limits, prices policy.PriceTable) {
	if limits.MaxCost > 0 && prices == nil && c.computeRate == 0 {
		logger.Warn("token meter blind: supply --prices, or the cost budget cannot trip")
	}
}

// shortID returns a short random run id such as "a3f9".
func shortID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%04x", time.Now().UnixNano()&0xffff)
	}
	return hex.EncodeToString(b[:])
}
