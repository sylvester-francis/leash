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

func TestParseUsageJSONAnthropicThinkingAndServerTools(t *testing.T) {
	body := []byte(`{
		"model": "claude-x",
		"content": [{"type": "text", "text": "hi"}],
		"usage": {"input_tokens": 100, "output_tokens": 50,
			"output_tokens_details": {"thinking_tokens": 20},
			"server_tool_use": {"web_search_requests": 2, "web_fetch_requests": 1}}
	}`)
	res, err := ParseUsageJSON(Anthropic, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	want := policy.Usage{
		Model: "claude-x", InputTokens: 100, OutputTokens: 50,
		ReasoningTokens: 20, WebSearchRequests: 2, WebFetchRequests: 1,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
}

const anthropicToolStream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-x","usage":{"input_tokens":100,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":50,"output_tokens_details":{"thinking_tokens":20},"server_tool_use":{"web_search_requests":2}}}

`

func TestStreamMeterAnthropicThinkingAndServerTools(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(Anthropic)
	if err := m.Tee(&dst, strings.NewReader(anthropicToolStream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	res := m.Result()
	want := policy.Usage{
		Model: "claude-x", InputTokens: 100, OutputTokens: 50,
		ReasoningTokens: 20, WebSearchRequests: 2,
	}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
}
