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

// Command fakeupstream is a standard-library stand-in for a provider, used by the
// leash demos. It answers all three wire formats leash meters, each with a fixed
// reply and a usage block you can shape with flags: OpenAI-compatible chat
// completions on /v1/chat/completions, Anthropic messages on /v1/messages, and
// Gemini's native generateContent under /v1beta/. So a demo needs no real API key
// and spends no real money. It is a demo aid, not part of the leash product. With
// --no-usage it omits usage entirely, to show the blind-meter path.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strings"
)

type tokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
	CachedTokens    int64 `json:"cached_tokens,omitempty"`
}

type usage struct {
	PromptTokens            int64         `json:"prompt_tokens"`
	CompletionTokens        int64         `json:"completion_tokens"`
	CompletionTokensDetails *tokenDetails `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails     *tokenDetails `json:"prompt_tokens_details,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type choice struct {
	Message message `json:"message"`
}

type response struct {
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

// Anthropic-shaped response, used by the /v1/messages handler so a demo can show
// the Anthropic wire format (thinking tokens, and server-side tool requests that
// leash cannot price from the token table).
type serverToolUse struct {
	WebSearchRequests int64 `json:"web_search_requests,omitempty"`
}

type anthropicOutputDetails struct {
	ThinkingTokens int64 `json:"thinking_tokens,omitempty"`
}

type anthropicUsage struct {
	InputTokens         int64                   `json:"input_tokens"`
	OutputTokens        int64                   `json:"output_tokens"`
	OutputTokensDetails *anthropicOutputDetails `json:"output_tokens_details,omitempty"`
	ServerToolUse       *serverToolUse          `json:"server_tool_use,omitempty"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	Model   string             `json:"model"`
	Content []anthropicContent `json:"content"`
	Usage   *anthropicUsage    `json:"usage,omitempty"`
}

// Gemini-shaped response, used by the /v1beta generateContent handler. On the
// Gemini API candidatesTokenCount already includes thoughtsTokenCount, so this
// mirrors that: candidates = completion tokens, thoughts a subset of them.
type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiCandidate struct {
	Content struct {
		Parts []geminiPart `json:"parts"`
		Role  string       `json:"role"`
	} `json:"content"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string               `json:"modelVersion"`
}

func main() {
	addr := flag.String("listen", "127.0.0.1:9099", "address to listen on")
	model := flag.String("model", "demo-model", "model name to report")
	reply := flag.String("reply", "ok", "assistant reply content")
	promptTokens := flag.Int64("prompt-tokens", 1000, "prompt (input) tokens to report")
	completionTokens := flag.Int64("completion-tokens", 500, "completion (output) tokens to report")
	reasoningTokens := flag.Int64("reasoning-tokens", 0, "reasoning tokens to report (a subset of completion)")
	cachedTokens := flag.Int64("cached-tokens", 0, "cached prompt tokens to report (a subset of prompt)")
	serverToolRequests := flag.Int64("server-tool-requests", 0, "Anthropic web-search requests to report in server_tool_use (a per-request charge leash cannot price)")
	noUsage := flag.Bool("no-usage", false, "omit the usage block, so leash's token meter is blind")
	flag.Parse()

	// OpenAI-compatible chat completions.
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		resp := response{
			Model:   *model,
			Choices: []choice{{Message: message{Role: "assistant", Content: *reply}}},
		}
		if !*noUsage {
			u := &usage{PromptTokens: *promptTokens, CompletionTokens: *completionTokens}
			if *reasoningTokens > 0 {
				u.CompletionTokensDetails = &tokenDetails{ReasoningTokens: *reasoningTokens}
			}
			if *cachedTokens > 0 {
				u.PromptTokensDetails = &tokenDetails{CachedTokens: *cachedTokens}
			}
			resp.Usage = u
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Anthropic messages.
	http.HandleFunc("/v1/messages", func(w http.ResponseWriter, _ *http.Request) {
		resp := anthropicResponse{
			Model:   *model,
			Content: []anthropicContent{{Type: "text", Text: *reply}},
		}
		if !*noUsage {
			u := &anthropicUsage{InputTokens: *promptTokens, OutputTokens: *completionTokens}
			if *reasoningTokens > 0 {
				u.OutputTokensDetails = &anthropicOutputDetails{ThinkingTokens: *reasoningTokens}
			}
			if *serverToolRequests > 0 {
				u.ServerToolUse = &serverToolUse{WebSearchRequests: *serverToolRequests}
			}
			resp.Usage = u
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Gemini native generateContent (path is /v1beta/models/<model>:generateContent).
	http.HandleFunc("/v1beta/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(strings.ToLower(r.URL.Path), "generatecontent") {
			http.NotFound(w, r)
			return
		}
		var c geminiCandidate
		c.Content.Role = "model"
		c.Content.Parts = []geminiPart{{Text: *reply}}
		resp := geminiResponse{Candidates: []geminiCandidate{c}, ModelVersion: *model}
		if !*noUsage {
			resp.UsageMetadata = &geminiUsageMetadata{
				PromptTokenCount:        *promptTokens,
				CandidatesTokenCount:    *completionTokens, // includes thoughts on the Gemini API
				ThoughtsTokenCount:      *reasoningTokens,
				CachedContentTokenCount: *cachedTokens,
				TotalTokenCount:         *promptTokens + *completionTokens,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	log.Printf("fakeupstream listening on http://%s (model=%s)", *addr, *model)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
