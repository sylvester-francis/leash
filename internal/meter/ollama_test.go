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
	"bytes"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/policy"
)

func TestDetectProviderOllama(t *testing.T) {
	for _, path := range []string{
		"/api/chat",
		"/api/generate",
	} {
		if got := DetectProvider(path, nil); got != Ollama {
			t.Fatalf("DetectProvider(%q) = %v, want Ollama", path, got)
		}
	}
	// The OpenAI-compatible Ollama endpoint is a /v1/chat/completions path and
	// is metered as OpenAI, not Ollama.
	if got := DetectProvider("/v1/chat/completions", nil); got != OpenAI {
		t.Fatalf("Ollama OpenAI-compat path detected as %v, want OpenAI", got)
	}
}

func TestParseUsageJSONOllama(t *testing.T) {
	body := []byte(`{
		"model": "llama3.2",
		"created_at": "2024-01-01T00:00:00Z",
		"message": {"role": "assistant", "content": "Hello from Ollama"},
		"done": true,
		"total_duration": 12345,
		"load_duration": 123,
		"prompt_eval_count": 10,
		"prompt_eval_duration": 500,
		"eval_count": 20,
		"eval_duration": 1000
	}`)
	res, err := ParseUsageJSON(Ollama, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	want := policy.Usage{
		Model: "llama3.2", InputTokens: 10, OutputTokens: 20,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello from Ollama") {
		t.Fatalf("Fingerprint did not match assistant text")
	}
}

func TestParseUsageJSONOllamaNoUsageIsBlind(t *testing.T) {
	body := []byte(`{
		"model": "llama3.2",
		"message": {"role": "assistant", "content": "Hi"},
		"done": true
	}`)
	res, err := ParseUsageJSON(Ollama, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if res.HasUsage {
		t.Fatalf("HasUsage = true, want false when usage fields are absent")
	}
}

const ollamaStream = `data: {"model":"llama3.2","created_at":"2024-01-01T00:00:00Z","message":{"role":"assistant","content":"Hello"},"done":false}

data: {"model":"llama3.2","created_at":"2024-01-01T00:00:00Z","message":{"role":"assistant","content":" from"},"done":false}

data: {"model":"llama3.2","created_at":"2024-01-01T00:00:00Z","message":{"role":"assistant","content":" Ollama"},"done":true,"total_duration":12345,"prompt_eval_count":10,"eval_count":3}

`

func TestStreamMeterOllama(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(Ollama)
	if err := m.Tee(&dst, strings.NewReader(ollamaStream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	if dst.String() != ollamaStream {
		t.Fatalf("streamed bytes were altered: got %q want %q", dst.String(), ollamaStream)
	}
	res := m.Result()
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	want := policy.Usage{
		Model: "llama3.2", InputTokens: 10, OutputTokens: 3,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello from Ollama") {
		t.Fatalf("Fingerprint did not match streamed text")
	}
}

func TestStreamMeterOllamaNoUsageIsBlind(t *testing.T) {
	stream := `data: {"model":"llama3.2","message":{"role":"assistant","content":"hi"},"done":true}

`
	var dst bytes.Buffer
	m := NewStreamMeter(Ollama)
	if err := m.Tee(&dst, strings.NewReader(stream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	res := m.Result()
	if res.HasUsage {
		t.Fatalf("HasUsage = true, want false when stream has no usage fields")
	}
	if res.Usage.TotalTokens() != 0 {
		t.Fatalf("blind stream usage should be zero, got %+v", res.Usage)
	}
}
