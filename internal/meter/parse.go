package meter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sylvester-francis/leash/internal/policy"
)

// openAIUsage is the usage block of an OpenAI-compatible response or final
// stream chunk. A nil pointer means usage was absent (a blind call).
type openAIUsage struct {
	PromptTokens            int64 `json:"prompt_tokens"`
	CompletionTokens        int64 `json:"completion_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// anthropicUsage is the usage block of an Anthropic response or stream event.
type anthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// openAIResponse is a non-streaming OpenAI-compatible response.
type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
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
		res := Result{Fingerprint: policy.Fingerprint(text.String())}
		if r.Usage != nil {
			res.HasUsage = true
			res.Usage = policy.Usage{
				Model:           r.Model,
				InputTokens:     r.Usage.PromptTokens,
				OutputTokens:    r.Usage.CompletionTokens,
				ReasoningTokens: r.Usage.CompletionTokensDetails.ReasoningTokens,
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
		if r.Usage != nil {
			res.HasUsage = true
			res.Usage = policy.Usage{
				Model:        r.Model,
				InputTokens:  r.Usage.InputTokens,
				OutputTokens: r.Usage.OutputTokens,
			}
		}
		return res, nil
	default:
		return Result{}, nil
	}
}
