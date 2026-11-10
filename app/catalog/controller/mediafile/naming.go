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

package mediafile

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// maxExpectedPathLen is NamingStatus.ExpectedPath's CRD MaxLength. A longer
// render is Unrenderable rather than sent: the apiserver would reject the
// whole status apply, and with it every other field this manager owns.
const maxExpectedPathLen = 4096

// The NamingCurrent condition's reasons when a path was rendered. When none
// was, the condition's reason is the NamingReason itself.
const (
	namingConditionCurrent = "Current"
	namingConditionStale   = "Stale"
)

// namingOwner is what renderNaming needs of the catalog item a MediaFile
// backs: the identity half of the naming context, the RootFolder the item
// lives under, the QualityProfile its configured language comes from, and
// the renderer for its kind.
type namingOwner struct {
	context           naming.Context
	rootFolderRef     string
	qualityProfileRef string
	render            func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error)
}

// NamingLookup is what LoadNaming read for a file: the item it backs (or why
// none can name it), its RootFolder, and a failed read to keep the
// proposal over.
type NamingLookup struct {
	owner       *namingOwner
	reason      catalogv1alpha1.NamingReason
	root        *catalogv1alpha1.RootFolder
	rootMissing bool
	err         error
}

// LoadNaming reads what RenderNaming needs through c: the probe-free half of
// renderNaming. A failed read (anything but NotFound) is recorded, never
// returned: RenderNaming keeps the proposal over it and asks for a retry.
func LoadNaming(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) NamingLookup {
	owner, reason, err := namingOwnerOf(ctx, c, mf)
	look := NamingLookup{owner: owner, reason: reason, err: err}
	if err != nil || owner == nil || reason != "" {
		return look
	}
	var root catalogv1alpha1.RootFolder
	switch err := c.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: owner.rootFolderRef}, &root); {
	case apierrors.IsNotFound(err):
		look.rootMissing = true
	case err != nil:
		look.err = err
	default:
		look.root = &root
	}
	if look.err == nil {
		look.err = setConfiguredLanguage(ctx, c, owner)
	}
	return look
}

// setConfiguredLanguage sets owner's naming context's Language from its
// item's QualityProfile (catalogctx.ConfiguredLanguage). A profile that is
// not named or not found names no language, so English; one that does not
// parse still says its languages, which FromCRD copies verbatim.
func setConfiguredLanguage(ctx context.Context, c client.Reader, owner *namingOwner) error {
	var p *quality.Profile
	if owner.qualityProfileRef != "" {
		var qp catalogv1alpha1.QualityProfile
		switch err := c.Get(ctx, types.NamespacedName{Name: owner.qualityProfileRef}, &qp); {
		case apierrors.IsNotFound(err):
		case err != nil:
			return err
		default:
			parsed, _ := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
			p = &parsed
		}
	}
	owner.context.Language = catalogctx.ConfiguredLanguage(p)
	return nil
}

// RenderNaming proposes mf's canonical path from look, the draft's mi and
// specPath (spec.path as of this pass's main apply): renderNaming's decision,
// unchanged, with no read. prev is the proposal on the object, kept over a
// failed lookup.
//
// The result is nil for a kind this phase does not name (anything but a
// movie or an episode). Otherwise it carries a Reason and no ExpectedPath
// when the path cannot be rendered yet -- MetadataPending, TranscodePending
// (a transcode running or not yet incorporated: spec D6, a rename never
// touches a file another job holds), ProbePending (never probed, or the
// bytes changed and the re-probe failed: probeStale), Unrenderable -- or the
// ExpectedPath and whether specPath already is it. Recycling is never
// returned: nothing here knows of a file in the recycle bin. Quality, the
// probe-corrected quality the rename re-applies into spec.quality, is set
// whenever a probe describes the file, even when it agreed with the name, so
// the rename has one value to apply.
//
// A failed lookup keeps prev, with Current re-judged against specPath, and
// reports retry: a cache blip must not replace a good proposal, returning nil
// would drop status.naming, and without a requeue nothing would render it
// again until the next unrelated event.
func RenderNaming(mf *catalogv1alpha1.MediaFile, look NamingLookup, mi *commonv1.MediaInfo, prev *catalogv1alpha1.NamingStatus,
	specPath string, probeStale, transcodePending bool,
) (*catalogv1alpha1.NamingStatus, bool) {
	switch {
	case look.err != nil:
		return keepNaming(prev, specPath), true
	case look.owner == nil && look.reason == "":
		return nil, false
	}
	if probeStale {
		mi = nil
	}
	out := &catalogv1alpha1.NamingStatus{}
	spec := mf.Spec
	spec.Path = specPath
	if mi != nil {
		corrected, _ := quality.AugmentFromMediaInfo(spec.Quality, mi)
		spec.Quality = corrected
		out.Quality = &corrected
	}
	switch {
	case look.reason != "":
		out.Reason = look.reason
		return out, false
	case transcodePending:
		out.Reason = catalogv1alpha1.NamingReasonTranscodePending
		return out, false
	case mi == nil:
		out.Reason = catalogv1alpha1.NamingReasonProbePending
		return out, false
	case look.rootMissing:
		out.Reason = catalogv1alpha1.NamingReasonUnrenderable
		return out, false
	}
	// catalogctx.File's context carries only the catalogue's regexp deadline
	// and logging, so the pure render passes context.Background(). The
	// extension is the probed container's for a video file and the file's
	// own for every other kind (catalogctx.FileExt).
	expected, err := look.owner.render(look.root, catalogctx.File(context.Background(), look.owner.context, &spec, mi), catalogctx.FileExt(spec.MediaRef.Kind, mi, spec.Path))
	if err == nil && len(expected) > maxExpectedPathLen {
		err = fmt.Errorf("the rendered path is %d bytes, over the %d status.naming.expectedPath holds", len(expected), maxExpectedPathLen)
	}
	if err != nil {
		out.Reason = catalogv1alpha1.NamingReasonUnrenderable
		return out, false
	}
	out.ExpectedPath = expected
	out.Current = filepath.Clean(spec.Path) == expected
	return out, false
}

// keepNaming is prev, the proposal already on the object, re-judged
// against specPath: kept over a failed lookup, it must still say whether
// the file is at its proposed path now that a swap may have moved it.
func keepNaming(prev *catalogv1alpha1.NamingStatus, specPath string) *catalogv1alpha1.NamingStatus {
	if prev == nil {
		return nil
	}
	out := *prev
	if out.ExpectedPath != "" {
		out.Current = filepath.Clean(specPath) == out.ExpectedPath
	}
	return &out
}

// namingOwnerOf loads, through c, the item mf backs: a Movie; every Episode
// the file covers (spec.mediaRef.name plus keys, a multi-episode file) and
// their Series; an Album's track (spec.mediaRef.track in the Album's
// status.tracks) and its Artist; a Book and its Author, when it has one; an
// Audiobook; or an Issue and its Comic (2026-11-10 design D7). A missing
// item or container, or one whose metadata has not arrived, is
// MetadataPending; an episode reference naming no episode, episodes of more
// than one series, an album file naming no track or a track its Album does
// not list, a standalone Book with no rootFolderRef, or a file covering more
// than one issue is Unrenderable. It returns nil, "", nil for any other
// kind. Each kind's RootFolder is its container's -- an Album's its
// Artist's, a Book's its own override else its Author's, an Issue's its
// Comic's, an Audiobook's its own -- as the rename actuator resolves it.
func namingOwnerOf(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (*namingOwner, catalogv1alpha1.NamingReason, error) {
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: mf.Namespace, Name: name}
	}
	ref := mf.Spec.MediaRef
	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := c.Get(ctx, key(ref.Name), &m); err != nil {
			return ownerNotLoaded(err)
		}
		c, ok := catalogctx.Movie(&m)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:           c,
			rootFolderRef:     m.Spec.RootFolderRef,
			qualityProfileRef: m.Spec.QualityProfileRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.MovieFilePath(root, &m, c, ext)
			},
		}, "", nil

	case commonv1.MediaKindEpisode:
		names := coveredEpisodeNames(ref)
		if len(names) == 0 {
			logging.FromContext(ctx).Warn("mediafile: the file's episode reference names no episode; it cannot be named")
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		}
		eps := make([]catalogv1alpha1.Episode, 0, len(names))
		for _, n := range names {
			var ep catalogv1alpha1.Episode
			if err := c.Get(ctx, key(n), &ep); err != nil {
				return ownerNotLoaded(err)
			}
			eps = append(eps, ep)
		}
		// fileimport renders from its matched episodes in this order.
		slices.SortFunc(eps, func(a, b catalogv1alpha1.Episode) int {
			if a.Spec.SeasonNumber != b.Spec.SeasonNumber {
				return int(a.Spec.SeasonNumber - b.Spec.SeasonNumber)
			}
			return int(a.Spec.EpisodeNumber - b.Spec.EpisodeNumber)
		})
		seriesRef := eps[0].Spec.SeriesRef
		for _, ep := range eps[1:] {
			if ep.Spec.SeriesRef != seriesRef {
				logging.FromContext(ctx).Warn("mediafile: the file covers episodes of more than one series; it cannot be named",
					"series", seriesRef, "otherSeries", ep.Spec.SeriesRef)
				return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
			}
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, key(seriesRef), &s); err != nil {
			return ownerNotLoaded(err)
		}
		c, ok := catalogctx.Episode(&s, eps)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:           c,
			rootFolderRef:     s.Spec.RootFolderRef,
			qualityProfileRef: s.Spec.QualityProfileRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.EpisodeFilePath(root, &s, c, ext)
			},
		}, "", nil

	case commonv1.MediaKindAlbum:
		var album catalogv1alpha1.Album
		if err := c.Get(ctx, key(ref.Name), &album); err != nil {
			return ownerNotLoaded(err)
		}
		var artist catalogv1alpha1.Artist
		if err := c.Get(ctx, key(album.Spec.ArtistRef), &artist); err != nil {
			return ownerNotLoaded(err)
		}
		if ref.Track == "" {
			logging.FromContext(ctx).Warn("mediafile: the file's album reference names no track; it cannot be named", "album", album.Name)
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		}
		// The tracks arrive with the Album's metadata (syncTracks); a file
		// whose recording is on none of them is not on the release the Album
		// holds, and no preset names it.
		if len(album.Status.Tracks) == 0 {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		i := slices.IndexFunc(album.Status.Tracks, func(t catalogv1alpha1.Track) bool { return t.RecordingID == ref.Track })
		if i < 0 {
			logging.FromContext(ctx).Warn("mediafile: the file's track is not on its album's release; it cannot be named",
				"album", album.Name, "recording", ref.Track)
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		}
		c, ok := catalogctx.Album(&artist, &album, &album.Status.Tracks[i])
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:           c,
			rootFolderRef:     artist.Spec.RootFolderRef,
			qualityProfileRef: ptr.Deref(album.Spec.QualityProfileRef, artist.Spec.QualityProfileRef),
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.TrackFilePath(root, &artist, &album, c, ext)
			},
		}, "", nil

	case commonv1.MediaKindBook:
		var book catalogv1alpha1.Book
		if err := c.Get(ctx, key(ref.Name), &book); err != nil {
			return ownerNotLoaded(err)
		}
		var author *catalogv1alpha1.Author
		rootRef, profileRef := ptr.Deref(book.Spec.RootFolderRef, ""), ptr.Deref(book.Spec.QualityProfileRef, "")
		if authorRef := ptr.Deref(book.Spec.AuthorRef, ""); authorRef != "" {
			author = &catalogv1alpha1.Author{}
			if err := c.Get(ctx, key(authorRef), author); err != nil {
				return ownerNotLoaded(err)
			}
			if rootRef == "" {
				rootRef = author.Spec.RootFolderRef
			}
			if profileRef == "" {
				profileRef = author.Spec.QualityProfileRef
			}
		}
		if rootRef == "" {
			logging.FromContext(ctx).Warn("mediafile: a standalone book names no rootFolderRef; its file cannot be named", "book", book.Name)
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		}
		c, ok := catalogctx.Book(author, &book)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:           c,
			rootFolderRef:     rootRef,
			qualityProfileRef: profileRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.BookFilePath(root, author, c, ext)
			},
		}, "", nil

	case commonv1.MediaKindAudiobook:
		var ab catalogv1alpha1.Audiobook
		if err := c.Get(ctx, key(ref.Name), &ab); err != nil {
			return ownerNotLoaded(err)
		}
		number, count, err := audiobookPartNumber(ctx, c, mf)
		if err != nil {
			return ownerNotLoaded(err)
		}
		c, ok := catalogctx.Audiobook(&ab, number)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		c.PartCount = count
		return &namingOwner{
			context:           c,
			rootFolderRef:     ab.Spec.RootFolderRef,
			qualityProfileRef: ab.Spec.QualityProfileRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.AudiobookFilePath(root, &ab, c, ext)
			},
		}, "", nil

	case commonv1.MediaKindIssue:
		names := coveredNames(ref)
		switch {
		case len(names) == 0:
			logging.FromContext(ctx).Warn("mediafile: the file's issue reference names no issue; it cannot be named")
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		case len(names) > 1:
			// The issue preset names one issue; a file holding several keeps
			// the name the import gave it.
			logging.FromContext(ctx).Warn("mediafile: the file covers more than one issue; it cannot be named", "issues", names)
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		}
		var issue catalogv1alpha1.Issue
		if err := c.Get(ctx, key(names[0]), &issue); err != nil {
			return ownerNotLoaded(err)
		}
		var comic catalogv1alpha1.Comic
		if err := c.Get(ctx, key(issue.Spec.ComicRef), &comic); err != nil {
			return ownerNotLoaded(err)
		}
		c, ok := catalogctx.Issue(&comic, &issue)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:           c,
			rootFolderRef:     comic.Spec.RootFolderRef,
			qualityProfileRef: comic.Spec.QualityProfileRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.IssueFilePath(root, &comic, c, ext)
			},
		}, "", nil
	}
	return nil, "", nil
}

// audiobookPartNumber is mf's place among its Audiobook's parts in play
// order, and how many parts there are: catalogctx.PartNumbers over the
// Audiobook's MediaFiles, listed through the item index (mfindex.Item) --
// the one rule the import's placeParts and the Audiobook controller's
// OrderParts order by -- 1..n, and 0 for a lone part, which is what
// {PartNumber} renders (2026-11-10 design D7, D11). The file's own
// spec.partIndex is the ordering key the import froze (the tags' disc and
// track as disc*1000+track, else the name's part number, else 0), never the
// number: disc 1 track 3 is the third part, not part 1003. The number is
// built here, where every kind's naming context is, so no caller of
// LoadNaming can hand the file a number of its own, and the naming planner
// has no audiobook case. mf's own spec stands for it in the list, and a
// file the cache has yet to list (its own create) counts itself; the count
// is the listing's, as the import's is its own files', so number and count
// come from one read, never from the status.partCount the Audiobook's next
// pass writes. A listing that fails is the lookup's error, which
// RenderNaming keeps the previous proposal over (ownerNotLoaded), never a
// number from a partial list. A sibling appearing, going or changing its
// key renumbers the part, which the naming planner's S12/MediaFile source
// wakes it for (app/remediation/naming).
func audiobookPartNumber(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (number int32, count int, err error) {
	files, err := mfindex.FilesOf(ctx, c, mf.Namespace, commonv1.MediaKindAudiobook, mf.Spec.MediaRef.Name, client.UnsafeDisableDeepCopy)
	if err != nil {
		return 0, 0, err
	}
	parts := make([]catalogctx.Part, 0, len(files)+1)
	self := -1
	for i := range files {
		if files[i].Name == mf.Name {
			self = len(parts)
		}
		parts = append(parts, catalogctx.Part{Index: files[i].Spec.PartIndex, Path: files[i].Spec.Path})
	}
	if self < 0 {
		self = len(parts)
		parts = append(parts, catalogctx.Part{})
	}
	parts[self] = catalogctx.Part{Index: mf.Spec.PartIndex, Path: mf.Spec.Path}
	return catalogctx.PartNumbers(parts)[self], len(parts), nil
}

// ownerNotLoaded maps a failed Get of the item a file backs: an item that
// does not exist (yet) has no metadata to name the file from; anything else
// is an error the caller keeps its previous proposal over.
func ownerNotLoaded(err error) (*namingOwner, catalogv1alpha1.NamingReason, error) {
	if apierrors.IsNotFound(err) {
		return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
	}
	return nil, "", err
}

// coveredEpisodeNames is every Episode an episode MediaRef covers, first
// the one it names, then keys (a multi-episode file lists every episode it
// holds there, its own name included -- fileimport.EpisodeFileRef),
// without duplicates or empty names. coveredNames is the same reading of an
// issue MediaRef, whose keys list the issues of a pack.
func coveredEpisodeNames(ref commonv1.MediaRef) []string { return coveredNames(ref) }

func coveredNames(ref commonv1.MediaRef) []string {
	out := make([]string, 0, 1+len(ref.Keys))
	for _, n := range append([]string{ref.Name}, ref.Keys...) {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// MarkNamingCurrent mirrors n onto the NamingCurrent condition, for kubectl
// and the UI: True/Current when spec.path is the proposal, False/Stale when
// it is not, and Unknown with the NamingReason as its reason when nothing
// was rendered to compare spec.path with. A nil n (a kind this phase does
// not name) sets nothing. applyStatus calls it, and nothing else sets this
// condition, so every apply carries exactly one NamingCurrent entry that
// agrees with the status.naming beside it.
func MarkNamingCurrent(obj *catalogv1alpha1.MediaFile, conditions *[]metav1.Condition, n *catalogv1alpha1.NamingStatus) {
	const cond = catalogv1alpha1.ConditionNamingCurrent
	switch {
	case n == nil:
	case n.Reason == catalogv1alpha1.NamingReasonMetadataPending:
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "the item's metadata has not arrived; the file cannot be named yet")
	case n.Reason == catalogv1alpha1.NamingReasonProbePending:
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "no probe describes the file's current bytes; it cannot be named yet")
	case n.Reason == catalogv1alpha1.NamingReasonTranscodePending:
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "a transcode of the file is running or has not been incorporated; the file is held")
	case n.Reason != "":
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "the file's canonical path could not be rendered; see the controller log")
	case n.Current:
		k8s.MarkTrue(obj, conditions, cond, namingConditionCurrent, "the file is at its canonical path")
	default:
		k8s.MarkFalse(obj, conditions, cond, namingConditionStale, "the file's canonical path is %s", n.ExpectedPath)
	}
}
