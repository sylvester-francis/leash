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

package ledger

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

// kernel32's LockFileEx / UnlockFileEx, loaded through the standard-library
// syscall package so no external dependency is added (see docs/adr/0004).
var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errLockViolation        = syscall.Errno(0x21) // ERROR_LOCK_VIOLATION (33)
)

// overlapped mirrors the Win32 OVERLAPPED struct. A zero value locks from offset
// zero, which is all we need for a whole-file advisory lock.
type overlapped struct {
	Internal     uintptr
	InternalHigh uintptr
	Offset       uint32
	OffsetHigh   uint32
	HEvent       syscall.Handle
}

// acquireFileLock takes an exclusive, non-blocking lock on path, creating it if
// needed. ok is false when another process already holds it. Windows byte-range
// locks are released when the handle closes, including on process death, so a
// crashed governor never leaves the ledger permanently locked. This is the
// Windows counterpart to the Unix flock in filelock_unix.go.
func acquireFileLock(path string) (io.Closer, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	var ol overlapped
	r1, _, e1 := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, // reserved, must be zero
		1, // bytes to lock, low word
		0, // bytes to lock, high word
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		_ = f.Close()
		if e1 == errLockViolation {
			return nil, false, nil // held by another process
		}
		return nil, false, fmt.Errorf("LockFileEx %s: %w", path, e1)
	}
	return &windowsFileLock{f: f}, true, nil
}

type windowsFileLock struct{ f *os.File }

func (l *windowsFileLock) Close() error {
	var ol overlapped
	_, _, _ = procUnlockFileEx.Call(l.f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	return l.f.Close()
}
