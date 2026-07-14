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
	if got := DetectProvider("/api/chat/completions", nil); got != OpenAI {
		t.Fatalf("DetectProvider(/api/chat/completions) = %v, want OpenAI (Open WebUI compat path must not flip to Ollama)", got)
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

// ollamaStream is a real NDJSON stream from Ollama's native /api/chat endpoint.
// Each line is a bare JSON object; there is no data: prefix.
const ollamaStream = "{\"model\":\"llama3.2\",\"created_at\":\"2024-01-01T00:00:00Z\",\"message\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"done\":false}\n{\"model\":\"llama3.2\",\"created_at\":\"2024-01-01T00:00:00Z\",\"message\":{\"role\":\"assistant\",\"content\":\" from\"},\"done\":false}\n{\"model\":\"llama3.2\",\"created_at\":\"2024-01-01T00:00:00Z\",\"message\":{\"role\":\"assistant\",\"content\":\" Ollama\"},\"done\":true,\"total_duration\":12345,\"prompt_eval_count\":10,\"eval_count\":3}\n"

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
	stream := "{\"model\":\"llama3.2\",\"message\":{\"role\":\"assistant\",\"content\":\"hi\"},\"done\":true}\n"
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

func TestParseUsageJSONOllamaGenerate(t *testing.T) {
	body := []byte(`{
		"model": "llama3.2",
		"created_at": "2024-01-01T00:00:00Z",
		"response": "Hello from generate",
		"done": true,
		"total_duration": 12345,
		"prompt_eval_count": 15,
		"eval_count": 25
	}`)
	res, err := ParseUsageJSON(Ollama, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	want := policy.Usage{
		Model: "llama3.2", InputTokens: 15, OutputTokens: 25,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello from generate") {
		t.Fatalf("Fingerprint = %q, want %q", res.Fingerprint, "Hello from generate")
	}
}

// generateStream is a real NDJSON stream from Ollama's /api/generate endpoint.
// Chunks carry the top-level "response" field, not "message.content".
const generateStream = "{\"model\":\"llama3.2\",\"created_at\":\"2024-01-01T00:00:00Z\",\"response\":\"Hello\",\"done\":false}\n{\"model\":\"llama3.2\",\"created_at\":\"2024-01-01T00:00:00Z\",\"response\":\" world\",\"done\":true,\"total_duration\":12345,\"prompt_eval_count\":8,\"eval_count\":4}\n"

func TestStreamMeterOllamaGenerate(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(Ollama)
	if err := m.Tee(&dst, strings.NewReader(generateStream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	if dst.String() != generateStream {
		t.Fatalf("streamed bytes were altered: got %q want %q", dst.String(), generateStream)
	}
	res := m.Result()
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	want := policy.Usage{
		Model: "llama3.2", InputTokens: 8, OutputTokens: 4,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello world") {
		t.Fatalf("Fingerprint = %q, want %q", res.Fingerprint, "Hello world")
	}
}
