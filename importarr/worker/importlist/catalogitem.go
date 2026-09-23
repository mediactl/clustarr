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
	"fmt"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// FieldManager is the server-side-apply field manager every catalog-item
// write in this package uses: k8s.ManagerImportarrWorker, not
// k8s.ManagerImportarr. That constant's own doc comment names "an importarr
// scan, list or file-import worker" as its three co-owners and explicitly
// calls out that "the library-rescan worker also applies MovieSpec under
// this name when it creates the Movie a scanned file is attributed to" --
// this package is the "list" co-owner that same comment anticipates.
//
// Sharing one manager name with importarr/worker/rescan is deliberate, not
// an oversight: a movie already in the library (created by a scan) that
// also appears on a followed list, and a movie a list adds that a later
// scan finds a file for, are the SAME Movie object under
// [k8s.ChildName]'s deterministic naming (hash of "movie"+tmdbID, title-
// independent) -- so the second writer is meant to update the first
// writer's object, not collide with it. What CLAUDE.md's field-manager
// gotchas warn against is a LATER apply that sends FEWER fields than an
// earlier one silently releasing the difference; this package avoids that
// by always sending its own complete, self-consistent field set on every
// apply (see applyMovie/applySeries below), never a partial one, so it can
// only ever gain ownership of a field rescan did not send, never release
// one rescan is still asserting.
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

// movieName is the deterministic name a Movie for tmdbID gets, matching
// importarr/worker/rescan's own naming exactly (k8s.ChildName hashes
// "movie"+tmdbID, not title) so a list sync and a library scan that
// identify the same film always agree on one object.
func movieName(title string, tmdbID int64) string {
	return k8s.ChildName(title, "movie", strconv.FormatInt(tmdbID, 10))
}

// seriesName is movieName's series counterpart, hashing "series"+tvdbID.
func seriesName(title string, tvdbID int64) string {
	return k8s.ChildName(title, "series", strconv.FormatInt(tvdbID, 10))
}

// catalogName is the name kind's item for id gets: movieName or seriesName.
func catalogName(kind commonv1.MediaKind, title string, id int64) string {
	if kind == commonv1.MediaKindSeries {
		return seriesName(title, id)
	}
	return movieName(title, id)
}

// applyMovie creates or updates the Movie item identifies, under
// [FieldManager]. It always sends the complete set of fields this package
// ever sets -- see FieldManager's doc comment for why a partial send would
// be unsafe here.
func applyMovie(
	ctx context.Context, c client.Client, namespace, listName string,
	item importlist.Item, defaults catalogv1alpha1.ListDefaults, tmdbID int64,
) (string, error) {
	name := movieName(item.Title, tmdbID)
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

// applySeries is applyMovie's series counterpart.
func applySeries(
	ctx context.Context, c client.Client, namespace, listName string,
	item importlist.Item, defaults catalogv1alpha1.ListDefaults, tvdbID int64,
) (string, error) {
	name := seriesName(item.Title, tvdbID)
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
	ac := catalogac.Series(name, namespace).WithSpec(spec)
	if _, err := k8s.Apply(ctx, c, FieldManager, ac); err != nil {
		return "", fmt.Errorf("importlist: apply series %s: %w", name, err)
	}
	return name, nil
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
	if _, err := applyMovie(ctx, c, namespace, listName, item, defaults, si.ResolvedID); err != nil {
		return fmt.Errorf("importlist: unmonitor movie %s: %w", si.ObjectName, err)
	}
	return nil
}

// unmonitorSeries is unmonitorMovie's series counterpart.
func unmonitorSeries(
	ctx context.Context, c client.Client, namespace, listName string, si StoredItem, defaults catalogv1alpha1.ListDefaults,
) error {
	item := si.Item
	item.Monitored = boolPtr(false)
	if _, err := applySeries(ctx, c, namespace, listName, item, defaults, si.ResolvedID); err != nil {
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
