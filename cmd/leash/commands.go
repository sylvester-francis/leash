package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/leash/internal/proxy"
	"github.com/sylvester-francis/leash/internal/wrap"
)

// serveShutdownTimeout bounds graceful shutdown of the standalone server.
const serveShutdownTimeout = 5 * time.Second

// standbyRetryInterval is how often a --standby instance retries acquiring the
// governance lease while another instance holds it.
const standbyRetryInterval = 5 * time.Second

// acquireProxy builds a proxy, honoring --standby. Without standby it returns
// any error immediately. With standby, when the lease is held by another
// instance (ErrGovernorHeld) it logs and retries every retry interval until the
// lease frees, which is the active/passive failover: exactly one instance
// governs a given ledger, and a warm standby takes over when it steps down.
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
	warnIfBlind(c, limits, prices)

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

	l, err := ledger.Open(c.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer l.Close()

	p, err := proxy.New(proxy.Config{
		Ledger:                l,
		Governor:              g,
		DefaultRun:            runID,
		Upstream:              upstream,
		Inject:                !c.noInject,
		MaxBodyBytes:          c.maxBodyBytes,
		UpstreamHeaderTimeout: c.upstreamHeaderTimeout,
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
	standby := fs.Bool("standby", envBool("LEASH_STANDBY", false),
		"wait for the governance lease instead of erroring when another instance holds it (active/passive HA)")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}

	g, limits, prices, err := buildGovernor(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 2
	}
	warnIfBlind(c, limits, prices)

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

	// The stop line is printed straight to stderr (not through the structured
	// logger) so it keeps its exact human-readable "leash: stopped run ..." form.
	observers := proxy.MultiObserver{
		proxy.StopLineObserver(func(s *policy.State) { fmt.Fprintln(os.Stderr, policy.StopLine(s)) }),
	}
	var metrics *proxy.Metrics
	if *admin != "" {
		metrics = proxy.NewMetrics(version, g.Prices)
		observers = append(observers, metrics)
	}

	if *standby {
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
		Logger:                logger,
		Observer:              observers,
	}, *standby, standbyRetryInterval, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leash: %v\n", err)
		return 1
	}
	defer p.Shutdown()

	srv := proxy.HardenedServer(*listen, p)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The admin listener (health, readiness, metrics) runs on its own address so
	// it never collides with proxied API paths and can be network-segmented.
	var adminSrv *http.Server
	if *admin != "" {
		adminSrv = proxy.NewAdminServer(*admin, l, p, metrics)
		go func() {
			if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("admin server error", "err", err)
			}
		}()
		logger.Info("admin listener started", "addr", *admin)
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), serveShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if adminSrv != nil {
			_ = adminSrv.Shutdown(shutCtx)
		}
	}()

	logger.Info("serving", "version", version, "addr", *listen, "db", c.db)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tCALLS\tTOKENS$\tCOMPUTE$\tTOTAL$\tSTATUS\tREASON")
	for _, s := range summaries {
		fmt.Fprintf(tw, "%s\t%d\t%.2f\t%.2f\t%.2f\t%s\t%s\n",
			s.Run, s.Calls, s.TokenCost, s.ComputeCost, s.TotalCost, s.Status, s.Reason)
	}
	_ = tw.Flush()
	return 0
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

	fmt.Printf("run %s  status %s  calls %d\n", runID, runStatus(s), s.Calls)
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
