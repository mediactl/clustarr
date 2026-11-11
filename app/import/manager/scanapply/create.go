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

package scanapply

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// createName is the name a proposed item takes: names.Movie, names.Series,
// names.Artist, names.Author or names.Audiobook of its title and provider
// id, the name an import list and Add New give it too. The agent's proposed
// name is never trusted.
func createName(c *schema.ItemCreate) string {
	switch commonv1.MediaKind(c.Kind) {
	case commonv1.MediaKindMovie:
		return names.Movie(c.Title, c.TmdbID)
	case commonv1.MediaKindSeries:
		return names.Series(c.Title, c.TvdbID)
	case commonv1.MediaKindArtist:
		return names.Artist(c.Title, c.MusicBrainzID)
	case commonv1.MediaKindAuthor:
		return names.Author(c.Title, c.OpenLibraryID)
	case commonv1.MediaKindAudiobook:
		return names.Audiobook(c.Title, c.ASIN)
	}
	return c.Name
}

// ensureItem creates the Movie, Series, Artist, Author or Audiobook a
// scan's attribution proposes, when absent (P36, P39; the three containers
// since the 2026-11-10 non-video libraries design's D16, a root scan
// creating them from their folders). ok is false when it refused, listing
// the file as unmatched: an ImportExclusion names the provider id (P37: a
// TMDB, TheTVDB, MusicBrainz artist, Open Library author or ASIN id, keyed
// as librarydelete writes the item's exclusion); the root folder sets no
// default qualityProfileRef, which a new item requires (P38); an object of
// that name exists with another provider id, which is never overwritten
// (nameCollision), or with the same id under another root folder, whose
// files are never attributed from this one (otherRootFolder, smoke finding
// O4). created is true when this call created it; an item already there
// with the same id is used as it is, never re-applied.
func (a *Applier) ensureItem(
	ctx context.Context, obs schema.ScanObservation, root *catalogv1alpha1.RootFolder, c *schema.ItemCreate,
	rel string, t *tally,
) (ok, created bool, err error) {
	ns := obs.Scan.Namespace
	name := createName(c)
	var (
		provider, id string
		obj          client.Object
		idOf, rootOf func() string
	)
	switch commonv1.MediaKind(c.Kind) {
	case commonv1.MediaKindMovie:
		if c.TmdbID <= 0 {
			return false, false, fmt.Errorf("%w: a movie proposal without a TMDB id", errMalformed)
		}
		provider, id = catalogv1alpha1.ExclusionIDKeyTMDB, strconv.FormatInt(c.TmdbID, 10)
		m := &catalogv1alpha1.Movie{}
		obj, idOf = m, func() string { return strconv.FormatInt(m.Spec.TmdbID, 10) }
		rootOf = func() string { return m.Spec.RootFolderRef }
	case commonv1.MediaKindSeries:
		if c.TvdbID <= 0 {
			return false, false, fmt.Errorf("%w: a series proposal without a TheTVDB id", errMalformed)
		}
		provider, id = catalogv1alpha1.ExclusionIDKeyTVDB, strconv.FormatInt(c.TvdbID, 10)
		s := &catalogv1alpha1.Series{}
		obj, idOf = s, func() string { return strconv.FormatInt(s.Spec.TvdbID, 10) }
		rootOf = func() string { return s.Spec.RootFolderRef }
	case commonv1.MediaKindArtist:
		if c.MusicBrainzID == "" {
			return false, false, fmt.Errorf("%w: an artist proposal without a MusicBrainz id", errMalformed)
		}
		provider, id = catalogv1alpha1.ExclusionIDKeyMusicBrainz, c.MusicBrainzID
		ar := &catalogv1alpha1.Artist{}
		obj, idOf = ar, func() string { return ar.Spec.MusicBrainzID }
		rootOf = func() string { return ar.Spec.RootFolderRef }
	case commonv1.MediaKindAuthor:
		if !openLibraryAuthorID.MatchString(c.OpenLibraryID) {
			return false, false, fmt.Errorf("%w: an author proposal without an Open Library author id (%q)", errMalformed, c.OpenLibraryID)
		}
		provider, id = catalogv1alpha1.ExclusionIDKeyOpenLibrary, c.OpenLibraryID
		au := &catalogv1alpha1.Author{}
		obj, idOf = au, func() string { return au.Spec.OpenLibraryID }
		rootOf = func() string { return au.Spec.RootFolderRef }
	case commonv1.MediaKindAudiobook:
		if c.ASIN == "" {
			return false, false, fmt.Errorf("%w: an audiobook proposal without an ASIN", errMalformed)
		}
		provider, id = catalogv1alpha1.ExclusionIDKeyASIN, c.ASIN
		ab := &catalogv1alpha1.Audiobook{}
		obj, idOf = ab, func() string { return ab.Spec.ASIN }
		rootOf = func() string { return ab.Spec.RootFolderRef }
	default:
		return false, false, fmt.Errorf("%w: a %q proposal; scans create movies, series, artists, authors and audiobooks only",
			errMalformed, c.Kind)
	}
	folder, err := createFolder(c, rel)
	if err != nil {
		return false, false, err
	}

	// An existing object is read fresh: one created since the walk read its
	// cache is used, never overwritten.
	switch err := a.apiReader().Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); {
	case err == nil:
		if got := idOf(); got != id {
			t.unmatched(root, rel, CodeNameCollision, fmt.Sprintf(
				"the file names %s id %s, but %s %s exists with %s id %s; it is never overwritten",
				provider, id, c.Kind, name, provider, got), []string{name}, a.now())
			return false, false, nil
		}
		if itemRoot := rootOf(); itemRoot != root.Name {
			a.otherRootFolder(t, root, rel, commonv1.MediaRef{Kind: commonv1.MediaKind(c.Kind), Name: name}, itemRoot)
			return false, false, nil
		}
		return true, false, nil
	case !apierrors.IsNotFound(err):
		return false, false, fmt.Errorf("scanapply: get %s %s: %w", c.Kind, name, err)
	}

	if ex, excluded, err := a.excluded(ctx, provider, id); err != nil {
		return false, false, err
	} else if excluded {
		t.unmatched(root, rel, CodeExcluded, fmt.Sprintf(
			"excluded: the file names %s id %s, which import exclusion %s/%s keeps out",
			provider, id, ex.Namespace, ex.Name), nil, a.now())
		return false, false, nil
	}
	profile := root.Spec.Defaults.QualityProfileRef
	if profile == "" {
		t.unmatched(root, rel, CodeNoQualityProfile, fmt.Sprintf(
			"root folder %q sets no default qualityProfileRef, which a new %s requires", root.Name, c.Kind), nil, a.now())
		return false, false, nil
	}

	if err := a.pace(ctx, types.UID(ns+"/"+c.Kind+"/"+name)); err != nil {
		return false, false, err
	}
	switch commonv1.MediaKind(c.Kind) {
	case commonv1.MediaKindMovie:
		err = a.applyMovie(ctx, ns, name, c.TmdbID, profile, root.Name)
	case commonv1.MediaKindSeries:
		err = a.applySeries(ctx, ns, name, c.TvdbID, profile, root.Name, folder)
	case commonv1.MediaKindArtist:
		err = a.applyArtist(ctx, ns, name, c.MusicBrainzID, root, folder)
	case commonv1.MediaKindAuthor:
		err = a.applyAuthor(ctx, ns, name, c.OpenLibraryID, root, folder)
	case commonv1.MediaKindAudiobook:
		err = a.applyAudiobook(ctx, ns, name, c, root, folder)
	}
	if err != nil {
		return false, false, err
	}
	t.ItemsCreated++
	logging.FromContext(ctx).Info("scanapply: created an item a scanned file names", "kind", c.Kind, "item", name, provider, id)
	return true, true, nil
}

// openLibraryAuthorID is AuthorSpec.openLibraryID's pattern, checked here
// so a proposal that cannot key an Author is dead-lettered as malformed
// rather than refused by the apiserver at the apply.
var openLibraryAuthorID = regexp.MustCompile(`^OL[0-9]+A$`)

// seriesFolder is the folder a scanned series is pinned to: the first
// segment of the file's path under the root folder, the folder whose
// TheTVDB id named it.
func seriesFolder(rel string) string {
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return first
}

// createFolder is the folder, relative to the root, a proposed item is
// pinned to (spec.folder), so its status.path is the folder its files are
// already in: none for a movie (the scan pins no movie's folder); a
// series' first segment (seriesFolder); an artist's or author's the
// proposal's Folder, the folder the scan resolved, else the path's first
// segment; an audiobook's the proposal's Folder ("<author>/<title>"), else
// the path's first two segments when a file lies below them. A Folder the
// observed path does not lie in -- or one that is absolute or climbs out
// of the root -- is malformed: the agent resolved the folder from the path
// it reports.
func createFolder(c *schema.ItemCreate, rel string) (string, error) {
	rel = filepath.ToSlash(rel)
	switch commonv1.MediaKind(c.Kind) {
	case commonv1.MediaKindMovie:
		return "", nil
	case commonv1.MediaKindSeries:
		return seriesFolder(rel), nil
	}
	if c.Folder == "" {
		segs := strings.Split(rel, "/")
		switch {
		case commonv1.MediaKind(c.Kind) != commonv1.MediaKindAudiobook:
			return segs[0], nil
		case len(segs) >= 3:
			return segs[0] + "/" + segs[1], nil
		}
		return "", nil
	}
	folder := path.Clean(filepath.ToSlash(c.Folder))
	if path.IsAbs(folder) || folder == "." || folder == ".." || strings.HasPrefix(folder, "../") {
		return "", fmt.Errorf("%w: a %s proposal's folder %q is not a folder under the root", errMalformed, c.Kind, c.Folder)
	}
	if rel != folder && !strings.HasPrefix(rel, folder+"/") {
		return "", fmt.Errorf("%w: a %s proposal's folder %q does not hold the observed path %q", errMalformed, c.Kind, c.Folder, rel)
	}
	return folder, nil
}

// monitoredByDefault is the root's defaults.monitored, true when unset (the
// CRD's default, which a RootFolder created from Go may not carry).
func monitoredByDefault(d catalogv1alpha1.RootDefaults) bool {
	return d.Monitored == nil || *d.Monitored
}

// applyArtist creates the Artist a scan found by its folder (D16), under
// import-worker: pinned to the folder its files are in (spec.folder), with
// the root's defaults -- qualityProfileRef, monitored, monitorNewItems when
// set and tags -- every album monitored (addOptions.monitor all; a root
// whose missing albums are not wanted sets defaults.monitored false before
// its first scan, and the Artist gates its Albums) and no add-time search,
// so a scan never starts a search storm. metadataProfile is sent empty so
// the apiserver defaults its types and statuses (album and ep, studio,
// official): its nested defaults apply only to an object that is present,
// and an Artist without them lists no album at all.
func (a *Applier) applyArtist(ctx context.Context, ns, name, mbid string, root *catalogv1alpha1.RootFolder, folder string) error {
	d := root.Spec.Defaults
	spec := catalogac.ArtistSpec().
		WithMusicBrainzID(mbid).
		WithMonitored(monitoredByDefault(d)).
		WithMetadataProfile(catalogac.MusicMetadataProfile()).
		WithQualityProfileRef(d.QualityProfileRef).
		WithRootFolderRef(root.Name).
		WithAddOptions(catalogac.ArtistAddOptions().
			WithMonitor(catalogv1alpha1.ArtistMonitorAll).
			WithSearchForMissing(false))
	if d.MonitorNewItems != "" {
		spec = spec.WithMonitorNewItems(d.MonitorNewItems)
	}
	if folder != "" {
		spec = spec.WithFolder(folder)
	}
	if len(d.Tags) > 0 {
		spec = spec.WithTags(d.Tags...)
	}
	if _, err := k8s.Apply(ctx, a.Client, mediafilespec.FieldManager, catalogac.Artist(name, ns).WithSpec(spec)); err != nil {
		return fmt.Errorf("scanapply: create artist %s: %w", name, err)
	}
	return nil
}

// applyAuthor creates the Author a scan found by its folder (D16), as
// applyArtist does the Artist: the pinned folder, the root's defaults,
// every book monitored and no add-time search, and works listed in English
// alone (metadataProfile.allowedLanguages [en], as Add New's author). An
// Author's monitorNewItems is all or none: the root's "new", which only an
// artist has, leaves it to the CRD's default, all.
func (a *Applier) applyAuthor(ctx context.Context, ns, name, olid string, root *catalogv1alpha1.RootFolder, folder string) error {
	d := root.Spec.Defaults
	spec := catalogac.AuthorSpec().
		WithOpenLibraryID(olid).
		WithMonitored(monitoredByDefault(d)).
		WithMetadataProfile(catalogac.BookMetadataProfile().WithAllowedLanguages(scannedAuthorLanguage)).
		WithQualityProfileRef(d.QualityProfileRef).
		WithRootFolderRef(root.Name).
		WithAddOptions(catalogac.AuthorAddOptions().
			WithMonitor(catalogv1alpha1.AuthorMonitorAll).
			WithSearchForMissing(false))
	switch mode := catalogv1alpha1.MonitorNewChildrenMode(d.MonitorNewItems); mode {
	case catalogv1alpha1.MonitorNewChildrenAll, catalogv1alpha1.MonitorNewChildrenNone:
		spec = spec.WithMonitorNewItems(mode)
	}
	if folder != "" {
		spec = spec.WithFolder(folder)
	}
	if len(d.Tags) > 0 {
		spec = spec.WithTags(d.Tags...)
	}
	if _, err := k8s.Apply(ctx, a.Client, mediafilespec.FieldManager, catalogac.Author(name, ns).WithSpec(spec)); err != nil {
		return fmt.Errorf("scanapply: create author %s: %w", name, err)
	}
	return nil
}

// scannedAuthorLanguage is the one language a scanned author lists works in
// (spec.metadataProfile.allowedLanguages), Add New's own (ui/actions'
// addedAuthorLanguage).
const scannedAuthorLanguage = "en"

// applyAudiobook creates the Audiobook a scan found by its
// "<author>/<title>" folder (D16): pinned to that folder, in the Audible
// region the agent resolved its ASIN in (the root's defaults.region when
// the proposal names none; unset, the CRD's us), with the root's defaults
// and no add-time search, its files on disk already; linked to its Author
// (spec.authorRef) when exactly one Author of the namespace is named as the
// author folder is (audiobookAuthor), as Add New links one. No Author is
// created.
func (a *Applier) applyAudiobook(
	ctx context.Context, ns, name string, c *schema.ItemCreate, root *catalogv1alpha1.RootFolder, folder string,
) error {
	d := root.Spec.Defaults
	spec := catalogac.AudiobookSpec().
		WithASIN(c.ASIN).
		WithMonitored(monitoredByDefault(d)).
		WithQualityProfileRef(d.QualityProfileRef).
		WithRootFolderRef(root.Name).
		WithAddOptions(catalogac.AudiobookAddOptions().WithSearchForMissing(false))
	region := catalogv1alpha1.AudiobookRegion(c.Region)
	if region == "" {
		region = d.Region
	}
	if region != "" {
		spec = spec.WithRegion(region)
	}
	if folder != "" {
		spec = spec.WithFolder(folder)
	}
	if len(d.Tags) > 0 {
		spec = spec.WithTags(d.Tags...)
	}
	authorRef, err := a.audiobookAuthor(ctx, ns, folder)
	if err != nil {
		return err
	}
	if authorRef != "" {
		spec = spec.WithAuthorRef(authorRef)
	}
	if _, err := k8s.Apply(ctx, a.Client, mediafilespec.FieldManager, catalogac.Audiobook(name, ns).WithSpec(spec)); err != nil {
		return fmt.Errorf("scanapply: create audiobook %s: %w", name, err)
	}
	return nil
}

// audiobookAuthor is the Author a scanned audiobook links to: the one
// Author of ns whose metadata name is the author folder's (the first
// segment of "<author>/<title>"), compared through release.TitleNorm, as Add
// New compares (ui's authorsNamed). None -- no author folder, no Author of
// the name, two of it, an Author without metadata yet -- links none, never
// a guess: the audiobook's Edit dialog links one by hand.
func (a *Applier) audiobookAuthor(ctx context.Context, ns, folder string) (string, error) {
	author, _, ok := strings.Cut(folder, "/")
	if !ok {
		return "", nil
	}
	want := release.TitleNorm(author)
	if want == "" {
		return "", nil
	}
	var list catalogv1alpha1.AuthorList
	if err := a.reader().List(ctx, &list, client.InNamespace(ns)); err != nil {
		return "", fmt.Errorf("scanapply: list authors in %s: %w", ns, err)
	}
	var match []string
	for i := range list.Items {
		if md := list.Items[i].Status.Metadata; md != nil && md.Name != "" && release.TitleNorm(md.Name) == want {
			match = append(match, list.Items[i].Name)
		}
	}
	if len(match) != 1 {
		logging.FromContext(ctx).Info("scanapply: no one Author of the audiobook's author folder; no author linked",
			"authorFolder", author, "authors", match)
		return "", nil
	}
	return match[0], nil
}

// applyMovie creates the Movie a scanned file names, under import-worker,
// as the rescan rendered it: addMethod "scan", and no search for it -- its
// file is already on disk.
func (a *Applier) applyMovie(ctx context.Context, ns, name string, tmdbID int64, profile, root string) error {
	ac := catalogac.Movie(name, ns).WithSpec(
		catalogac.MovieSpec().
			WithTmdbID(tmdbID).
			WithQualityProfileRef(profile).
			WithRootFolderRef(root).
			WithAddOptions(catalogac.MovieAddOptions().
				WithSearchForMovie(false).
				WithAddMethod(catalogv1alpha1.MovieAddMethodScan)),
	)
	if _, err := k8s.Apply(ctx, a.Client, mediafilespec.FieldManager, ac); err != nil {
		return fmt.Errorf("scanapply: create movie %s: %w", name, err)
	}
	return nil
}

// applySeries creates the series a scan found by its folder's TheTVDB id,
// under import-worker, as the rescan rendered it: pinned to that folder
// (spec.folder), monitoring every season but the specials (monitor
// unmonitorSpecials: a monitored series monitors all of its seasons, owner
// 2026-10-27; it monitored nothing until then), and no add-time search.
func (a *Applier) applySeries(ctx context.Context, ns, name string, tvdbID int64, profile, root, folder string) error {
	spec := catalogac.SeriesSpec().
		WithTvdbID(tvdbID).
		WithQualityProfileRef(profile).
		WithRootFolderRef(root).
		WithAddOptions(catalogac.SeriesAddOptions().
			WithMonitor(catalogv1alpha1.SeriesMonitorUnmonitorSpecials).
			WithSearchForMissing(false).
			WithSearchForCutoffUnmet(false))
	if folder != "" {
		spec = spec.WithFolder(folder)
	}
	if _, err := k8s.Apply(ctx, a.Client, mediafilespec.FieldManager, catalogac.Series(name, ns).WithSpec(spec)); err != nil {
		return fmt.Errorf("scanapply: create series %s: %w", name, err)
	}
	return nil
}

// excluded looks provider/id up in the clustarr-import-exclusions bucket,
// the lookup an import list makes. With no bus nothing is excluded.
func (a *Applier) excluded(ctx context.Context, provider, id string) (events.ExclusionEntry, bool, error) {
	if a.Bus == nil || id == "" {
		return events.ExclusionEntry{}, false, nil
	}
	entry, err := a.Bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey(provider, id))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return events.ExclusionEntry{}, false, nil
	case err != nil:
		return events.ExclusionEntry{}, false, fmt.Errorf("scanapply: look up exclusion %s/%s: %w", provider, id, err)
	}
	decoded, err := events.DecodeExclusionEntry(entry.Value)
	return decoded, true, err
}

// validateManual validates a person's manual target again before anything
// is written (P45): it must hold files of the root folder's kind, exist,
// and be stored under this root folder. reason is why it no longer fits.
func (a *Applier) validateManual(
	ctx context.Context, ns string, root *catalogv1alpha1.RootFolder, ref commonv1.MediaRef,
) (reason string, err error) {
	if !importtarget.FileRefFitsRoot(ref, root.Spec.Kind) {
		return fmt.Sprintf("the manual target %s/%s cannot be assigned files under root folder %q, which is a %s root",
			ref.Kind, ref.Name, root.Name, root.Spec.Kind), nil
	}
	itemRoot, ok, err := itemRootFolder(ctx, a.reader(), ns, ref)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return fmt.Sprintf("the manual target %s/%s no longer exists", ref.Kind, ref.Name), nil
	case itemRoot != root.Name:
		return fmt.Sprintf("the manual target %s/%s is stored under root folder %q, not %q",
			ref.Kind, ref.Name, itemRoot, root.Name), nil
	}
	return "", nil
}

// itemRootFolder reads the item ref names and whatever parent names its
// root folder: ok is false when any of them does not exist.
func itemRootFolder(ctx context.Context, r client.Reader, ns string, ref commonv1.MediaRef) (root string, ok bool, err error) {
	get := func(name string, obj client.Object) (bool, error) {
		switch err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); {
		case apierrors.IsNotFound(err):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("scanapply: get %s %s/%s: %w", ref.Kind, ns, name, err)
		}
		return true, nil
	}
	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		ok, err := get(ref.Name, &m)
		return m.Spec.RootFolderRef, ok, err
	case commonv1.MediaKindSeries:
		var s catalogv1alpha1.Series
		ok, err := get(ref.Name, &s)
		return s.Spec.RootFolderRef, ok, err
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if ok, err := get(ref.Name, &ep); !ok || err != nil {
			return "", ok, err
		}
		var s catalogv1alpha1.Series
		ok, err := get(ep.Spec.SeriesRef, &s)
		return s.Spec.RootFolderRef, ok, err
	case commonv1.MediaKindAlbum:
		var al catalogv1alpha1.Album
		if ok, err := get(ref.Name, &al); !ok || err != nil {
			return "", ok, err
		}
		var ar catalogv1alpha1.Artist
		ok, err := get(al.Spec.ArtistRef, &ar)
		return ar.Spec.RootFolderRef, ok, err
	case commonv1.MediaKindBook:
		var b catalogv1alpha1.Book
		if ok, err := get(ref.Name, &b); !ok || err != nil {
			return "", ok, err
		}
		if r := ptr.Deref(b.Spec.RootFolderRef, ""); r != "" {
			return r, true, nil
		}
		if au := ptr.Deref(b.Spec.AuthorRef, ""); au != "" {
			var author catalogv1alpha1.Author
			ok, err := get(au, &author)
			return author.Spec.RootFolderRef, ok, err
		}
		return "", true, nil
	case commonv1.MediaKindAudiobook:
		var ab catalogv1alpha1.Audiobook
		ok, err := get(ref.Name, &ab)
		return ab.Spec.RootFolderRef, ok, err
	case commonv1.MediaKindIssue:
		var is catalogv1alpha1.Issue
		if ok, err := get(ref.Name, &is); !ok || err != nil {
			return "", ok, err
		}
		var co catalogv1alpha1.Comic
		ok, err := get(is.Spec.ComicRef, &co)
		return co.Spec.RootFolderRef, ok, err
	}
	return "", false, nil
}
