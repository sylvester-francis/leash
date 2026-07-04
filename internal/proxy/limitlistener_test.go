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
	"testing"
	"time"
)

func TestLimitListenerCapsConcurrentConns(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ll := LimitListener(base, 1)
	defer ll.Close()

	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := ll.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	c1, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer c1.Close()
	var a1 net.Conn
	select {
	case a1 = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("first connection was not accepted")
	}

	c2, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer c2.Close()
	// At the cap of 1, the second connection must not be accepted yet.
	select {
	case <-accepted:
		t.Fatal("second connection accepted while at the connection cap")
	case <-time.After(150 * time.Millisecond):
	}

	// Closing the first frees a slot; the second is then accepted.
	a1.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("second connection not accepted after a slot freed")
	}
}
