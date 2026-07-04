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

package proxy

import (
	"context"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// Ledger is the durable account the proxy governs against. *ledger.Ledger
// satisfies it; depending on the interface (not the concrete type) lets a test
// substitute a fake to exercise the write-failure paths.
type Ledger interface {
	// Acquire leases the ledger for governing; acquired is false when held.
	Acquire(ctx context.Context, runID string) (*ledger.Lease, bool, error)
	// EnsureRun creates the run if new, or resumes an existing one.
	EnsureRun(ctx context.Context, runID string, at time.Time) error
	// LoadAt folds the journal into state and returns the last journal seq.
	LoadAt(ctx context.Context, runID string, g *policy.Governor) (*policy.State, int, error)
	// CancelRequested reports a pending durable cancel; supported is false when
	// the store cannot serve the fast flag.
	CancelRequested(ctx context.Context, runID string) (requested, supported bool, err error)
	// AppendCallAt records a call at the hinted seq, returning the seq used.
	AppendCallAt(ctx context.Context, runID string, callIndex int64, rec policy.CallRecord, hintSeq int) (int, error)
	// AppendStop records a run's terminal stop.
	AppendStop(ctx context.Context, runID string, s *policy.State, at time.Time) error
	// Ping confirms the ledger is writable (see ledger.Ledger.Ping).
	Ping(ctx context.Context) error
}
