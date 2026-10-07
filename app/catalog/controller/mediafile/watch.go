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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	seriesctl "github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The naming watches' field indexes. Each is this package's own, under a
// name no other controller registers: an informer refuses a second indexer
// of the same name. A file is found by its item through the loop's one item
// index (mfindex.Item) and a Series' Episodes through the Series
// controller's seriesctl.EpisodeBySeriesRefIndex (loop spec §3.16,
// "Consolidated").
const (
	// movieByRootFolderIndex and seriesByRootFolderIndex index an item by
	// spec.rootFolderRef.
	movieByRootFolderIndex  = "mediafile.clustarr.io/movie-rootfolder"
	seriesByRootFolderIndex = "mediafile.clustarr.io/series-rootfolder"
)

// registerNamingIndexes registers the two indexes of its own the naming
// watches' map functions read.
func registerNamingIndexes(ctx context.Context, idx client.FieldIndexer) error {
	for _, ix := range []struct {
		obj     client.Object
		field   string
		extract client.IndexerFunc
	}{
		{&catalogv1alpha1.Movie{}, movieByRootFolderIndex, indexMovieByRootFolder},
		{&catalogv1alpha1.Series{}, seriesByRootFolderIndex, indexSeriesByRootFolder},
	} {
		if err := idx.IndexField(ctx, ix.obj, ix.field, ix.extract); err != nil {
			return err
		}
	}
	return nil
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

// movieFileInput, seriesFileInput and episodeNaming are the status fields a
// file's planners read from each owner kind: what renderNaming reads
// (catalogctx.Movie, Episode and EpisodeFilePath's series folder), on a
// Movie and a Series the original language the subtitles planner counts
// untagged audio as (loop spec §3.3 S2', S4'; no watch existed on it
// before), and on a Movie and an Episode the graft planner's inputs: the
// item's status.audio.missing and its donor (S2', S3'; without them a donor
// appearing on the item never wakes its file). A spec change reaches the
// watch through GenerationChanged. Everything else in those statuses --
// phase, file rollups, artwork -- is churn the file watches must not wake
// for.
type movieFileInput struct {
	title            string
	year             int32
	imdb             string
	originalLanguage string
	graft            graftInput
}

// graftInput is what the graft planner reads of an item's status.audio.
type graftInput struct {
	missing         string
	donorPath       string
	donorRelease    string
	donorImportedAt int64
}

func graftInputs(a *catalogv1alpha1.AudioState) graftInput {
	if a == nil {
		return graftInput{}
	}
	out := graftInput{missing: strings.Join(a.Missing, ",")}
	if d := a.Donor; d != nil {
		out.donorPath, out.donorRelease, out.donorImportedAt = d.Path, d.Release, d.ImportedAt.UTC().Unix()
	}
	return out
}

type seriesFileInput struct {
	title            string
	year             int32
	path             string
	originalLanguage string
}

type episodeNaming struct {
	title    string
	absolute int32
	airDate  int64
	graft    graftInput
}

func movieFileInputs(o client.Object) movieFileInput {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok {
		return movieFileInput{}
	}
	out := movieFileInput{graft: graftInputs(m.Status.Audio)}
	if md := m.Status.Metadata; md != nil {
		out.title, out.year, out.imdb, out.originalLanguage = md.Title, md.Year, md.ExternalIDs["imdb"], md.OriginalLanguage
	}
	return out
}

func seriesFileInputs(o client.Object) seriesFileInput {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok {
		return seriesFileInput{}
	}
	out := seriesFileInput{path: s.Status.Path}
	if md := s.Status.Metadata; md != nil {
		out.title, out.year, out.originalLanguage = md.Title, md.Year, md.OriginalLanguage
	}
	return out
}

func episodeNamingInputs(o client.Object) episodeNaming {
	ep, ok := o.(*catalogv1alpha1.Episode)
	if !ok {
		return episodeNaming{}
	}
	out := episodeNaming{title: ep.Status.Title, graft: graftInputs(ep.Status.Audio)}
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
		client.MatchingFields{seriesctl.EpisodeBySeriesRefIndex: series}, client.UnsafeDisableDeepCopy); err != nil {
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
		client.MatchingFields{mfindex.Item: mfindex.ItemKey(kind, name)}, client.UnsafeDisableDeepCopy); err != nil {
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
// RootFolderNamingChanged are the file watches' predicates (S2'-S4', S11):
// a spec change, or the status fields a render reads (on a Movie and a
// Series, the original language too).
func MovieNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(movieFileInputs))
}

// EpisodeNamingChanged is an Episode's naming predicate.
func EpisodeNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(episodeNamingInputs))
}

// SeriesNamingChanged is a Series' naming predicate.
func SeriesNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(seriesFileInputs))
}

// RootFolderNamingChanged is a RootFolder's naming predicate.
func RootFolderNamingChanged() predicate.Predicate { return rootFolderNamingChanged() }
