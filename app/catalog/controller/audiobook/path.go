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

package audiobook

import (
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// Path resolves the folder an Audiobook is stored under, which is the
// folder the import places its parts in (catalogctx.ItemFolder, the rule
// catalogctx.AudiobookFilePath joins a part's file onto when the
// Audiobook's status.path is not yet written, and holds status.path to once
// it is; 2026-11-10 non-video design §5.5): folderOverride (spec.folder)
// as written under the root when it pins one (a folder on disk, never
// sanitized), otherwise the audiobook
// preset (pkg/naming/preset.go's audiobookFolderTemplate, the one template
// BuildFolder renders for every dialect, "{Author Name}/{Book Series}/...")
// rendered from c -- every component sanitized as the import sanitizes it
// and held under the root, so an author or a title with a "?" or a trailing
// dot names the folders on disk. A known title the rule cannot render is an
// error the reconciler reports as catalogctx.ReasonFolderUnrenderable on
// Ready.
func Path(root *catalogv1alpha1.RootFolder, folderOverride *string, c naming.Context) (string, error) {
	return catalogctx.ItemFolder(root, commonv1.MediaKindAudiobook, folderOverride, c)
}

// namingContext builds the naming.Context the audiobook folder template
// (pkg/naming/preset.go's audiobookFolderTemplate: "{Author Name}/{Book
// Series}/{Book SeriesPosition - }{Release Year - }{Book Title}{ Narrator}",
// where an empty series drops its whole segment and an empty position or
// year drops its own " - " separator) renders against, from spec and the
// gateway-cached metadata. meta is expected non-nil -- callers only reach
// this once status.metadata != nil, the same gate movie's reconciler
// applies before computing Path.
//
// AuthorName and Narrator each collapse a list onto pkg/naming's single-
// string token: Context has one AuthorName/Narrator field per render call
// (it describes exactly one item, per its own doc comment), while
// AudiobookMetadata.Authors/Narrators are lists because Audnexus reports
// every credited author and narrator. Joining with ", " is the same
// judgment call FileState's doc comment makes elsewhere in this package --
// not spec-mandated, documented here so a future task can change it in one
// place. AudiobookMetadata.Series is already a single SeriesLink (not a
// list): app/catalog/metadata/patch.go's buildAudiobookMetadataAC collapses
// the provider's series list onto it before this ever runs, so there is no
// further selection to make here.
func namingContext(spec catalogv1alpha1.AudiobookSpec, meta *catalogv1alpha1.AudiobookMetadata) naming.Context {
	ctx := naming.Context{Kind: commonv1.MediaKindAudiobook}
	if meta == nil {
		return ctx
	}

	ctx.BookTitle = meta.Title
	ctx.AuthorName = joinNames(meta.Authors)
	ctx.Narrator = strings.Join(meta.Narrators, ", ")
	if meta.ReleaseDate != nil {
		// .UTC() before .Year(), deliberately: metav1.Time.UnmarshalJSON
		// (k8s.io/apimachinery) calls pt.Local() on every value that has
		// round-tripped through the apiserver, so status.metadata.releaseDate
		// -- always UTC on the wire (MarshalJSON forces UTC) -- comes back
		// out of a live client.Get in the RECONCILING REPLICA's local zone.
		// A midnight UTC release date west of UTC then reads one calendar
		// day (and, for a January date, one calendar year) earlier than what
		// the provider sent. Movie sidesteps this entirely by capturing
		// Year as a plain int32 once, at ingestion
		// (pkg/metadata/clients/tmdb's own t.Year() call, on the freshly
		// parsed, not-yet-round-tripped time.Time); AudiobookMetadata has no
		// such field (spec §4.2 gives it only ReleaseDate), so this is the
		// one place in this package a date is turned into a year, and it is
		// the one place that round-trip matters.
		ctx.Year = meta.ReleaseDate.UTC().Year()
	}
	if meta.Series != nil {
		ctx.BookSeries = meta.Series.Series
		ctx.BookSeriesPosition = meta.Series.Position
	}
	return ctx
}

// joinNames renders a NamedRef list as pkg/naming's single AuthorName token
// expects: display names only, comma-joined -- the ASIN each NamedRef also
// carries has no token of its own to fill.
func joinNames(refs []catalogv1alpha1.NamedRef) string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}
