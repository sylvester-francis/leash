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

//go:build unix

package ledger

import (
	"context"
	"path/filepath"
	"testing"
)

// TestSQLiteGovernorIsSingleAcrossHandles proves the cross-process guarantee: two
// separate ledger handles on one SQLite file - which each have their own
// in-process lease and would both govern without the OS lock - cannot both
// acquire. The second is refused until the first releases.
func TestSQLiteGovernorIsSingleAcrossHandles(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "leash.db")

	a, err := Open(db)
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	defer a.Close()
	b, err := Open(db)
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	defer b.Close()

	leaseA, okA, err := a.Acquire(ctx, "leash-governor")
	if err != nil || !okA {
		t.Fatalf("A acquire = (%v, %v), want acquired", okA, err)
	}
	if _, okB, _ := b.Acquire(ctx, "leash-governor"); okB {
		t.Fatalf("B acquired the same SQLite ledger while A holds it (double-governor)")
	}

	// After A releases, B can take over.
	if err := leaseA.Release(); err != nil {
		t.Fatalf("A release: %v", err)
	}
	leaseB, okB, err := b.Acquire(ctx, "leash-governor")
	if err != nil || !okB {
		t.Fatalf("B acquire after A released = (%v, %v), want acquired", okB, err)
	}
	_ = leaseB.Release()
}
