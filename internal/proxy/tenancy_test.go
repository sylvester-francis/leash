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
	"net/http"
	"testing"

	"github.com/sylvester-francis/leash/internal/policy"
)

// TestTenancyIsolatesRunsByCredential proves one tenant cannot burn or observe
// another tenant's budget by naming the same run id: with auth on, a run id is
// scoped to the presenting credential.
func TestTenancyIsolatesRunsByCredential(t *testing.T) {
	front, up, _ := buildProxy(t, func(c *Config) {
		c.AuthTokens = []string{"tenant-a-token", "tenant-b-token"}
		c.Governor = policy.NewGovernor(policy.Limits{MaxCalls: 1}, nil, 0)
	})

	a := http.Header{"X-Leash-Token": {"tenant-a-token"}, "X-Loop-Id": {"shared"}}
	b := http.Header{"X-Leash-Token": {"tenant-b-token"}, "X-Loop-Id": {"shared"}}

	// Tenant A spends its single call, then is refused.
	if code, _ := postBody(t, front, a, `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("A first call = %d, want 200", code)
	}
	if code, _ := postBody(t, front, a, `{"model":"gpt-4o"}`); code != http.StatusTooManyRequests {
		t.Fatalf("A second call = %d, want 429 (A exhausted)", code)
	}

	// Tenant B names the same run id but has its own budget - it still works.
	if code, _ := postBody(t, front, b, `{"model":"gpt-4o"}`); code != http.StatusOK {
		t.Fatalf("B call under the same run id = %d, want 200 (isolated budget)", code)
	}
	if up.count() != 2 {
		t.Fatalf("upstream saw %d calls, want 2 (A once, B once)", up.count())
	}
}
