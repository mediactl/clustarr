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

package rescan

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/release"
)

// Identifying an unmapped folder (the 2026-11-10 non-video libraries
// design's D16, §5.7). A first segment under a music or book root that no
// Artist or Author of the root names, or an audiobook's "<author>/<title>"
// folder no Audiobook holds, is the container its files belong to, and the
// walk proposes it: an ItemCreate the manager's scan applier creates,
// pinned to that folder, as a Series is from its folder's TheTVDB id. This
// is amendment §A1.5 as written -- an embedded identifier first, then the
// catalog, then the metadata gateway, where the gateway is asked and only
// an exact, unique answer is taken -- and Lidarr's Library Import, whose
// IdentificationService fetches candidates for an unmapped folder and a
// person confirms a match. In order:
//
//   - a token in the folder's name: [mbid-<uuid>] or {mbid-<uuid>} for an
//     artist, [olid-OL...A] for an author, an ASIN ("[ASIN B0...]",
//     "[B0...]") for an audiobook, which is read in the root's
//     defaults.region;
//   - for an artist, the MUSICBRAINZ_ARTISTID that up to five of the
//     folder's audio files agree on, each whose album artist (else artist)
//     tag is the folder's name, validated by the gateway's lookup
//     (events.RPCMetadataLookup), whose answer names the artist;
//   - the gateway's search (events.RPCMetadataSearch) for the folder's
//     name, read strictly: the hits whose name equals the folder's by
//     [release.TitleNorm] (CleanTitle on ASCII; a non-Latin name keeps its
//     letters), and exactly one of them at the top score among those
//     (MusicBrainz scores its hits; Open Library and Audible score none,
//     so there it is exactly one such hit). An audiobook's hit must also
//     name the author folder among its authors, and its year the folder's
//     when both say one.
//
// A folder none of them resolves is never guessed: it is listed once in
// status.unmatched (UnmatchedFile.folder) with the hits as candidates,
// "Name (disambiguation) [id]", for the Unmatched page to confirm (D20), and
// its files are counted as awaiting, not listed. An identity an existing
// item of the namespace already has is that item's: this root's takes the
// folder's files, another root's leaves the folder unmatched, and no
// identity is proposed twice in one walk.

// CodeLookupFailed means the metadata gateway could not be asked about a
// folder, or could not answer: nothing was learnt, so the folder is
// unmatched this time and the next scan asks again. Never a guess.
const CodeLookupFailed = "lookup_failed"

// maxTaggedFiles is how many of an unmapped artist folder's audio files the
// walk probes for the MUSICBRAINZ_ARTISTID they agree on.
const maxTaggedFiles = 5

var (
	// mbidFolderRE is a MusicBrainz artist id token in a folder name,
	// "[mbid-<uuid>]" or "{mbid-<uuid>}" (Lidarr's and Plex's spellings).
	mbidFolderRE = regexp.MustCompile(
		`(?i)\s*[\[{]mbid-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})[\]}]`)

	// olidFolderRE is an Open Library author id token, "[olid-OL23919A]".
	olidFolderRE = regexp.MustCompile(`(?i)\s*[\[{]olid-(OL\d+A)[\]}]`)

	// mbidRE is a bare MusicBrainz id, as a tag carries it.
	mbidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// LookupFunc asks the metadata gateway's lookup verb
// (events.RPCMetadataLookup) for the item of kind ids name, and answers its
// canonical name. An error is a lookup that did not answer: the gateway was
// unreachable, or the provider knows no such item.
type LookupFunc func(ctx context.Context, kind commonv1.MediaKind, ids map[string]string) (name string, err error)

// SearchFunc asks the metadata gateway's search verb
// (events.RPCMetadataSearch) for items of kind named text, in the Audible
// region for an audiobook, and answers the provider's hits in its order.
type SearchFunc func(ctx context.Context, kind commonv1.MediaKind, text, region string) ([]metadata.SearchHit, error)

// FolderIdentity is the verdict on an unmapped folder: the provider id and
// name of the item it is, or why it is none.
type FolderIdentity struct {
	// ID is the provider id: a MusicBrainz artist id, an Open Library
	// author id ("OL...A") or an ASIN.
	ID string

	// Title is the item's name: the lookup's or the hit's, else the
	// folder's own with its token taken out. Year is an audiobook's.
	Title string
	Year  int

	// Evidence says what identified the folder: "token", "tags" or
	// "search".
	Evidence string

	// Unmatched, Code, Reason and Candidates mean what they mean on
	// [ItemMatch]; Candidates are the provider's hits as
	// "Name (disambiguation) [id]".
	Unmatched  bool
	Code       string
	Reason     string
	Candidates []string
}

// IdentifyArtist identifies the artist an unmapped folder under a music root
// is: its [mbid-...] token; else the MUSICBRAINZ_ARTISTID tags carries (the
// probed tags of up to five of its audio files) agree on, validated by
// lookup; else search, read strictly.
func IdentifyArtist(
	ctx context.Context, folder string, tags []*commonv1.MediaTags, lookup LookupFunc, search SearchFunc,
) FolderIdentity {
	if m := mbidFolderRE.FindStringSubmatch(folder); m != nil {
		return FolderIdentity{ID: strings.ToLower(m[1]), Title: tokenless(folder, mbidFolderRE), Evidence: "token"}
	}
	if id, ok := agreedArtistID(folder, tags); ok {
		if lookup == nil {
			return FolderIdentity{Unmatched: true, Code: CodeLookupFailed, Reason: fmt.Sprintf(
				"the files' MUSICBRAINZ_ARTISTID %s needs the metadata gateway to validate it and no lookup is configured", id)}
		}
		name, err := lookup(ctx, commonv1.MediaKindArtist, map[string]string{metadata.KeyMBArtist: id})
		if err != nil || strings.TrimSpace(name) == "" {
			reason := fmt.Sprintf("the metadata gateway could not validate the MUSICBRAINZ_ARTISTID %s the folder's files "+
				"agree on, so the folder is not identified by it", id)
			if err != nil {
				reason = fmt.Sprintf("%s: %v", reason, err)
			}
			return FolderIdentity{Unmatched: true, Code: CodeUnresolvedID, Reason: reason}
		}
		return FolderIdentity{ID: id, Title: name, Evidence: "tags"}
	}
	return searchStrictly(ctx, search, commonv1.MediaKindArtist, folder, folder, "", metadata.KeyMBArtist, nil)
}

// IdentifyAuthor identifies the author an unmapped folder under a book root
// is: its [olid-...] token, else search, read strictly. Open Library has no
// tag a book file carries, so there is no tag layer.
func IdentifyAuthor(ctx context.Context, folder string, search SearchFunc) FolderIdentity {
	if m := olidFolderRE.FindStringSubmatch(folder); m != nil {
		return FolderIdentity{ID: strings.ToUpper(m[1]), Title: tokenless(folder, olidFolderRE), Evidence: "token"}
	}
	return searchStrictly(ctx, search, commonv1.MediaKindAuthor, folder, folder, "", metadata.KeyOpenLibraryAuthor, nil)
}

// IdentifyAudiobook identifies the audiobook a "<author>/<title>" folder
// under an audiobook root is: the ASIN its title folder names, read in
// region (the root's defaults.region); else Audible's search for the
// folder's title in region, read strictly, a hit counting only when its
// authors name the author folder and its year is the folder's when both
// know one. author is "" for a title folder directly under the root, which
// only a token identifies.
func IdentifyAudiobook(ctx context.Context, author, folder, region string, search SearchFunc) FolderIdentity {
	if asin := embeddedASIN(folder); asin != "" {
		title, year := parseAudiobookFolder(tokenless(folder, asinTokenREs...))
		return FolderIdentity{ID: asin, Title: title, Year: year, Evidence: "token"}
	}
	if strings.TrimSpace(author) == "" {
		return FolderIdentity{Unmatched: true, Code: CodeUnrecognisedLayout, Reason: fmt.Sprintf(
			"the audiobook folder %q names no ASIN and sits in no author folder, so the search has no author to confirm a hit by",
			folder)}
	}
	title, year := parseAudiobookFolder(folder)
	id := searchStrictly(ctx, search, commonv1.MediaKindAudiobook, title, title, region, metadata.KeyASIN,
		func(h metadata.SearchHit) bool {
			names := make([]string, 0, len(h.Authors)+1)
			for _, a := range h.Authors {
				names = append(names, a.Name)
			}
			// pkg/naming's {Author Name} joins a book's authors with ", ".
			names = append(names, strings.Join(names, ", "))
			return slicesAnyNormEq(names, author) && yearOK(hitYear(h), year)
		})
	if !id.Unmatched && id.Year == 0 {
		id.Year = year
	}
	return id
}

// agreedArtistID is the MUSICBRAINZ_ARTISTID the tags whose album artist
// (else artist) is the folder's name agree on: the first id of each such
// file's tag, at most maxTaggedFiles files read. ok is false when none
// carries one, or two disagree.
func agreedArtistID(folder string, tags []*commonv1.MediaTags) (id string, ok bool) {
	for i, t := range tags {
		if i >= maxTaggedFiles {
			break
		}
		if t == nil {
			continue
		}
		name := t.AlbumArtist
		if strings.TrimSpace(name) == "" {
			name = t.Artist
		}
		if !normEq(name, folder) {
			continue
		}
		got := firstID(t.MusicBrainzArtistID)
		if !mbidRE.MatchString(got) {
			continue
		}
		got = strings.ToLower(got)
		if id != "" && id != got {
			return "", false
		}
		id = got
	}
	return id, id != ""
}

// firstID is the first of a multi-valued id tag ("id1/id2", "id1; id2").
func firstID(tag string) string {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, "/;, "); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// searchStrictly asks search for items of kind named text and reads the
// hits strictly: those carrying an idKey id whose name equals name by
// [release.TitleNorm] and that keep (nil keeps every one); exactly one of
// them at the top score among them is the item; none is CodeNoMatch, two or
// more CodeAmbiguousTitle, each listing the hits as candidates.
func searchStrictly(
	ctx context.Context, search SearchFunc, kind commonv1.MediaKind, text, name, region, idKey string,
	keep func(metadata.SearchHit) bool,
) FolderIdentity {
	if search == nil {
		return FolderIdentity{Unmatched: true, Code: CodeLookupFailed, Reason: fmt.Sprintf(
			"no %s of this root folder is named %q, and no metadata search is configured to identify it", kind, name)}
	}
	hits, err := search(ctx, kind, text, region)
	if err != nil {
		return FolderIdentity{Unmatched: true, Code: CodeLookupFailed, Reason: fmt.Sprintf(
			"no %s of this root folder is named %q, and the metadata gateway's search for it failed, so the "+
				"folder is not identified this time: %v", kind, name, err)}
	}
	want := release.TitleNorm(name)
	var exact, listed []metadata.SearchHit
	for _, h := range hits {
		if h.IDs[idKey] == "" {
			continue
		}
		listed = append(listed, h)
		if want != "" && release.TitleNorm(h.Title) == want && (keep == nil || keep(h)) {
			exact = append(exact, h)
		}
	}
	if len(exact) == 0 {
		return FolderIdentity{
			Unmatched: true, Code: CodeNoMatch, Candidates: candidateLabels(listed, idKey),
			Reason: fmt.Sprintf("no %s of this root folder is named %q, and none of the %d the metadata gateway's "+
				"search answered is exactly that, so the folder is not identified: a near name is never taken; "+
				"add the %s by hand, or name its id in the folder", kind, name, len(listed), kind),
		}
	}
	sort.SliceStable(exact, func(i, j int) bool { return exact[i].Score > exact[j].Score })
	top := 0
	for _, h := range exact {
		if h.Score == exact[0].Score {
			top++
		}
	}
	if top == 1 {
		return FolderIdentity{ID: exact[0].IDs[idKey], Title: exact[0].Title, Year: hitYear(exact[0]), Evidence: "search"}
	}
	return FolderIdentity{
		Unmatched: true, Code: CodeAmbiguousTitle, Candidates: candidateLabels(exact, idKey),
		Reason: fmt.Sprintf("ambiguous: the metadata gateway's search names %d %ss called %q at its top score; "+
			"the folder is not identified until a person confirms one", top, kind, name),
	}
}

// candidateLabels renders hits as the Unmatched page's candidates,
// "Name (disambiguation) [id]", at most MaxCandidates of them. The
// parenthesis is the provider's disambiguation, else an audiobook's
// authors, else an author's birth year; it is left out when there is none.
func candidateLabels(hits []metadata.SearchHit, idKey string) []string {
	out := make([]string, 0, min(len(hits), MaxCandidates))
	for _, h := range hits {
		if len(out) == MaxCandidates {
			break
		}
		note := strings.TrimSpace(h.Disambiguation)
		if note == "" && len(h.Authors) > 0 {
			names := make([]string, 0, len(h.Authors))
			for _, a := range h.Authors {
				names = append(names, a.Name)
			}
			note = strings.Join(names, ", ")
		}
		if note == "" && idKey == metadata.KeyOpenLibraryAuthor && h.Year > 0 {
			note = fmt.Sprintf("born %d", h.Year)
		}
		label := strings.TrimSpace(h.Title)
		if note != "" {
			label += " (" + note + ")"
		}
		out = append(out, label+" ["+h.IDs[idKey]+"]")
	}
	return out
}

// hitYear is a hit's year: its release date's, read in UTC, else its Year.
func hitYear(h metadata.SearchHit) int {
	if h.ReleaseDate != nil && !h.ReleaseDate.IsZero() {
		return h.ReleaseDate.UTC().Year()
	}
	return int(h.Year)
}

// tokenless is folder with every match of res taken out, or folder itself
// when nothing else is left.
func tokenless(folder string, res ...*regexp.Regexp) string {
	s := folder
	for _, re := range res {
		s = re.ReplaceAllString(s, "")
	}
	if s = strings.TrimSpace(s); s == "" {
		return folder
	}
	return s
}

// normEq is TitleNorm equality with an empty side never matching: the
// identification's name rule, CleanTitle's on ASCII.
func normEq(a, b string) bool {
	na := release.TitleNorm(a)
	return na != "" && na == release.TitleNorm(b)
}

func slicesAnyNormEq(names []string, want string) bool {
	for _, n := range names {
		if normEq(n, want) {
			return true
		}
	}
	return false
}
