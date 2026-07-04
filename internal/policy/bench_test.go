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

package policy

import (
	"testing"
	"time"
)

// BenchmarkFold measures folding a fixed-size journal into fresh state, the work
// a cold load does to rebuild a run's totals.
func BenchmarkFold(b *testing.B) {
	const n = 1000
	g := NewGovernor(Limits{}, PriceTable{"gpt-4o": {InputPerM: 2, OutputPerM: 8}}, 0)
	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	recs := make([]CallRecord, n)
	for i := range recs {
		recs[i] = CallRecord{
			Usage: Usage{Model: "gpt-4o", InputTokens: int64(10 + i), OutputTokens: int64(5 + i)},
			At:    base.Add(time.Duration(i) * time.Second),
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s := &State{RunID: "bench"}
		for j := range recs {
			g.Fold(s, recs[j])
		}
	}
}
