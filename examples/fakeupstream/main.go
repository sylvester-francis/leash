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

// Command fakeupstream is a standard-library stand-in for an OpenAI-compatible
// provider, used by the leash demos. It answers every chat completion with a
// fixed reply and a usage block you can shape with flags, so a demo needs no
// real API key and spends no real money. It is a demo aid, not part of the leash
// product. With --no-usage it omits usage entirely, to show the blind-meter path.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
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

func main() {
	addr := flag.String("listen", "127.0.0.1:9099", "address to listen on")
	model := flag.String("model", "demo-model", "model name to report")
	reply := flag.String("reply", "ok", "assistant reply content")
	promptTokens := flag.Int64("prompt-tokens", 1000, "prompt (input) tokens to report")
	completionTokens := flag.Int64("completion-tokens", 500, "completion (output) tokens to report")
	reasoningTokens := flag.Int64("reasoning-tokens", 0, "reasoning tokens to report (a subset of completion)")
	cachedTokens := flag.Int64("cached-tokens", 0, "cached prompt tokens to report (a subset of prompt)")
	noUsage := flag.Bool("no-usage", false, "omit the usage block, so leash's token meter is blind")
	flag.Parse()

	handler := func(w http.ResponseWriter, r *http.Request) {
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
	}
	http.HandleFunc("/v1/chat/completions", handler)

	log.Printf("fakeupstream listening on http://%s (model=%s)", *addr, *model)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
