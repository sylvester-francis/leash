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
	} `json:"completion_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// normalize maps either OpenAI wire shape to input, output, reasoning, and
// cached-input counts, and reports whether any recognized token field was
// present. OpenAI's prompt/input token count already includes cached tokens.
func (u *openAIUsage) normalize() (in, out, reasoning, cachedRead int64, present bool) {
	if u.PromptTokens != nil {
		in, present = *u.PromptTokens, true
	}
	if u.InputTokens != nil {
		in, present = *u.InputTokens, true
	}
	if u.CompletionTokens != nil {
		out, present = *u.CompletionTokens, true
	}
	if u.OutputTokens != nil {
		out, present = *u.OutputTokens, true
	}
	reasoning = u.CompletionTokensDetails.ReasoningTokens + u.OutputTokensDetails.ReasoningTokens
	cachedRead = u.PromptTokensDetails.CachedTokens + u.InputTokensDetails.CachedTokens
	return in, out, reasoning, cachedRead, present
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
}

// present reports whether the block carried any recognized token field.
func (u *anthropicUsage) present() bool {
	return u.InputTokens != nil || u.OutputTokens != nil ||
		u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil
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
	Model   string `json:"model"`
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
			if in, out, reasoning, cachedRead, present := r.Usage.normalize(); present {
				res.HasUsage = true
				res.Usage = policy.Usage{
					Model:            r.Model,
					InputTokens:      in,
					CachedReadTokens: cachedRead,
					OutputTokens:     out,
					ReasoningTokens:  reasoning,
				}
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
			res.HasUsage = true
			res.Usage = policy.Usage{
				Model:            r.Model,
				InputTokens:      r.Usage.totalInput(),
				CachedReadTokens: deref(r.Usage.CacheReadInputTokens),
				CacheWriteTokens: deref(r.Usage.CacheCreationInputTokens),
				OutputTokens:     deref(r.Usage.OutputTokens),
			}
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
