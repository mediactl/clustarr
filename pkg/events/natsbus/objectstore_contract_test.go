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

package natsbus_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// objectStoreRoundTripSize is 20 MiB: comfortably past the embedded server's
// default max_payload (1 MiB, server.MAX_PAYLOAD_SIZE) so a naive
// single-message Put would be refused outright. jetstream.ObjectStore chunks
// every object into a series of stream messages under that limit (128 KiB
// per chunk by default, object.go's objDefaultChunkSize) and Get
// reassembles them, so a size this far past max_payload round-tripping
// byte-identical is exactly the proof spec §B.1 asks for: "the max_payload
// limit does not apply, JetStream chunks."
const objectStoreRoundTripSize = 20 * 1024 * 1024

// TestObjectStoreRoundTripsPastMaxPayload proves a 20 MiB object survives a
// real Put/Get round trip against an embedded server that has not raised its
// default max_payload, byte-for-byte and digest-for-digest. This is the one
// case membus cannot stand in for: membus has no wire protocol and no
// max_payload at all, so only a real server proves chunking actually works.
func TestObjectStoreRoundTripsPastMaxPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() {
		if err := bus.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := bus.Ensure(ctx, contracttest.Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	data := make([]byte, objectStoreRoundTripSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	sum := sha256.Sum256(data)
	wantDigest := hex.EncodeToString(sum[:])

	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/uid-chunked/fanart/original"

	putInfo, err := store.Put(ctx, name, bytes.NewReader(data),
		map[string]string{"Content-Type": "image/jpeg"})
	if err != nil {
		t.Fatalf("Put %d bytes: %v", len(data), err)
	}
	if putInfo.Size != int64(len(data)) {
		t.Errorf("Put Size = %d, want %d", putInfo.Size, len(data))
	}
	if putInfo.Digest != wantDigest {
		t.Errorf("Put Digest = %q, want %q", putInfo.Digest, wantDigest)
	}

	info, rc, err := store.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read Get content: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("round-tripped %d bytes, want %d", len(got), len(data))
	}
	if !bytes.Equal(got, data) {
		t.Fatal("round-tripped bytes differ from what was Put")
	}
	if info.Digest != wantDigest {
		t.Errorf("Get Digest = %q, want %q", info.Digest, wantDigest)
	}
}
