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

// TestParseUsageJSONOpenAIResponses covers the OpenAI Responses API shape, which
// reports input_tokens/output_tokens and text in output[].content[] - previously
// metered as a silent $0 because only the chat/completions shape was parsed.
func TestParseUsageJSONOpenAIResponses(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"output": [{"type":"message","content":[{"type":"output_text","text":"Hello there"}]}],
		"usage": {"input_tokens": 10, "output_tokens": 5,
			"output_tokens_details": {"reasoning_tokens": 3}}
	}`)
	res, err := ParseUsageJSON(OpenAI, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON: %v", err)
	}
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true for a Responses body")
	}
	want := policy.Usage{Model: "gpt-4o", InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello there") {
		t.Fatalf("Fingerprint did not match the Responses output text")
	}
}

// TestParseUsageJSONMisTagIsBlind: an OpenAI chat body parsed as Anthropic (the
// Anthropic-Version mis-tag evasion) must be blind, not a silent $0 with
// HasUsage=true. The usage object is present but carries none of Anthropic's
// expected fields.
func TestParseUsageJSONMisTagIsBlind(t *testing.T) {
	openAIBody := []byte(`{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}],
		"usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	res, err := ParseUsageJSON(Anthropic, openAIBody)
	if err != nil {
		t.Fatalf("ParseUsageJSON: %v", err)
	}
	if res.HasUsage {
		t.Fatalf("mis-tagged body reported HasUsage=true (silent $0 evasion); want blind")
	}
	if res.Usage.TotalTokens() != 0 {
		t.Fatalf("mis-tagged body produced nonzero usage: %+v", res.Usage)
	}
}

// TestStreamMeterOpenAIResponses covers the Responses API SSE event shape.
func TestStreamMeterOpenAIResponses(t *testing.T) {
	stream := `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.completed
data: {"type":"response.completed","response":{"model":"gpt-4o","usage":{"input_tokens":11,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":1}}}}

data: [DONE]

`
	var dst bytes.Buffer
	m := NewStreamMeter(OpenAI)
	if err := m.Tee(&dst, strings.NewReader(stream)); err != nil {
		t.Fatalf("Tee: %v", err)
	}
	if dst.String() != stream {
		t.Fatalf("Responses stream was modified by the tee")
	}
	res := m.Result()
	want := policy.Usage{Model: "gpt-4o", InputTokens: 11, OutputTokens: 2, ReasoningTokens: 1}
	if !res.HasUsage || res.Usage != want {
		t.Fatalf("Responses stream usage = %+v (hasUsage=%v), want %+v", res.Usage, res.HasUsage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello") {
		t.Fatalf("Responses stream fingerprint did not match the deltas")
	}
}
