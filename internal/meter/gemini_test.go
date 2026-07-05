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

func TestDetectProviderGemini(t *testing.T) {
	for _, path := range []string{
		"/v1beta/models/gemini-2.5-flash:generateContent",
		"/v1beta/models/gemini-2.5-flash:streamGenerateContent",
	} {
		if got := DetectProvider(path, nil); got != Gemini {
			t.Fatalf("DetectProvider(%q) = %v, want Gemini", path, got)
		}
	}
	// The OpenAI-compatible Gemini endpoint is a /chat/completions path and is
	// metered as OpenAI, not Gemini.
	if got := DetectProvider("/v1beta/openai/chat/completions", nil); got != OpenAI {
		t.Fatalf("Gemini OpenAI-compat path detected as %v, want OpenAI", got)
	}
}

func TestParseUsageJSONGemini(t *testing.T) {
	// candidatesTokenCount includes thoughtsTokenCount on the Gemini API, and
	// cachedContentTokenCount is a subset of promptTokenCount.
	body := []byte(`{
		"candidates": [{"content": {"role": "model", "parts": [{"text": "Hello from "}, {"text": "Gemini"}]}}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 8,
			"thoughtsTokenCount": 3, "cachedContentTokenCount": 4, "totalTokenCount": 18},
		"modelVersion": "gemini-2.5-flash"
	}`)
	res, err := ParseUsageJSON(Gemini, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	want := policy.Usage{
		Model: "gemini-2.5-flash", InputTokens: 10, CachedReadTokens: 4,
		OutputTokens: 8, ReasoningTokens: 3,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello from Gemini") {
		t.Fatalf("Fingerprint did not match the concatenated candidate parts")
	}
}

func TestParseUsageJSONGeminiNoUsageIsBlind(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.5-flash"}`)
	res, err := ParseUsageJSON(Gemini, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if res.HasUsage {
		t.Fatalf("HasUsage = true, want false when usageMetadata is absent")
	}
}

const geminiStream = `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"modelVersion":"gemini-2.5-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}

data: {"candidates":[{"content":{"parts":[{"text":" world"}]}}],"modelVersion":"gemini-2.5-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"cachedContentTokenCount":4,"totalTokenCount":15}}

`

func TestStreamMeterGemini(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(Gemini)
	if err := m.Tee(&dst, strings.NewReader(geminiStream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	if dst.String() != geminiStream {
		t.Fatalf("streamed bytes were altered")
	}
	res := m.Result()
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	// The last usageMetadata (cumulative) wins.
	want := policy.Usage{
		Model: "gemini-2.5-flash", InputTokens: 10, CachedReadTokens: 4,
		OutputTokens: 5, ReasoningTokens: 2,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello world") {
		t.Fatalf("Fingerprint did not match streamed text")
	}
}

// TestGeminiThoughtsNotDoubleCounted proves the cost mapping: thoughts are a
// subset of candidates (output), so they are priced once. With reasoning priced
// below output, the same call must cost less than pricing all output at the
// output rate, and never more.
func TestGeminiThoughtsNotDoubleCounted(t *testing.T) {
	body := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "x"}]}}],
		"usageMetadata": {"promptTokenCount": 0, "candidatesTokenCount": 1000, "thoughtsTokenCount": 400, "totalTokenCount": 1000},
		"modelVersion": "gemini-2.5-flash"
	}`)
	res, _ := ParseUsageJSON(Gemini, body)
	table := policy.PriceTable{"gemini-2.5-flash": {InputPerM: 1, OutputPerM: 10, ReasoningPerM: 5}}
	// 600 non-reasoning output @ $10/M + 400 reasoning @ $5/M = 0.006 + 0.002.
	got := policy.TokenCost(res.Usage, table)
	const want = 600.0/1_000_000*10 + 400.0/1_000_000*5
	if got != want {
		t.Fatalf("TokenCost = %v, want %v (thoughts priced once at the reasoning rate)", got, want)
	}
}
