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
	"errors"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/version"
)

// FieldManager is the server-side-apply field manager every catalog-item
// write a list's diff makes uses: k8s.ManagerImportWorker, not
// k8s.ManagerImport, which owns ImportList status and the token Secret. The
// names are kept (R7) though the writes moved from the import agent's list
// worker into this controller (ADR-0019 §7.6, W26-W28).
//
// A list writes only the items it added itself. diffKind finds every item
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
const FieldManager = k8s.ManagerImportWorker

// resolveMonitored applies the "item override, else list default" rule
// pkg/importlist/types.go documents on Item.Monitored: "Nil means: use the
// ImportList's ListDefaults; the controller fills the gap." This controller
// is that gap-filler.
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
// (names.Movie), matching the library scan's own naming and the UI's Add
// New exactly, so a list sync, a library scan and an add that identify the
// same film always agree on one object.
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
	uid  types.UID
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
	put := func(id int64, name string, uid types.UID, src *commonv1.AddSource) {
		it := libraryItem{name: name, uid: uid}
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
			put(l.Items[i].Spec.TmdbID, l.Items[i].Name, l.Items[i].UID, l.Items[i].Spec.Source)
		}
	case commonv1.MediaKindSeries:
		var l catalogv1alpha1.SeriesList
		if err := c.List(ctx, &l, client.InNamespace(namespace)); err != nil {
			return nil, fmt.Errorf("importlist: list series: %w", err)
		}
		for i := range l.Items {
			put(l.Items[i].Spec.TvdbID, l.Items[i].Name, l.Items[i].UID, l.Items[i].Spec.Source)
		}
	}
	return out, nil
}

// applyMovie creates or updates the Movie named name, which item
// identifies, under [FieldManager]. It always sends the complete set of fields this package
// ever sets -- see FieldManager's doc comment for why a partial send would
// be unsafe here.
//
// Like applySeries it is a compare-and-swap read through r, the API reader,
// and a Movie the catalog classifier moved to its RootFolder's anime
// profile keeps that profile (classified), for the same reason: this apply
// forces ownership, so re-sending the list's default would undo the
// classification on every sync.
func applyMovie(
	ctx context.Context, c client.Client, r client.Reader, namespace, listName, name string,
	item importlist.Item, defaults catalogv1alpha1.ListDefaults, tmdbID int64,
) (string, error) {
	if r == nil {
		r = c
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		spec := movieSpecFor(listName, item, defaults, tmdbID)
		ac := catalogac.Movie(name, namespace)
		var existing catalogv1alpha1.Movie
		switch err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &existing); {
		case err == nil:
			if movieClassified(&existing) {
				spec = spec.WithQualityProfileRef(existing.Spec.QualityProfileRef)
			}
			ac = ac.WithResourceVersion(existing.ResourceVersion)
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("importlist: get movie %s: %w", name, err)
		}
		if _, err := k8s.Apply(ctx, c, FieldManager, ac.WithSpec(spec)); err != nil {
			return fmt.Errorf("importlist: apply movie %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return name, nil
}

// movieSpecFor is the list's complete declaration of a Movie's spec, before
// a classified Movie's profile is kept.
func movieSpecFor(
	listName string, item importlist.Item, defaults catalogv1alpha1.ListDefaults, tmdbID int64,
) *catalogac.MovieSpecApplyConfiguration {
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
	return spec
}

// applySeries is applyMovie's series counterpart, and a compare-and-swap
// (spec 2026-10-06 §5.5, OD13): it reads the Series through r, the API
// reader, because the classifier's ownership is in managedFields, which the
// cache strips; it applies with that read's resourceVersion when the Series
// exists, and redoes the read and the apply on a Conflict.
//
// A Series the catalog classifier moved to its RootFolder's anime
// defaults keeps its current profile and type: this apply forces
// ownership, so re-sending the list's defaults would undo the
// classification on every sync (anime dual-audio spec §4). It is
// classified once status.classification says anime, or once
// catalog-classify owns spec.qualityProfileRef -- the classifier's spec
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
			// Every season but the specials: a monitored series monitors
			// all of its seasons (owner, 2026-10-27).
			WithMonitor(catalogv1alpha1.SeriesMonitorUnmonitorSpecials).
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

// classified reports whether the catalog classifier has claimed s:
// status.classification says anime, or catalog-classify owns
// spec.qualityProfileRef in s's managedFields (read through the API reader;
// the cache strips them).
func classified(s *catalogv1alpha1.Series) bool {
	if cl := s.Status.Classification; cl != nil && cl.Anime {
		return true
	}
	return classifierOwnsProfile(s.ManagedFields)
}

// movieClassified is classified's Movie counterpart.
func movieClassified(m *catalogv1alpha1.Movie) bool {
	if cl := m.Status.Classification; cl != nil && cl.Anime {
		return true
	}
	return classifierOwnsProfile(m.ManagedFields)
}

// classifierOwnsProfile reports whether catalog-classify owns
// spec.qualityProfileRef in an item's managedFields: the classifier's spec
// patch lands before its status write, and a sync reading between the two
// would otherwise see no classification and force its defaults back.
func classifierOwnsProfile(managed []metav1.ManagedFieldsEntry) bool {
	for _, mf := range managed {
		if mf.Manager != string(k8s.ManagerCatalogClassify) || mf.FieldsV1 == nil {
			continue
		}
		var fields map[string]map[string]any
		if json.Unmarshal(mf.FieldsV1.GetRawBytes(), &fields) == nil {
			if _, ok := fields["f:spec"]["f:qualityProfileRef"]; ok {
				return true
			}
		}
	}
	return false
}

// unmonitor re-applies the item si records with monitored forced to false,
// under [FieldManager].
//
// This is deliberately NOT a single-field "just spec.monitored" apply. It
// shares FieldManager with applyMovie and applySeries, and server-side apply
// replaces a manager's WHOLE ownership set on every apply rather than
// merging it -- so a narrower send here would RELEASE tmdbID,
// qualityProfileRef and rootFolderRef the moment this manager stops
// re-asserting them, and the apiserver rejects the resulting object
// outright (all three are +required). Routing through applyMovie and
// applySeries, with the item's Monitored override forced false, is what
// guarantees this call and a normal diff's call can never drift apart in
// which fields they send.
func (r *Reconciler) unmonitor(ctx context.Context, il *catalogv1alpha1.ImportList, si importliststate.StoredItem) error {
	item := si.Item
	item.Monitored = new(false)
	var err error
	if si.ObjectKind == string(commonv1.MediaKindSeries) {
		_, err = applySeries(ctx, r.Client, r.apiReader(), il.Namespace, il.Name, si.ObjectName, item, il.Spec.Defaults, si.ResolvedID)
	} else {
		_, err = applyMovie(ctx, r.Client, r.apiReader(), il.Namespace, il.Name, si.ObjectName, item, il.Spec.Defaults, si.ResolvedID)
	}
	if err != nil {
		return fmt.Errorf("importlist: unmonitor %s %s: %w", si.ObjectKind, si.ObjectName, err)
	}
	return nil
}

// ErrOutsideRootFolder is a MediaFile whose path does not lie strictly under
// the RootFolder its item names. removeAndDelete refuses to recycle it: a
// path it cannot place inside the library is not one it may take out of it.
var ErrOutsideRootFolder = errors.New("importlist: file is not under the item's root folder")

// ErrNoRecycleBin is a RootFolder with an empty spec.recycleBin.path (the
// CRD defaults it, so this is a hand-cleared value). removeAndDelete never
// falls back to unlinking.
var ErrNoRecycleBin = errors.New("importlist: root folder has no recycle bin path")

// liveItem reads the Movie or Series si names from the cache: its UID, its
// root folder and the list that added it. ok is false when it is gone.
func (r *Reconciler) liveItem(ctx context.Context, namespace string, si importliststate.StoredItem) (item client.Object, rootFolder string, source *commonv1.AddSource, ok bool, err error) {
	key := types.NamespacedName{Namespace: namespace, Name: si.ObjectName}
	if si.ObjectKind == string(commonv1.MediaKindSeries) {
		var s catalogv1alpha1.Series
		if err := r.Client.Get(ctx, key, &s); err != nil {
			return nil, "", nil, false, client.IgnoreNotFound(err)
		}
		return &s, s.Spec.RootFolderRef, s.Spec.Source, true, nil
	}
	var m catalogv1alpha1.Movie
	if err := r.Client.Get(ctx, key, &m); err != nil {
		return nil, "", nil, false, client.IgnoreNotFound(err)
	}
	return &m, m.Spec.RootFolderRef, m.Spec.Source, true, nil
}

// addedBy reports whether src names listName as the list that added an
// item.
func addedBy(src *commonv1.AddSource, listName string) bool {
	return src != nil && src.ImportListRef == listName
}

// deleteWithPrecondition deletes obj only while its UID is the one read
// (W28): a same-named object recreated meanwhile is another item, not this
// list's to remove. Gone already is done.
func (r *Reconciler) deleteWithPrecondition(ctx context.Context, obj client.Object) error {
	uid := obj.GetUID()
	if err := r.Client.Delete(ctx, obj, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete %T %s: %w", obj, obj.GetName(), err)
	}
	return nil
}

// remove deletes the item si records, keeping its files and MediaFiles
// (syncLevel removeAndKeep). An item gone already is done.
//
// "Remove the catalog item, keep files" deletes only the Movie or Series,
// and leaves every file and every MediaFile record where it is. Radarr also
// drops the MovieFile rows, but Radarr's disk scan only ever scans movies it
// already has; Clustarr's library scan instead adopts every unrecorded file
// under a root folder and creates the item it belongs to. The MediaFile
// record is what marks a file as accounted for, so deleting it here would
// be exactly what lets the next scan undo the removal. Kept, it also
// re-attaches by itself if a list adds the item back: the item's name is
// deterministic. An ImportExclusion is not written either: Radarr's list
// clean-up calls DeleteMovie(id, false), so a movie that comes back onto a
// list is added again.
func (r *Reconciler) remove(ctx context.Context, item client.Object) error {
	return r.deleteWithPrecondition(ctx, item)
}

// removeWithFiles is syncLevel removeAndDelete (W29, P81): the item is
// removed and so are its files. It is Radarr's and Sonarr's "remove and
// delete", whose file half goes through the recycle bin, never a bare
// unlink -- and so does this: the controller publishes one
// schema.RecycleFilesTask naming every file it recorded for the item (each
// MediaFile attributed to it, spec.mediaRef, and each sidecar on its
// status) under the item's RootFolder, which the import agent moves into
// spec.recycleBin.path; then it deletes each MediaFile and the item, each
// with a UID precondition. Anything else in the folder -- a file nobody
// imported -- stays.
//
// The checks the agent repeats are made here first, before anything is
// published or deleted: a root folder with no recycle bin, or a recorded
// path outside the root folder, refuses the whole removal, so the item is
// kept and retried, as it was when the list worker recycled in-process.
// A failure anywhere returns an error, the caller keeps the item remembered
// and the next snapshot's diff repeats the whole call: the task is
// published again (the agent finds recycled files gone) and the deletes
// find the objects gone.
func (r *Reconciler) removeWithFiles(
	ctx context.Context, il *catalogv1alpha1.ImportList, item client.Object, rootFolderRef string, syncedAt time.Time,
) error {
	belongs, err := r.belongsTo(ctx, item)
	if err != nil {
		return err
	}
	var files catalogv1alpha1.MediaFileList
	if err := r.Client.List(ctx, &files, client.InNamespace(item.GetNamespace())); err != nil {
		return fmt.Errorf("list media files: %w", err)
	}
	var owned []*catalogv1alpha1.MediaFile
	for i := range files.Items {
		if belongs(files.Items[i].Spec.MediaRef) {
			owned = append(owned, &files.Items[i])
		}
	}
	if len(owned) > 0 {
		var rf catalogv1alpha1.RootFolder
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: item.GetNamespace(), Name: rootFolderRef}, &rf); err != nil {
			return fmt.Errorf("root folder %s: %w", rootFolderRef, err)
		}
		if rf.Spec.RecycleBin.Path == "" {
			return fmt.Errorf("%w: %s", ErrNoRecycleBin, rf.Name)
		}
		var paths []string
		for _, mf := range owned {
			candidates := []string{mf.Spec.Path}
			for _, sc := range mf.Status.Sidecars {
				if p, ok := sc.PathBeside(mf.Spec.Path); ok {
					candidates = append(candidates, p)
				}
			}
			for _, p := range candidates {
				if !fsops.StrictlyUnder(p, rf.Spec.Path) {
					return fmt.Errorf("%w: media file %s path %q, root folder %s is %q",
						ErrOutsideRootFolder, mf.Name, p, rf.Name, rf.Spec.Path)
				}
				if !slices.Contains(paths, p) {
					paths = append(paths, p)
				}
			}
		}
		task := schema.RecycleFilesTask{
			RootFolder: schema.Ref{Namespace: rf.Namespace, Name: rf.Name, UID: string(rf.UID)},
			Paths:      paths,
		}
		if err := r.publishRecycle(ctx, il, item, task, syncedAt); err != nil {
			return fmt.Errorf("publish the recycle task: %w", err)
		}
		for _, mf := range owned {
			if err := r.deleteWithPrecondition(ctx, mf); err != nil {
				return err
			}
		}
		logging.FromContext(ctx).Info("importlist: recycling the files of an item that fell off the list",
			"object", item.GetName(), "mediaFiles", len(owned), "paths", len(paths))
	}
	return r.deleteWithPrecondition(ctx, item)
}

// belongsTo returns the test of a MediaFile's mediaRef against item: the
// movie itself, or the series and any of its episodes (a pack's
// MediaRef.keys lists the episodes it covers).
func (r *Reconciler) belongsTo(ctx context.Context, item client.Object) (func(commonv1.MediaRef) bool, error) {
	name := item.GetName()
	if _, ok := item.(*catalogv1alpha1.Series); !ok {
		return func(ref commonv1.MediaRef) bool {
			return ref.Kind == commonv1.MediaKindMovie && ref.Name == name
		}, nil
	}
	var eps catalogv1alpha1.EpisodeList
	if err := r.Client.List(ctx, &eps, client.InNamespace(item.GetNamespace())); err != nil {
		return nil, fmt.Errorf("list episodes of series %s: %w", name, err)
	}
	episodes := make(map[string]bool)
	for i := range eps.Items {
		if eps.Items[i].Spec.SeriesRef == name {
			episodes[eps.Items[i].Name] = true
		}
	}
	return func(ref commonv1.MediaRef) bool {
		switch ref.Kind {
		case commonv1.MediaKindSeries:
			return ref.Name == name
		case commonv1.MediaKindEpisode:
			return episodes[ref.Name] || slices.ContainsFunc(ref.Keys, func(k string) bool { return episodes[k] })
		default:
			return false
		}
	}, nil
}

// publishRecycle puts one recycle-files task on import-recycle
// (events.SubjectImportRecycleFiles, keyed by the item's UID). Its Msg-Id
// names the item and the snapshot, so a retry of the same diff is one task
// and a later snapshot's retry is another.
func (r *Reconciler) publishRecycle(
	ctx context.Context, il *catalogv1alpha1.ImportList, item client.Object, task schema.RecycleFilesTask, syncedAt time.Time,
) error {
	schemaName, data, err := schema.Encode(task)
	if err != nil {
		return err
	}
	uid := string(item.GetUID())
	id := fmt.Sprintf("recycle/%s/%d", uid, syncedAt.Unix())
	env := &events.Envelope{
		ID:     id,
		Type:   "import.RecycleFilesTask",
		Schema: schemaName,
		Source: "import@" + version.String(),
		Key:    task.RootFolder.Namespace + "/" + task.RootFolder.Name,
		Time:   r.now(),
		Data:   data,
	}
	_, err = r.Bus.Publish(ctx, events.SubjectImportRecycleFiles(uid), env, events.WithMsgID(id))
	if err != nil {
		return fmt.Errorf("importlist %s/%s: %w", il.Namespace, il.Name, err)
	}
	return nil
}
