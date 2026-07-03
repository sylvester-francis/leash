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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
	"github.com/sylvester-francis/rerun"
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
}

// Lease is a held run lease. Release it when governing ends.
type Lease struct {
	release io.Closer
}

// Release returns the lease. It is safe to call once.
func (le *Lease) Release() error {
	if le == nil || le.release == nil {
		return nil
	}
	return le.release.Close()
}

// Open opens (or creates) a SQLite-backed ledger at path, creating the parent
// directory if needed. The SQLite backend panics on open failure; Open recovers
// that into an ordinary error so callers never see a panic.
func Open(path string) (l *Ledger, err error) {
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
	return &Ledger{store: store, closer: store}, nil
}

// Close releases the underlying store.
func (l *Ledger) Close() error {
	if l.closer == nil {
		return nil
	}
	return l.closer.Close()
}

// Acquire tries to lease the run for governing. It is non-blocking: acquired is
// false when the lease is already held. For the SQLite backend the lease is
// process-local (it guards against governing the same run twice inside one
// process); cross-process mutual exclusion requires the postgres backend.
func (l *Ledger) Acquire(ctx context.Context, runID string) (*Lease, bool, error) {
	release, acquired, err := l.store.Acquire(ctx, runID)
	if err != nil {
		return nil, false, fmt.Errorf("acquire lease for run %s: %w", runID, err)
	}
	if !acquired {
		return nil, false, nil
	}
	return &Lease{release: release}, true, nil
}

// EnsureRun creates the run if it is new. Resuming an existing run is not an
// error: because Create fails only on a healthy database when the run already
// exists, a Create failure paired with a working read is treated as a resume.
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
	// The run may already exist (a resume). Probe with a read: if the database
	// is healthy enough to list the journal, the Create failure was a duplicate.
	if _, loadErr := l.store.LoadLogs(ctx, runID); loadErr == nil {
		return nil
	}
	return fmt.Errorf("create run %s: %w", runID, err)
}

// AppendCall records one governed call at the given call index. The payload is
// the accounting-only CallRecord; no bodies are stored.
func (l *Ledger) AppendCall(ctx context.Context, runID string, callIndex int64, rec policy.CallRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal call record: %w", err)
	}
	tag := fmt.Sprintf("%s%d", tagCallPrefix, callIndex)
	return l.appendNext(ctx, runID, tag, payload, rec.At)
}

// AppendKill records a durable kill. A subsequent load folds it into the state
// so the next governed call trips the kill switch. It works from a second
// process against the same database.
func (l *Ledger) AppendKill(ctx context.Context, runID string, at time.Time) error {
	return l.appendNext(ctx, runID, tagKill, nil, at)
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

// pingProbeRun is a sentinel run id whose (always empty) journal a readiness
// probe loads to confirm the ledger is reachable without listing real runs.
const pingProbeRun = "__leash_readyz_probe__"

// Ping performs a cheap durable read to confirm the ledger is reachable. It
// backs the admin /readyz check: loading a sentinel run's (empty) journal
// touches the store without depending on how many real runs exist.
func (l *Ledger) Ping(ctx context.Context) error {
	if _, err := l.store.LoadLogs(ctx, pingProbeRun); err != nil {
		return fmt.Errorf("ledger ping: %w", err)
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
	logs, err := l.store.LoadLogs(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load journal for run %s: %w", runID, err)
	}
	s := &policy.State{RunID: runID}
	for _, lg := range logs {
		switch {
		case strings.HasPrefix(lg.Tag, tagCallPrefix):
			var rec policy.CallRecord
			if err := json.Unmarshal(lg.Payload, &rec); err != nil {
				return nil, fmt.Errorf("decode call log %s seq %d: %w", runID, lg.Seq, err)
			}
			g.Fold(s, rec)
		case lg.Tag == tagKill:
			s.Killed = true
		case lg.Tag == tagStop:
			var sp stopPayload
			if err := json.Unmarshal(lg.Payload, &sp); err != nil {
				return nil, fmt.Errorf("decode stop log %s seq %d: %w", runID, lg.Seq, err)
			}
			// Freeze the time-derived costs at their stop-time snapshot; token
			// cost from folding the calls is already authoritative.
			s.StopReason = sp.Reason
			s.ComputeCost = sp.ComputeCost
			s.TotalCost = s.TokenCost + sp.ComputeCost
		}
	}
	return s, nil
}

// appendNext writes one journal entry at the next free sequence. The sequence
// is max(seq)+1 read from the journal; a uniqueness conflict means another
// writer took that position, so it re-reads and retries a bounded number of
// times. Any other error is returned immediately.
func (l *Ledger) appendNext(ctx context.Context, runID, tag string, payload []byte, at time.Time) error {
	var lastErr error
	for range appendRetries {
		logs, err := l.store.LoadLogs(ctx, runID)
		if err != nil {
			return fmt.Errorf("load journal to sequence append for run %s: %w", runID, err)
		}
		seq := 0
		if n := len(logs); n > 0 {
			seq = logs[n-1].Seq + 1 // LoadLogs returns rows in sequence order.
		}
		err = l.store.Append(ctx, runID, rerun.Log{Seq: seq, Tag: tag, Payload: payload, At: at})
		if err == nil {
			return nil
		}
		if isSequenceConflict(err) {
			lastErr = err
			continue
		}
		return fmt.Errorf("append %s to run %s: %w", tag, runID, err)
	}
	return fmt.Errorf("append %s to run %s: exhausted %d attempts racing for a journal position: %w",
		tag, runID, appendRetries, lastErr)
}

// isSequenceConflict reports whether err is a journal primary-key collision,
// which means a concurrent writer claimed the same sequence number.
func isSequenceConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "constraint") || strings.Contains(msg, "unique")
}
