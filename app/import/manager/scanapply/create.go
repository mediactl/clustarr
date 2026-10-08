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
	"path/filepath"
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
)

// createName is the name a proposed item takes: names.Movie or
// names.Series of its title and provider id, the name an import list and
// Add New give it too. The agent's proposed name is never trusted.
func createName(c *schema.ItemCreate) string {
	switch commonv1.MediaKind(c.Kind) {
	case commonv1.MediaKindMovie:
		return names.Movie(c.Title, c.TmdbID)
	case commonv1.MediaKindSeries:
		return names.Series(c.Title, c.TvdbID)
	}
	return c.Name
}

// ensureItem creates the Movie or Series a scan's attribution proposes,
// when absent (P36, P39). ok is false when it refused, listing the file as
// unmatched: an ImportExclusion names the provider id (P37); the root
// folder sets no default qualityProfileRef, which a new item requires
// (P38); an object of that name exists with another provider id, which is
// never overwritten (nameCollision), or with the same id under another root
// folder, whose files are never attributed from this one (otherRootFolder,
// smoke finding O4). created is true when this call created it; an item
// already there with the same id is used as it is, never re-applied.
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
	default:
		return false, false, fmt.Errorf("%w: a %q proposal; scans create movies and series only", errMalformed, c.Kind)
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
		err = a.applySeries(ctx, ns, name, c.TvdbID, profile, root.Name, seriesFolder(rel))
	}
	if err != nil {
		return false, false, err
	}
	t.ItemsCreated++
	logging.FromContext(ctx).Info("scanapply: created an item a scanned file names", "kind", c.Kind, "item", name, provider, id)
	return true, true, nil
}

// seriesFolder is the folder a scanned series is pinned to: the first
// segment of the file's path under the root folder, the folder whose
// TheTVDB id named it.
func seriesFolder(rel string) string {
	first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return first
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
// (spec.folder), monitoring nothing (monitor none: the owner turns on the
// seasons they want searched), and no add-time search.
func (a *Applier) applySeries(ctx context.Context, ns, name string, tvdbID int64, profile, root, folder string) error {
	spec := catalogac.SeriesSpec().
		WithTvdbID(tvdbID).
		WithQualityProfileRef(profile).
		WithRootFolderRef(root).
		WithAddOptions(catalogac.SeriesAddOptions().
			WithMonitor(catalogv1alpha1.SeriesMonitorNone).
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
