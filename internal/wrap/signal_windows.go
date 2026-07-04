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

//go:build windows

package wrap

import "os/exec"

// forwardSignals is a no-op on Windows, which has no mechanism to relay
// Interrupt or SIGTERM to a child process. The wrapper still governs and reports
// exit codes; it just cannot pass a Ctrl-C through to the child. This limitation
// is documented in docs/deployment.md.
func forwardSignals(_ *exec.Cmd) (stop func()) {
	return func() {}
}
