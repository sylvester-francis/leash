# Metering the wire

leash never estimates tokens. It counts only what the provider puts on the
wire, and when the wire is silent it marks the call blind rather than guess.
This guide is how that reading works: the four wire formats leash understands
(OpenAI-compatible, Anthropic, Gemini, and Ollama), the two shapes each can take
(one JSON body, or a streamed response -- SSE or NDJSON), and the one request
rewrite that makes an OpenAI stream report its usage at all.

leash keys on the wire *format*, not the model name, so it does not go stale when
a provider ships a new model: add a row to your price table and it works. And
because the OpenAI format is the ecosystem's lingua franca, "OpenAI-compatible"
covers far more than OpenAI itself, including Gemini through its
OpenAI-compatible endpoint, plus OpenRouter, Groq, Together, vLLM, and the rest.
The Gemini and Ollama formats below are for their *native* APIs; both also expose
OpenAI-compatible endpoints that are metered as OpenAI.

The code lives in `internal/meter`: `provider.go` (detection, the `Result`
type, and content-type checks), `parse.go` (non-streaming JSON),
`stream.go` (the streaming tee-and-meter), and `inject.go` (the request rewrite).

## What one call produces: Result

Metering a call yields a `Result` with three parts:

- `Usage` - the token accounting read from the wire (model, input, output,
  reasoning).
- `Fingerprint` - a hash of the assistant's text, used by the stall boundary to
  notice identical responses in a row. Empty when the text is blank.
- `HasUsage` - true when usage numbers were actually present on the wire.
  **`HasUsage == false` means the token meter was blind for that call**: leash
  records zero tokens for it rather than inventing a number.

Usage and fingerprint are independent. A call can be blind on tokens
(`HasUsage == false`) and still carry a fingerprint, because the assistant text
and the usage block arrive in different places on the wire.

## Provider detection

`DetectProvider` picks the wire format from the request path and headers:

- An `Anthropic-Version` header wins outright -> Anthropic.
- Otherwise the path decides: a path containing `/messages` -> Anthropic; a path
  ending in `/api/chat` or `/api/generate` -> Ollama; a path containing
  `/completions` or `/responses` -> OpenAI (both `/chat/completions` and
  `/completions` contain `/completions`); a path containing `generateContent`
  (case-insensitive, covering `:generateContent` and `:streamGenerateContent`) ->
  Gemini.
- Anything else -> Unknown. leash forwards Unknown requests but does not meter
  them.

Detection selects the format. A separate signal, the *response* `Content-Type`,
selects the shape: `IsStreamed` returns true for a Content-Type that begins with
`text/event-stream` or `application/x-ndjson` (trimmed, case-insensitive), which
routes the response through the streaming meter instead of the JSON meter.

## OpenAI wire format

### Non-streaming JSON

leash reads `usage` and the assistant text from the complete body:

```json
{
  "model": "gpt-4o",
  "choices": [{"message": {"role": "assistant", "content": "Hello there"}}],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 5,
    "completion_tokens_details": {"reasoning_tokens": 3}
  }
}
```

- `usage.prompt_tokens` -> input tokens
- `usage.completion_tokens` -> output tokens
- `usage.completion_tokens_details.reasoning_tokens` -> reasoning tokens
- the text of every `choices[].message.content` is concatenated into the stall
  fingerprint

If the `usage` object is absent, the call is blind: `HasUsage` is false, tokens
are zero, and the fingerprint is still taken from the content.

### Streaming SSE

A stream carries the text as a run of `delta` chunks. The usage block appears
only in a **final chunk with an empty `choices` array**, and that chunk is
emitted only when the request asked for it (see the injection section below).

```text
data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}

data: {"model":"gpt-4o","choices":[{"index":0,"delta":{"content":" world"}}]}

data: {"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":1}}}

data: [DONE]
```

leash concatenates each `choices[].delta.content` into the fingerprint
("Hello" + " world") and reads the usage fields from the final chunk. The
`[DONE]` sentinel and any non-`data:` line are ignored. If no usage chunk ever
arrives, the stream is blind on tokens but the fingerprint still holds
"Hello world".

## Anthropic wire format

### Non-streaming JSON

```json
{
  "model": "claude-3-5-sonnet",
  "content": [{"type": "text", "text": "Hi"}, {"type": "text", "text": " there"}],
  "usage": {"input_tokens": 12, "output_tokens": 7}
}
```

- `usage.input_tokens` -> input tokens
- `usage.output_tokens` -> output tokens
- the `text` of every `content[]` block whose `type` is `text` is concatenated
  into the fingerprint

Anthropic reports extended-thinking tokens in `usage.output_tokens_details.thinking_tokens`,
which leash maps to reasoning (a subset of output, priced at the reasoning rate);
it also reads `usage.server_tool_use` request counts and the `cache_creation` TTL
split. An absent `usage` object makes the call blind, with the fingerprint still
taken from the text blocks.

### Streaming SSE

Anthropic spreads its usage across events. Input tokens arrive up front in
`message_start`; output tokens arrive as a running total in `message_delta`.

```text
event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet","usage":{"input_tokens":12,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}
```

- `message_start` -> model and `input_tokens` (plus an initial `output_tokens`)
- `content_block_delta` with a `text_delta` -> its `text` is appended to the
  fingerprint ("Hi" + " there")
- `message_delta` -> `output_tokens`, which Anthropic reports **cumulatively**.
  leash overwrites its output count on each `message_delta`, so the latest one
  wins (here, 5).

leash reads only `data:` lines; the `event:` lines are informational and
ignored.

## Gemini wire format

This is Gemini's native `generateContent` API. (Gemini's OpenAI-compatible
endpoint is a `/chat/completions` path and is metered as OpenAI.) Usage lives in
`usageMetadata`, and the billed model name is `modelVersion`.

### Non-streaming JSON

leash maps `usageMetadata.promptTokenCount` to input, `candidatesTokenCount` to
output, `thoughtsTokenCount` to reasoning, and `cachedContentTokenCount` to
cache-read input. On the Gemini API, `candidatesTokenCount` already includes any
thinking tokens (also reported in `thoughtsTokenCount`), which is exactly leash's
reasoning-is-a-subset-of-output model, so a thinking model is priced once:
non-reasoning output at the output rate, reasoning at the reasoning rate.
Assistant text is the candidates' `content.parts[].text`.

Note: Vertex AI reports `candidatesTokenCount` *excluding* thinking, the opposite
of the Gemini API. leash targets the Gemini API
(`generativelanguage.googleapis.com`); route Vertex through an OpenAI-compatible
endpoint or price with that difference in mind.

### Streaming SSE

`:streamGenerateContent?alt=sse` emits `data:` chunks, each a
`GenerateContentResponse` with partial `parts` text and a cumulative
`usageMetadata`. leash concatenates the text and takes the last `usageMetadata`
as authoritative, the same last-wins rule as Anthropic's `message_delta`. Gemini
needs no usage injection: `usageMetadata` is always present.

## Ollama native wire format

This is Ollama's native `/api/chat` and `/api/generate` API. (Ollama's
OpenAI-compatible endpoint is a `/v1/chat/completions` path and is metered as
OpenAI.) Usage is in `prompt_eval_count` and `eval_count` on the final chunk.
Unlike the other providers, Ollama's native API streams NDJSON
(`Content-Type: application/x-ndjson`) -- one bare JSON object per line, no SSE
`data:` framing.

### Non-streaming JSON

```json
{
  "model": "llama3.2",
  "message": {"role": "assistant", "content": "Hello there"},
  "done": true,
  "prompt_eval_count": 10,
  "eval_count": 5,
  "total_duration": 12345
}
```

- `prompt_eval_count` -> input tokens
- `eval_count` -> output tokens
- the assistant text is `message.content` for `/api/chat`, or the top-level
  `response` string for `/api/generate`

If `prompt_eval_count` and `eval_count` are both absent, the call is blind
(`HasUsage` is false) and the fingerprint is still taken from the text.

### Streaming NDJSON

```text
{"model":"llama3.2","message":{"role":"assistant","content":"Hello"},"done":false}
{"model":"llama3.2","message":{"role":"assistant","content":" world"},"done":true,"prompt_eval_count":10,"eval_count":3}
```

Each line is a bare JSON object. leash parses every line for the Ollama provider
(this is why the tee asserts the teed bytes equal the source exactly -- NDJSON
must pass through untouched). Usage is read from the final chunk marked
`"done": true`. If no chunk carries usage fields, the stream is blind on tokens
but the fingerprint still holds "Hello world".

### Upstream configuration

Ollama has no default upstream; in gateway mode you must pass `--upstream` with
the host:port of a running Ollama instance (e.g. `--upstream http://localhost:11434`).
Clients must set `OLLAMA_HOST` to the gateway address to route through the proxy;
the `serve` docs walk through this setup in
[the gateway guide](gateway.md#pointing-a-client-at-it).

Ollama management endpoints (`/api/tags`, `/api/show`, `/api/version`) detect as
Unknown (their paths do not match `/api/chat` or `/api/generate`) and fail closed
under a cost budget, so a client that lists models through the gateway sees 402s;
only `/api/chat` and `/api/generate` are governed.

## The tee: byte for byte, never buffered

leash never holds a stream to meter it. `StreamMeter.Tee` wraps the upstream
body in an `io.TeeReader`, so every byte is written to the client at the instant
it is read from the upstream - the client sees tokens exactly as fast, and in
exactly the bytes, the provider emits them. leash parses usage from that copy on
the side.

Two consequences follow, both deliberate:

- The client's stream is never reordered, rebuffered, or altered. The tests
  assert the teed bytes equal the upstream bytes exactly, including the `event:`
  framing and the blank lines between events.
- A malformed event cannot break the client. A parse error on any single SSE
  line is swallowed, so a chunk leash cannot read costs it that chunk's usage
  but never truncates what the client receives.

The `Result` is read only when the stream ends, once the final usage chunk
(OpenAI) or the last `message_delta` (Anthropic) has been seen.

## Asking OpenAI streams for usage: InjectIncludeUsage

An OpenAI stream reports usage only when the request set
`stream_options.include_usage`. Most agent code does not set it, so without help
every streaming OpenAI call would be blind. For OpenAI requests, leash rewrites
the request body to add it.

`InjectIncludeUsage` is conservative:

- It acts only on a **streaming** request - one whose body has `"stream": true`.
  A missing `stream` key or `"stream": false` is returned untouched.
- It **preserves** any `stream_options` already present, adding `include_usage`
  alongside them rather than replacing the object.
- It does nothing when `include_usage` is already true.
- On a body it cannot parse as JSON it returns the original bytes with an error;
  leash then forwards the request unchanged and treats the call as blind rather
  than corrupt a request it does not understand.

A streaming request goes in:

```json
{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}
```

and comes out with the one field added:

```json
{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}],"stream_options":{"include_usage":true}}
```

leash calls this only for OpenAI requests; Anthropic streams already carry usage
in their own events and are never rewritten, and non-streaming requests are left
alone. Standard OpenAI SDKs tolerate the extra final usage chunk (its `choices`
array is empty, so it adds no text); a hand-rolled client that does not is the
reason the rewrite has an off switch.

## The off switch and the blind path

`--no-inject` turns the rewrite off:

```sh
leash --no-inject -- python my_agent.py
```

With it set, leash simply does not call `InjectIncludeUsage`, so a streaming
OpenAI request that did not already ask for usage produces no usage chunk.

That call's token meter is then blind: leash records **zero tokens** for it and
warns once per run that the meter is blind, rather than repeat the warning on
every call. It does not estimate. The cost budget and the rate limit can act
only on the tokens leash actually saw, so a blind call is invisible to them; but
the boundaries that need no token counts still hold the run - calls, deadline,
stall, and the kill switch. Reach for `--no-inject` only when a client genuinely
cannot handle the trailing chunk, and expect to lean on those other boundaries.

## Unknown providers, and the fingerprint on a blind call

A request whose path and headers match neither format is `Unknown`. leash
forwards it to the upstream untouched but does not meter it: the JSON path
returns an empty `Result` - no usage, and no fingerprint, because leash does not
know where the assistant text lives in an unknown format.

That is distinct from a call that is blind only on **tokens**. When the provider
is known but its response simply omits usage - an OpenAI stream with no usage
chunk, an Anthropic body with no `usage` object - leash still parses the visible
assistant text into a fingerprint. The token meter is blind, but the stall
boundary keeps working, because it depends on the content leash can see, not on
the token counts it cannot.

leash reads a usage block only when it carries the fields it expects. A body
whose `usage` object is present but shaped for a different provider (for example
an OpenAI response mis-tagged as Anthropic by a stray `Anthropic-Version` header)
is treated as blind, not as a real zero-token call - so a mis-tag cannot silently
zero the meter. Both OpenAI wire shapes are understood: chat/completions
(`prompt_tokens`/`completion_tokens`) and the Responses API
(`input_tokens`/`output_tokens`, text in `output[]`), streaming and not.

## Failing closed on an unmeterable call

For a spend governor, the dangerous failure is metering a real, billed call at
$0. When a cost budget is active, `--on-blind` decides what happens to a call
leash cannot price:

- `refuse` (default) fails closed. An `Unknown`-provider call is rejected with a
  402 before it is forwarded. A known-provider call that comes back with no
  readable usage is delivered once (the upstream already billed it) and then the
  run is stopped with reason `meter_blind`, so no further spend goes uncounted.
- `warn` keeps the older behavior: forward the call and warn once per run.
- `allow` forwards silently.

The same policy covers **billed activity leash cannot price**, not just missing
usage. A call that reports server-side tool requests (Anthropic
`server_tool_use.web_search_requests` / `web_fetch_requests`) carries a per-request
charge that is not a token count and has no entry in the price table, so leash
cannot account for it. Under a cost budget, `--on-blind` treats it the same way: a
metered call that also billed such a request is delivered, then the run stops with
reason `server_tool_unpriced` (or warns, or is allowed). The count is exported as
`leash_server_tool_requests_total` regardless, so the spend is at least visible.

With no cost budget set, a blind call is harmless to the budget and is allowed;
the call, deadline, stall, and kill boundaries still hold the run.

Beyond the base counts, leash also reads the detail fields that refine cost:
reasoning/thinking tokens across OpenAI, Anthropic, and Gemini
(`completion_tokens_details.reasoning_tokens`, `output_tokens_details.thinking_tokens`,
Gemini's `thoughtsTokenCount`) map to the reasoning rate, audio tokens and
cache-read/cache-write (with its 5m/1h TTL split) to their own rates. These are all subsets of the base
input or output counts, so they refine pricing without ever double-counting.
