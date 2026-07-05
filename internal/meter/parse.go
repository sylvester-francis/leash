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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sylvester-francis/leash/internal/policy"
)

// openAIUsage covers both OpenAI wire shapes: chat/completions
// (prompt_tokens/completion_tokens) and the Responses API
// (input_tokens/output_tokens). Token fields are pointers so an absent field is
// distinguishable from a real zero: a usage object carrying none of the expected
// fields (a mis-tagged or unrecognized body) is treated as blind, not as a
// genuine zero-token call.
type openAIUsage struct {
	PromptTokens            *int64 `json:"prompt_tokens"`
	CompletionTokens        *int64 `json:"completion_tokens"`
	InputTokens             *int64 `json:"input_tokens"`
	OutputTokens            *int64 `json:"output_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
		AudioTokens     int64 `json:"audio_tokens"`
	} `json:"completion_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
		AudioTokens     int64 `json:"audio_tokens"`
	} `json:"output_tokens_details"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
		AudioTokens  int64 `json:"audio_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
		AudioTokens  int64 `json:"audio_tokens"`
	} `json:"input_tokens_details"`
}

// toUsage maps either OpenAI wire shape (chat/completions or Responses) to a
// partial policy.Usage, and reports whether any recognized token field was
// present. Model and ServiceTier are set by the caller. OpenAI's prompt/input
// token count already includes cached and audio tokens.
func (u *openAIUsage) toUsage() (policy.Usage, bool) {
	var out policy.Usage
	present := false
	if u.PromptTokens != nil {
		out.InputTokens, present = *u.PromptTokens, true
	}
	if u.InputTokens != nil {
		out.InputTokens, present = *u.InputTokens, true
	}
	if u.CompletionTokens != nil {
		out.OutputTokens, present = *u.CompletionTokens, true
	}
	if u.OutputTokens != nil {
		out.OutputTokens, present = *u.OutputTokens, true
	}
	out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens + u.OutputTokensDetails.ReasoningTokens
	out.CachedReadTokens = u.PromptTokensDetails.CachedTokens + u.InputTokensDetails.CachedTokens
	out.AudioInputTokens = u.PromptTokensDetails.AudioTokens + u.InputTokensDetails.AudioTokens
	out.AudioOutputTokens = u.CompletionTokensDetails.AudioTokens + u.OutputTokensDetails.AudioTokens
	return out, present
}

// anthropicUsage is the usage block of an Anthropic response or stream event.
// The pointers distinguish an absent field from a real zero (see openAIUsage).
// Anthropic reports cache tokens separately from input_tokens, so the total
// input is their sum (see totalInput).
type anthropicUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheCreation            struct {
		// The TTL breakdown of CacheCreationInputTokens, priced at their own rates.
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails struct {
		// ThinkingTokens is a subset of OutputTokens (Anthropic's extended
		// thinking), mapped to reasoning so it can be priced at the reasoning rate.
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	ServerToolUse struct {
		// Per-request charges, not tokens: priced from the table's per-request
		// rates, or (when unpriced) what fails the run closed under a budget.
		WebSearchRequests int64 `json:"web_search_requests"`
		WebFetchRequests  int64 `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
	// ServiceTier selects a per-tier price override when set (standard/priority/batch).
	ServiceTier string `json:"service_tier"`
}

// present reports whether the block carried any recognized token field.
func (u *anthropicUsage) present() bool {
	return u.InputTokens != nil || u.OutputTokens != nil ||
		u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil
}

// toUsage maps the block to a partial policy.Usage; the caller sets Model.
func (u *anthropicUsage) toUsage() policy.Usage {
	return policy.Usage{
		InputTokens:        u.totalInput(),
		CachedReadTokens:   deref(u.CacheReadInputTokens),
		CacheWriteTokens:   deref(u.CacheCreationInputTokens),
		CacheWrite5mTokens: u.CacheCreation.Ephemeral5m,
		CacheWrite1hTokens: u.CacheCreation.Ephemeral1h,
		OutputTokens:       deref(u.OutputTokens),
		ReasoningTokens:    u.OutputTokensDetails.ThinkingTokens,
		WebSearchRequests:  u.ServerToolUse.WebSearchRequests,
		WebFetchRequests:   u.ServerToolUse.WebFetchRequests,
		ServiceTier:        u.ServiceTier,
	}
}

// totalInput is the full prompt token count: Anthropic's input_tokens excludes
// cache tokens, so the cache-read and cache-write counts are added back.
func (u *anthropicUsage) totalInput() int64 {
	return deref(u.InputTokens) + deref(u.CacheReadInputTokens) + deref(u.CacheCreationInputTokens)
}

// deref returns *p, or 0 when p is nil.
func deref(p *int64) int64 {
	if p != nil {
		return *p
	}
	return 0
}

// geminiUsage is the usageMetadata block of a Gemini generateContent response or
// stream chunk. Pointers distinguish an absent field from a real zero. On the
// Gemini API (generativelanguage), candidatesTokenCount already includes any
// thinking tokens, which are also reported separately in thoughtsTokenCount, so
// the mapping matches leash's reasoning-is-a-subset-of-output model;
// cachedContentTokenCount is the cached portion of promptTokenCount. (Vertex AI
// reports candidatesTokenCount excluding thinking; leash targets the Gemini API.)
type geminiUsage struct {
	PromptTokenCount        *int64 `json:"promptTokenCount"`
	CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
	CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
	TotalTokenCount         *int64 `json:"totalTokenCount"`
}

// present reports whether the block carried any recognized token field.
func (u *geminiUsage) present() bool {
	return u.PromptTokenCount != nil || u.CandidatesTokenCount != nil || u.TotalTokenCount != nil
}

// toUsage maps usageMetadata onto policy.Usage for the given model.
func (u *geminiUsage) toUsage(model string) policy.Usage {
	return policy.Usage{
		Model:            model,
		InputTokens:      deref(u.PromptTokenCount),
		CachedReadTokens: deref(u.CachedContentTokenCount),
		OutputTokens:     deref(u.CandidatesTokenCount),
		ReasoningTokens:  deref(u.ThoughtsTokenCount),
	}
}

// geminiResponse is a Gemini generateContent response, non-streaming or one SSE
// chunk (the wire shape is the same). Text arrives in the candidates' parts and
// the billed model name is modelVersion.
type geminiResponse struct {
	ModelVersion string `json:"modelVersion"`
	Candidates   []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *geminiUsage `json:"usageMetadata"`
}

// text concatenates the assistant text across the response's candidate parts.
func (r *geminiResponse) text() string {
	var b strings.Builder
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// openAIResponse is a non-streaming OpenAI response in either wire shape:
// chat/completions (choices[].message.content) or Responses
// (output[].content[].text).
type openAIResponse struct {
	Model       string `json:"model"`
	ServiceTier string `json:"service_tier"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Output []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *openAIUsage `json:"usage"`
}

// anthropicResponse is a non-streaming Anthropic response.
type anthropicResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *anthropicUsage `json:"usage"`
}

// ParseUsageJSON reads usage and assistant text from a complete non-streaming
// response body for the given provider. A missing usage block yields a blind
// result (HasUsage false, zero tokens) rather than an error. An Unknown
// provider yields an empty result and no error.
func ParseUsageJSON(p Provider, body []byte) (Result, error) {
	switch p {
	case OpenAI:
		var r openAIResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return Result{}, fmt.Errorf("parse openai response: %w", err)
		}
		var text strings.Builder
		for _, c := range r.Choices {
			text.WriteString(c.Message.Content)
		}
		for _, o := range r.Output {
			for _, c := range o.Content {
				if c.Type == "output_text" || c.Type == "text" {
					text.WriteString(c.Text)
				}
			}
		}
		res := Result{Fingerprint: policy.Fingerprint(text.String())}
		if r.Usage != nil {
			if u, present := r.Usage.toUsage(); present {
				u.Model = r.Model
				u.ServiceTier = r.ServiceTier
				res.HasUsage = true
				res.Usage = u
			}
		}
		return res, nil
	case Anthropic:
		var r anthropicResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return Result{}, fmt.Errorf("parse anthropic response: %w", err)
		}
		var text strings.Builder
		for _, c := range r.Content {
			if c.Type == "text" {
				text.WriteString(c.Text)
			}
		}
		res := Result{Fingerprint: policy.Fingerprint(text.String())}
		if r.Usage != nil && r.Usage.present() {
			u := r.Usage.toUsage()
			u.Model = r.Model
			res.HasUsage = true
			res.Usage = u
		}
		return res, nil
	case Gemini:
		var r geminiResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return Result{}, fmt.Errorf("parse gemini response: %w", err)
		}
		res := Result{Fingerprint: policy.Fingerprint(r.text())}
		if r.UsageMetadata != nil && r.UsageMetadata.present() {
			res.HasUsage = true
			res.Usage = r.UsageMetadata.toUsage(r.ModelVersion)
		}
		return res, nil
	default:
		return Result{}, nil
	}
}
