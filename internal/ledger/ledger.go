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

// Package ledger is leash's durable per-run account. It stores every governed
// call, kill, and stop as an append-only journal on a rerun.Store (SQLite by
// default), and rebuilds a run's totals by folding that journal. The journal is
// the source of truth: in-memory state is only ever a cache of it, so a crash
// and restart resume with the budget intact and no entry double counted.
//
// Privacy is a property of what this package stores: usage numbers, content
// fingerprint hashes, timestamps, and stop reasons only. It never persists
// request or response bodies, and never persists secrets.
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/rerun"
	"github.com/sylvester-francis/rerun/postgres"
	"github.com/sylvester-francis/rerun/sqlite"
)

// workflowName is the rerun workflow every leash run is created under.
const workflowName = "leash"

// Journal tag conventions. Call logs carry an incrementing index for readable
// inspection; kill and stop are singletons per run.
const (
	tagCallPrefix = "call-"
	tagKill       = "kill"
	tagStop       = "stop"
)

// appendRetries bounds how many times an append re-reads the sequence and
// retries after losing a race for a journal position to another writer (for
// example a concurrent `leash kill`).
const appendRetries = 8

// Ledger is a durable account backed by a rerun.Store.
type Ledger struct {
	store  rerun.Store
	closer io.Closer
	// lockPath is the sidecar lock file for a SQLite ledger, empty otherwise. It
	// backs a cross-process governance lock that the in-process SQLite lease
	// cannot provide.
	lockPath string
}

// Lease is a held run lease. Release it when governing ends.
type Lease struct {
	release  io.Closer
	fileLock io.Closer
}

// Release returns the lease. It is safe to call once.
func (le *Lease) Release() error {
	if le == nil {
		return nil
	}
	var err error
	if le.fileLock != nil {
		err = le.fileLock.Close()
	}
	if le.release != nil {
		if rerr := le.release.Close(); rerr != nil && err == nil {
			err = rerr
		}
	}
	return err
}

// Open opens (or creates) a ledger from a data-source string. A dsn beginning
// with postgres:// or postgresql:// selects the cross-process PostgreSQL backend;
// anything else is treated as a SQLite file path. Both backends panic on open
// failure; Open recovers that into an ordinary error so callers never see a
// panic.
func Open(dsn string) (*Ledger, error) {
	if isPostgresDSN(dsn) {
		return openPostgres(dsn)
	}
	return openSQLite(dsn)
}

// isPostgresDSN reports whether dsn selects the PostgreSQL backend.
func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// openPostgres opens the PostgreSQL-backed ledger, recovering the backend's
// open-failure panic into an error. Its governance lease is a real cross-process
// advisory lock, which is what makes active/passive HA possible.
func openPostgres(dsn string) (l *Ledger, err error) {
	defer func() {
		if r := recover(); r != nil {
			l, err = nil, fmt.Errorf("open postgres ledger: %v", r)
		}
	}()
	store := postgres.New(dsn)
	return &Ledger{store: store, closer: store}, nil
}

// openSQLite opens (or creates) the SQLite-backed ledger at path, creating the
// parent directory if needed and recovering the backend's open-failure panic.
func openSQLite(path string) (l *Ledger, err error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return nil, fmt.Errorf("create ledger directory %s: %w", dir, mkErr)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			l, err = nil, fmt.Errorf("open ledger at %s: %v", path, r)
		}
	}()
	store := sqlite.New(path)
	return &Ledger{store: store, closer: store, lockPath: path + ".govlock"}, nil
}

// Close releases the underlying store.
func (l *Ledger) Close() error {
	if l.closer == nil {
		return nil
	}
	return l.closer.Close()
}

// Acquire tries to lease the ledger for governing. It is non-blocking: acquired
// is false when the lease is already held. The store's in-process lease guards
// against two governors inside one process; for SQLite an exclusive OS lock on a
// sidecar file adds the cross-process guarantee (on unix), so a second process
// governing the same file is refused rather than silently double-spending.
// Postgres needs no OS lock: its lease is already a cross-process advisory lock.
func (l *Ledger) Acquire(ctx context.Context, runID string) (*Lease, bool, error) {
	release, acquired, err := l.store.Acquire(ctx, runID)
	if err != nil {
		return nil, false, fmt.Errorf("acquire lease for run %s: %w", runID, err)
	}
	if !acquired {
		return nil, false, nil
	}
	var fileLock io.Closer
	if l.lockPath != "" {
		fl, ok, lerr := acquireFileLock(l.lockPath)
		if lerr != nil {
			_ = release.Close()
			return nil, false, fmt.Errorf("acquire governance lock %s: %w", l.lockPath, lerr)
		}
		if !ok {
			_ = release.Close()
			return nil, false, nil // another process already governs this ledger
		}
		fileLock = fl
	}
	return &Lease{release: release, fileLock: fileLock}, true, nil
}

// EnsureRun creates the run if it is new. Resuming an existing run is not an
// error: a duplicate-key Create failure paired with a working read is a resume.
// Any other Create failure (a real write error, e.g. a full disk) is returned,
// so a partial database failure fails closed rather than being mistaken for a
// resume.
func (l *Ledger) EnsureRun(ctx context.Context, runID string, at time.Time) error {
	err := l.store.Create(ctx, rerun.Run{
		ID:       runID,
		Workflow: workflowName,
		Status:   rerun.Running,
		Created:  at,
	})
	if err == nil {
		return nil
	}
	// A duplicate run id (a resume) surfaces as rerun.ErrRunExists. Only treat the
	// failure as a resume when it is that and the journal is readable; any other
	// Create error (a real write failure, e.g. a full disk) is returned so it
	// fails closed rather than being mistaken for a resume.
	if errors.Is(err, rerun.ErrRunExists) {
		if _, loadErr := l.store.LoadLogs(ctx, runID); loadErr == nil {
			return nil
		}
	}
	return fmt.Errorf("create run %s: %w", runID, err)
}

// AppendCall records one governed call at the given call index. The payload is
// the accounting-only CallRecord; no bodies are stored.
func (l *Ledger) AppendCall(ctx context.Context, runID string, callIndex int64, rec policy.CallRecord) error {
	_, err := l.AppendCallAt(ctx, runID, callIndex, rec, -1)
	return err
}

// AppendCallAt records a call, trying journal position hintSeq first (the warm
// path's O(1) append) and falling back to re-reading the journal to sequence
// when hintSeq is negative or already taken. It returns the seq actually used.
func (l *Ledger) AppendCallAt(ctx context.Context, runID string, callIndex int64, rec policy.CallRecord, hintSeq int) (int, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("marshal call record: %w", err)
	}
	tag := fmt.Sprintf("%s%d", tagCallPrefix, callIndex)
	return l.appendAt(ctx, runID, tag, payload, rec.At, hintSeq)
}

// AppendKill records a durable kill and, best-effort, sets the fast cancel flag.
// The journal entry is authoritative (folded on any reload); the flag lets a
// warm governor observe the kill without a full reload. A flag write failure is
// ignored because the journal entry already guarantees enforcement.
func (l *Ledger) AppendKill(ctx context.Context, runID string, at time.Time) error {
	if err := l.appendNext(ctx, runID, tagKill, nil, at); err != nil {
		return err
	}
	if c, ok := l.store.(rerun.Canceller); ok {
		_ = c.RequestCancel(ctx, runID)
	}
	return nil
}

// CancelRequested reports whether a durable cancel is pending. supported is
// false when the store has no Canceller, signalling the warm path to fall back
// to a full journal reload.
func (l *Ledger) CancelRequested(ctx context.Context, runID string) (requested, supported bool, err error) {
	c, ok := l.store.(rerun.Canceller)
	if !ok {
		return false, false, nil
	}
	requested, err = c.CancelRequested(ctx, runID)
	if err != nil {
		return false, true, fmt.Errorf("cancel requested for run %s: %w", runID, err)
	}
	return requested, true, nil
}

// stopPayload is the accounting snapshot recorded when a run stops.
type stopPayload struct {
	Reason      string  `json:"reason"`
	Calls       int64   `json:"calls"`
	TokenCost   float64 `json:"token_cost"`
	ComputeCost float64 `json:"compute_cost"`
	TotalCost   float64 `json:"total_cost"`
}

// AppendStop records the terminal stop with its reason and frozen final totals.
// The caller records a stop exactly once (guarded by the loaded StopReason).
func (l *Ledger) AppendStop(ctx context.Context, runID string, s *policy.State, at time.Time) error {
	payload, err := json.Marshal(stopPayload{
		Reason:      s.StopReason,
		Calls:       s.Calls,
		TokenCost:   s.TokenCost,
		ComputeCost: s.ComputeCost,
		TotalCost:   s.TotalCost,
	})
	if err != nil {
		return fmt.Errorf("marshal stop record: %w", err)
	}
	return l.appendNext(ctx, runID, tagStop, payload, at)
}

// Finish marks a run terminal in the runs table, retiring it from the active
// list. ok true records Done, false records Failed.
func (l *Ledger) Finish(ctx context.Context, runID string, ok bool) error {
	status := rerun.Done
	if !ok {
		status = rerun.Failed
	}
	if err := l.store.Finish(ctx, runID, status); err != nil {
		return fmt.Errorf("finish run %s: %w", runID, err)
	}
	return nil
}

// pingProbeRun is a sentinel run id the write probe targets.
const pingProbeRun = "__leash_readyz_probe__"

// Ping confirms the ledger is WRITABLE, backing the admin /readyz check and the
// proxy's fail-closed recovery. It must exercise a write, not just a read: on a
// full or read-only-remounted disk, reads still succeed while every governed
// append fails, so a read probe would report healthy while governance is broken.
// Finish on a sentinel run is a harmless idempotent write (it updates zero rows
// when the run does not exist) that fails when the store cannot write.
func (l *Ledger) Ping(ctx context.Context) error {
	if err := l.store.Finish(ctx, pingProbeRun, rerun.Done); err != nil {
		return fmt.Errorf("ledger write probe: %w", err)
	}
	return nil
}

// Incomplete lists the runs still active (Pending or Running), which is the set
// `leash ps` folds and displays.
func (l *Ledger) Incomplete(ctx context.Context) ([]rerun.Run, error) {
	runs, err := l.store.Incomplete(ctx)
	if err != nil {
		return nil, fmt.Errorf("list incomplete runs: %w", err)
	}
	return runs, nil
}

// Entry kinds for a decoded journal record.
const (
	KindCall = "call"
	KindKill = "kill"
	KindStop = "stop"
)

// Entry is a decoded, presentation-facing journal record for `leash inspect`.
type Entry struct {
	// Seq is the journal position.
	Seq int
	// Tag is the raw journal tag.
	Tag string
	// At is when the record was written.
	At time.Time
	// Kind is one of KindCall, KindKill, KindStop.
	Kind string
	// Usage is set for call records.
	Usage policy.Usage
	// Fingerprint is set for call records.
	Fingerprint string
	// Reason is set for stop records.
	Reason string
}

// Entries returns a run's journal decoded for display, in sequence order.
func (l *Ledger) Entries(ctx context.Context, runID string) ([]Entry, error) {
	logs, err := l.store.LoadLogs(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load journal for run %s: %w", runID, err)
	}
	out := make([]Entry, 0, len(logs))
	for _, lg := range logs {
		e := Entry{Seq: lg.Seq, Tag: lg.Tag, At: lg.At}
		switch {
		case strings.HasPrefix(lg.Tag, tagCallPrefix):
			e.Kind = KindCall
			var rec policy.CallRecord
			if err := json.Unmarshal(lg.Payload, &rec); err == nil {
				e.Usage = rec.Usage
				e.Fingerprint = rec.Fingerprint
			}
		case lg.Tag == tagKill:
			e.Kind = KindKill
		case lg.Tag == tagStop:
			e.Kind = KindStop
			var sp stopPayload
			if err := json.Unmarshal(lg.Payload, &sp); err == nil {
				e.Reason = sp.Reason
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// RawLogs returns a run's journal entries in sequence order. It backs
// `leash inspect` and lets tests confirm that only accounting, never a secret
// or a body, was ever persisted.
func (l *Ledger) RawLogs(ctx context.Context, runID string) ([]rerun.Log, error) {
	logs, err := l.store.LoadLogs(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load journal for run %s: %w", runID, err)
	}
	return logs, nil
}

// Load rebuilds a run's state by folding its journal under the governor's
// prices. Folding is deterministic, so loading the same journal twice yields
// identical totals: a call is counted exactly when its log exists, never twice.
func (l *Ledger) Load(ctx context.Context, runID string, g *policy.Governor) (*policy.State, error) {
	s, _, err := l.LoadAt(ctx, runID, g)
	return s, err
}

// LoadAt is Load plus the last journal seq (or -1 when empty), so the warm path
// can seed its append hint and skip the re-read to sequence.
func (l *Ledger) LoadAt(ctx context.Context, runID string, g *policy.Governor) (*policy.State, int, error) {
	logs, err := l.store.LoadLogs(ctx, runID)
	if err != nil {
		return nil, -1, fmt.Errorf("load journal for run %s: %w", runID, err)
	}
	s := &policy.State{RunID: runID}
	lastSeq := -1
	for _, lg := range logs {
		lastSeq = lg.Seq
		switch {
		case strings.HasPrefix(lg.Tag, tagCallPrefix):
			var rec policy.CallRecord
			if err := json.Unmarshal(lg.Payload, &rec); err != nil {
				return nil, -1, fmt.Errorf("decode call log %s seq %d: %w", runID, lg.Seq, err)
			}
			g.Fold(s, rec)
		case lg.Tag == tagKill:
			s.Killed = true
		case lg.Tag == tagStop:
			var sp stopPayload
			if err := json.Unmarshal(lg.Payload, &sp); err != nil {
				return nil, -1, fmt.Errorf("decode stop log %s seq %d: %w", runID, lg.Seq, err)
			}
			// Freeze the time-derived costs at their stop-time snapshot; token
			// cost from folding the calls is already authoritative.
			s.StopReason = sp.Reason
			s.ComputeCost = sp.ComputeCost
			s.TotalCost = s.TokenCost + sp.ComputeCost
		}
	}
	return s, lastSeq, nil
}

// appendNext writes one journal entry at the next free sequence, re-reading the
// journal to find it.
func (l *Ledger) appendNext(ctx context.Context, runID, tag string, payload []byte, at time.Time) error {
	_, err := l.appendAt(ctx, runID, tag, payload, at, -1)
	return err
}

// appendAt writes one journal entry, returning the seq used. A non-negative
// hintSeq is tried first (an O(1) append); on a uniqueness conflict, or when
// hintSeq is negative, it re-reads the journal for the next free position and
// retries a bounded number of times. Any non-conflict error is returned at once.
func (l *Ledger) appendAt(ctx context.Context, runID, tag string, payload []byte, at time.Time, hintSeq int) (int, error) {
	if hintSeq >= 0 {
		err := l.store.Append(ctx, runID, rerun.Log{Seq: hintSeq, Tag: tag, Payload: payload, At: at})
		if err == nil {
			return hintSeq, nil
		}
		if !errors.Is(err, rerun.ErrSeqConflict) {
			return 0, fmt.Errorf("append %s to run %s: %w", tag, runID, err)
		}
		// The hint was taken by a concurrent writer; fall back to a re-read.
	}
	var lastErr error
	for range appendRetries {
		logs, err := l.store.LoadLogs(ctx, runID)
		if err != nil {
			return 0, fmt.Errorf("load journal to sequence append for run %s: %w", runID, err)
		}
		seq := 0
		if n := len(logs); n > 0 {
			seq = logs[n-1].Seq + 1 // LoadLogs returns rows in sequence order.
		}
		err = l.store.Append(ctx, runID, rerun.Log{Seq: seq, Tag: tag, Payload: payload, At: at})
		if err == nil {
			return seq, nil
		}
		if errors.Is(err, rerun.ErrSeqConflict) {
			lastErr = err
			continue
		}
		return 0, fmt.Errorf("append %s to run %s: %w", tag, runID, err)
	}
	return 0, fmt.Errorf("append %s to run %s: exhausted %d attempts racing for a journal position: %w",
		tag, runID, appendRetries, lastErr)
}
