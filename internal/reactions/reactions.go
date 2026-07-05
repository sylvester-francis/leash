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

// Package reactions delivers durable escalations when a run stops or approaches a
// budget. It is leash's one use of rerun's execution layer (everywhere else uses
// only the Store): a stop or warning becomes a rerun workflow,
// notify-webhook -> run-command-hook, retried and resumed across a crash. See
// docs/adr/0009-durable-governance-reactions.md for the design and the one
// deliberate gap (the enqueue is asynchronous off the enforcement path, so a
// crash in the window between the stop and the durable Create loses that one
// reaction; it does not re-fire because RunStopped is a live-only transition).
package reactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/leash/internal/proxy"
	"github.com/sylvester-francis/rerun"
	"github.com/sylvester-francis/rerun/postgres"
	"github.com/sylvester-francis/rerun/sqlite"
)

const (
	workflowName       = "reaction"
	queueDepth         = 256
	defaultMaxAttempts = 5
	defaultHTTPTimeout = 10 * time.Second
	commandHookTimeout = 30 * time.Second
)

// Event is the durable seed of a reaction. It is journaled by Start and replayed
// on recovery, so it must be self-contained and JSON-serializable.
type Event struct {
	Kind      string  `json:"kind"` // "stopped" or "warning"
	Run       string  `json:"run"`
	Reason    string  `json:"reason"`
	Calls     int64   `json:"calls"`
	TotalCost float64 `json:"total_cost"`
	Used      float64 `json:"used,omitempty"`
	Limit     float64 `json:"limit,omitempty"`
	Fraction  float64 `json:"fraction,omitempty"`
	At        string  `json:"at"`
}

// id is the reaction's run id, and thus its idempotency key: one stop and one
// warning per run. A duplicate enqueue collides on the store's primary key and is
// dropped, giving at-least-once with dedup.
func (e Event) id() string {
	if e.Kind == "warning" {
		return "warn:" + e.Run
	}
	return "stop:" + e.Run
}

// Config configures a Dispatcher. DSN is the reactions store (a SQLite path or a
// postgres:// DSN) and must differ from the governance ledger. WebhookURL and
// Command are the two sinks; either may be empty.
type Config struct {
	DSN         string
	WebhookURL  string
	Command     string
	Logger      *slog.Logger
	Now         func() time.Time
	HTTPClient  *http.Client
	RetryPolicy rerun.RetryPolicy // zero value uses the default
}

// Dispatcher is a proxy.Observer that turns stop and warning events into durable
// rerun workflows. RunStopped and BudgetWarning enqueue without blocking; a
// background worker performs the durable Create off the enforcement path. All
// other Observer methods are inherited no-ops.
type Dispatcher struct {
	proxy.NopObserver
	eng        *rerun.Engine
	store      io.Closer
	webhookURL string
	command    string
	client     *http.Client
	policy     rerun.RetryPolicy
	logger     *slog.Logger
	now        func() time.Time
	ch         chan Event
	quit       chan struct{}
	done       chan struct{}
}

// NewDispatcher opens the reactions store, registers the reaction workflow, and
// starts the background enqueue worker. Call Recover once the governance lease is
// won, and Close on shutdown.
func NewDispatcher(cfg Config) (*Dispatcher, error) {
	store, closer, err := openStore(cfg.DSN)
	if err != nil {
		return nil, err
	}
	pol := cfg.RetryPolicy
	if pol.MaxAttempts <= 0 {
		pol = rerun.RetryPolicy{MaxAttempts: defaultMaxAttempts, Backoff: rerun.ExpBackoff(time.Second, time.Minute)}
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	d := &Dispatcher{
		eng:        rerun.New(store),
		store:      closer,
		webhookURL: cfg.WebhookURL,
		command:    cfg.Command,
		client:     client,
		policy:     pol,
		logger:     cfg.Logger,
		now:        now,
		ch:         make(chan Event, queueDepth),
		quit:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	d.eng.Handle(workflowName, d.workflow)
	go d.run()
	return d, nil
}

// RunStopped enqueues a durable stop reaction. Non-blocking: a full queue drops
// the event rather than delay the enforcement path.
func (d *Dispatcher) RunStopped(s *policy.State) {
	d.enqueue(Event{
		Kind: "stopped", Run: s.RunID, Reason: s.StopReason,
		Calls: s.Calls, TotalCost: s.TotalCost, At: d.now().UTC().Format(time.RFC3339),
	})
}

// BudgetWarning enqueues a durable warning reaction. Non-blocking, as RunStopped.
func (d *Dispatcher) BudgetWarning(s *policy.State, st policy.BudgetStatus) {
	d.enqueue(Event{
		Kind: "warning", Run: s.RunID, Reason: st.Reason,
		Used: st.Used, Limit: st.Limit, Fraction: st.Fraction,
		Calls: s.Calls, TotalCost: s.TotalCost, At: d.now().UTC().Format(time.RFC3339),
	})
}

func (d *Dispatcher) enqueue(ev Event) {
	select {
	case d.ch <- ev:
	default:
		d.logger.Warn("reaction queue full; dropping event", "kind", ev.Kind, "run", ev.Run)
	}
}

// run drains the queue and performs the durable Create off the enforcement path.
func (d *Dispatcher) run() {
	defer close(d.done)
	for {
		select {
		case ev := <-d.ch:
			err := d.eng.Start(context.Background(), workflowName, ev.id(), ev)
			if err != nil && !errors.Is(err, rerun.ErrRunExists) {
				d.logger.Warn("reaction enqueue failed", "run", ev.Run, "err", err)
			}
		case <-d.quit:
			return
		}
	}
}

// workflow is the durable reaction body: webhook then command hook, each retried.
// The tag sequence is fixed so replay never diverges; an unconfigured sink is a
// no-op step, not a missing one.
func (d *Dispatcher) workflow(w *rerun.W) error {
	ev, err := rerun.Input[Event](w)
	if err != nil {
		return err
	}
	if _, err := rerun.Retry(w, "notify-webhook", d.policy, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, d.postWebhook(ctx, ev)
	}); err != nil {
		return err
	}
	_, err = rerun.Retry(w, "run-command-hook", d.policy, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, d.execHook(ctx, ev)
	})
	return err
}

func (d *Dispatcher) postWebhook(ctx context.Context, ev Event) error {
	if d.webhookURL == "" {
		return nil
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s returned %d", d.webhookURL, resp.StatusCode)
	}
	return nil
}

// execHook runs the operator's command with event data in LEASH_* environment
// variables. The command is invoked directly (no shell), so there is no shell
// interpolation; arguments belong in a script the command points at.
func (d *Dispatcher) execHook(ctx context.Context, ev Event) error {
	if d.command == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, commandHookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.command)
	cmd.Env = append(os.Environ(),
		"LEASH_EVENT="+ev.Kind,
		"LEASH_RUN="+ev.Run,
		"LEASH_REASON="+ev.Reason,
		"LEASH_CALLS="+strconv.FormatInt(ev.Calls, 10),
		"LEASH_TOTAL_COST="+strconv.FormatFloat(ev.TotalCost, 'f', -1, 64),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command hook %q: %w: %s", d.command, err, bytes.TrimSpace(out))
	}
	return nil
}

// Recover resumes reactions a crash left in flight. Call it once, after the
// governance lease is won, so a passive HA node does not double-fire.
func (d *Dispatcher) Recover(ctx context.Context) error {
	return d.eng.Recover(ctx)
}

// Close stops the worker, parks in-flight reactions for the next boot to resume,
// and closes the store.
func (d *Dispatcher) Close(ctx context.Context) error {
	close(d.quit)
	<-d.done
	err := d.eng.Shutdown(ctx)
	if cerr := d.store.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// openStore builds a rerun store from a DSN, mirroring the ledger's selection:
// a postgres:// DSN uses the Postgres backend, anything else a SQLite file.
func openStore(dsn string) (store rerun.Store, closer io.Closer, err error) {
	defer func() {
		if r := recover(); r != nil {
			store, closer, err = nil, nil, fmt.Errorf("open reactions store at %s: %v", dsn, r)
		}
	}()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		s := postgres.New(dsn)
		return s, s, nil
	}
	if dir := filepath.Dir(dsn); dir != "" && dir != "." {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return nil, nil, fmt.Errorf("create reactions directory %s: %w", dir, mkErr)
		}
	}
	s := sqlite.New(dsn)
	return s, s, nil
}
