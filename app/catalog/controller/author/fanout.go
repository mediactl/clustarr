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

package author

import (
	"crypto/sha1" //nolint:gosec // content-addressing a k8s object name, not a security boundary
	"encoding/hex"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// DesiredBook is one Book object the Author reconciler should ensure exists,
// with the spec fields to set (only at creation -- see this package's doc.go
// for why nothing is ever written onto an already-existing Book).
type DesiredBook struct {
	Name   string
	WorkID string
	// Monitored is non-nil on the first fan-out (addOptionsApplied == false,
	// decided by InitialBookMonitored) and for a brand-new book appearing
	// after add (decided by spec.monitorNewItems == all/none). It is nil for
	// a book that already exists as a Book object on a later refresh --
	// BookSpec.Monitored belongs to the user after creation, mirroring
	// DesiredEpisode.Monitored's identical contract in the series package.
	Monitored *bool
}

// BookName returns the deterministic object name for a Book fanned out from
// author authorName's Open Library work workID: "<author>-<uid8>", mirroring
// Album's "<artist>-<releasegroup-uid8>" convention (design spec §4.2).
//
// The design spec states that convention for Album (and Issue's own,
// unrelated "<comic>-<calculatedNumber...>") explicitly but leaves Book's
// name convention unstated -- grepped for "uid8", "<author>-" and Book's own
// §4.2 entry, not found -- because Book, unlike Album, can also be
// standalone (BookSpec.AuthorRef nil per book_types.go:159-161), so a
// parent-derived name only ever applies to a FANNED-OUT Book; a standalone
// Book's name is whatever the user who created it chose. This mirrors
// Album's pattern for the fanned-out case, the closest precedent this
// package has.
//
// uid8 exists because WorkID ("OL45883W") is not itself a valid Kubernetes
// object-name component (uppercase, no DNS-1123 guarantee): an 8-hex-
// character sha1 prefix of workID is lowercase and DNS-1123-safe by
// construction, the same shape a "<parent>-<uid8>" name needs regardless of
// which provider id feeds it.
func BookName(authorName, workID string) string {
	return authorName + "-" + uid8(workID)
}

func uid8(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // same use as above: a short, deterministic, non-secret suffix
	return hex.EncodeToString(sum[:])[:8]
}

// InitialBookMonitored decides a book's monitored flag at add time, per the
// author's AuthorMonitorMode. released mirrors Series' own "aired" test
// (EpisodeCandidate.AirDate vs now): a work with no known FirstPublished
// date reads as not yet released, the same conservative default
// InitialEpisodeMonitored uses for a nil AirDate.
//
// AuthorMonitorMode has no FirstSeason/LastSeason/Pilot/Recent/Specials
// counterpart (author_types.go's five-value enum: All/Future/Missing/
// Existing/None), so unlike InitialEpisodeMonitored this needs no sibling
// list to rank ep against -- every mode here is decided from b and now
// alone.
func InitialBookMonitored(mode catalogv1alpha1.AuthorMonitorMode, b metadata.Book, now time.Time) bool {
	released := b.FirstPublished != nil && !b.FirstPublished.After(now)
	switch mode {
	case catalogv1alpha1.AuthorMonitorAll:
		return true
	case catalogv1alpha1.AuthorMonitorFuture:
		return !released
	case catalogv1alpha1.AuthorMonitorMissing:
		return released
	case catalogv1alpha1.AuthorMonitorExisting:
		return false
	case catalogv1alpha1.AuthorMonitorNone:
		return false
	default:
		return false
	}
}

// MatchesProfile decides whether an Open Library work becomes a Book, per
// AuthorSpec.MetadataProfile. See this package's doc.go for which fields
// openlibrary.Client.Books() fills (subjects and first-publication date, but
// no editions); this function is implemented against pkg/metadata.Book's
// full field set (proven by this package's tests against synthetic,
// fully-populated values), so the edition dimensions start working the day
// that call carries editions, without a second change here.
//
// Absent data never excludes a work -- only data that IS present and fails
// the check does. This matches artist.AlbumAccepted's own ReleaseStatuses
// handling (app/catalog/controller/artist/fanout.go, `if len(alb.Releases) >
// 0 { ... }`), the sibling controller's identical fix for the identical
// class of gap (MusicBrainz never populating Album.Releases): an earlier
// version of this function excluded on absence, which meant any Author with
// a MetadataProfile set got zero Books, silently, with nothing to explain
// it. "The provider gave no value" and "the provider gave a value that
// fails the filter" are deliberately kept distinct below; only the second
// excludes.
//
// SkipMissingDate is the one dimension whose check IS an absence check, and
// it is Readarr's own rule verbatim: MetadataProfileService.FilterBooks
// drops a work when `!p.SkipMissingDate || x.ReleaseDate.HasValue` is false
// (Readarr develop 0b79d300, src/NzbDrone.Core/Profiles/Metadata/
// MetadataProfileService.cs). It can act here because the works listing now
// asks the provider for the date: openlibrary.Client.Books maps each work
// record's first_publish_date (hardcover.Client.Books its release_date) into
// FirstPublished, so a nil FirstPublished is the provider's own answer --
// "no publication date for this work" -- not "never fetched", which is the
// distinction the absent-never-excludes rule above exists to protect.
// Readarr exempts a book already in the library (FilterByPredicate's
// localItems: manually added, or with files); that exemption holds here by
// construction, because the fan-out only ever creates Books and never
// deletes or edits one, so a filter only decides whether a work becomes a
// Book in the first place.
//
// SkipMissingISBN stays a documented no-op. Readarr applies it per edition
// (FilterEditions: `!p.SkipMissingIsbn || x.Isbn13 ... || x.Asin ...`) and
// then drops a work whose editions were all filtered out, but the works
// listing fetches no editions (one more request per work;
// openlibrary.Client.Book fetches them for a single work), so its absence
// here is "never fetched", and acting on it would drop every work. It stays
// declared, not deleted, so a reader sees this was decided, not missed.
//
// MinPopularity is the one dimension with nowhere to read from at all: no
// field on pkg/metadata.Book, pkg/metadata.Author or pkg/metadata.Rating
// carries a "popularity" score (Rating carries Votes per named source, but
// no source is designated as a popularity proxy anywhere in this pipeline).
// It is a documented no-op -- never disqualifies a work -- rather than an
// invented source, matching this project's "never guess" rule; it is
// already consistent with the absent-never-excludes rule above by
// construction, since it never even reads p.MinPopularity.
func MatchesProfile(p catalogv1alpha1.BookMetadataProfile, b metadata.Book) bool {
	if p.SkipMissingDate && b.FirstPublished == nil {
		return false
	}
	// SkipMissingISBN: see the doc comment above -- the works listing
	// carries no editions, so no code follows for it; this comment is that
	// decision's record.

	if p.SkipPartsAndSets && len(b.Subjects) > 0 && isPartOfASet(b) {
		return false
	}
	if p.SkipSeriesSecondary && len(b.Series) > 0 && isSeriesSecondaryOnly(b.Series) {
		return false
	}
	if len(p.AllowedLanguages) > 0 && len(b.Editions) > 0 && !anyEditionInLanguages(b.Editions, p.AllowedLanguages) {
		return false
	}
	if p.MinPages > 0 && len(b.Editions) > 0 && !anyEditionHasMinPages(b.Editions, p.MinPages) {
		return false
	}
	return true
}

// isPartOfASet has no dedicated field on pkg/metadata.Book (Open Library
// exposes this, when it knows it at all, as a free-text subject/type on the
// work -- there is no structured "this is a box set or a volume of a larger
// set" boolean anywhere this pipeline reads). Subjects is the closest field
// that exists, so this checks it for Open Library's own conventional
// subject strings rather than leaving the flag entirely inert. Called only
// when len(b.Subjects) > 0 (MatchesProfile).
func isPartOfASet(b metadata.Book) bool {
	for _, s := range b.Subjects {
		switch s {
		case "Boxed sets", "Omnibus", "Sets":
			return true
		}
	}
	return false
}

// isSeriesSecondaryOnly is true when EVERY one of links is non-Primary -- a
// work that is only ever a secondary entry in the reading orders it belongs
// to. Called only when len(links) > 0 (MatchesProfile); an empty list is
// absent data, handled by the caller, not by returning false here for the
// same reason.
func isSeriesSecondaryOnly(links []metadata.SeriesLink) bool {
	for _, l := range links {
		if l.Primary {
			return false
		}
	}
	return true
}

func anyEditionInLanguages(editions []metadata.Edition, allowed []string) bool {
	set := make(map[string]bool, len(allowed))
	for _, l := range allowed {
		set[l] = true
	}
	for _, ed := range editions {
		if set[ed.Language] {
			return true
		}
	}
	return false
}

func anyEditionHasMinPages(editions []metadata.Edition, minPages int32) bool {
	for _, ed := range editions {
		if ed.PageCount >= minPages {
			return true
		}
	}
	return false
}

// DesiredBooks projects the metadata gateway's works list into the Book
// objects the reconciler should ensure exist for a, applying
// a.Spec.MetadataProfile (MatchesProfile) and the add-time/steady-state
// monitoring decision. existingNames is the set of Book object names already
// owned by a (from the reconciler's own List call), threaded in explicitly
// for the same reason DesiredEpisodes takes it -- see DesiredBook.Monitored's
// doc comment.
//
// Works with a duplicate or empty Open Library work id are dropped, first
// occurrence wins for a duplicate: the provider is assumed not to send
// either, but this defends anyway, mirroring DesiredEpisodes' identical
// dedup guard.
func DesiredBooks(
	a *catalogv1alpha1.Author,
	addOptionsApplied bool,
	existingNames map[string]bool,
	books []metadata.Book,
	now time.Time,
) []DesiredBook {
	seen := make(map[string]bool, len(books))
	out := make([]DesiredBook, 0, len(books))
	for _, b := range books {
		workID := b.IDs[metadata.KeyOpenLibraryWork]
		if workID == "" || seen[workID] {
			continue
		}
		seen[workID] = true

		if !MatchesProfile(a.Spec.MetadataProfile, b) {
			continue
		}

		name := BookName(a.Name, workID)

		var monitored *bool
		switch {
		case !addOptionsApplied:
			v := InitialBookMonitored(a.Spec.AddOptions.Monitor, b, now)
			monitored = &v
		case !existingNames[name] && a.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewChildrenAll:
			v := true
			monitored = &v
		case !existingNames[name] && a.Spec.MonitorNewItems == catalogv1alpha1.MonitorNewChildrenNone:
			v := false
			monitored = &v
		default:
			monitored = nil
		}

		out = append(out, DesiredBook{Name: name, WorkID: workID, Monitored: monitored})
	}
	return out
}
