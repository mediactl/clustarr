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

package importlist

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
)

// FieldManager is the server-side-apply field manager every catalog-item
// write in this package uses: k8s.ManagerImportarrWorker, not
// k8s.ManagerImportarr. That constant's own doc comment names "an importarr
// scan, list or file-import worker" as its three co-owners; this package is
// the "list" one.
//
// A list writes only the items it added itself. syncKind finds every item
// already in the namespace by the provider id its kind is keyed on
// (libraryByID) and leaves one it did not add -- added by hand, by a
// library rescan or by another list -- exactly as it is, the way Radarr
// skips a list movie already in the library. Before that rule a list
// applied its defaults, under force, over any item of the same name, so it
// took a hand-added item's root folder and profile, and two lists took one
// Movie from each other on alternate syncs; and an item of the same id
// under another title (item names hash the title as well as the id) got a
// second, duplicate object. Every apply still sends this package's
// complete, self-consistent field set (see applyMovie/applySeries), never a
// partial one, since a narrower send from the same manager releases the
// difference.
const FieldManager = k8s.ManagerImportarrWorker

// resolveMonitored applies the "item override, else list default" rule
// pkg/importlist/types.go documents on Item.Monitored: "Nil means: use the
// ImportList's ListDefaults; the controller fills the gap." This package is
// that gap-filler.
func resolveMonitored(item *bool, def *bool) bool {
	if item != nil {
		return *item
	}
	if def != nil {
		return *def
	}
	return true
}

// resolveBool is resolveMonitored without an item-level override, for the
// several ListDefaults fields (searchOnAdd, seasonFolder) that have no
// per-item counterpart.
func resolveBool(def *bool, fallback bool) bool {
	if def != nil {
		return *def
	}
	return fallback
}

// mapMonitorNewItems narrows catalogv1alpha1.MonitorNewItemsMode's three
// values (all, none, new) onto SeriesSpec's two-valued
// MonitorNewChildrenMode (all, none): "new" -- monitor only items a refresh
// finds that are themselves new, as opposed to "all", every item regardless
// of when it was found -- has no equivalent in the two-valued enum, so it
// maps to All. That is the closer of the two available meanings: a
// followed list is itself an "add everything new" mechanism, and
// unmonitoring newly-discovered seasons of a show the user is actively
// following via a list would be the more surprising choice of the two.
func mapMonitorNewItems(mode catalogv1alpha1.MonitorNewItemsMode) catalogv1alpha1.MonitorNewChildrenMode {
	if mode == catalogv1alpha1.MonitorNewItemsNone {
		return catalogv1alpha1.MonitorNewChildrenNone
	}
	return catalogv1alpha1.MonitorNewChildrenAll
}

// movieName is the deterministic name a Movie for tmdbID gets
// (names.Movie), matching app/import/worker/rescan's own naming and the
// UI's Add New exactly, so a list sync, a library scan and an add that
// identify the same film always agree on one object.
func movieName(title string, tmdbID int64) string { return names.Movie(title, tmdbID) }

// seriesName is movieName's series counterpart (names.Series).
func seriesName(title string, tvdbID int64) string { return names.Series(title, tvdbID) }

// catalogName is the name kind's item for id gets: movieName or seriesName.
func catalogName(kind commonv1.MediaKind, title string, id int64) string {
	if kind == commonv1.MediaKindSeries {
		return seriesName(title, id)
	}
	return movieName(title, id)
}

// libraryItem is a catalog item already in the namespace.
type libraryItem struct {
	name string
	// listRef is spec.source.importListRef: the list that added the item,
	// or "" for one added by hand or by a library rescan.
	listRef string
}

// libraryByID returns kind's items in namespace by the provider id the
// kind is keyed on (tmdbID for a movie, tvdbID for a series), so a sync
// finds an item whatever title it was named under. Where two items carry
// one id, the one listName added wins, so the list keeps acting on its own.
func libraryByID(
	ctx context.Context, c client.Client, namespace, listName string, kind commonv1.MediaKind,
) (map[int64]libraryItem, error) {
	out := map[int64]libraryItem{}
	put := func(id int64, name string, src *commonv1.AddSource) {
		it := libraryItem{name: name}
		if src != nil {
			it.listRef = src.ImportListRef
		}
		if prev, ok := out[id]; ok && prev.listRef == listName {
			return
		}
		out[id] = it
	}
	switch kind {
	case commonv1.MediaKindMovie:
		var l catalogv1alpha1.MovieList
		if err := c.List(ctx, &l, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("importlist: list movies: %w", err)
		}
		for i := range l.Items {
			put(l.Items[i].Spec.TmdbID, l.Items[i].Name, l.Items[i].Spec.Source)
		}
	case commonv1.MediaKindSeries:
		var l catalogv1alpha1.SeriesList
		if err := c.List(ctx, &l, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("importlist: list series: %w", err)
		}
		for i := range l.Items {
			put(l.Items[i].Spec.TvdbID, l.Items[i].Name, l.Items[i].Spec.Source)
		}
	}
	return out, nil
}

// applyMovie creates or updates the Movie named name, which item
// identifies, under [FieldManager]. It always sends the complete set of fields this package
// ever sets -- see FieldManager's doc comment for why a partial send would
// be unsafe here.
func applyMovie(
	ctx context.Context, c client.Client, namespace, listName, name string,
	item importlist.Item, defaults catalogv1alpha1.ListDefaults, tmdbID int64,
) (string, error) {
	spec := catalogac.MovieSpec().
		WithTmdbID(tmdbID).
		WithMonitored(resolveMonitored(item.Monitored, defaults.Monitored)).
		WithQualityProfileRef(defaults.QualityProfileRef).
		WithRootFolderRef(defaults.RootFolderRef).
		WithAddOptions(catalogac.MovieAddOptions().
			WithSearchForMovie(resolveBool(defaults.SearchOnAdd, true)).
			WithAddMethod(catalogv1alpha1.MovieAddMethodList)).
		WithSource(commonv1.AddSource{ImportListRef: listName})
	if defaults.MinimumAvailability != "" {
		spec = spec.WithMinimumAvailability(defaults.MinimumAvailability)
	}
	if defaults.DelayProfileRef != nil {
		spec = spec.WithDelayProfileRef(*defaults.DelayProfileRef)
	}
	if len(defaults.Tags) > 0 {
		spec = spec.WithTags(defaults.Tags...)
	}
	ac := catalogac.Movie(name, namespace).WithSpec(spec)
	if _, err := k8s.Apply(ctx, c, FieldManager, ac); err != nil {
		return "", fmt.Errorf("importlist: apply movie %s: %w", name, err)
	}
	return name, nil
}

// applySeries is applyMovie's series counterpart, and a compare-and-swap
// (spec 2026-10-06 §5.5, OD13): it reads the Series through r, the API
// reader, because the classifier's ownership is in managedFields, which the
// cache strips; it applies with that read's resourceVersion when the Series
// exists, and redoes the read and the apply on a Conflict.
//
// A Series the catalogarr classifier moved to its RootFolder's anime
// defaults keeps its current profile and type: this apply forces
// ownership, so re-sending the list's defaults would undo the
// classification on every sync (anime dual-audio spec §4). It is
// classified once status.classification says anime, or once
// catalogarr-classify owns spec.qualityProfileRef -- the classifier's spec
// patch lands before its status write, and a sync reading between the two
// would otherwise see no classification and force its defaults back.
func applySeries(
	ctx context.Context, c client.Client, r client.Reader, namespace, listName, name string,
	item importlist.Item, defaults catalogv1alpha1.ListDefaults, tvdbID int64,
) (string, error) {
	if r == nil {
		r = c
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		spec := seriesSpecFor(listName, item, defaults, tvdbID)
		ac := catalogac.Series(name, namespace)
		var existing catalogv1alpha1.Series
		switch err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &existing); {
		case err == nil:
			if classified(&existing) {
				spec = spec.WithQualityProfileRef(existing.Spec.QualityProfileRef).WithSeriesType(existing.Spec.SeriesType)
			}
			ac = ac.WithResourceVersion(existing.ResourceVersion)
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("importlist: get series %s: %w", name, err)
		}
		if _, err := k8s.Apply(ctx, c, FieldManager, ac.WithSpec(spec)); err != nil {
			return fmt.Errorf("importlist: apply series %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return name, nil
}

// seriesSpecFor is the list's complete declaration of a Series' spec, before
// a classified Series' profile and type are kept.
func seriesSpecFor(
	listName string, item importlist.Item, defaults catalogv1alpha1.ListDefaults, tvdbID int64,
) *catalogac.SeriesSpecApplyConfiguration {
	spec := catalogac.SeriesSpec().
		WithTvdbID(tvdbID).
		WithMonitored(resolveMonitored(item.Monitored, defaults.Monitored)).
		WithMonitorNewItems(mapMonitorNewItems(defaults.MonitorNewItems)).
		WithSeasonFolder(resolveBool(defaults.SeasonFolder, true)).
		WithQualityProfileRef(defaults.QualityProfileRef).
		WithRootFolderRef(defaults.RootFolderRef).
		WithAddOptions(catalogac.SeriesAddOptions().
			WithSearchForMissing(resolveBool(defaults.SearchOnAdd, true))).
		WithSource(commonv1.AddSource{ImportListRef: listName})
	if defaults.SeriesType != "" {
		spec = spec.WithSeriesType(defaults.SeriesType)
	}
	if defaults.DelayProfileRef != nil {
		spec = spec.WithDelayProfileRef(*defaults.DelayProfileRef)
	}
	if len(defaults.Tags) > 0 {
		spec = spec.WithTags(defaults.Tags...)
	}
	return spec
}

// classified reports whether the catalogarr classifier has claimed s:
// status.classification says anime, or catalogarr-classify owns
// spec.qualityProfileRef in s's managedFields (read through the API reader;
// the cache strips them).
func classified(s *catalogv1alpha1.Series) bool {
	if cl := s.Status.Classification; cl != nil && cl.Anime {
		return true
	}
	for _, mf := range s.ManagedFields {
		if mf.Manager != string(k8s.ManagerCatalogarrClassify) || mf.FieldsV1 == nil {
			continue
		}
		var fields map[string]map[string]any
		if json.Unmarshal(mf.FieldsV1.Raw, &fields) == nil {
			if _, ok := fields["f:spec"]["f:qualityProfileRef"]; ok {
				return true
			}
		}
	}
	return false
}

// unmonitorMovie re-applies the Movie si records with monitored forced to
// false, under [FieldManager].
//
// This is deliberately NOT a single-field "just spec.monitored" apply.
// unmonitorMovie shares FieldManager with applyMovie, and server-side apply
// replaces a manager's WHOLE ownership set on every apply rather than
// merging it -- so a narrower send here would RELEASE tmdbID,
// qualityProfileRef and rootFolderRef the moment this manager stops
// re-asserting them, and the apiserver rejects the resulting object outright
// (all three are +required on MovieSpec). This is CLAUDE.md's "early return
// built a partial status" hazard, restated for spec: the fix is the same
// one -- send the complete declaration every time, never a delta -- and
// routing through applyMovie itself, with the item's Monitored override
// forced false, is what guarantees this call and a normal sync's call can
// never drift apart in which fields they send.
func unmonitorMovie(
	ctx context.Context, c client.Client, namespace, listName string, si StoredItem, defaults catalogv1alpha1.ListDefaults,
) error {
	item := si.Item
	item.Monitored = boolPtr(false)
	if _, err := applyMovie(ctx, c, namespace, listName, si.ObjectName, item, defaults, si.ResolvedID); err != nil {
		return fmt.Errorf("importlist: unmonitor movie %s: %w", si.ObjectName, err)
	}
	return nil
}

// unmonitorSeries is unmonitorMovie's series counterpart. r is the API
// reader applySeries reads the Series through.
func unmonitorSeries(
	ctx context.Context, c client.Client, r client.Reader, namespace, listName string, si StoredItem, defaults catalogv1alpha1.ListDefaults,
) error {
	item := si.Item
	item.Monitored = boolPtr(false)
	if _, err := applySeries(ctx, c, r, namespace, listName, si.ObjectName, item, defaults, si.ResolvedID); err != nil {
		return fmt.Errorf("importlist: unmonitor series %s: %w", si.ObjectName, err)
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// deleteMovie removes the Movie a list sync previously added: all of
// SyncLevelRemoveAndKeep, and the last step of SyncLevelRemoveAndDelete
// once removeWithFiles (delete.go) has recycled its files.
func deleteMovie(ctx context.Context, c client.Client, namespace, name string) error {
	m := &catalogv1alpha1.Movie{}
	m.Namespace, m.Name = namespace, name
	if err := c.Delete(ctx, m); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// deleteSeries is deleteMovie's series counterpart.
func deleteSeries(ctx context.Context, c client.Client, namespace, name string) error {
	s := &catalogv1alpha1.Series{}
	s.Namespace, s.Name = namespace, name
	if err := c.Delete(ctx, s); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}
