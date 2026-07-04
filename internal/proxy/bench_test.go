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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvester-francis/leash/internal/ledger"
	"github.com/sylvester-francis/leash/internal/policy"
)

// benchUpstream returns a fixed non-streaming OpenAI response with small usage.
func benchUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
}

// seedJournal appends n call records to run under a fresh ledger at db.
func seedJournal(b *testing.B, db, run string, n int) {
	b.Helper()
	l, err := ledger.Open(db)
	if err != nil {
		b.Fatalf("open ledger: %v", err)
	}
	defer l.Close()
	ctx := context.Background()
	at := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	if err := l.EnsureRun(ctx, run, at); err != nil {
		b.Fatalf("ensure run: %v", err)
	}
	rec := policy.CallRecord{Usage: policy.Usage{Model: "gpt-4o", InputTokens: 10, OutputTokens: 5}, At: at}
	for i := range n {
		if err := l.AppendCall(ctx, run, int64(i), rec); err != nil {
			b.Fatalf("seed append %d: %v", i, err)
		}
	}
}

// BenchmarkGovernedCall measures one end-to-end governed call at pre-seeded
// journal sizes, exposing per-call overhead as the journal grows.
func BenchmarkGovernedCall(b *testing.B) {
	for _, n := range []int{0, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("journal-%d", n), func(b *testing.B) {
			const run = "bench"
			db := filepath.Join(b.TempDir(), "leash.db")
			seedJournal(b, db, run, n)

			l, err := ledger.Open(db)
			if err != nil {
				b.Fatalf("open ledger: %v", err)
			}
			defer l.Close()
			up := benchUpstream()
			defer up.Close()
			upURL, _ := url.Parse(up.URL)

			p, err := New(Config{
				Ledger:   l,
				Governor: policy.NewGovernor(policy.Limits{}, nil, 0), // no boundaries: never stops
				Upstream: upURL,
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				b.Fatalf("new proxy: %v", err)
			}
			defer p.Shutdown()
			front := httptest.NewServer(p)
			defer front.Close()

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				req, _ := http.NewRequest(http.MethodPost, front.URL+chatPath, strings.NewReader(`{"model":"gpt-4o"}`))
				req.Header.Set("X-Loop-Id", run)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					b.Fatalf("call: %v", err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		})
	}
}
