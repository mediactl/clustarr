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

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/test/par2test"
)

// An NZB can list two files under one name: an indexer's listing of an
// obfuscated post gave the episode and its par2 index the same hash, with
// nothing else in the subject to tell them apart. Both were written to that
// one path -- whichever first article came second truncated the file to its
// own size and wrote over the other's start -- and the job failed "scratch
// files are gone or altered: b0bf....mkv is 1515354440 bytes, its yEnc
// header declared 40472" (talos, 2026-11-28: ten downloads in an hour, nine
// of them One Piece episodes from one indexer). Each file has its own path,
// in either order of listing, and the episode is published whole under the
// name its par2 set records.
func TestTwoFilesListedUnderOneNameAreEachWritten(t *testing.T) {
	const hash = "b0bfb9714c1bc7e48e4f76456269701b"
	const episode = "One.Piece.S16E01.1080p.WEB-DL.mkv"

	stage := t.TempDir()
	content := make([]byte, 96<<10)
	_, _ = rand.New(rand.NewSource(16)).Read(content)
	copy(content, mkvHead(""))
	require.NoError(t, os.WriteFile(filepath.Join(stage, episode), content, 0o644))
	par2test.CreateSet(t, stage, episode+".par2", []string{episode}, 16, 10, 0)
	index, err := os.ReadFile(filepath.Join(stage, episode+".par2"))
	require.NoError(t, err)

	const parts = 4
	size := len(content) / parts
	episodeParts := make([][]byte, parts)
	for i := range parts {
		episodeParts[i] = content[i*size : min((i+1)*size, len(content))]
	}
	listed := map[string][]fileSpec{
		"the par2 index first": {{name: hash, parts: [][]byte{index}}, {name: hash, parts: episodeParts}},
		"the episode first":    {{name: hash, parts: episodeParts}, {name: hash, parts: [][]byte{index}}},
	}
	for name, specs := range listed {
		t.Run(name, func(t *testing.T) {
			srv := newStubServer(t)
			nzb := buildNZB(t, srv, "One.Piece.S16E01", specs)
			c, _, dataDir := newTestClient(t, Config{
				Providers:   []Provider{srv.provider("solo", 2, 1)},
				Repairer:    libraryRepairer(t),
				PostProcess: PostProcess{Par2: true, DeleteArchives: true},
			})
			id, err := c.Add(context.Background(), download.AddRequest{Name: "One.Piece.S16E01", Payload: nzb})
			require.NoError(t, err)
			it := waitForTerminal(t, c, id)
			require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)

			published := filepath.Join(dataDir, "default", "One.Piece.S16E01")
			got, err := os.ReadFile(filepath.Join(published, episode))
			require.NoError(t, err, "the episode takes the name its par2 set records")
			require.Equal(t, content, got, "the episode is what was posted, its start not the par2 index's")
			left, err := os.ReadDir(published)
			require.NoError(t, err)
			require.Len(t, left, 1, "the par2 index is not published")
		})
	}
}

// Two files of one name that differ are two files; two that agree in every
// article's size are one file posted twice, and stay on one path as they
// always were.
func TestParseNZBNamesApartFilesListedUnderOneName(t *testing.T) {
	srv := newStubServer(t)
	big := [][]byte{partPayload(1, 2048), partPayload(2, 2048), partPayload(3, 2048)}
	small := [][]byte{partPayload(4, 512)}
	nzb := buildNZB(t, srv, "", []fileSpec{
		{name: "0a1b2c3d", parts: small},
		{name: "0a1b2c3d", parts: big},
		{name: "0a1b2c3d", parts: big},
		{name: "other.mkv", parts: small},
		{name: "0a1b2c3d-2", parts: small},
	})
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	names := make([]string, len(parsed.Files))
	for i, f := range parsed.Files {
		names[i] = f.Name
	}
	require.Equal(t, []string{"0a1b2c3d-3", "0a1b2c3d", "0a1b2c3d", "other.mkv", "0a1b2c3d-2"}, names,
		"the largest keeps the name, a repost shares it, and a name given is one nothing holds")

	again, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	for i, f := range again.Files {
		require.Equal(t, names[i], f.Name, "the names are the same at every reading: a restart parses again")
	}
}
