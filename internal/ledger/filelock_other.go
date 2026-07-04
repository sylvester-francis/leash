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

//go:build !unix

package ledger

import "io"

// acquireFileLock is a no-op on non-unix platforms (notably Windows): leash does
// not enforce the single-governor rule for SQLite there. Run one governor per
// SQLite ledger, or use the PostgreSQL backend, whose lease is a real
// cross-process advisory lock. Documented in docs/durability.md.
func acquireFileLock(string) (io.Closer, bool, error) {
	return io.NopCloser(nil), true, nil
}
