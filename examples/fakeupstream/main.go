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

// Command fakeupstream is a standard-library stand-in for an OpenAI-compatible
// provider, used by the leash 60-second demo. It answers every chat completion
// with a fixed reply and a fixed usage block, so a demo needs no real API key
// and spends no real money. It is a demo aid, not part of the leash product.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:9099", "address to listen on")
	flag.Parse()

	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"demo-model",`+
			`"choices":[{"message":{"role":"assistant","content":"ok"}}],`+
			`"usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	})

	log.Printf("fakeupstream listening on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
