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
	"io"
	"net/http"
	"strings"
	"testing"
)

// anthropicWithTool returns a metered Anthropic response that also billed a
// server-side web search, which leash cannot price from the token table.
func anthropicWithTool() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-x","content":[{"type":"text","text":"hi"}],
			"usage":{"input_tokens":100,"output_tokens":50,"server_tool_use":{"web_search_requests":2}}}`)
	}
}

// anthropicHeader routes a request to the Anthropic meter regardless of path.
var anthropicHeader = http.Header{"Anthropic-Version": {"2023-06-01"}}

func TestUnpricedServerToolStopsRun(t *testing.T) {
	// A metered call that also billed an unpriceable web search under a cost
	// budget: it is delivered (already billed), then the run stops so nothing
	// further spends uncounted, exactly like a blind meter.
	front, up, _ := buildBlind(t, BlindRefuse, anthropicWithTool())
	if code, _ := postBody(t, front, anthropicHeader, `{"model":"claude-x"}`); code != http.StatusOK {
		t.Fatalf("first call = %d, want 200 (already billed)", code)
	}
	code, body := postBody(t, front, anthropicHeader, `{"model":"claude-x"}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("second call = %d, want 429 (run stopped)", code)
	}
	if !strings.Contains(body, unpricedToolStopReason) {
		t.Fatalf("stop reason not %q: %s", unpricedToolStopReason, body)
	}
	if up.count() != 1 {
		t.Fatalf("upstream saw %d calls, want 1 (stopped after the first)", up.count())
	}
}

func TestUnpricedServerToolAllowDoesNotStop(t *testing.T) {
	// --on-blind=allow accepts the uncounted tool spend without stopping.
	front, up, _ := buildBlind(t, BlindAllow, anthropicWithTool())
	postBody(t, front, anthropicHeader, `{"model":"claude-x"}`)
	postBody(t, front, anthropicHeader, `{"model":"claude-x"}`)
	if up.count() != 2 {
		t.Fatalf("allow mode forwarded %d, want 2 (no stop)", up.count())
	}
}

func TestUnpricedServerToolWarnDoesNotStop(t *testing.T) {
	// --on-blind=warn logs once but does not stop the run.
	front, up, logs := buildBlind(t, BlindWarn, anthropicWithTool())
	postBody(t, front, anthropicHeader, `{"model":"claude-x"}`)
	postBody(t, front, anthropicHeader, `{"model":"claude-x"}`)
	if up.count() != 2 {
		t.Fatalf("warn mode forwarded %d, want 2 (no stop)", up.count())
	}
	if !strings.Contains(logs.String(), "server-side tool requests leash cannot price") {
		t.Fatalf("warn mode did not warn about unpriced tools: %s", logs.String())
	}
}
