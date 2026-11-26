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

package torrent

import (
	"bytes"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

// These are canaries on anacrolix itself, not tests of this package. Each
// pins an upstream behaviour at the pinned version (engineVersion) that
// Client.Add works around, so a module bump that changes the behaviour
// fails here by name instead of leaving a workaround quietly redundant --
// or, worse, a workaround quietly wrong. The carried Phase D2 note asked for
// exactly this re-test "against any future anacrolix bump"; these run on
// every `go test`.
//
// A failure here is not a bug in Clustarr. It means the workaround named in
// the failure message can be re-evaluated against the new upstream.
//
// One went that way on 2026-11-26: the canary that AddTorrentOpts'
// DisallowDataDownload was declared and never read failed on the move to
// upstream's master, which honours it (anacrolix/torrent#1250). Client.Add's
// explicit pause after the add holds under either behaviour and stayed, so
// nothing here depends on that option and its canary was removed.

// addRawLeecher adds payload to a bare anacrolix client with spec mutated
// first, connects seeder as a direct peer, and returns the torrent.
func addRawLeecher(t *testing.T, payload []byte, seeder *anatorrent.Client, mutate func(*anatorrent.TorrentSpec)) *anatorrent.Torrent {
	t.Helper()
	cl := newRawClient(t, t.TempDir(), false)
	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	require.NoError(t, err)
	if mutate != nil {
		mutate(spec)
	}
	tr, _, err := cl.AddTorrentSpec(spec)
	require.NoError(t, err)
	require.Positive(t, tr.AddClientPeer(seeder))
	return tr
}

// TestUpstream_NewTorrentWantsNothingUntilPiecesAreMarked pins that a
// freshly added torrent, with a complete seeder connected, fetches nothing
// until something raises a piece's priority -- the reason Client.Add always
// runs applySelection (DownloadAll, or per-file priorities) rather than
// relying on a default. If the first half fails, upstream now downloads by
// default, and a WantFile selection would have to cancel the unwanted files
// instead of only raising the wanted ones.
func TestUpstream_NewTorrentWantsNothingUntilPiecesAreMarked(t *testing.T) {
	content := bytes.Repeat([]byte("nothing is wanted "), 4096)
	seeder, payload := newSeeder(t, content)

	tr := addRawLeecher(t, payload, seeder, nil)
	<-tr.GotInfo()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		require.Zero(t, tr.BytesCompleted(),
			"anacrolix now fetches a torrent nobody marked wanted; pkg/download/torrent.applySelection's "+
				"premise (priorities default to none) no longer holds")
		time.Sleep(50 * time.Millisecond)
	}

	tr.DownloadAll()
	select {
	case <-tr.Complete().On():
	case <-time.After(10 * time.Second):
		t.Fatal("marking every piece wanted did not complete the transfer; the canary itself is broken")
	}
}
