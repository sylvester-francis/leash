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
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sylvester-francis/leash/internal/policy"
)

// StreamMeter accumulates usage and assistant text from an SSE response while
// the bytes flow through to the client untouched. Construct one per streaming
// call, run Tee, then read Result.
type StreamMeter struct {
	provider Provider
	usage    policy.Usage
	text     strings.Builder
	hasUsage bool
}

// NewStreamMeter returns a StreamMeter for the given provider.
func NewStreamMeter(p Provider) *StreamMeter {
	return &StreamMeter{provider: p}
}

// Tee copies the SSE stream from src to dst exactly as it arrives, parsing
// usage events on the side. It never buffers a whole stream: bytes are written
// to dst as they are read, so the client sees tokens as the upstream emits
// them. It returns when the stream ends. A parse error on any single event is
// ignored so that a malformed event can never truncate the client's stream.
func (m *StreamMeter) Tee(dst io.Writer, src io.Reader) error {
	br := bufio.NewReader(io.TeeReader(src, dst))
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			m.parseLine(bytes.TrimRight(line, "\r\n"))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read sse stream: %w", err)
		}
	}
}

// Result returns the accumulated usage, content fingerprint, and blindness.
func (m *StreamMeter) Result() Result {
	return Result{
		Usage:       m.usage,
		Fingerprint: policy.Fingerprint(m.text.String()),
		HasUsage:    m.hasUsage,
	}
}

// parseLine handles one raw SSE line. Only data lines carry payloads; event,
// comment, and blank lines are ignored.
func (m *StreamMeter) parseLine(line []byte) {
	const prefix = "data:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	data := bytes.TrimSpace(line[len(prefix):])
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	switch m.provider {
	case OpenAI:
		m.parseOpenAIData(data)
	case Anthropic:
		m.parseAnthropicData(data)
	case Gemini:
		m.parseGeminiData(data)
	}
}

// openAIChunk is one OpenAI streaming event in either wire shape. For
// chat/completions the usage-bearing final chunk (sent only when
// stream_options.include_usage is set) has an empty choices array. For the
// Responses API, Type names a typed event: response.output_text.delta carries a
// text delta and response.completed carries the final usage.
type openAIChunk struct {
	Type        string `json:"type"`
	Model       string `json:"model"`
	ServiceTier string `json:"service_tier"`
	Choices     []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage    *openAIUsage `json:"usage"`
	Response struct {
		Model       string       `json:"model"`
		ServiceTier string       `json:"service_tier"`
		Usage       *openAIUsage `json:"usage"`
	} `json:"response"`
}

func (m *StreamMeter) parseOpenAIData(data []byte) {
	var c openAIChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return
	}
	if c.Model != "" {
		m.usage.Model = c.Model
	}
	if c.Response.Model != "" {
		m.usage.Model = c.Response.Model
	}
	if c.ServiceTier != "" {
		m.usage.ServiceTier = c.ServiceTier
	}
	if c.Response.ServiceTier != "" {
		m.usage.ServiceTier = c.Response.ServiceTier
	}
	for _, ch := range c.Choices {
		m.text.WriteString(ch.Delta.Content)
	}
	if c.Type == "response.output_text.delta" {
		// The Responses text delta is a bare string field named "delta"; decode
		// it separately since the chat shape uses delta as an object.
		var e struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(data, &e) == nil {
			m.text.WriteString(e.Delta)
		}
	}
	m.applyOpenAIUsage(c.Usage)
	m.applyOpenAIUsage(c.Response.Usage)
}

// applyOpenAIUsage records the token counts from a usage block (the final chunk
// carries the full totals), preserving the model and service tier read from the
// chunk envelope.
func (m *StreamMeter) applyOpenAIUsage(u *openAIUsage) {
	if u == nil {
		return
	}
	if got, present := u.toUsage(); present {
		m.hasUsage = true
		got.Model, got.ServiceTier = m.usage.Model, m.usage.ServiceTier
		m.usage = got
	}
}

// parseGeminiData handles one Gemini SSE chunk (a GenerateContentResponse).
// usageMetadata is cumulative across chunks, so the last one seen wins, matching
// how the final chunk carries the authoritative totals.
func (m *StreamMeter) parseGeminiData(data []byte) {
	var c geminiResponse
	if err := json.Unmarshal(data, &c); err != nil {
		return
	}
	if c.ModelVersion != "" {
		m.usage.Model = c.ModelVersion
	}
	m.text.WriteString(c.text())
	if c.UsageMetadata != nil && c.UsageMetadata.present() {
		m.hasUsage = true
		m.usage = c.UsageMetadata.toUsage(m.usage.Model)
	}
}

// anthropicEvent covers the fields leash reads across Anthropic stream events.
// message_start carries input tokens; content_block_delta carries text;
// message_delta carries cumulative output tokens plus thinking and tool usage.
type anthropicEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
}

func (m *StreamMeter) parseAnthropicData(data []byte) {
	var e anthropicEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return
	}
	switch e.Type {
	case "message_start":
		if e.Message.Model != "" {
			m.usage.Model = e.Message.Model
		}
		if e.Message.Usage != nil && e.Message.Usage.present() {
			m.hasUsage = true
			// Input-side fields arrive with message_start.
			iu := e.Message.Usage.toUsage()
			m.usage.InputTokens = iu.InputTokens
			m.usage.CachedReadTokens = iu.CachedReadTokens
			m.usage.CacheWriteTokens = iu.CacheWriteTokens
			m.usage.CacheWrite5mTokens = iu.CacheWrite5mTokens
			m.usage.CacheWrite1hTokens = iu.CacheWrite1hTokens
			m.usage.OutputTokens = iu.OutputTokens
			if iu.ServiceTier != "" {
				m.usage.ServiceTier = iu.ServiceTier
			}
		}
	case "content_block_delta":
		if e.Delta.Type == "text_delta" {
			m.text.WriteString(e.Delta.Text)
		}
	case "message_delta":
		if e.Usage != nil && e.Usage.present() {
			m.hasUsage = true
			// Output-side fields (cumulative) arrive with message_delta.
			ou := e.Usage.toUsage()
			m.usage.OutputTokens = ou.OutputTokens
			m.usage.ReasoningTokens = ou.ReasoningTokens
			m.usage.WebSearchRequests = ou.WebSearchRequests
			m.usage.WebFetchRequests = ou.WebFetchRequests
			if ou.ServiceTier != "" {
				m.usage.ServiceTier = ou.ServiceTier
			}
		}
	}
}
