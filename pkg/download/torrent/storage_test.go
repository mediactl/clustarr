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
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

// A transfer's piece completion is kept in memory, not in a SQLite database
// beside its data. On /data -- NFS on talos -- that database's commit, the
// fsync of the whole file it brought on every verified piece, and its query
// per piece to learn whether the file was done made verifying a 5.7 GB
// release take over an hour after the download itself had finished
// (2026-10-20). A file written whole is still known complete when its
// storage is opened again, so a restarted engine hashes no seeding release.
func TestTorrentStorageKeepsPieceCompletionInMemory(t *testing.T) {
	const pieceLen = 16 * 1024
	content := bytes.Repeat([]byte("clustarr"), 4*pieceLen/8)
	mi, err := metainfo.Load(bytes.NewReader(buildTestTorrent(t, t.TempDir(), "release.mkv", content)))
	require.NoError(t, err)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)
	require.Equal(t, 4, info.NumPieces())

	dir := t.TempDir()
	cl := fileStorage(dir)
	tor, err := cl.OpenTorrent(t.Context(), &info, mi.HashInfoBytes())
	require.NoError(t, err)
	for i := range info.NumPieces() {
		p := tor.Piece(info.Piece(i))
		_, err := p.WriteAt(content[i*pieceLen:(i+1)*pieceLen], 0)
		require.NoError(t, err)
		require.NoError(t, p.MarkComplete())
	}
	_, err = os.Stat(filepath.Join(dir, ".torrent.db"))
	require.ErrorIs(t, err, fs.ErrNotExist, "no completion database beside the data")
	require.NoError(t, tor.Close())
	require.NoError(t, cl.Close())

	again := fileStorage(dir)
	t.Cleanup(func() { _ = again.Close() })
	tor, err = again.OpenTorrent(t.Context(), &info, mi.HashInfoBytes())
	require.NoError(t, err)
	for i := range info.NumPieces() {
		c := tor.Piece(info.Piece(i)).Completion()
		require.True(t, c.Ok && c.Complete, "piece %d of a file written whole is complete after a restart", i)
	}
}
