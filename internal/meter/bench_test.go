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

package meter

import (
	"io"
	"strings"
	"testing"
)

// BenchmarkStreamMeter measures tee-and-meter throughput over an SSE stream.
// SetBytes reports it as MB/s so the streaming overhead is legible.
func BenchmarkStreamMeter(b *testing.B) {
	stream := strings.Repeat(openAIStream, 100)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m := NewStreamMeter(OpenAI)
		if err := m.Tee(io.Discard, strings.NewReader(stream)); err != nil {
			b.Fatalf("tee: %v", err)
		}
	}
}
