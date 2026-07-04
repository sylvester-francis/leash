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

package proxy

import (
	"net"
	"sync"
)

// LimitListener bounds the number of simultaneously accepted connections to n.
// Beyond the cap, Accept blocks until an open connection closes, so a flood of
// slow or idle clients cannot exhaust file descriptors or goroutines. It is the
// standard semaphore-wrapped listener, kept in-tree to preserve leash's
// single-dependency footprint.
func LimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, sem: make(chan struct{}, n)}
}

type limitListener struct {
	net.Listener
	sem chan struct{}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{} // acquire a slot, blocking when at the cap
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(c.release)
	return err
}
