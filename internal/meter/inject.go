package meter

import (
	"encoding/json"
	"fmt"
)

// InjectIncludeUsage rewrites an OpenAI-style request body so that a streaming
// request also asks for a final usage chunk, by setting
// stream_options.include_usage to true. It only touches streaming requests
// (stream is true) and preserves any other stream_options already present. It
// returns the possibly-rewritten body and whether anything changed.
//
// Standard SDKs tolerate the extra final usage chunk; a hand-rolled client
// might not, which is why the caller exposes an off switch (--no-inject) and
// simply does not call this function when injection is disabled. On a body it
// cannot parse it returns the original bytes unchanged with an error, so the
// caller can forward the request untouched and warn that the meter is blind.
func InjectIncludeUsage(body []byte) (out []byte, changed bool, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, false, fmt.Errorf("parse request body: %w", err)
	}

	streamRaw, ok := obj["stream"]
	if !ok {
		return body, false, nil
	}
	var stream bool
	if err := json.Unmarshal(streamRaw, &stream); err != nil || !stream {
		return body, false, nil
	}

	opts := map[string]json.RawMessage{}
	if raw, ok := obj["stream_options"]; ok {
		if err := json.Unmarshal(raw, &opts); err != nil {
			// stream_options is present but not an object: do not risk
			// corrupting a request leash does not understand.
			return body, false, nil
		}
	}
	if existing, ok := opts["include_usage"]; ok {
		var iu bool
		if json.Unmarshal(existing, &iu) == nil && iu {
			return body, false, nil // already asking for usage
		}
	}

	opts["include_usage"] = json.RawMessage("true")
	optsBytes, err := json.Marshal(opts)
	if err != nil {
		return body, false, fmt.Errorf("marshal stream_options: %w", err)
	}
	obj["stream_options"] = optsBytes

	out, err = json.Marshal(obj)
	if err != nil {
		return body, false, fmt.Errorf("marshal request body: %w", err)
	}
	return out, true, nil
}
