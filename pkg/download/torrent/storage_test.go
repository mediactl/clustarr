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
	"io"
	"io/fs"
	"os"
	"os/exec"
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

// Hashing a piece reads it through the storage's WriteTo. Under anacrolix
// v1.61's classic file I/O -- the engine's, since its mmap I/O never unmaps
// and was OOM-killed (2026-10-15) -- that path hands os.File.WriteTo a
// writer limited to the piece, which refuses the file's bytes past it as a
// short write, so every piece but a file's last hashed correctly and still
// came back io.ErrShortWrite: 125k WARN lines a day on talos ("finished
// hashing piece", correct true, err short write) and no smart-ban
// attribution, since anacrolix skips it on a storage error. Upstream fixed
// it in v1.62.0, 196 commits and a Go floor away; until that bump the
// storage clears the error itself when exactly the piece was written.
//
// The file I/O is chosen by the package's init from the environment, so the
// test runs itself again with the engine's setting.
func TestHashingAPieceUnderClassicFileIOIsNoShortWrite(t *testing.T) {
	const fileIOEnv = "TORRENT_STORAGE_DEFAULT_FILE_IO"
	if os.Getenv(fileIOEnv) != "classic" {
		cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), fileIOEnv+"=classic")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "with %s=classic:\n%s", fileIOEnv, out)
		require.Contains(t, string(out), "--- PASS: "+t.Name(), "%s", out)
		return
	}

	const pieceLen = 16 * 1024
	content := bytes.Repeat([]byte("clustarr"), 4*pieceLen/8)
	mi, err := metainfo.Load(bytes.NewReader(buildTestTorrent(t, t.TempDir(), "release.mkv", content)))
	require.NoError(t, err)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)
	require.Equal(t, 4, info.NumPieces())

	cl := fileStorage(t.TempDir())
	t.Cleanup(func() { _ = cl.Close() })
	tor, err := cl.OpenTorrent(t.Context(), &info, mi.HashInfoBytes())
	require.NoError(t, err)
	for i := range info.NumPieces() {
		p := tor.Piece(info.Piece(i))
		_, err := p.WriteAt(content[i*pieceLen:(i+1)*pieceLen], 0)
		require.NoError(t, err)
		require.NoError(t, p.MarkComplete())
	}
	for i := range info.NumPieces() {
		wt, ok := tor.Piece(info.Piece(i)).(io.WriterTo)
		require.True(t, ok, "the file storage's piece hashes through WriteTo")
		var hashed bytes.Buffer
		n, err := wt.WriteTo(&hashed)
		require.NoError(t, err, "piece %d of 4: the whole piece was written, so there is no short write", i)
		require.Equal(t, int64(pieceLen), n, "piece %d", i)
		require.Equal(t, content[i*pieceLen:(i+1)*pieceLen], hashed.Bytes(), "piece %d", i)
	}
}
