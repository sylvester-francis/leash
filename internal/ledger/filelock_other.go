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

//go:build !unix && !windows

package ledger

import "io"

// acquireFileLock is a no-op on the remaining platforms (e.g. plan9, wasm),
// where leash does not enforce the single-governor rule for SQLite. Unix uses
// flock (filelock_unix.go) and Windows uses LockFileEx (filelock_windows.go);
// elsewhere, run one governor per SQLite ledger, or use the PostgreSQL backend,
// whose lease is a real cross-process advisory lock. See docs/durability.md.
func acquireFileLock(string) (io.Closer, bool, error) {
	return io.NopCloser(nil), true, nil
}
