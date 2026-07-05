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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/leash/internal/proxy"
	"github.com/sylvester-francis/leash/internal/reactions"
	"github.com/sylvester-francis/leash/internal/term"
	"github.com/sylvester-francis/leash/internal/wrap"
)

// standbyRetryInterval is how often a --standby instance retries acquiring the
// governance lease while another instance holds it.
const standbyRetryInterval = 5 * time.Second

// acquireProxy builds a proxy, honoring --standby. Without standby it returns
// any error at once. With standby, when the lease is held (ErrGovernorHeld) it
// logs and retries until the lease frees: active/passive failover.
func acquireProxy(cfg proxy.Config, standby bool, retry time.Duration, logger *slog.Logger) (*proxy.Proxy, error) {
	for {
		p, err := proxy.New(cfg)
		if err == nil || !standby || !errors.Is(err, proxy.ErrGovernorHeld) {
			return p, err
		}
		logger.Info("ledger governed by another instance; standing by", "retry", retry.String())
		time.Sleep(retry)
	}
}

// setUsage installs a richer -h/--help for a subcommand: a one-line synopsis,
// the usage line, examples, then the flag defaults.
func setUsage(fs *flag.FlagSet, synopsis, usage string, examples ...string) {
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintf(w, "%s\n\nUsage:\n  %s\n\n", synopsis, usage)
		if len(examples) > 0 {
			fmt.Fprintln(w, "Examples:")
			for _, e := range examples {
				fmt.Fprintf(w, "  %s\n", e)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "Flags:")
		fs.PrintDefaults()
	}
}

// parsePositional parses flags that may appear before or after a single
// positional argument (the run id), returning that positional or empty. The
// standard flag package stops at the first non-flag token, so a second parse of
// the remainder lets `leash inspect <run> --db X` and `leash inspect --db X
// <run>` both work.
func parsePositional(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", nil
	}
	pos := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return "", err
	}
	return pos, nil
}

// parseBlindPolicy maps the --on-blind flag to a proxy.BlindPolicy.
func parseBlindPolicy(s string) (proxy.BlindPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "refuse", "":
		return proxy.BlindRefuse, nil
	case "warn":
		return proxy.BlindWarn, nil
	case "allow":
		return proxy.BlindAllow, nil
	default:
		return 0, fmt.Errorf("invalid --on-blind %q (want refuse, warn, or allow)", s)
	}
}

// parseUpstream validates an --upstream override, returning nil when unset.
func parseUpstream(s string) (*url.URL, error) {
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid --upstream %q: %w", s, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid --upstream %q: need a scheme and host", s)
	}
	return u, nil
}

// cmdRun is the Tier 1 wrapper: leash [flags] -- <command> [args...].
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("leash", flag.ContinueOnError)
	c := registerCommon(fs)
	setUsage(fs, "leash - wrap a command under the spend governor (Tier 1).",
		"leash [flags] -- <command> [args...]",
		"leash --max-cost 5 --deadline 15m --prices prices.json -- python agent.py",
		"leash --max-calls 500 --rate 200000/1m --stall 4 -- ./agent.sh")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if c.run != "" && !policy.ValidRunID(c.run) {
		fmt.Fprintf(os.Stderr, "leash: invalid --run %q: %s\n", c.run, policy.RunIDRule)
		return 1
	}
	command := fs.Args()
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "leash: no command; usage: leash [flags] -- <command> [args...]")
		return 2
	}

	g, limits, prices, err := buildGovernor(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}

	upstream, err := parseUpstream(c.upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}

	runID := c.run
	if runID == "" {
		runID = shortID()
	}

	logger, err := buildLogger(c.logLevel, c.logFormat, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}
	warnIfBlind(logger, c, limits, prices)

	l, err := ledger.Open(c.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer l.Close()

	onBlind, err := parseBlindPolicy(c.onBlind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}

	p, err := proxy.New(proxy.Config{
		Ledger:                l,
		Governor:              g,
		DefaultRun:            runID,
		Upstream:              upstream,
		Inject:                !c.noInject,
		MaxBodyBytes:          c.maxBodyBytes,
		UpstreamHeaderTimeout: c.upstreamHeaderTimeout,
		OnBlind:               onBlind,
		MaxCostPerCall:        c.maxCostPerCall,
		WarnAt:                c.warnAt,
		Logger:                logger,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer p.Shutdown()

	res, err := wrap.Run(context.Background(), wrap.Options{
		Handler:  p,
		Ledger:   l,
		Governor: g,
		RunID:    runID,
		Command:  command,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	return res.ExitCode
}

// cmdServe is the Tier 2 standalone gateway.
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("leash serve", flag.ContinueOnError)
	c := registerCommon(fs)
	listen := fs.String("listen", envStr("LEASH_LISTEN", ":8088"), "address to listen on")
	requireRunID := fs.Bool("require-run-id", envBool("LEASH_REQUIRE_RUN_ID", false),
		"refuse requests without an X-Loop-Id instead of pooling them into the default run")
	admin := fs.String("admin", envStr("LEASH_ADMIN", ""),
		"address for the admin listener serving /healthz, /readyz, /metrics (empty disables)")
	webhook := fs.String("webhook", envStr("LEASH_WEBHOOK", ""),
		"URL to POST a JSON event to when a run approaches a budget (--warn-at) or a boundary stops it (empty disables)")
	reactionsDB := fs.String("reactions-db", envStr("LEASH_REACTIONS_DB", ""),
		"durable reactions store (a separate SQLite path or postgres:// DSN, distinct from --db); when set, --webhook is delivered durably and --on-event-exec is enabled (empty keeps reactions best-effort)")
	onEventExec := fs.String("on-event-exec", envStr("LEASH_ON_EVENT_EXEC", ""),
		"command to run on a stop or warning, with event data in LEASH_* env vars (requires --reactions-db)")
	standby := fs.Bool("standby", envBool("LEASH_STANDBY", false),
		"wait for the governance lease instead of erroring when another instance holds it (active/passive HA)")
	authToken := fs.String("auth-token", envStr("LEASH_AUTH_TOKEN", ""),
		"require an X-Leash-Token header matching this value; space-separate two to rotate with no downtime (prefer LEASH_AUTH_TOKEN so it stays out of the process list; empty disables)")
	maxRuns := fs.Int("max-runs", envInt("LEASH_MAX_RUNS", 0),
		"cap on runs tracked in memory at once; a new run beyond it is refused 503 (0 disables)")
	insecure := fs.Bool("insecure", envBool("LEASH_INSECURE", false),
		"allow the gateway to run with no --auth-token (forwarding live API keys unauthenticated)")
	authTokenFile := fs.String("auth-token-file", envStr("LEASH_AUTH_TOKEN_FILE", ""),
		"read auth token(s) from this file (whitespace-separated) instead of a flag or env, keeping them off the process list")
	maxConns := fs.Int("max-conns", envInt("LEASH_MAX_CONNS", 0),
		"cap on simultaneous client connections; beyond it new connections wait (0 disables)")
	shutdownTimeout := fs.Duration("shutdown-timeout", envDuration("LEASH_SHUTDOWN_TIMEOUT", 30*time.Second),
		"how long graceful shutdown waits for in-flight streams to finish before forcing")
	drainDelay := fs.Duration("drain-delay", envDuration("LEASH_DRAIN_DELAY", 0),
		"on shutdown, mark /readyz not-ready then wait this long before draining, so a load balancer can deregister (0 disables)")
	setUsage(fs, "leash serve - run the standalone governor gateway (Tier 2).",
		"leash serve [flags]",
		"leash serve --listen :8088 --max-cost 20 --prices prices.json",
		"LEASH_AUTH_TOKEN=$(cat token) leash serve --require-run-id --max-runs 1000 --admin :9090")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}

	authTokens := strings.Fields(*authToken)
	if *authTokenFile != "" {
		data, ferr := os.ReadFile(*authTokenFile)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "leash: read --auth-token-file: %v\n", ferr)
			return 1
		}
		authTokens = append(authTokens, strings.Fields(string(data))...)
	}
	if len(authTokens) == 0 && !*insecure {
		fmt.Fprintln(os.Stderr, "leash: serve requires --auth-token (or LEASH_AUTH_TOKEN); pass --insecure to run open and forward API keys unauthenticated")
		return 2
	}
	onBlind, err := parseBlindPolicy(c.onBlind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}
	if *onEventExec != "" && *reactionsDB == "" {
		fmt.Fprintln(os.Stderr, "leash: --on-event-exec requires --reactions-db (the command hook is a durable reaction)")
		return 2
	}
	if *reactionsDB != "" && *reactionsDB == c.db {
		fmt.Fprintln(os.Stderr, "leash: --reactions-db must differ from --db; reactions use a separate store")
		return 2
	}

	g, limits, prices, err := buildGovernor(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}

	upstream, err := parseUpstream(c.upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}

	l, err := ledger.Open(c.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer l.Close()

	logger, err := buildLogger(c.logLevel, c.logFormat, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}
	warnIfBlind(logger, c, limits, prices)
	if upstream != nil && upstream.Scheme == "http" {
		logger.Warn("upstream is plain http; the client credential is forwarded in cleartext", "upstream", upstream.Redacted())
	}

	// The stop line is printed straight to stderr (not through the structured
	// logger) so it keeps its exact human-readable "leash: stopped run ..." form,
	// colored by boundary reason when stderr is a terminal.
	stopPaint := term.NewPainter(os.Stderr)
	observers := proxy.MultiObserver{
		proxy.StopLineObserver(func(s *policy.State) {
			fmt.Fprintln(os.Stderr, stopPaint.StopReasonColor(policy.StopLine(s), s.StopReason))
		}),
	}
	var metrics *proxy.Metrics
	if *admin != "" {
		metrics = proxy.NewMetrics(version, g.Prices)
		observers = append(observers, metrics)
	}
	// Durable reactions upgrade the webhook from best-effort to a crash-surviving
	// rerun workflow and enable the command hook; without --reactions-db the
	// webhook stays best-effort exactly as before.
	var reactionDisp *reactions.Dispatcher
	if *reactionsDB != "" {
		reactionDisp, err = reactions.NewDispatcher(reactions.Config{
			DSN: *reactionsDB, WebhookURL: *webhook, Command: *onEventExec,
			Logger: logger, Now: time.Now,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "leash: %v\n", err)
			return 1
		}
		observers = append(observers, reactionDisp)
		logger.Info("durable reactions enabled", "db", *reactionsDB,
			"webhook", *webhook != "", "command", *onEventExec != "")
	} else if *webhook != "" {
		observers = append(observers, proxy.NewWebhookNotifier(*webhook, logger, time.Now))
		logger.Info("webhook notifications enabled (best-effort; set --reactions-db for durable)", "url", *webhook)
	}

	if *standby {
		// --standby is only meaningful across processes, which SQLite's
		// process-local lease cannot coordinate. Refuse it rather than silently
		// providing no mutual exclusion (a split-brain footgun).
		if !strings.HasPrefix(c.db, "postgres://") && !strings.HasPrefix(c.db, "postgresql://") {
			fmt.Fprintln(os.Stderr, "leash: --standby requires a postgres ledger (--db postgres://...); a SQLite lease is process-local")
			return 2
		}
		logger.Info("standby mode: will wait for the governance lease", "db", c.db)
	}
	p, err := acquireProxy(proxy.Config{
		Ledger:                l,
		Governor:              g,
		Upstream:              upstream,
		Inject:                !c.noInject,
		MaxBodyBytes:          c.maxBodyBytes,
		UpstreamHeaderTimeout: c.upstreamHeaderTimeout,
		RequireRunID:          *requireRunID,
		AuthTokens:            authTokens,
		MaxRuns:               *maxRuns,
		OnBlind:               onBlind,
		MaxCostPerCall:        c.maxCostPerCall,
		WarnAt:                c.warnAt,
		Logger:                logger,
		Observer:              observers,
	}, *standby, standbyRetryInterval, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer p.Shutdown()

	// The governance lease is now won, so it is safe to resume reactions a crash
	// left in flight without a passive HA node double-firing. Close parks any
	// in-flight reaction for the next boot to resume.
	if reactionDisp != nil {
		defer func() {
			cctx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
			defer cancel()
			_ = reactionDisp.Close(cctx)
		}()
		if rerr := reactionDisp.Recover(context.Background()); rerr != nil {
			logger.Warn("reactions recover failed", "err", rerr)
		}
	}

	srv := proxy.HardenedServer(*listen, p)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// draining flips /readyz to 503 the moment shutdown begins, so a load
	// balancer stops routing new work to this instance before its streams drain.
	var draining atomic.Bool

	// The admin listener (health, readiness, metrics) runs on its own address so
	// it never collides with proxied API paths and can be network-segmented.
	var adminSrv *http.Server
	if *admin != "" {
		adminSrv = proxy.NewAdminServer(*admin, l, p, metrics, authTokens, &draining)
		go func() {
			if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("admin server error", "err", err)
			}
		}()
		logger.Info("admin listener started", "addr", *admin)
	}

	go func() {
		<-ctx.Done()
		// Signal not-ready first, optionally pause so a load balancer can
		// deregister, then drain in-flight streams within the timeout.
		draining.Store(true)
		if *drainDelay > 0 {
			logger.Info("draining: /readyz now failing, pausing before shutdown", "delay", *drainDelay)
			time.Sleep(*drainDelay)
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if adminSrv != nil {
			_ = adminSrv.Shutdown(shutCtx)
		}
	}()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Error("listen failed", "addr", *listen, "err", err)
		return 1
	}
	if *maxConns > 0 {
		ln = proxy.LimitListener(ln, *maxConns)
	}

	logger.Info("serving", "version", version, "addr", *listen, "db", c.db,
		"auth", len(authTokens) > 0, "max_conns", *maxConns)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		logger.Error("server error", "err", err)
		return 1
	}
	return 0
}

// cmdPs lists active runs from the ledger.
func cmdPs(args []string) int {
	fs := flag.NewFlagSet("leash ps", flag.ContinueOnError)
	c := registerCommon(fs)
	asJSON := fs.Bool("json", false, "emit a stable JSON array instead of a human table")
	setUsage(fs, "leash ps - list active runs from the ledger.",
		"leash ps [flags]",
		"leash ps",
		"leash ps --json --db ./team.db")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	g, _, _, err := buildGovernor(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}
	l, err := ledger.Open(c.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer l.Close()

	runs, err := l.Incomplete(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}

	summaries := make([]runJSON, 0, len(runs))
	for _, r := range runs {
		s, err := l.Load(context.Background(), r.ID, g)
		if err != nil {
			continue
		}
		if s.StopReason == "" {
			s.Refresh(time.Now(), g.ComputeRate)
		}
		summaries = append(summaries, toRunJSON(s))
	}

	if *asJSON {
		return encodeJSON(summaries)
	}
	if len(summaries) == 0 {
		fmt.Println("leash: no active runs")
		return 0
	}
	printRunTable(os.Stdout, summaries)
	return 0
}

// printRunTable prints the ps table with visible-width alignment and a colored
// status column. Padding is computed on the plain text, so the ANSI codes never
// throw off column widths; color auto-disables off a terminal.
func printRunTable(w *os.File, rows []runJSON) {
	p := term.NewPainter(w)
	headers := []string{"RUN", "CALLS", "TOKENS$", "COMPUTE$", "TOTAL$", "STATUS", "REASON"}
	const statusCol = 5
	cells := make([][]string, len(rows))
	for i, s := range rows {
		cells[i] = []string{
			s.Run, strconv.FormatInt(s.Calls, 10),
			fmt.Sprintf("%.2f", s.TokenCost), fmt.Sprintf("%.2f", s.ComputeCost),
			fmt.Sprintf("%.2f", s.TotalCost), s.Status, s.Reason,
		}
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range cells {
		for i, c := range row {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}

	var b strings.Builder
	writeRow := func(row []string, color bool) {
		for i, c := range row {
			last := i == len(row)-1
			pad := c
			if !last {
				pad = fmt.Sprintf("%-*s", widths[i], c)
			}
			if color && i == statusCol {
				pad = colorStatus(p, c, pad)
			}
			b.WriteString(pad)
			if !last {
				b.WriteString("  ")
			}
		}
		b.WriteByte('\n')
	}
	writeRow(headers, false)
	for i := range cells {
		writeRow(cells[i], true)
	}
	fmt.Fprint(w, b.String())
}

// colorStatus colors a padded status cell by its status word.
func colorStatus(p term.Painter, status, cell string) string {
	switch status {
	case "running":
		return p.Green(cell)
	case "stopped":
		return p.Amber(cell)
	case "killed":
		return p.Red(cell)
	default:
		return cell
	}
}

// encodeJSON writes v as indented JSON to stdout, returning a CLI exit code.
func encodeJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "leash: encode json: %v\n", err)
		return 1
	}
	return 0
}

// runStatus names a run's state for display.
func runStatus(s *policy.State) string {
	switch {
	case s.StopReason != "":
		return "stopped"
	case s.Killed:
		return "killed"
	default:
		return "running"
	}
}

// cmdInspect shows one run's folded journal.
func cmdInspect(args []string) int {
	fs := flag.NewFlagSet("leash inspect", flag.ContinueOnError)
	c := registerCommon(fs)
	asJSON := fs.Bool("json", false, "emit a stable JSON object instead of a human report")
	setUsage(fs, "leash inspect - show one run's folded journal.",
		"leash inspect [flags] <run>",
		"leash inspect nightly-42",
		"leash inspect --json nightly-42 --db ./team.db")
	runID, err := parsePositional(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if runID == "" {
		fmt.Fprintln(os.Stderr, "leash: usage: leash inspect [flags] <run>")
		return 2
	}
	g, _, _, err := buildGovernor(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}
	l, err := ledger.Open(c.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer l.Close()

	s, err := l.Load(context.Background(), runID, g)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	entries, err := l.Entries(context.Background(), runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	if s.StopReason == "" {
		s.Refresh(time.Now(), g.ComputeRate)
	}

	if *asJSON {
		out := inspectJSON{runJSON: toRunJSON(s), Entries: make([]entryJSON, 0, len(entries))}
		for _, e := range entries {
			out.Entries = append(out.Entries, toEntryJSON(e))
		}
		return encodeJSON(out)
	}

	if len(entries) == 0 {
		fmt.Printf("leash: no journal for run %s\n", runID)
		return 0
	}

	pt := term.NewPainter(os.Stdout)
	fmt.Printf("run %s  status %s  calls %d\n", runID, pt.Status(runStatus(s)), s.Calls)
	fmt.Printf("cost   $%.2f tokens + $%.2f compute = $%.2f\n", s.TokenCost, s.ComputeCost, s.TotalCost)
	fmt.Printf("tokens in %d  out %d  reasoning %d\n\n", s.InputTokens, s.OutputTokens, s.ReasoningTokens)

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tTAG\tWHEN\tDETAIL")
	for _, e := range entries {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", e.Seq, e.Tag, e.At.Format(time.RFC3339), entryDetail(e))
	}
	_ = tw.Flush()
	return 0
}

// entryDetail renders one journal entry's detail column.
func entryDetail(e ledger.Entry) string {
	switch e.Kind {
	case ledger.KindCall:
		return fmt.Sprintf("%s in=%d out=%d reasoning=%d",
			e.Usage.Model, e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.ReasoningTokens)
	case ledger.KindKill:
		return "durable kill"
	case ledger.KindStop:
		return "stop: " + e.Reason
	default:
		return ""
	}
}

// cmdKill durably stops a run on its next call, working from a second process.
func cmdKill(args []string) int {
	fs := flag.NewFlagSet("leash kill", flag.ContinueOnError)
	db := fs.String("db", envStr("LEASH_DB", defaultDBPath()), "ledger database path")
	setUsage(fs, "leash kill - durably stop a run on its next call.",
		"leash kill [flags] <run>",
		"leash kill nightly-42",
		"leash kill nightly-42 --db ./team.db")
	runID, err := parsePositional(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if runID == "" {
		fmt.Fprintln(os.Stderr, "leash: usage: leash kill [flags] <run>")
		return 2
	}
	l, err := ledger.Open(*db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer l.Close()

	if err := l.AppendKill(context.Background(), runID, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	fmt.Printf("leash: kill recorded for run %s; it stops on its next call\n", runID)
	return 0
}

// flagExit maps a flag parse error to an exit code: help is success.
func flagExit(err error) int {
	if err == flag.ErrHelp {
		return 0
	}
	return 2
}
