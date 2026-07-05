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
	"fmt"
	"path/filepath"
	"testing"
	"testing/quick"
	"time"

	"github.com/sylvester-francis/leash/internal/policy"
)

// TestPropAtMostOnce is the at-most-once invariant as a property: for a random
// number of distinct calls and a random pattern of retries of already-written
// calls (the committed-but-errored write, replayed), the folded call count is
// exactly the number of distinct calls. A duplicate would inflate it.
func TestPropAtMostOnce(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	g := policy.NewGovernor(policy.Limits{}, nil, 0)
	rec := policy.CallRecord{Usage: policy.Usage{Model: "m"}, At: time.Now()}
	iter := 0

	f := func(nRaw uint8, retries []uint8) bool {
		iter++
		n := int(nRaw%15) + 1 // 1..15 distinct calls
		l, err := Open(filepath.Join(dir, fmt.Sprintf("p%d.db", iter)))
		if err != nil {
			return false
		}
		defer l.Close()
		if err := l.EnsureRun(ctx, "r", time.Now()); err != nil {
			return false
		}
		for i := range n {
			if _, err := l.AppendCallAt(ctx, "r", int64(i), rec, -1); err != nil {
				return false
			}
		}
		// Retries of already-written indexes must be idempotent, not duplicated.
		for _, raw := range retries {
			if _, err := l.AppendCallAt(ctx, "r", int64(int(raw)%n), rec, -1); err != nil {
				return false
			}
		}
		st, err := l.Load(ctx, "r", g)
		if err != nil {
			return false
		}
		return st.Calls == int64(n)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
}
