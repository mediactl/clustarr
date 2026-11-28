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

package usenet

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/test/par2test"
)

// mkvHead is the opening of a Matroska file: the EBML header, a segment of
// unknown size and its information, holding title when one is given.
func mkvHead(title string) []byte {
	el := func(id []byte, body []byte) []byte {
		out := append([]byte(nil), id...)
		out = append(out, 0x40|byte(len(body)>>8), byte(len(body))) // a two-byte size
		return append(out, body...)
	}
	info := el([]byte{0x2A, 0xD7, 0xB1}, []byte{0x0F, 0x42, 0x40}) // TimestampScale
	if title != "" {
		info = append(info, el([]byte{0x7B, 0xA9}, []byte(title))...)
	}
	head := el([]byte{0x1A, 0x45, 0xDF, 0xA3}, el([]byte{0x42, 0x82}, []byte("matroska")))
	head = append(head, 0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF) // Segment, size unknown
	head = append(head, el([]byte{0xEC}, make([]byte, 40))...)                                  // Void, as a SeekHead's room
	return append(head, el([]byte{0x15, 0x49, 0xA9, 0x66}, info)...)
}

func TestMatroskaTitle(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, append(b, make([]byte, 4096)...), 0o644))
		return p
	}
	require.Equal(t, "One.Piece.S14E01.1080p.CR.WEB-DL.DUAL-Kitsune", matroskaTitle(write("a.mkv", mkvHead("One.Piece.S14E01.1080p.CR.WEB-DL.DUAL-Kitsune"))))
	require.Empty(t, matroskaTitle(write("b.mkv", mkvHead(""))))
	require.Empty(t, matroskaTitle(write("c.mkv", []byte("not matroska at all"))))
	require.Empty(t, matroskaTitle(filepath.Join(dir, "missing.mkv")))
}

// hashedPack is a post of two episodes named by hashes, each of parts
// articles, the first article opening as a Matroska file with title.
func hashedPack(t *testing.T, srv *stubServer, title string) []byte {
	t.Helper()
	files := make([]fileSpec, 2)
	for f, name := range []string{"0101688cc115e016dcbeabe25daf4f0c.mkv", "034a004ab378b525f66918c1c54022df.mkv"} {
		parts := make([][]byte, 6)
		for i := range parts {
			parts[i] = partPayload(byte(f*17+i), 2048)
		}
		head := mkvHead(title)
		copy(parts[0], head)
		files[f] = fileSpec{name: name, parts: parts}
	}
	return buildNZB(t, srv, "One.Piece.S14.1080p.CR.WEB-DL.DUAL.DDP2.0.H.264-Kitsune", files)
}

// A pack whose files nothing names is refused when their first articles
// have arrived, not after the whole of it: 56 GB of One Piece's fourteenth
// season was fetched on talos (2026-11-28), published as 34 files named by
// hashes, and held at import for a day ("is a hash, not a name"). The same
// post whose files carry a title is fetched whole, as it was.
func TestAPackNothingNamesIsRefusedBeforeItIsFetched(t *testing.T) {
	old := unnameableMinBytes
	unnameableMinBytes = 4096
	t.Cleanup(func() { unnameableMinBytes = old })

	t.Run("named by nothing", func(t *testing.T) {
		srv := newStubServer(t)
		nzb := hashedPack(t, srv, "")
		c, _, dataDir := newTestClient(t, Config{
			Providers: []Provider{srv.provider("solo", 2, 1)}, HealthAction: downloadv1alpha1.HealthActionDelete,
		})
		id, err := c.Add(context.Background(), download.AddRequest{Name: "One.Piece.S14", Payload: nzb, Category: "tv"})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusFailed, it.Status, "message: %s", it.Message)
		require.Equal(t, commonv1alpha1.DownloadFailureImportRejected, it.FailureReason)
		require.Contains(t, it.Message, "named by hashes")
		require.Zero(t, srv.servedCount("f0-p3@clustarr.test"), "nothing past the first articles was fetched")
		require.Zero(t, srv.servedCount("f1-p5@clustarr.test"))
		_, err = os.Stat(filepath.Join(dataDir, "tv", "One.Piece.S14"))
		require.True(t, os.IsNotExist(err), "nothing is published")
	})
	t.Run("named by the files' own title", func(t *testing.T) {
		srv := newStubServer(t)
		nzb := hashedPack(t, srv, "One.Piece.S14E01.1080p.CR.WEB-DL.DUAL.DDP2.0.H.264-Kitsune")
		c, _, _ := newTestClient(t, Config{
			Providers: []Provider{srv.provider("solo", 2, 1)}, HealthAction: downloadv1alpha1.HealthActionDelete,
		})
		id, err := c.Add(context.Background(), download.AddRequest{Name: "One.Piece.S14", Payload: nzb, Category: "tv"})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	})
}

func TestHashName(t *testing.T) {
	for name, want := range map[string]bool{
		"0101688cc115e016dcbeabe25daf4f0c.mkv":              true,
		"GiDY8v0wuYowRRaQ.7z.001":                           false, // obfuscated, but no hash: not held against a post
		"One.Piece.S14E01.1080p.CR.WEB-DL.DUAL-Kitsune.mkv": false,
		"b082fa0beaa644d3aa01045d5b8d0b36.vol000+01.par2":   true,
		"[Judas] Lupin the Third - S03E20.mkv":              false,
	} {
		require.Equal(t, want, hashName(name), name)
	}
}

// The par2 set is what names an obfuscated post, so the gate reads it: a
// set that records real names lets the post through for the rename step,
// and one that records the same hashes does not.
func TestThePar2SetDecidesWhetherAHashedPackHasNames(t *testing.T) {
	old := unnameableMinBytes
	unnameableMinBytes = 900 // nzbWith declares 1000 bytes a file
	t.Cleanup(func() { unnameableMinBytes = old })
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})
	hashed := []string{"0101688cc115e016dcbeabe25daf4f0c.mkv", "034a004ab378b525f66918c1c54022df.mkv"}

	for name, tc := range map[string]struct {
		setNames []string
		refused  bool
	}{
		"the set names the episodes": {[]string{"One.Piece.S14E01.1080p-Kitsune.mkv", "One.Piece.S14E02.1080p-Kitsune.mkv"}, false},
		"the set records the hashes": {hashed, true},
	} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			for i, n := range tc.setNames {
				data := make([]byte, 64<<10)
				_, _ = rand.New(rand.NewSource(int64(i + 1))).Read(data)
				require.NoError(t, os.WriteFile(filepath.Join(work, n), data, 0o644))
			}
			par2test.CreateSet(t, work, "b082fa0beaa644d3aa01045d5b8d0b36.par2", tc.setNames, 16, 10, 0)

			nzb := nzbWith(t, "", hashed[0], hashed[1], "b082fa0beaa644d3aa01045d5b8d0b36.par2")
			parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
			require.NoError(t, err)
			j := c.newJob("job-"+name, "download-1", "tv", filepath.Join(c.cfg.ScratchDir, "tv", "job-"+name), parsed)
			require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
			for i, n := range tc.setNames {
				require.NoError(t, os.Rename(filepath.Join(work, n), filepath.Join(j.contentDir(), hashed[i])))
			}
			require.NoError(t, os.Rename(filepath.Join(work, "b082fa0beaa644d3aa01045d5b8d0b36.par2"), filepath.Join(j.contentDir(), "b082fa0beaa644d3aa01045d5b8d0b36.par2")))

			err = j.unnameableGate()
			if tc.refused {
				require.ErrorIs(t, err, ErrUnnameable)
				require.Contains(t, err.Error(), "its par2 set records the same hashes")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
