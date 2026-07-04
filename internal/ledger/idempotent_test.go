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

package ledger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

// TestAppendIsIdempotentByTag reproduces the committed-but-errored write: an
// append that reaches the store but whose caller retries it. Because a call
// record's tag is unique, the retry must return the existing position rather
// than write a duplicate that a later fold would count again.
func TestAppendIsIdempotentByTag(t *testing.T) {
	ctx := context.Background()
	l, err := Open(filepath.Join(t.TempDir(), "leash.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()
	if err := l.EnsureRun(ctx, "r1", time.Now()); err != nil {
		t.Fatalf("ensure run: %v", err)
	}

	rec := policy.CallRecord{Usage: policy.Usage{Model: "m", InputTokens: 10, OutputTokens: 5}, At: time.Now()}

	// Record call index 0.
	seq, err := l.AppendCallAt(ctx, "r1", 0, rec, -1)
	if err != nil {
		t.Fatalf("append call 0: %v", err)
	}

	// Retry the same call with a hint that collides with the record already
	// there (the warm-path retry after a committed-but-errored write).
	seq2, err := l.AppendCallAt(ctx, "r1", 0, rec, seq)
	if err != nil {
		t.Fatalf("retry with colliding hint: %v", err)
	}
	if seq2 != seq {
		t.Fatalf("retry returned seq %d, want the existing %d", seq2, seq)
	}
	// Retry again via the direct re-read path (no hint).
	if _, err := l.AppendCallAt(ctx, "r1", 0, rec, -1); err != nil {
		t.Fatalf("retry via re-read: %v", err)
	}

	// A genuinely new call index still records.
	if _, err := l.AppendCallAt(ctx, "r1", 1, rec, -1); err != nil {
		t.Fatalf("append call 1: %v", err)
	}

	st, err := l.Load(ctx, "r1", policy.NewGovernor(policy.Limits{}, nil, 0))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Without idempotency the two retries would each duplicate call 0, so the
	// fold would report 4 calls; with it, call 0 is counted once, plus call 1.
	if st.Calls != 2 {
		t.Fatalf("Calls = %d, want 2 (call 0 recorded once despite two retries, plus call 1)", st.Calls)
	}
}
