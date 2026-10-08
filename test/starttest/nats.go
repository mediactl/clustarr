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
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// EmbeddedNATS starts an in-process JetStream server on a free port for the
// test's lifetime and returns its client URL.
func EmbeddedNATS(t testing.TB) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-starttest",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		t.Fatalf("new NATS server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}
