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

package projection

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// relatedIndex buckets every resource pipeline.Project needs beyond the
// catalog item itself, keyed by the owning object's UID rather than any
// Name or Kind field. A Name match is a guess: names can be reused once an
// object is deleted and recreated, and nothing stops a stale reference from
// silently resolving to the wrong, newer object. A metav1.OwnerReference's
// UID cannot: UID is unique for the life of the cluster, so a deleted
// owner's UID never resolves to a different object -- it just stops
// resolving, which is the correct outcome.
//
// Downloads and Searches are owned directly by the catalog item they
// target: app/catalog/worker/grab/perform.go and
// app/catalog/controller/search/reconciler.go both call
// k8s.OwnerReferenceAC(owner, ...) against the resolved catalog item before
// creating a Download, and that owner reference is what this index reads.
// TranscodeJobs and SubtitleRequests are owned by the MediaFile they act on
// (per their doc comments and pkg/pipeline.Related's own field docs), not by
// the catalog item directly, so resolving one to a pipeline row goes through
// mediaFileOwner: a TranscodeJob's or SubtitleRequest's owner UID looks up
// which MediaFile owns it, and mediaFileOwner in turn maps that MediaFile's
// own owner UID to the catalog item.
//
// As of this task, importarr's MediaFile writer (app/import/worker/rescan)
// sets no OwnerReference on the MediaFiles it creates -- it links back only
// through the Name-based spec.mediaRef, which this index deliberately does
// not consult, for the same reason Download's Target field is not consulted
// either. Until a later phase sets that reference (and the TranscodeJob and
// SubtitleRequest references squasharr and captionarr do not exist yet to
// set), every bucket past downloads and searches stays empty and every
// pipeline row simply does not advance past the stages that do not need
// them. The moment those controllers start setting the owner reference,
// this index starts filling in with no changes here.
type relatedIndex struct {
	downloads map[types.UID][]downloadv1.Download
	searches  map[types.UID][]catalogv1.Search
	jobs      map[types.UID][]transcodev1.TranscodeJob
	subtitles map[types.UID][]subtitlev1.SubtitleRequest

	// all is every Download this round's single List call returned,
	// regardless of ownership -- unlike downloads above, which drops any
	// Download with no controlling owner reference. [relatedIndex.AllDownloads]
	// exposes it so Projection can feed /events/downloads (Task D3-3) from
	// the same list round this index already does for ownership
	// resolution, per ruling R4: no second List call for the downloads
	// stream.
	all []downloadv1.Download

	mediaFiles map[types.UID]*catalogv1.MediaFile

	// mediaFileOwner maps a MediaFile's own UID to whichever catalog item's
	// UID owns it, so jobs and subtitles -- owned by the MediaFile, not the
	// catalog item -- can be re-bucketed under the item a pipeline row is
	// rendered for.
	mediaFileOwner map[types.UID]types.UID
}

// buildRelatedIndex lists Download, TranscodeJob, SubtitleRequest, Search
// and MediaFile once each -- five List calls total, whatever the catalogue's
// size or the number of open SSE connections -- and buckets every item by
// its owning object's UID.
func buildRelatedIndex(ctx context.Context, r client.Reader) (*relatedIndex, error) {
	idx := &relatedIndex{
		downloads:      map[types.UID][]downloadv1.Download{},
		searches:       map[types.UID][]catalogv1.Search{},
		jobs:           map[types.UID][]transcodev1.TranscodeJob{},
		subtitles:      map[types.UID][]subtitlev1.SubtitleRequest{},
		mediaFiles:     map[types.UID]*catalogv1.MediaFile{},
		mediaFileOwner: map[types.UID]types.UID{},
	}

	// MediaFile is listed first: jobs and subtitles below need
	// mediaFileOwner populated before they can resolve through it.
	var mediaFiles catalogv1.MediaFileList
	if err := r.List(ctx, &mediaFiles); err != nil {
		return nil, fmt.Errorf("projection: list media files: %w", err)
	}
	for i := range mediaFiles.Items {
		mf := &mediaFiles.Items[i]
		if owner, ok := controllingOwnerUID(mf); ok {
			idx.mediaFiles[owner] = mf
			idx.mediaFileOwner[mf.UID] = owner
		}
	}

	var downloads downloadv1.DownloadList
	if err := r.List(ctx, &downloads); err != nil {
		return nil, fmt.Errorf("projection: list downloads: %w", err)
	}
	idx.all = downloads.Items
	for i := range downloads.Items {
		if owner, ok := controllingOwnerUID(&downloads.Items[i]); ok {
			idx.downloads[owner] = append(idx.downloads[owner], downloads.Items[i])
		}
	}

	var searches catalogv1.SearchList
	if err := r.List(ctx, &searches); err != nil {
		return nil, fmt.Errorf("projection: list searches: %w", err)
	}
	for i := range searches.Items {
		if owner, ok := controllingOwnerUID(&searches.Items[i]); ok {
			idx.searches[owner] = append(idx.searches[owner], searches.Items[i])
		}
	}

	var jobs transcodev1.TranscodeJobList
	if err := r.List(ctx, &jobs); err != nil {
		return nil, fmt.Errorf("projection: list transcode jobs: %w", err)
	}
	for i := range jobs.Items {
		if mfOwner, ok := controllingOwnerUID(&jobs.Items[i]); ok {
			if item, ok := idx.mediaFileOwner[mfOwner]; ok {
				idx.jobs[item] = append(idx.jobs[item], jobs.Items[i])
			}
		}
	}

	var subs subtitlev1.SubtitleRequestList
	if err := r.List(ctx, &subs); err != nil {
		return nil, fmt.Errorf("projection: list subtitle requests: %w", err)
	}
	for i := range subs.Items {
		if mfOwner, ok := controllingOwnerUID(&subs.Items[i]); ok {
			if item, ok := idx.mediaFileOwner[mfOwner]; ok {
				idx.subtitles[item] = append(idx.subtitles[item], subs.Items[i])
			}
		}
	}

	return idx, nil
}

// controllingOwnerUID returns the UID of obj's controlling owner reference,
// per metav1.GetControllerOf -- the one owner reference Kubernetes garbage
// collection itself treats as authoritative -- falling back to the first
// owner reference when none is marked as controller, so a non-controlling
// reference (k8s.SetOwnerReference, used for a shared, non-exclusive link)
// still resolves.
func controllingOwnerUID(obj metav1.Object) (types.UID, bool) {
	if ref := metav1.GetControllerOf(obj); ref != nil {
		return ref.UID, true
	}
	refs := obj.GetOwnerReferences()
	if len(refs) == 0 {
		return "", false
	}
	return refs[0].UID, true
}

// AllDownloads returns every Download buildRelatedIndex's single List call
// returned this round, regardless of ownership -- the same population the
// Downloads page (ui/routes.go's listDownloads) shows via its own,
// independent List call. Projection uses this one instead of making a
// second List of its own, so the SAME list round that feeds pipeline rows
// also feeds /events/downloads (Task D3-3, ruling R4).
func (idx *relatedIndex) AllDownloads() []downloadv1.Download {
	return idx.all
}

// Related returns the Related bundle buildRelatedIndex has for the catalog
// item with the given UID: everything pipeline.Project needs beyond the
// item itself. A UID this index has no data for (nothing owned by it, or
// the item itself has no related resources) returns the zero Related,
// exactly as pipeline.Project already treats "nothing to report".
func (idx *relatedIndex) Related(uid types.UID) pipeline.Related {
	return pipeline.Related{
		Downloads: idx.downloads[uid],
		Jobs:      idx.jobs[uid],
		Subtitles: idx.subtitles[uid],
		Search:    latestSearch(idx.searches[uid]),
		MediaFile: idx.mediaFiles[uid],
	}
}

// latestSearch picks the Search Project should use when an item has more
// than one. pipeline.Related.Search's doc comment calls for "the active or
// most recent interactive Search for this item": an active search (Pending
// or Running) wins outright over a finished one regardless of age, and
// among searches with the same activity ranking the most recently created
// wins.
func latestSearch(searches []catalogv1.Search) *catalogv1.Search {
	var best *catalogv1.Search
	for i := range searches {
		s := &searches[i]
		switch {
		case best == nil:
			best = s
		case searchRank(s) != searchRank(best):
			if searchRank(s) > searchRank(best) {
				best = s
			}
		case s.CreationTimestamp.After(best.CreationTimestamp.Time):
			best = s
		}
	}
	return best
}

// searchRank orders a Search's activity for [latestSearch]: an active
// search outranks a finished one.
func searchRank(s *catalogv1.Search) int {
	switch s.Status.Phase {
	case catalogv1.SearchPhasePending, catalogv1.SearchPhaseRunning:
		return 1
	default:
		return 0
	}
}

// Index is the lookup ui/plex needs over the catalog's Movie, Series and
// Episode objects: by object UID (Plex's ratingKey, D.3), by the external
// ids a Plex match request's guid carries (tmdb, tvdb, imdb -- D.4), and a
// series' episodes (for the /children and /grandchildren routes, D.2). It is
// built on demand (BuildIndex) rather than riding the shared Projection
// ticker: unlike the streamed pages, the Plex provider is an occasional,
// unauthenticated protocol call from Plex Media Server, not an open SSE
// connection, so there is no steady subscriber to broadcast to. Plex asks
// in bursts, though, so ui memoises one Index for [IndexTTL]
// ([IndexMemo]): an Index is shared by concurrent requests and must be
// treated as read-only.
type Index struct {
	movies   map[types.UID]*catalogv1.Movie
	series   map[types.UID]*catalogv1.Series
	episodes map[types.UID]*catalogv1.Episode

	tmdbMovies map[int64]*catalogv1.Movie
	tvdbSeries map[int64]*catalogv1.Series
	imdbMovies map[string]*catalogv1.Movie
	imdbSeries map[string]*catalogv1.Series

	// episodesBySeries buckets every Episode by its owning Series' UID
	// (metav1.GetControllerOf, exactly relatedIndex's own reading of
	// app/catalog/controller/series/reconciler.go's
	// k8s.SetControllerReference(s, &ep, r.Scheme)) -- not by
	// EpisodeSpec.SeriesRef, which is the Series' Name, not its UID, and so
	// no more trustworthy here than it is for relatedIndex (see that type's
	// own doc comment).
	episodesBySeries map[types.UID][]*catalogv1.Episode

	// episodeSeries is episodesBySeries' reverse: one Episode's UID to its
	// owning Series' UID, for [Index.SeriesOfEpisode].
	episodeSeries map[types.UID]types.UID
}

// BuildIndex lists every Movie, Series and Episode once each and returns the
// [Index] ui/plex's routes look everything up through. A nil reader (no
// cluster configured) returns an empty, still-usable Index rather than an
// error, matching every other read-only seam in this package (a nil
// Options.Reader renders "nothing to show", never a 500). opts are passed to
// every List ([NewIndexMemo] passes client.UnsafeDisableDeepCopy).
func BuildIndex(ctx context.Context, r client.Reader, opts ...client.ListOption) (*Index, error) {
	idx := &Index{
		movies:           map[types.UID]*catalogv1.Movie{},
		series:           map[types.UID]*catalogv1.Series{},
		episodes:         map[types.UID]*catalogv1.Episode{},
		tmdbMovies:       map[int64]*catalogv1.Movie{},
		tvdbSeries:       map[int64]*catalogv1.Series{},
		imdbMovies:       map[string]*catalogv1.Movie{},
		imdbSeries:       map[string]*catalogv1.Series{},
		episodesBySeries: map[types.UID][]*catalogv1.Episode{},
		episodeSeries:    map[types.UID]types.UID{},
	}
	if r == nil {
		return idx, nil
	}

	var movies catalogv1.MovieList
	if err := r.List(ctx, &movies, opts...); err != nil {
		return nil, fmt.Errorf("projection: list movies: %w", err)
	}
	for i := range movies.Items {
		m := &movies.Items[i]
		idx.movies[m.UID] = m
		if m.Spec.TmdbID != 0 {
			idx.tmdbMovies[m.Spec.TmdbID] = m
		}
		if md := m.Status.Metadata; md != nil {
			if imdb := md.ExternalIDs["imdb"]; imdb != "" {
				idx.imdbMovies[imdb] = m
			}
		}
	}

	var series catalogv1.SeriesList
	if err := r.List(ctx, &series, opts...); err != nil {
		return nil, fmt.Errorf("projection: list series: %w", err)
	}
	for i := range series.Items {
		s := &series.Items[i]
		idx.series[s.UID] = s
		if s.Spec.TvdbID != 0 {
			idx.tvdbSeries[s.Spec.TvdbID] = s
		}
		if md := s.Status.Metadata; md != nil {
			if imdb := md.ExternalIDs["imdb"]; imdb != "" {
				idx.imdbSeries[imdb] = s
			}
		}
	}

	var episodes catalogv1.EpisodeList
	if err := r.List(ctx, &episodes, opts...); err != nil {
		return nil, fmt.Errorf("projection: list episodes: %w", err)
	}
	for i := range episodes.Items {
		ep := &episodes.Items[i]
		idx.episodes[ep.UID] = ep
		if owner, ok := controllingOwnerUID(ep); ok {
			idx.episodesBySeries[owner] = append(idx.episodesBySeries[owner], ep)
			idx.episodeSeries[ep.UID] = owner
		}
	}

	return idx, nil
}

// ByUID returns the Movie, Series or Episode with the given UID -- Plex's
// ratingKey, per D.3, always addresses exactly one of the three. A season
// has no object of its own (it is derived from a Series' status.seasons, see
// ui/plex/ratingkey.go's SeasonKey), so this never resolves one.
func (idx *Index) ByUID(uid types.UID) (client.Object, bool) {
	if m, ok := idx.movies[uid]; ok {
		return m, true
	}
	if s, ok := idx.series[uid]; ok {
		return s, true
	}
	if e, ok := idx.episodes[uid]; ok {
		return e, true
	}
	return nil, false
}

// MovieByUID narrows [Index.ByUID] to a Movie.
func (idx *Index) MovieByUID(uid types.UID) (*catalogv1.Movie, bool) {
	m, ok := idx.movies[uid]
	return m, ok
}

// SeriesByUID narrows [Index.ByUID] to a Series.
func (idx *Index) SeriesByUID(uid types.UID) (*catalogv1.Series, bool) {
	s, ok := idx.series[uid]
	return s, ok
}

// EpisodeByUID narrows [Index.ByUID] to an Episode.
func (idx *Index) EpisodeByUID(uid types.UID) (*catalogv1.Episode, bool) {
	e, ok := idx.episodes[uid]
	return e, ok
}

// ByTMDB resolves a match request's "tmdb://<id>" guid (D.4 rule 1) against
// spec.tmdbID. Only Movie carries a tmdbID today (Series' identity is
// spec.tvdbID, [Index.ByTVDB]); kind is taken all the same so a future kind
// that gains one needs no signature change here.
func (idx *Index) ByTMDB(kind commonv1.MediaKind, id int64) (client.Object, bool) {
	if kind != commonv1.MediaKindMovie {
		return nil, false
	}
	m, ok := idx.tmdbMovies[id]
	return m, ok
}

// ByTVDB resolves a match request's "tvdb://<id>" guid (D.4 rule 1) against
// spec.tvdbID -- Series only, the one catalog kind with a tvdbID.
func (idx *Index) ByTVDB(id int64) (*catalogv1.Series, bool) {
	s, ok := idx.tvdbSeries[id]
	return s, ok
}

// ByIMDb resolves a match request's "imdb://tt<id>" guid (D.4 rule 1) against
// status.metadata.externalIDs.imdb, for whichever kind the request names
// (movie or show; D.4's guid resolution never targets a season or episode
// directly -- rule 3 resolves their show first, by this same method).
func (idx *Index) ByIMDb(kind commonv1.MediaKind, id string) (client.Object, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		m, ok := idx.imdbMovies[id]
		return m, ok
	case commonv1.MediaKindSeries:
		s, ok := idx.imdbSeries[id]
		return s, ok
	default:
		return nil, false
	}
}

// Movies returns every indexed Movie, for D.4 rule 2's title/alternate-title
// scan when no guid resolves a match. Order is unspecified; a caller that
// needs a stable order (ui/plex/match.go) sorts it.
func (idx *Index) Movies() []*catalogv1.Movie {
	out := make([]*catalogv1.Movie, 0, len(idx.movies))
	for _, m := range idx.movies {
		out = append(out, m)
	}
	return out
}

// AllSeries returns every indexed Series, [Index.Movies]'s counterpart for
// D.4 rule 2's show matching.
func (idx *Index) AllSeries() []*catalogv1.Series {
	out := make([]*catalogv1.Series, 0, len(idx.series))
	for _, s := range idx.series {
		out = append(out, s)
	}
	return out
}

// Episodes returns every Episode owned by the Series with the given UID,
// for the /children (a season's episodes) and /grandchildren (a show's
// episodes) routes. Order is unspecified; ui/plex/children.go sorts it by
// season and episode number. The slice is a copy: an Index is shared by
// every request inside [IndexTTL], and a caller sorting the index's own
// slice in place would race every other reader.
func (idx *Index) Episodes(seriesUID types.UID) []*catalogv1.Episode {
	return append([]*catalogv1.Episode(nil), idx.episodesBySeries[seriesUID]...)
}

// SeriesOfEpisode returns the Series that owns the Episode with the given
// UID -- [Index.Episodes]' reverse, for a ratingKey that resolves straight
// to an Episode (spec §D.3: an episode's ratingKey is its own UID, with no
// season or series segment), which still needs its show and season to build
// its parent/grandparent fields (spec §D.5).
func (idx *Index) SeriesOfEpisode(episodeUID types.UID) (*catalogv1.Series, bool) {
	owner, ok := idx.episodeSeries[episodeUID]
	if !ok {
		return nil, false
	}
	s, ok := idx.series[owner]
	return s, ok
}
