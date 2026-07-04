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

//go:build unix

package ledger

import (
	"io"
	"os"
	"syscall"
)

// acquireFileLock takes an exclusive, non-blocking advisory lock on path,
// creating it if needed. ok is false when another process already holds it. The
// lock is released, and the file closed, by the returned Closer. flock locks are
// tied to the open file description and released automatically if the process
// dies, so a crashed governor never leaves the ledger permanently locked.
func acquireFileLock(path string) (io.Closer, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &unixFileLock{f: f}, true, nil
}

type unixFileLock struct{ f *os.File }

func (l *unixFileLock) Close() error {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
