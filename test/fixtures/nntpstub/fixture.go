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

package nntpstub

import (
	"encoding/xml"
	"fmt"
	"os"
	"time"

	"github.com/javi11/rapidyenc"
)

const (
	// FileName is the name of the fixture's one content file, exactly as it
	// appears in the NZB subject and in a client's published output.
	//
	// Its shape is a real movie-release name, not a bare "clustarr-fixture"
	// stem -- see test/fixtures/seeder.ContentName's doc comment for the
	// full reasoning (a real pkg/fsops.MediaExtensions video extension, a
	// year token pkg/release/movie.go's parseMovie requires to match at
	// all, and a quality/revision pair chosen to match
	// libraryscan_test.go's fixtureMovieFile's (resolution, source) with a
	// higher revision, deliberately, for import_test.go's upgrade proof).
	// This package's own value MUST equal seeder's byte for byte: both
	// fixtures stand in for "the same release available from two
	// protocols" in test/e2e/download_test.go.
	FileName = "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv"

	// DefaultSegmentBytes is one article's DECODED size. Real posts run
	// 512KiB-768KiB (pkg/download/usenet/conn.go's own comment); this
	// fixture defaults much smaller so `go test` stays fast. A caller
	// wanting a release that clears pkg/fsops.IsSuspectedSample's 50MiB
	// video size floor builds a bigger one with [Build].
	DefaultSegmentBytes = 16 * 1024

	// DefaultSegmentCount is how many articles the fixture's one file is
	// split into by default. It must be at least 2 for a caller to deny one
	// segment on one server and still have the fixture mean something.
	DefaultSegmentCount = 4

	group  = "alt.binaries.clustarr"
	poster = "poster@clustarr.fixture.test"
)

// Article is one decoded NNTP article: a yEnc part plus the message-id
// (without angle brackets) it is filed under.
type Article struct {
	ID   string
	Meta rapidyenc.Meta
	Data []byte
}

// Fixture is a small, deterministic usenet post -- one content file, its
// segments and the .nzb XML describing them -- built the same way every
// time it is asked for. Two independent `nntp-stub` processes (different
// Deployments, different --deny flags) call [Build] with the same
// arguments and get byte-identical articles and NZB without sharing any
// state, which is what lets them serve two "providers" of the SAME release,
// disagreeing only about which articles they carry.
type Fixture struct {
	Title    string
	NZB      []byte
	Articles []Article
}

// Build derives a Fixture deterministically. segmentBytes <= 0 or
// segmentCount <= 0 fall back to the package defaults.
func Build(segmentBytes, segmentCount int) Fixture {
	if segmentBytes <= 0 {
		segmentBytes = DefaultSegmentBytes
	}
	if segmentCount <= 0 {
		segmentCount = DefaultSegmentCount
	}

	fileSize := int64(segmentBytes) * int64(segmentCount)
	articles := make([]Article, 0, segmentCount)
	var offset int64
	for i := range segmentCount {
		payload := segmentPayload(byte(i), segmentBytes)
		articles = append(articles, Article{
			ID:   fmt.Sprintf("seg%d.%d@clustarr.fixture.test", i, segmentBytes),
			Data: payload,
			Meta: rapidyenc.Meta{
				FileName:   FileName,
				FileSize:   fileSize,
				PartNumber: int64(i + 1),
				TotalParts: int64(segmentCount),
				Offset:     offset,
				PartSize:   int64(len(payload)),
			},
		})
		offset += int64(len(payload))
	}

	title := fmt.Sprintf("Clustarr.Fixture.%dx%d", segmentCount, segmentBytes)
	return Fixture{
		Title:    title,
		NZB:      buildNZB(title, articles),
		Articles: articles,
	}
}

// BuildFromFile derives a Fixture from REAL bytes read from path (e.g.
// test/fixtures/seed.BakedClipPath), sliced into segmentBytes-sized
// articles (the last one shorter), instead of Build's synthesized,
// non-media payload. This is what lets a Download completed against this
// fixture's NZB become a file pkg/mediainfo can genuinely ffprobe once
// imported -- the same reason test/fixtures/seeder.Config.ContentPath
// exists. segmentCount is not a parameter: it is however many segments the
// real file's length divides into at segmentBytes, unlike Build where the
// caller picks both independently.
//
// Article IDs use the IDENTICAL "seg<n>.<segmentBytes>@clustarr.fixture.
// test" scheme Build uses, so test/e2e/download_test.go's
// TestDownloadUsenetNoInfoHashWithCrossServerFailover can keep computing
// its expected --deny id with the cheap, no-file-access Build(segmentBytes,
// 0).Articles[0].ID rather than needing the real baked clip on the host
// running `go test` (main_test.go's own doc comment: this suite reaches
// neither NATS nor a fixture Service directly, only the apiserver and
// /data -- the baked clip lives only inside the fixture image).
// segmentBytes <= 0 falls back to DefaultSegmentBytes.
func BuildFromFile(path string, segmentBytes int) (Fixture, error) {
	if segmentBytes <= 0 {
		segmentBytes = DefaultSegmentBytes
	}
	data, err := os.ReadFile(path) //nolint:gosec // fixture reads a path this package's own caller controls
	if err != nil {
		return Fixture{}, fmt.Errorf("nntpstub: read content %s: %w", path, err)
	}
	if len(data) == 0 {
		return Fixture{}, fmt.Errorf("nntpstub: content %s is empty", path)
	}

	fileSize := int64(len(data))
	segmentCount := (len(data) + segmentBytes - 1) / segmentBytes
	articles := make([]Article, 0, segmentCount)
	var offset int64
	for i := range segmentCount {
		start := i * segmentBytes
		end := min(start+segmentBytes, len(data))
		payload := data[start:end]
		articles = append(articles, Article{
			ID:   fmt.Sprintf("seg%d.%d@clustarr.fixture.test", i, segmentBytes),
			Data: payload,
			Meta: rapidyenc.Meta{
				FileName:   FileName,
				FileSize:   fileSize,
				PartNumber: int64(i + 1),
				TotalParts: int64(segmentCount),
				Offset:     offset,
				PartSize:   int64(len(payload)),
			},
		})
		offset += int64(len(payload))
	}

	title := fmt.Sprintf("Clustarr.Fixture.%dx%d", segmentCount, segmentBytes)
	return Fixture{
		Title:    title,
		NZB:      buildNZB(title, articles),
		Articles: articles,
	}, nil
}

// segmentPayload makes a deterministic, non-trivial article body: bytes
// yEnc must escape (NUL, CR, LF, '=') plus a line starting with '.', so the
// decode path this fixture drives exercises escaping and NNTP
// dot-unstuffing rather than only plain ASCII -- the same shape
// pkg/download/usenet's own test helper (partPayload, pool_test.go) uses.
func segmentPayload(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) ^ seed
	}
	copy(b, []byte("\r\n.=\x00 leading dot line\r\n"))
	return b
}

// buildNZB renders a real NZB document -- the same shape
// github.com/javi11/nzbparser parses in pkg/download/usenet/nzb.go -- for
// one file made of articles, in segment order.
func buildNZB(title string, articles []Article) []byte {
	type seg struct {
		Bytes  int    `xml:"bytes,attr"`
		Number int    `xml:"number,attr"`
		ID     string `xml:",chardata"`
	}
	type file struct {
		Poster   string   `xml:"poster,attr"`
		Date     int64    `xml:"date,attr"`
		Subject  string   `xml:"subject,attr"`
		Groups   []string `xml:"groups>group"`
		Segments []seg    `xml:"segments>segment"`
	}
	type meta struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	}
	type nzbDoc struct {
		XMLName xml.Name `xml:"nzb"`
		NS      string   `xml:"xmlns,attr"`
		Head    []meta   `xml:"head>meta"`
		Files   []file   `xml:"file"`
	}

	f := file{
		Poster:  poster,
		Date:    time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC).Unix(),
		Subject: fmt.Sprintf("[1/1] - %q yEnc (1/%d)", FileName, len(articles)),
		Groups:  []string{group},
	}
	for _, a := range articles {
		f.Segments = append(f.Segments, seg{Bytes: len(a.Data), Number: int(a.Meta.PartNumber), ID: a.ID})
	}

	doc := nzbDoc{
		NS:    "http://www.newzbin.com/DTD/2003/nzb",
		Head:  []meta{{Type: "title", Value: title}},
		Files: []file{f},
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		// Every field above is a static, controlled value -- xml.Marshal
		// can only fail on an unsupported type, which is a programming
		// error this package's own tests catch, not a runtime condition a
		// caller can act on.
		panic(fmt.Sprintf("nntpstub: marshal fixture nzb: %v", err))
	}
	return append([]byte(xml.Header), body...)
}
