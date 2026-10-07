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
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Field index keys. Registered against TranscodeJob and SubtitleRequest so
// Reconcile can List "every {TranscodeJob,SubtitleRequest} for this
// MediaFile" -- the direction the mapping functions below don't need,
// because both spec types name their MediaFile directly.
//
// Each is spelled as the apiserver spells the field label of a CRD
// selectable field -- no leading dot -- so one client.MatchingFields reads
// the cache's index on the manager's client and the apiserver's
// fieldSelector on a raw one. SubtitleRequest declares the field
// selectable; TranscodeJob does not yet (see transcodeJobsOf).
const (
	transcodeJobMediaFileRefIndex    = "spec.mediaFileRef"
	subtitleRequestMediaFileRefIndex = "spec.mediaFileRef"
)

func indexTranscodeJobByMediaFileRef(o client.Object) []string {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok || tj.Spec.MediaFileRef == "" {
		return nil
	}
	return []string{tj.Spec.MediaFileRef}
}

func indexSubtitleRequestByMediaFileRef(o client.Object) []string {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok || sr.Spec.MediaFileRef == "" {
		return nil
	}
	return []string{sr.Spec.MediaFileRef}
}

// extractTranscodeJobPhase is §10's predicate: catalogarr's mediafile
// controller only reacts to a TranscodeJob reaching Succeeded, via
// k8s.StatusFieldIn. A wrong-typed object (never happens in practice --
// Watches only calls this for TranscodeJobs -- but StatusFieldChanged's own
// doc comment models exactly this defensive shape) returns "".
func extractTranscodeJobPhase(o client.Object) string {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok {
		return ""
	}
	return string(tj.Status.Phase)
}

// extractSubtitleItemsSignature is §10's other predicate:
// "status.items changed", via k8s.StatusFieldChanged. Items is a slice, not
// comparable, so this renders a comparable projection of the fields the
// sidecar feedback path cares about (langKey/state/path), not the whole
// struct -- a score or attempts-count change alone should not re-trigger
// this controller.
func extractSubtitleItemsSignature(o client.Object) string {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok {
		return ""
	}
	sig := ""
	for _, it := range sr.Status.Items {
		sig += it.LangKey + "=" + string(it.State) + ":" + it.Path + ";"
	}
	return sig
}

// mediaFileForTranscodeJob and mediaFileForSubtitleRequest map the watched
// object straight to its named MediaFile -- both spec types carry the ref
// directly, so no List/field-index round trip is needed here (the index
// above is for the reverse direction, used inside Reconcile).
func (r *Reconciler) mediaFileForTranscodeJob(ctx context.Context, o client.Object) []reconcile.Request {
	return requestsOf(FileOfTranscodeJob(ctx, o))
}

// FileOfTranscodeJob is the file a TranscodeJob names (spec.mediaFileRef).
func FileOfTranscodeJob(_ context.Context, o client.Object) []types.NamespacedName {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok || tj.Spec.MediaFileRef == "" {
		return nil
	}
	return []types.NamespacedName{{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}}
}

// TranscodeJobPhaseChanged is the predicate a TranscodeJob's file is woken
// through: every phase transition, since a transcode that starts or stops
// running holds or releases the rename (TranscodePending, ruling R18), and a
// Succeeded one is a swap to incorporate.
func TranscodeJobPhaseChanged() predicate.Predicate {
	return k8s.StatusFieldChanged(extractTranscodeJobPhase)
}

// requestsOf is nns as reconcile requests.
func requestsOf(nns []types.NamespacedName) []reconcile.Request {
	if len(nns) == 0 {
		return nil
	}
	out := make([]reconcile.Request, 0, len(nns))
	for _, nn := range nns {
		out = append(out, reconcile.Request{NamespacedName: nn})
	}
	return out
}

func (r *Reconciler) mediaFileForSubtitleRequest(ctx context.Context, o client.Object) []reconcile.Request {
	return requestsOf(FileOfSubtitleRequest(ctx, o))
}

// The naming watches' field indexes. Each is this package's own, under a
// name no other controller registers: an informer refuses a second indexer
// of the same name, and catalogarr runs every catalog controller in one
// manager (the Movie and Episode controllers' own MediaFile indexes, and
// the Series controller's ".spec.seriesRef" on Episode, are package-private
// to them).
const (
	// mediaFileByOwnerIndex indexes a MediaFile by "<kind>/<name>" of each
	// movie or episode it backs -- every covered episode of a multi-episode
	// file -- the shape fileimport.MediaFileByTargetIndexKey uses.
	mediaFileByOwnerIndex = "mediafile.clustarr.io/owner"
	// episodeBySeriesIndex indexes an Episode by spec.seriesRef.
	episodeBySeriesIndex = "mediafile.clustarr.io/episode-series"
	// movieByRootFolderIndex and seriesByRootFolderIndex index an item by
	// spec.rootFolderRef.
	movieByRootFolderIndex  = "mediafile.clustarr.io/movie-rootfolder"
	seriesByRootFolderIndex = "mediafile.clustarr.io/series-rootfolder"
)

// registerNamingIndexes registers the four indexes the naming watches' map
// functions read.
func registerNamingIndexes(ctx context.Context, idx client.FieldIndexer) error {
	for _, ix := range []struct {
		obj     client.Object
		field   string
		extract client.IndexerFunc
	}{
		{&catalogv1alpha1.MediaFile{}, mediaFileByOwnerIndex, indexMediaFileByOwner},
		{&catalogv1alpha1.Episode{}, episodeBySeriesIndex, indexEpisodeBySeries},
		{&catalogv1alpha1.Movie{}, movieByRootFolderIndex, indexMovieByRootFolder},
		{&catalogv1alpha1.Series{}, seriesByRootFolderIndex, indexSeriesByRootFolder},
	} {
		if err := idx.IndexField(ctx, ix.obj, ix.field, ix.extract); err != nil {
			return err
		}
	}
	return nil
}

func indexMediaFileByOwner(o client.Object) []string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Name == "" {
		return nil
	}
	switch ref := mf.Spec.MediaRef; ref.Kind {
	case commonv1.MediaKindMovie:
		return []string{ownerKey(ref.Kind, ref.Name)}
	case commonv1.MediaKindEpisode:
		names := coveredEpisodeNames(ref)
		keys := make([]string, 0, len(names))
		for _, n := range names {
			keys = append(keys, ownerKey(ref.Kind, n))
		}
		return keys
	}
	return nil
}

func indexEpisodeBySeries(o client.Object) []string {
	ep, ok := o.(*catalogv1alpha1.Episode)
	if !ok || ep.Spec.SeriesRef == "" {
		return nil
	}
	return []string{ep.Spec.SeriesRef}
}

func indexMovieByRootFolder(o client.Object) []string {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok || m.Spec.RootFolderRef == "" {
		return nil
	}
	return []string{m.Spec.RootFolderRef}
}

func indexSeriesByRootFolder(o client.Object) []string {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok || s.Spec.RootFolderRef == "" {
		return nil
	}
	return []string{s.Spec.RootFolderRef}
}

func ownerKey(kind commonv1.MediaKind, name string) string { return string(kind) + "/" + name }

// movieNaming, seriesNaming and episodeNaming are the status fields
// renderNaming reads from each owner kind (catalogctx.Movie, Episode and
// EpisodeFilePath's series folder); a spec change reaches the watch through
// GenerationChanged. Everything else in those statuses -- phase, file
// rollups, artwork -- is churn the naming watch must not wake for.
type movieNaming struct {
	title string
	year  int32
	imdb  string
}

type seriesNaming struct {
	title string
	year  int32
	path  string
}

type episodeNaming struct {
	title    string
	absolute int32
	airDate  int64
}

func movieNamingInputs(o client.Object) movieNaming {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok || m.Status.Metadata == nil {
		return movieNaming{}
	}
	md := m.Status.Metadata
	return movieNaming{title: md.Title, year: md.Year, imdb: md.ExternalIDs["imdb"]}
}

func seriesNamingInputs(o client.Object) seriesNaming {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok {
		return seriesNaming{}
	}
	out := seriesNaming{path: s.Status.Path}
	if md := s.Status.Metadata; md != nil {
		out.title, out.year = md.Title, md.Year
	}
	return out
}

func episodeNamingInputs(o client.Object) episodeNaming {
	ep, ok := o.(*catalogv1alpha1.Episode)
	if !ok {
		return episodeNaming{}
	}
	out := episodeNaming{title: ep.Status.Title}
	if ep.Status.AbsoluteNumber != nil {
		out.absolute = *ep.Status.AbsoluteNumber
	}
	if ep.Status.AirDate != nil {
		out.airDate = ep.Status.AirDate.UTC().Truncate(time.Second).Unix()
	}
	return out
}

// rootFolderNamingChanged passes a RootFolder update only when spec.path or
// spec.naming moved -- the two inputs of a render -- so editing its
// defaults, recycle bin or free-space floor does not re-render every file
// under it. Creates and deletes pass: a RootFolder appearing or going away
// is exactly what turns its files' proposals Unrenderable or back.
func rootFolderNamingChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRF, okOld := e.ObjectOld.(*catalogv1alpha1.RootFolder)
			newRF, okNew := e.ObjectNew.(*catalogv1alpha1.RootFolder)
			if !okOld || !okNew {
				return false
			}
			return oldRF.Spec.Path != newRF.Spec.Path || !equality.Semantic.DeepEqual(oldRF.Spec.Naming, newRF.Spec.Naming)
		},
	}
}

// mediaFilesForMovie, mediaFilesForEpisode, mediaFilesForSeries and
// mediaFilesForRootFolder are the old controller's map functions over
// FilesForMovie, FilesForEpisode, FilesForSeries and FilesForRootFolder.
func (r *Reconciler) mediaFilesForMovie(ctx context.Context, o client.Object) []reconcile.Request {
	return requestsOf(FilesForMovie(ctx, r.Client, o))
}

func (r *Reconciler) mediaFilesForEpisode(ctx context.Context, o client.Object) []reconcile.Request {
	return requestsOf(FilesForEpisode(ctx, r.Client, o))
}

func (r *Reconciler) mediaFilesForSeries(ctx context.Context, o client.Object) []reconcile.Request {
	return requestsOf(FilesForSeries(ctx, r.Client, o))
}

func (r *Reconciler) mediaFilesForRootFolder(ctx context.Context, o client.Object) []reconcile.Request {
	return requestsOf(FilesForRootFolder(ctx, r.Client, o))
}

// FilesForMovie, FilesForEpisode, FilesForSeries and FilesForRootFolder map
// a naming input's change to the MediaFiles whose proposal it moves: a
// Movie's or Episode's own files; a Series' episodes' files; and a
// RootFolder's every movie's and series' files -- a preset change renames
// the whole folder by design. Every List is served from c (the manager's
// cache) through an index, without a deep copy, since only names are read.
func FilesForMovie(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok {
		return nil
	}
	set := fileSet{}
	addMediaFilesOf(ctx, c, set, m.Namespace, commonv1.MediaKindMovie, m.Name)
	return set.names()
}

// FilesForEpisode is an Episode's files.
func FilesForEpisode(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	ep, ok := o.(*catalogv1alpha1.Episode)
	if !ok {
		return nil
	}
	set := fileSet{}
	addMediaFilesOf(ctx, c, set, ep.Namespace, commonv1.MediaKindEpisode, ep.Name)
	return set.names()
}

// FilesForSeries is a Series' episodes' files.
func FilesForSeries(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok {
		return nil
	}
	set := fileSet{}
	addSeriesMediaFiles(ctx, c, set, s.Namespace, s.Name)
	return set.names()
}

// FilesForRootFolder is a RootFolder's every movie's and series' files.
func FilesForRootFolder(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	rf, ok := o.(*catalogv1alpha1.RootFolder)
	if !ok {
		return nil
	}
	set := fileSet{}
	var movies catalogv1alpha1.MovieList
	if err := c.List(ctx, &movies, client.InNamespace(rf.Namespace),
		client.MatchingFields{movieByRootFolderIndex: rf.Name}, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("mediafile: list a RootFolder's movies to re-name their files", "rootFolder", rf.Name, "error", err)
	}
	for i := range movies.Items {
		addMediaFilesOf(ctx, c, set, rf.Namespace, commonv1.MediaKindMovie, movies.Items[i].Name)
	}
	var series catalogv1alpha1.SeriesList
	if err := c.List(ctx, &series, client.InNamespace(rf.Namespace),
		client.MatchingFields{seriesByRootFolderIndex: rf.Name}, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("mediafile: list a RootFolder's series to re-name their files", "rootFolder", rf.Name, "error", err)
	}
	for i := range series.Items {
		addSeriesMediaFiles(ctx, c, set, rf.Namespace, series.Items[i].Name)
	}
	return set.names()
}

// addSeriesMediaFiles adds the MediaFiles of every Episode of series.
func addSeriesMediaFiles(ctx context.Context, c client.Reader, set fileSet, ns, series string) {
	var eps catalogv1alpha1.EpisodeList
	if err := c.List(ctx, &eps, client.InNamespace(ns),
		client.MatchingFields{episodeBySeriesIndex: series}, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("mediafile: list a series' episodes to re-name their files", "series", series, "error", err)
		return
	}
	for i := range eps.Items {
		addMediaFilesOf(ctx, c, set, ns, commonv1.MediaKindEpisode, eps.Items[i].Name)
	}
}

// addMediaFilesOf adds the MediaFiles backing the named movie or episode.
// A failed List is logged and skipped: a map function cannot return an
// error, and the MediaFile is re-rendered on its next pass anyway.
func addMediaFilesOf(ctx context.Context, c client.Reader, set fileSet, ns string, kind commonv1.MediaKind, name string) {
	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.InNamespace(ns),
		client.MatchingFields{mediaFileByOwnerIndex: ownerKey(kind, name)}, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("mediafile: list an item's files to re-name them", "kind", kind, "name", name, "error", err)
		return
	}
	for i := range files.Items {
		set[types.NamespacedName{Namespace: files.Items[i].Namespace, Name: files.Items[i].Name}] = struct{}{}
	}
}

// fileSet deduplicates the files a fan-out collects: a multi-episode file is
// reached once through each episode it covers.
type fileSet map[types.NamespacedName]struct{}

func (s fileSet) names() []types.NamespacedName {
	if len(s) == 0 {
		return nil
	}
	out := make([]types.NamespacedName, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}

// MovieNamingChanged, EpisodeNamingChanged, SeriesNamingChanged and
// RootFolderNamingChanged are the naming watches' predicates: a spec change,
// or the status fields a render reads.
func MovieNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(movieNamingInputs))
}

// EpisodeNamingChanged is an Episode's naming predicate.
func EpisodeNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(episodeNamingInputs))
}

// SeriesNamingChanged is a Series' naming predicate.
func SeriesNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(seriesNamingInputs))
}

// RootFolderNamingChanged is a RootFolder's naming predicate.
func RootFolderNamingChanged() predicate.Predicate { return rootFolderNamingChanged() }

// SubtitleRequestItemsChanged passes a SubtitleRequest whose items' langKey,
// state or path moved.
func SubtitleRequestItemsChanged() predicate.Predicate {
	return k8s.StatusFieldChanged(extractSubtitleItemsSignature)
}

// FileOfSubtitleRequest is the file a SubtitleRequest names.
func FileOfSubtitleRequest(_ context.Context, o client.Object) []types.NamespacedName {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok || sr.Spec.MediaFileRef == "" {
		return nil
	}
	return []types.NamespacedName{{Namespace: sr.Namespace, Name: sr.Spec.MediaFileRef}}
}
