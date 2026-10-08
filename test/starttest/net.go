/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package starttest

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// FreeAddress asks the kernel for an unused port and hands back the address.
func FreeAddress(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return addr
}

// WaitFor polls cond until it holds or 30s pass.
func WaitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	waitUntil(t, what, 30*time.Second, 100*time.Millisecond, cond)
}

// WaitForLong is WaitFor with room for a leader election: a released lease
// is acquired on the elector's next retry, and only then do the controllers
// start their informers.
func WaitForLong(t testing.TB, what string, cond func() bool) {
	t.Helper()
	waitUntil(t, what, 90*time.Second, 200*time.Millisecond, cond)
}

func waitUntil(t testing.TB, what string, timeout, every time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(every)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// WaitForProbe polls url until it answers 200, for up to 60s.
func WaitForProbe(t testing.TB, url string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // bounded by the loop deadline
		if err != nil {
			last = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		code := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
		if code == http.StatusOK {
			return
		}
		last = fmt.Errorf("status %d", code)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never returned 200: %v", url, errors.Join(last))
}
