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
	provider  Provider
	model     string
	input     int64
	output    int64
	reasoning int64
	text      strings.Builder
	hasUsage  bool
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
		Usage: policy.Usage{
			Model:           m.model,
			InputTokens:     m.input,
			OutputTokens:    m.output,
			ReasoningTokens: m.reasoning,
		},
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
	}
}

// openAIChunk is one OpenAI streaming chunk. The usage-bearing final chunk (sent
// only when stream_options.include_usage is set) has an empty choices array.
type openAIChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

func (m *StreamMeter) parseOpenAIData(data []byte) {
	var c openAIChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return
	}
	if c.Model != "" {
		m.model = c.Model
	}
	for _, ch := range c.Choices {
		m.text.WriteString(ch.Delta.Content)
	}
	if c.Usage != nil {
		m.hasUsage = true
		m.input = c.Usage.PromptTokens
		m.output = c.Usage.CompletionTokens
		m.reasoning = c.Usage.CompletionTokensDetails.ReasoningTokens
	}
}

// anthropicEvent covers the fields leash reads across Anthropic stream events.
// message_start carries input tokens; content_block_delta carries text;
// message_delta carries cumulative output tokens.
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
			m.model = e.Message.Model
		}
		if e.Message.Usage != nil {
			m.hasUsage = true
			m.input = e.Message.Usage.InputTokens
			m.output = e.Message.Usage.OutputTokens
		}
	case "content_block_delta":
		if e.Delta.Type == "text_delta" {
			m.text.WriteString(e.Delta.Text)
		}
	case "message_delta":
		if e.Usage != nil {
			m.hasUsage = true
			m.output = e.Usage.OutputTokens // Anthropic reports cumulative output.
		}
	}
}
