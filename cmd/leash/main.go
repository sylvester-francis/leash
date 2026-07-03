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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

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
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		return cmdRun(args)
	}
}

// usage prints the top-level help.
func usage(w *os.File) {
	fmt.Fprint(w, `leash - durable agent spend governor

Usage:
  leash [flags] -- <command> [args...]   wrap an agent (Tier 1)
  leash serve --listen :8088 [flags]     standalone gateway (Tier 2)
  leash ps                               list runs from the ledger
  leash inspect <run>                    show one run's folded journal
  leash kill <run>                       durably stop a run on its next call

Flags are per-subcommand (pass -h after a subcommand); see the README for the full list.
`)
}

// commonFlags are the governance flags shared by run and serve (and reused,
// where they make sense, by ps and inspect).
type commonFlags struct {
	maxCost     float64
	maxCalls    int64
	deadline    time.Duration
	rate        string
	stall       int
	prices      string
	computeRate float64
	upstream    string
	db          string
	run         string
	noInject    bool
}

// registerCommon binds the shared flags with their documented defaults.
func registerCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.Float64Var(&c.maxCost, "max-cost", 5.00, "dollar budget over token + compute cost (0 disables)")
	fs.Int64Var(&c.maxCalls, "max-calls", 100, "maximum governed calls (0 disables)")
	fs.DurationVar(&c.deadline, "deadline", 30*time.Minute, "wall-clock budget from the first call (0 disables)")
	fs.StringVar(&c.rate, "rate", "", "trailing token rate as tokens/window, e.g. 100000/1m (empty disables)")
	fs.IntVar(&c.stall, "stall", 0, "consecutive identical responses tolerated (0 disables)")
	fs.StringVar(&c.prices, "prices", "", "path to a JSON price table (model -> input/output/reasoning per million)")
	fs.Float64Var(&c.computeRate, "compute-rate", 0, "compute meter in dollars per hour")
	fs.StringVar(&c.upstream, "upstream", "", "upstream base URL override (empty infers per provider)")
	fs.StringVar(&c.db, "db", defaultDBPath(), "ledger database path")
	fs.StringVar(&c.run, "run", "", "run name; reusing it on a later invocation resumes that budget")
	fs.BoolVar(&c.noInject, "no-inject", false, "do not add stream_options.include_usage to streaming requests")
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

// warnIfBlind prints one loud warning when a cost budget is set but nothing can
// make the token meter live: no prices and no compute rate.
func warnIfBlind(c *commonFlags, limits policy.Limits, prices policy.PriceTable) {
	if limits.MaxCost > 0 && prices == nil && c.computeRate == 0 {
		fmt.Fprintln(os.Stderr, "leash: token meter blind: supply --prices (the cost budget cannot trip without it)")
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
