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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javi11/rapidyenc"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// A server can answer a segment's message-ID with another post's article:
// frugal served AnoZu's S01E01 (posted 2026-02-16, part 1 of 2100) with
// "b5f29fcb1fe75f9e"'s part 237 of 503, posted 2026-08-12 under the same
// message-IDs, while easynews held the original (2026-10-20). Each such
// article is whole -- its own pcrc32 holds -- so it passed as verified, was
// written at its own post's offsets, and par2 was skipped: the file came
// out as foreign bytes and holes. An article whose yEnc part is not the
// segment's, or whose total is short of the file's segments, is no copy of
// it: the next server's is taken, the first article's included (whose
// size= the file is cut to), and the file is whole.
func TestAnotherPostsArticleUnderTheSameMessageIDIsTakenFromTheNextServer(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	parts := fourParts()
	nzb := buildNZB(t, primary, "Collided", []fileSpec{{name: "movie.mkv", parts: parts}})
	backup.mirror(primary)
	primary.addArticle("f0-p0@clustarr.test", "b5f29fcb1fe75f9e", 5000, 400, 237, 503, partPayload(9, 400))
	primary.addArticle("f0-p2@clustarr.test", "b5f29fcb1fe75f9e", 5000, 0, 3, 3, partPayload(8, 400))

	bp := backup.provider("backup", 2, 2)
	bp.Backup = true
	c, _, _ := newTestClient(t, Config{Providers: []Provider{primary.provider("primary", 2, 1), bp}, Ledger: NewLocalLedger()})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Collided", Payload: nzb})
	require.NoError(t, err)

	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	got, err := os.ReadFile(filepath.Join(it.OutputPath, "movie.mkv"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(bytes.Join(parts, nil), got), "the file is not the post's: %d bytes", len(got))
	require.Equal(t, 1, backup.servedCount("f0-p0@clustarr.test"))
	require.Equal(t, 1, backup.servedCount("f0-p2@clustarr.test"))
	require.Equal(t, 2, backup.servedTotal(), "the backup filled only the impostors")
}

// segmentMismatch reads the segment's file as its highest NZB segment
// number, 4 here, and its NZB bytes as bytes (0, unknown, unless a case
// says).
func TestSegmentMismatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		seg   segment
		meta  rapidyenc.Meta
		bytes int64
		wrong bool
	}{
		{"the segment's own part", segment{Number: 2}, rapidyenc.Meta{PartNumber: 2, TotalParts: 4}, 0, false},
		{"another part", segment{Number: 1}, rapidyenc.Meta{PartNumber: 237, TotalParts: 503}, 0, true},
		{"a total short of the file", segment{Number: 3}, rapidyenc.Meta{PartNumber: 3, TotalParts: 3}, 0, true},
		// An NZB that lost segments still lists numbers within the post's total.
		{"a total past the NZB's segments", segment{Number: 4}, rapidyenc.Meta{PartNumber: 4, TotalParts: 9}, 0, false},
		{"a single-part article names no part", segment{Number: 1}, rapidyenc.Meta{}, 0, false},
		{"an NZB segment without a number", segment{}, rapidyenc.Meta{PartNumber: 7, TotalParts: 9}, 0, false},
		// talos, 2026-10-28: a 1.5 GB file's first article came back as a
		// 40,484-byte file's single part, every check above passed, and the
		// file was written over from offset 0.
		{"a single-part article of a far smaller file", segment{Number: 1}, rapidyenc.Meta{FileSize: 40484}, 1_570_000_000, true},
		{"another post's part 1 of a far smaller file", segment{Number: 1}, rapidyenc.Meta{FileSize: 40484, PartNumber: 1, TotalParts: 4}, 1_570_000_000, true},
		{"the file the NZB lists", segment{Number: 1}, rapidyenc.Meta{FileSize: 1_522_520_842, PartNumber: 1, TotalParts: 4}, 1_570_000_000, false},
		// An NZB that lost segments lists less than the file holds.
		{"a file larger than the NZB lists", segment{Number: 1}, rapidyenc.Meta{FileSize: 4_000_000, PartNumber: 1, TotalParts: 9}, 1_000_000, false},
		{"a header that names no size", segment{Number: 1}, rapidyenc.Meta{PartNumber: 1, TotalParts: 4}, 1_570_000_000, false},
		// An article's own headers outweigh a tiny file: an sfv's 100 bytes
		// are an NZB segment of several hundred.
		{"a tiny file under its headers", segment{Number: 1}, rapidyenc.Meta{FileSize: 100}, 420, false},
	} {
		err := segmentMismatch(4, tc.bytes, tc.seg, tc.meta)
		require.Equal(t, tc.wrong, err != nil, "%s: %v", tc.name, err)
		if err != nil {
			require.ErrorIs(t, err, ErrArticleMismatch, tc.name)
		}
	}
}
