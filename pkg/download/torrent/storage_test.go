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
	"path/filepath"
	"strings"
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

// anacrolix's default file I/O maps every data file it touches into the
// process and never unmaps one for the torrent's life (mmapFileIo, still so
// in v1.62.0): on talos the engine's mapped files reached 1.2 GiB beside
// 436 MiB of its own memory within half an hour of a start, and it was
// OOM-killed at its 2Gi limit (2026-10-15). The storage asks for the classic
// I/O instead (NewFileClientOpts.DisableMmap, since v1.62.0; until then only
// the pod's TORRENT_STORAGE_DEFAULT_FILE_IO could choose it), which reads and
// writes through the page cache the kernel reclaims. So a data file the
// storage wrote is mapped nowhere in the process.
func TestTheTorrentStorageMapsNoDataFile(t *testing.T) {
	if _, err := os.ReadFile("/proc/self/maps"); err != nil {
		t.Skipf("no /proc/self/maps to read the process's mappings from: %v", err)
	}
	const pieceLen = 16 * 1024
	content := bytes.Repeat([]byte("clustarr"), 4*pieceLen/8)
	mi, err := metainfo.Load(bytes.NewReader(buildTestTorrent(t, t.TempDir(), "release.mkv", content)))
	require.NoError(t, err)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)

	dir := t.TempDir()
	cl := fileStorage(dir)
	t.Cleanup(func() { _ = cl.Close() })
	tor, err := cl.OpenTorrent(t.Context(), &info, mi.HashInfoBytes())
	require.NoError(t, err)
	p := tor.Piece(info.Piece(0))
	_, err = p.WriteAt(content[:pieceLen], 0)
	require.NoError(t, err)
	require.NoError(t, p.MarkComplete())

	maps, err := os.ReadFile("/proc/self/maps")
	require.NoError(t, err)
	var mapped []string
	for line := range strings.SplitSeq(string(maps), "\n") {
		if strings.Contains(line, dir) {
			mapped = append(mapped, line)
		}
	}
	require.Empty(t, mapped, "the storage's data files go through the page cache, never a mapping")
}

// Hashing a piece reads it through the storage's WriteTo. Under anacrolix
// v1.61's classic file I/O that path handed os.File.WriteTo a writer limited
// to the piece, which refused the file's bytes past it as a short write, so
// every piece but a file's last hashed correctly and still came back
// io.ErrShortWrite: 125k WARN lines a day on talos ("finished hashing
// piece", correct true, err short write) and no smart-ban attribution,
// since anacrolix skips it on a storage error. v1.62.0 fixed it upstream
// (#1132); this holds the bump to it, since the engine runs the classic I/O
// on purpose (above).
func TestHashingAPieceIsNoShortWrite(t *testing.T) {
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
