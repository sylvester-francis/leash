package meter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sylvester-francis/leash/internal/policy"
)

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		header http.Header
		want   Provider
	}{
		{"openai chat path", "/v1/chat/completions", nil, OpenAI},
		{"openai responses path", "/v1/responses", nil, OpenAI},
		{"anthropic messages path", "/v1/messages", nil, Anthropic},
		{"anthropic by version header", "/v1/foo", http.Header{"Anthropic-Version": {"2023-06-01"}}, Anthropic},
		{"unknown path", "/healthz", nil, Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectProvider(tt.path, tt.header); got != tt.want {
				t.Fatalf("DetectProvider(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestParseUsageJSONOpenAI(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"choices": [{"message": {"role": "assistant", "content": "Hello there"}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5,
			"completion_tokens_details": {"reasoning_tokens": 3}}
	}`)
	res, err := ParseUsageJSON(OpenAI, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	want := policy.Usage{Model: "gpt-4o", InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hello there") {
		t.Fatalf("Fingerprint did not match assistant text")
	}
}

func TestParseUsageJSONAnthropic(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-5-sonnet",
		"content": [{"type": "text", "text": "Hi"}, {"type": "text", "text": " there"}],
		"usage": {"input_tokens": 12, "output_tokens": 7}
	}`)
	res, err := ParseUsageJSON(Anthropic, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	want := policy.Usage{Model: "claude-3-5-sonnet", InputTokens: 12, OutputTokens: 7}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hi there") {
		t.Fatalf("Fingerprint did not match concatenated text blocks")
	}
}

func TestParseUsageJSONNoUsageIsBlind(t *testing.T) {
	body := []byte(`{"model": "gpt-4o", "choices": [{"message": {"content": "hi"}}]}`)
	res, err := ParseUsageJSON(OpenAI, body)
	if err != nil {
		t.Fatalf("ParseUsageJSON error: %v", err)
	}
	if res.HasUsage {
		t.Fatalf("HasUsage = true, want false when usage is absent")
	}
	if res.Usage.TotalTokens() != 0 {
		t.Fatalf("blind usage should be zero, got %+v", res.Usage)
	}
}

const openAIStream = `data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}

data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":" world"}}]}

data: {"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":1}}}

data: [DONE]

`

func TestStreamMeterOpenAI(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(OpenAI)
	if err := m.Tee(&dst, strings.NewReader(openAIStream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	// The client must receive the stream byte for byte, unmodified.
	if dst.String() != openAIStream {
		t.Fatalf("teed stream was modified:\n got %q\nwant %q", dst.String(), openAIStream)
	}
	res := m.Result()
	want := policy.Usage{Model: "gpt-4o", InputTokens: 10, OutputTokens: 2, ReasoningTokens: 1}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if !res.HasUsage {
		t.Fatalf("HasUsage = false, want true")
	}
	if res.Fingerprint != policy.Fingerprint("Hello world") {
		t.Fatalf("Fingerprint did not match streamed content")
	}
}

const anthropicStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet","usage":{"input_tokens":12,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStreamMeterAnthropic(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(Anthropic)
	if err := m.Tee(&dst, strings.NewReader(anthropicStream)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	if dst.String() != anthropicStream {
		t.Fatalf("teed stream was modified")
	}
	res := m.Result()
	// input from message_start, output is the cumulative value from message_delta.
	want := policy.Usage{Model: "claude-3-5-sonnet", InputTokens: 12, OutputTokens: 5}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
	if res.Fingerprint != policy.Fingerprint("Hi there") {
		t.Fatalf("Fingerprint did not match streamed content")
	}
}

const openAIStreamNoUsage = `data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"}}]}

data: [DONE]

`

func TestStreamMeterBlindWhenNoUsage(t *testing.T) {
	var dst bytes.Buffer
	m := NewStreamMeter(OpenAI)
	if err := m.Tee(&dst, strings.NewReader(openAIStreamNoUsage)); err != nil {
		t.Fatalf("Tee error: %v", err)
	}
	if dst.String() != openAIStreamNoUsage {
		t.Fatalf("teed stream was modified on the blind path")
	}
	res := m.Result()
	if res.HasUsage {
		t.Fatalf("HasUsage = true, want false with no usage chunk")
	}
	if res.Usage.TotalTokens() != 0 {
		t.Fatalf("blind stream usage should be zero, got %+v", res.Usage)
	}
	// Even blind, the content fingerprint is still available for stall detection.
	if res.Fingerprint != policy.Fingerprint("Hello") {
		t.Fatalf("Fingerprint should still be computed on the blind path")
	}
}

func TestInjectIncludeUsageStreaming(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out, changed, err := InjectIncludeUsage(body)
	if err != nil {
		t.Fatalf("InjectIncludeUsage error: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true for a streaming request")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	opts, ok := got["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options missing from output")
	}
	if opts["include_usage"] != true {
		t.Fatalf("include_usage = %v, want true", opts["include_usage"])
	}
}

func TestInjectIncludeUsagePreservesExistingOptions(t *testing.T) {
	body := []byte(`{"stream":true,"stream_options":{"chunk_size":5}}`)
	out, changed, err := InjectIncludeUsage(body)
	if err != nil || !changed {
		t.Fatalf("expected a change with no error, got changed=%v err=%v", changed, err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	opts := got["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("include_usage not set")
	}
	if opts["chunk_size"].(float64) != 5 {
		t.Fatalf("existing stream_options were clobbered")
	}
}

func TestInjectIncludeUsageNonStreamingUntouched(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","stream":false}`)
	out, changed, err := InjectIncludeUsage(body)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if changed {
		t.Fatalf("changed = true, want false for a non-streaming request")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("non-streaming body was modified")
	}
}

func TestInjectIncludeUsageAlreadySet(t *testing.T) {
	body := []byte(`{"stream":true,"stream_options":{"include_usage":true}}`)
	_, changed, err := InjectIncludeUsage(body)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if changed {
		t.Fatalf("changed = true, want false when include_usage is already set")
	}
}

func TestInjectIncludeUsageInvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	out, changed, err := InjectIncludeUsage(body)
	if err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
	if changed {
		t.Fatalf("changed = true on invalid JSON, want false")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("invalid JSON body should be returned unmodified")
	}
}
