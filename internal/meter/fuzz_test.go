package meter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The parsers are leash's attack surface: they read bytes straight off a
// provider wire. These fuzz targets assert the properties that must hold for any
// input at all, seeded from the known-good constants the unit tests use.

// FuzzParseUsageJSON asserts ParseUsageJSON never panics for either provider on
// any body. Malformed usage is a blind result or an error, never a crash.
func FuzzParseUsageJSON(f *testing.F) {
	f.Add(`{"model":"gpt-4o","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	f.Add(`{"model":"claude-3-5-sonnet","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":12,"output_tokens":7}}`)
	f.Add(`{"model":"gpt-4o","choices":[]}`)
	f.Add(``)
	f.Add(`null`)
	f.Add(`[1,2,3]`)
	f.Fuzz(func(t *testing.T, body string) {
		for _, p := range []Provider{OpenAI, Anthropic, Unknown} {
			_, _ = ParseUsageJSON(p, []byte(body))
		}
	})
}

// FuzzStreamMeterTee asserts the tee never panics and, above all, never alters
// the client's stream: dst must equal src byte for byte no matter what the
// upstream sends. That is the streaming-fidelity invariant under fuzzing.
func FuzzStreamMeterTee(f *testing.F) {
	f.Add(openAIStream)
	f.Add(anthropicStream)
	f.Add(openAIStreamNoUsage)
	f.Add("data: not json\n\n")
	f.Add("")
	f.Add("data:\n\ndata: [DONE]\n\n")
	f.Fuzz(func(t *testing.T, stream string) {
		for _, p := range []Provider{OpenAI, Anthropic} {
			var dst bytes.Buffer
			m := NewStreamMeter(p)
			if err := m.Tee(&dst, strings.NewReader(stream)); err != nil {
				t.Fatalf("Tee returned an error on a bytes.Reader (cannot fail): %v", err)
			}
			if !bytes.Equal(dst.Bytes(), []byte(stream)) {
				t.Fatalf("tee altered the stream:\n src %q\n dst %q", stream, dst.String())
			}
		}
	})
}

// FuzzInjectIncludeUsage asserts three properties for any body: an error leaves
// the input unchanged; a success yields valid JSON; and injection is idempotent
// (injecting twice equals injecting once).
func FuzzInjectIncludeUsage(f *testing.F) {
	f.Add(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	f.Add(`{"stream":true,"stream_options":{"chunk_size":5}}`)
	f.Add(`{"stream":true,"stream_options":{"include_usage":true}}`)
	f.Add(`{"model":"gpt-4o","stream":false}`)
	f.Add(`not json`)
	f.Add(`[1,2,3]`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, body string) {
		out, changed, err := InjectIncludeUsage([]byte(body))
		if err != nil {
			if !bytes.Equal(out, []byte(body)) {
				t.Fatalf("error path altered the body:\n in %q\nout %q", body, out)
			}
			return
		}
		if !json.Valid(out) {
			t.Fatalf("success produced invalid JSON: %q", out)
		}
		if !changed && !bytes.Equal(out, []byte(body)) {
			t.Fatalf("unchanged result differs from input:\n in %q\nout %q", body, out)
		}
		// Idempotence: injecting the output again must not change it further.
		out2, changed2, err2 := InjectIncludeUsage(out)
		if err2 != nil {
			t.Fatalf("re-injecting a valid output errored: %v", err2)
		}
		if changed2 {
			t.Fatalf("injection was not idempotent: second pass changed %q", out)
		}
		if !bytes.Equal(out2, out) {
			t.Fatalf("injecting twice != injecting once:\n once %q\ntwice %q", out, out2)
		}
	})
}
