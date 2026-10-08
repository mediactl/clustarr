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

package torznabstub

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
)

// The download scenarios' releases (test/e2e/download_test.go and the
// scenario 1 legs): the title they search for is Inception, the one movie
// the TMDB stub serves from a recording, and each release links to a REAL
// payload another fixture serves -- the seeder's .torrent, an NNTP stub's
// .nzb -- so a Search pick of one reaches a real transfer and a real
// import. Until the smoke test of 2026-10-07 every release here linked to
// /dl/*.torrent routes the stub never served, under made-up info hashes.
//
// The torrent personality (PathHealthy) answers a movie search for
// Inception with GUIDSeeder and GUIDGone; the usenet personality
// (PathUsenet, a Newznab indexer) with GUIDNZB. Every other id answers as
// before.
const (
	// InceptionTmdbID and InceptionImdbID are the ids an Inception movie
	// search carries (imdbid as Newznab sends it, without "tt").
	InceptionTmdbID = "27205"
	InceptionImdbID = "1375666"

	// GUIDSeeder is the release the seeder seeds: a REPACK, so it is a
	// revision upgrade over a planted Bluray-1080p v1 file (import_test.go's
	// TestFileImportUpgradeAttempt).
	GUIDSeeder  = "fixture-inception-seeder"
	TitleSeeder = "Inception.2010.REPACK.1080p.BluRay.x264-CLUSTARR"

	// GUIDGone is a release whose payload is gone: its link answers 404
	// (PathGone), so a grab of it fails payloadUnavailable, a release fault
	// that blocklists it. It is v1, so it is never an upgrade over a
	// Bluray-1080p file, and an automatic pick prefers GUIDSeeder.
	GUIDGone  = "fixture-inception-gone"
	TitleGone = "Inception.2010.1080p.BluRay.x264-GONE"

	// GUIDNZB is the usenet release: the NNTP stubs' post, with no info
	// hash.
	GUIDNZB  = "fixture-inception-nzb"
	TitleNZB = "Inception.2010.1080p.BluRay.DTS.x264-CLUSTARR"

	// PathUsenet is the Newznab personality (Indexer spec.generic.apiPath,
	// protocol usenet).
	PathUsenet = "/usenet/api"

	// PathGone serves the 404 GUIDGone's link names.
	PathGone = "/dl/gone/"

	// releaseBytes is the size every download release advertises: a
	// Bluray-1080p film at Inception's 148 minutes (and at pkg/decision's
	// 110-minute fallback) clears the quality's floor only at a real
	// encode's size, never at the 57 MiB clip's, which the smoke test's
	// first feed advertised and the decision refused (BelowMinimumSize).
	// The transfer is the clip; nothing compares the two.
	releaseBytes int64 = 9126805504
)

// Payloads are where the download releases' links point. A field left
// empty drops its release from the feed, so a stub without a seeder or an
// NNTP stub to point at advertises nothing it cannot deliver.
type Payloads struct {
	// TorrentURL is the seeder's .torrent (test/fixtures/seeder), and
	// TorrentInfoHash its info hash (seeder.InfoHash of the same clip), in
	// lowercase hex. A torrent release carries its hash, and the engine
	// refuses a payload whose hash differs (payloadMismatch).
	TorrentURL, TorrentInfoHash string
	// NZBURL is an NNTP stub's .nzb (test/fixtures/nntpstub).
	NZBURL string
	// GoneURL is this stub's own PathGone route, as the cluster reaches it.
	GoneURL string
}

// item is one feed item.
type item struct {
	guid, title, link, mime string
	categories              []int
	attrs                   [][2]string
}

// inceptionTorrents is the torrent personality's answer for Inception.
func (p Payloads) inceptionTorrents() []item {
	var out []item
	if p.TorrentURL != "" {
		attrs := [][2]string{{"seeders", "50"}, {"peers", "51"}}
		if p.TorrentInfoHash != "" {
			attrs = append(attrs, [2]string{"infohash", p.TorrentInfoHash})
		}
		out = append(out, item{
			guid: GUIDSeeder, title: TitleSeeder, link: p.TorrentURL, mime: "application/x-bittorrent",
			categories: []int{2000, 2050}, attrs: attrs,
		})
	}
	if p.GoneURL != "" {
		out = append(out, item{
			guid: GUIDGone, title: TitleGone, link: p.GoneURL, mime: "application/x-bittorrent",
			categories: []int{2000, 2050},
			attrs:      [][2]string{{"seeders", "40"}, {"peers", "41"}, {"infohash", "5555555555555555555555555555555555555555"}},
		})
	}
	return out
}

// inceptionNZBs is the usenet personality's answer for Inception.
func (p Payloads) inceptionNZBs() []item {
	if p.NZBURL == "" {
		return nil
	}
	return []item{{
		guid: GUIDNZB, title: TitleNZB, link: p.NZBURL, mime: "application/x-nzb",
		categories: []int{2000, 2040}, attrs: [][2]string{{"grabs", "100"}},
	}}
}

// isInception reports whether a movie search names Inception by either id.
func isInception(tmdbID, imdbID string) bool {
	return tmdbID == InceptionTmdbID || imdbID == InceptionImdbID || imdbID == "tt"+InceptionImdbID
}

// feed renders releases as a Torznab (ns "torznab") or Newznab (ns
// "newznab") result feed, the shape testdata/movie_900100.xml has.
func feed(ns string, rels []item) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, "<rss version=\"2.0\" xmlns:torznab=\"http://torznab.com/schemas/2015/feed\" "+
		"xmlns:newznab=\"http://www.newznab.com/DTD/2010/feeds/attributes/\">\n  <channel>\n"+
		"    <title>Clustarr E2E Fixture Indexer</title>\n")
	for _, r := range rels {
		b.WriteString("    <item>\n")
		el(&b, "title", r.title)
		el(&b, "guid", r.guid)
		el(&b, "link", r.link)
		el(&b, "pubDate", "Mon, 06 Oct 2026 09:00:00 +0000")
		el(&b, "size", strconv.FormatInt(releaseBytes, 10))
		for _, c := range r.categories {
			el(&b, "category", strconv.Itoa(c))
		}
		fmt.Fprintf(&b, "      <enclosure url=%s length=\"%d\" type=%s/>\n", attr(r.link), releaseBytes, attr(r.mime))
		attrs := append([][2]string{{"tmdbid", InceptionTmdbID}, {"imdbid", "tt" + InceptionImdbID}}, r.attrs...)
		for _, a := range attrs {
			fmt.Fprintf(&b, "      <%s:attr name=%s value=%s/>\n", ns, attr(a[0]), attr(a[1]))
		}
		b.WriteString("    </item>\n")
	}
	b.WriteString("  </channel>\n</rss>\n")
	return b.Bytes()
}

func el(b *bytes.Buffer, name, text string) {
	fmt.Fprintf(b, "      <%s>%s</%s>\n", name, esc(text), name)
}

// attr is s as a quoted XML attribute value.
func attr(s string) string { return `"` + esc(s) + `"` }

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
