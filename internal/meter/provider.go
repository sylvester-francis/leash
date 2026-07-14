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

// Package meter parses real token usage off provider response wires, in both
// non-streaming JSON and streaming (SSE or NDJSON) forms, for the
// OpenAI-compatible, Anthropic, Gemini, and Ollama native formats. Because it
// keys on the wire format rather than a model name, it governs any endpoint
// that speaks one of these (Gemini and Ollama both expose OpenAI-compatible
// APIs, for example), and it never goes stale on a new model version. It never
// estimates tokens: it counts only what the wire reports, and it tees streaming
// responses to the client byte for byte while reading usage on the side.
package meter

import (
	"net/http"
	"strings"

	"github.com/sylvester-francis/leash/internal/policy"
)

// Provider identifies which wire format a request or response uses.
type Provider int

const (
	// Unknown is a request leash does not recognize; its token meter is blind.
	Unknown Provider = iota
	// OpenAI is the OpenAI-compatible chat/completions/responses format.
	OpenAI
	// Anthropic is the Anthropic messages format.
	Anthropic
	// Gemini is the Google Gemini generateContent format (native API).
	Gemini
	// Ollama is the Ollama native API format (/api/chat, /api/generate).
	Ollama
)

// String returns the provider name for logs.
func (p Provider) String() string {
	switch p {
	case OpenAI:
		return "openai"
	case Anthropic:
		return "anthropic"
	case Gemini:
		return "gemini"
	case Ollama:
		return "ollama"
	default:
		return "unknown"
	}
}

// Result is the outcome of metering one call: the token usage, a normalized
// content fingerprint for stall detection, and whether any usage was found on
// the wire (false means the token meter was blind for this call).
type Result struct {
	// Usage is the token accounting read from the wire.
	Usage policy.Usage
	// Fingerprint is the hash of the assistant text, or empty when blank.
	Fingerprint string
	// HasUsage reports whether usage numbers were present on the wire.
	HasUsage bool
}

// DetectProvider infers the provider from the request path and headers. An
// Anthropic-Version header wins outright; otherwise the path decides. Unknown
// paths return Unknown so the caller can forward them without metering.
func DetectProvider(path string, header http.Header) Provider {
	if header != nil && header.Get("Anthropic-Version") != "" {
		return Anthropic
	}
	switch {
	case strings.Contains(path, "/messages"):
		return Anthropic
	case strings.HasSuffix(path, "/api/chat"), strings.HasSuffix(path, "/api/generate"):
		return Ollama
	case strings.Contains(path, "/completions"), strings.Contains(path, "/responses"):
		return OpenAI
	case strings.Contains(strings.ToLower(path), "generatecontent"):
		// Gemini native: .../models/<model>:generateContent and
		// :streamGenerateContent (case-insensitive, since streaming capitalizes
		// the G). Its OpenAI-compatible endpoint uses a /chat/completions path and
		// is metered as OpenAI above.
		return Gemini
	default:
		return Unknown
	}
}

// IsStreamed reports whether a response Content-Type is a streamed format (SSE
// or NDJSON) that should be routed through the streaming meter rather than
// buffered and parsed as a complete JSON body.
func IsStreamed(contentType string) bool {
	t := strings.ToLower(strings.TrimSpace(contentType))
	return strings.HasPrefix(t, "text/event-stream") || strings.HasPrefix(t, "application/x-ndjson")
}
