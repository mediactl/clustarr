/*
Copyright 2026 The clustarr Authors.

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
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// halves is b as two articles' worth.
func halves(b []byte) [][]byte { return [][]byte{b[:len(b)/2], b[len(b)/2:]} }

// A week of Lupin the 3rd's fourth season failed to unpack on talos, 216
// times, "open .../Lupin.The.3rd.S04E09.1080p.WEB.x264-ANiURL.part01.r00: no
// such file or directory", with every article fetched (2026-11-27). The
// release is an old-style scene set -- x.rar, x.r00, ..., x.r09 -- and the
// indexer's NZB lists its first volume as "<release>.part01.rar", so the
// volume after it was looked for beside a name the set never had; unrar
// itself fails the same way on those names. The articles carry the real
// one: the volume takes it, and the release unpacks.
//
// The download here is the whole path: the NZB's listing, the names on the
// wire, a real old-style multi-volume archive (stored, as `unrar t` accepts
// it) and the unpacker.
func TestAnOldStyleSetWhoseFirstVolumeTheNZBRenamedIsUnpacked(t *testing.T) {
	srv := newStubServer(t)
	payload := make([]byte, 200<<10)
	_, _ = rand.New(rand.NewSource(49)).Read(payload)
	vols := storedRarVolumes("rel-show.s04e09.1080p.web.mkv", payload, 64<<10, false)
	require.Len(t, vols, 4)

	nzb := buildNZB(t, srv, "Show.S04E09.1080p.WEB.x264-REL", []fileSpec{
		{name: "rel-show.s04e09.1080p.web.nfo", parts: [][]byte{[]byte("notes")}},
		{name: "rel-show.s04e09.1080p.web.r00", parts: halves(vols[1])},
		{name: "rel-show.s04e09.1080p.web.r01", parts: halves(vols[2])},
		{name: "rel-show.s04e09.1080p.web.r02", parts: halves(vols[3])},
		// As nzbgeek lists it; as its poster named it.
		{name: "Show.S04E09.1080p.WEB.x264-REL.part01.rar", wire: "rel-show.s04e09.1080p.web.rar", parts: halves(vols[0])},
	})

	c, _, dataDir := newTestClient(t, Config{
		Providers:   []Provider{srv.provider("solo", 2, 1)},
		PostProcess: PostProcess{Unpack: true, DeleteArchives: true},
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Show.S04E09", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	got, err := os.ReadFile(filepath.Join(dataDir, "default", "Show.S04E09", "rel-show.s04e09.1080p.web.mkv"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(payload, got), "the episode is what the set packed")
}

// The wire name is taken for a volume the listing strands, and for nothing
// else: an obfuscated post puts another name on every article, and a
// listing that already makes a set is the one the unpacker follows.
func TestAWireNameIsTakenOnlyForAVolumeItsListingStrands(t *testing.T) {
	vols := storedRarVolumes("episode.mkv", bytes.Repeat([]byte("stored "), 4096), 8<<10, false)
	require.GreaterOrEqual(t, len(vols), 3)

	for _, tc := range []struct {
		name string
		// listed -> wire, in the NZB's order; content is vols[i] unless notRar.
		listed, wire []string
		notRar       map[int]bool
		want         []string
	}{
		{
			name:   "the first volume listed outside its set takes its wire name",
			listed: []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			wire:   []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			want:   []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
		},
		{
			name:   "a volume listed under no archive name at all does too",
			listed: []string{"0f3a9c1e77", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			wire:   []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			want:   []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
		},
		{
			name:   "a listing that is a set already is left as listed",
			listed: []string{"Show.S01E01-GRP.part01.rar", "Show.S01E01-GRP.part02.rar", "Show.S01E01-GRP.part03.rar"},
			wire:   []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			want:   []string{"Show.S01E01-GRP.part01.rar", "Show.S01E01-GRP.part02.rar", "Show.S01E01-GRP.part03.rar"},
		},
		{
			name:   "nor does a volume with company leave it for another set in the download",
			listed: []string{"Show.S01E01-GRP.part01.rar", "Show.S01E01-GRP.part02.rar", "grp-show.s01e01.r00"},
			wire:   []string{"grp-show.s01e01.rar", "Show.S01E01-GRP.part02.rar", "grp-show.s01e01.r00"},
			want:   []string{"Show.S01E01-GRP.part01.rar", "Show.S01E01-GRP.part02.rar", "grp-show.s01e01.r00"},
		},
		{
			name:   "a wire name in no set of the download is an obfuscated article's",
			listed: []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			wire:   []string{"x9Kq2.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			want:   []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
		},
		{
			name:   "a name another file holds is not taken from it",
			listed: []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			wire:   []string{"grp-show.s01e01.r00", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			want:   []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
		},
		{
			name:   "a file that is no archive keeps the name the importer will read",
			listed: []string{"Show.S01E01-GRP.sample.mkv", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			wire:   []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			notRar: map[int]bool{0: true},
			want:   []string{"Show.S01E01-GRP.sample.mkv", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
		},
		{
			name:   "no wire name, nothing to take",
			listed: []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
			wire:   []string{"", "", ""},
			want:   []string{"Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00", "grp-show.s01e01.r01"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})
			nzb := nzbWith(t, "", tc.listed...)
			parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
			require.NoError(t, err)
			j := c.newJob("job1", "download-1", "tv", filepath.Join(c.cfg.ScratchDir, "tv", "job1"), parsed)
			require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
			for i, n := range tc.listed {
				content := vols[i]
				if tc.notRar[i] {
					content = []byte("\x1a\x45\xdf\xa3 a matroska sample")
				}
				require.NoError(t, os.WriteFile(filepath.Join(j.contentDir(), n), content, 0o644))
			}
			copy(j.wireNames, tc.wire)

			j.renameObfuscated(context.Background())

			var got []string
			for _, f := range j.nzb.Files {
				got = append(got, f.Name)
				assert.FileExists(t, filepath.Join(j.contentDir(), f.Name))
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// The wire names are kept with the job, so a download re-attached after a
// restart between its transfer and its unpack still has them.
func TestWireNamesSurviveARestart(t *testing.T) {
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}})
	nzb := nzbWith(t, "", "Show.S01E01-GRP.part01.rar", "grp-show.s01e01.r00")
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", "download-1", "tv", filepath.Join(c.cfg.ScratchDir, "tv", "job1"), parsed)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	copy(j.wireNames, []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00"})
	require.NoError(t, j.checkpoint())

	loaded, err := c.loadJob(j.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"grp-show.s01e01.rar", "grp-show.s01e01.r00"}, loaded.wireNames)
}
