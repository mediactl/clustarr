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
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// relatedIndex buckets every resource pipeline.Project needs beyond the
// catalog item itself.
//
// Searches are owned directly by the catalog item they target
// (app/catalog/controller/search calls k8s.OwnerReferenceAC against the
// resolved item), and that owner reference is what this index reads: a
// metav1.OwnerReference's UID cannot resolve to a newer object that reused
// a deleted one's name. Grabs are no objects of their own (ADR-0019 §6.2):
// each item's entries are read off the items themselves (entryIndex).
//
// MediaFiles are bucketed by spec.mediaRef instead (loop spec §7.7): no
// production MediaFile carries an owner reference -- the import links a
// file to its item only through spec.mediaRef -- so an owner-keyed bucket
// was always empty and no pipeline row ever advanced past Imported. A file
// is the item's by name, in the item's namespace; Related prefers the file
// the item's status.fileRef names, which the item's own reconciler wrote.
// The file's status.transcode and status.subtitles carry what the per-file
// objects ADR-0016 folded into it used to.
type relatedIndex struct {
	searches map[types.UID][]catalogv1.Search

	// files buckets every MediaFile under each item its spec.mediaRef names,
	// "<namespace>/<kind>/<name>" for mediaRef.name and every mediaRef.keys
	// entry, so a multi-episode file belongs to each of its Episodes.
	files  map[string][]*catalogv1.MediaFile
	byName map[types.NamespacedName]*catalogv1.MediaFile

	// progress reads a running transcode's percent; nil renders none.
	progress TranscodeProgressFunc
}

// buildRelatedIndex lists Search and MediaFile once each -- two List calls
// total, whatever the catalogue's size or the number of open SSE
// connections -- and buckets searches by their owning item's UID and files
// by the items their spec.mediaRef names.
func buildRelatedIndex(ctx context.Context, r client.Reader, opts ...client.ListOption) (*relatedIndex, error) {
	idx := &relatedIndex{
		searches: map[types.UID][]catalogv1.Search{},
		files:    map[string][]*catalogv1.MediaFile{},
		byName:   map[types.NamespacedName]*catalogv1.MediaFile{},
	}

	var mediaFiles catalogv1.MediaFileList
	if err := r.List(ctx, &mediaFiles, opts...); err != nil {
		return nil, fmt.Errorf("projection: list media files: %w", err)
	}
	for i := range mediaFiles.Items {
		mf := &mediaFiles.Items[i]
		idx.byName[types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name}] = mf
		ref := mf.Spec.MediaRef
		if ref.Name == "" {
			continue
		}
		idx.files[itemKey(mf.Namespace, ref.Kind, ref.Name)] = append(idx.files[itemKey(mf.Namespace, ref.Kind, ref.Name)], mf)
		for _, k := range ref.Keys {
			if k != "" && k != ref.Name {
				idx.files[itemKey(mf.Namespace, ref.Kind, k)] = append(idx.files[itemKey(mf.Namespace, ref.Kind, k)], mf)
			}
		}
	}

	var searches catalogv1.SearchList
	if err := r.List(ctx, &searches, opts...); err != nil {
		return nil, fmt.Errorf("projection: list searches: %w", err)
	}
	for i := range searches.Items {
		if owner, ok := controllingOwnerUID(&searches.Items[i]); ok {
			idx.searches[owner] = append(idx.searches[owner], searches.Items[i])
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

// progressTimeout bounds one transcode-progress read.
const progressTimeout = 2 * time.Second

// Related returns the Related bundle buildRelatedIndex has for the catalog
// item: everything pipeline.Project needs beyond the item itself and its
// grab entries. An item this index has no data for returns the zero
// Related, exactly as pipeline.Project already treats "nothing to report".
// A running transcode's percent is read from clustarr-progress under
// progressTimeout; a failed read renders no percent.
func (idx *relatedIndex) Related(ctx context.Context, item client.Object) pipeline.Related {
	r := pipeline.Related{Search: latestSearch(idx.searches[item.GetUID()]), MediaFile: idx.fileOf(item)}
	if mf := r.MediaFile; mf != nil && idx.progress != nil && mf.Status.Transcode != nil &&
		mf.Status.Transcode.Phase == catalogv1.TranscodePhaseRunning {
		pctx, cancel := context.WithTimeout(ctx, progressTimeout)
		if pct, ok, err := idx.progress(pctx, mf.UID); err == nil && ok {
			r.TranscodePercent = &pct
		}
		cancel()
	}
	return r
}

func itemKey(ns string, kind commonv1.MediaKind, name string) string {
	return ns + "/" + string(kind) + "/" + name
}

// fileOf is the file status.fileRef names (Movie, Episode, Book, Issue),
// else the newest file bucketed under the item.
func (idx *relatedIndex) fileOf(item client.Object) *catalogv1.MediaFile {
	var kind commonv1.MediaKind
	var ref *string
	switch o := item.(type) {
	case *catalogv1.Movie:
		kind, ref = commonv1.MediaKindMovie, o.Status.FileRef
	case *catalogv1.Episode:
		kind, ref = commonv1.MediaKindEpisode, o.Status.FileRef
	case *catalogv1.Book:
		kind, ref = commonv1.MediaKindBook, o.Status.FileRef
	case *catalogv1.Issue:
		kind, ref = commonv1.MediaKindIssue, o.Status.FileRef
	case *catalogv1.Album:
		kind = commonv1.MediaKindAlbum
	case *catalogv1.Audiobook:
		kind = commonv1.MediaKindAudiobook
	default:
		return nil
	}
	if ref != nil && *ref != "" {
		if mf := idx.byName[types.NamespacedName{Namespace: item.GetNamespace(), Name: *ref}]; mf != nil {
			return mf
		}
	}
	var newest *catalogv1.MediaFile
	for _, mf := range idx.files[itemKey(item.GetNamespace(), kind, item.GetName())] {
		if newest == nil || mf.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = mf
		}
	}
	return newest
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
// ids a Plex match request's guid carries (tmdb, tvdb, imdb -- D.4), a
// series' episodes (for the /children and /grandchildren routes, D.2), and
// the movie and episode files by path suffix (rule 0 of the filename-match
// spec, 2026-09-30) -- and, under [IndexOptions.Music], the same over
// Artist, Album, Author and Audiobook for the music and audiobook roots
// (2026-11-10 design §7.5). It is
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
	tmdbSeries map[int64]*catalogv1.Series
	tvdbSeries map[int64]*catalogv1.Series
	imdbMovies map[string]*catalogv1.Movie
	imdbSeries map[string]*catalogv1.Series

	// plexIDs maps a Plex metadata id to what it names ([Index.ByPlexID]);
	// plexShared holds the ids two items claim, which name neither.
	plexIDs    map[string]plexRef
	plexShared map[string]bool

	// collections buckets Movies by their TMDB collection id
	// (status.metadata.collection), and collectionPlex maps a collection's
	// Plex id to that TMDB id ([Index.CollectionByPlexID]); a Plex id two
	// collections claim is in collectionPlexShared and names neither.
	collections          map[int64][]*catalogv1.Movie
	collectionPlex       map[string]int64
	collectionPlexShared map[string]bool

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

	// movieByName and episodeByName key the same objects by namespace and
	// name: a MediaFile's spec.mediaRef names its item that way, not by
	// UID.
	movieByName   map[types.NamespacedName]*catalogv1.Movie
	episodeByName map[types.NamespacedName]*catalogv1.Episode

	// filesByBase buckets every movie and episode MediaFile -- and, under
	// [IndexOptions.Music], every album and audiobook one -- by its file's
	// base name, for [Index.FilesEndingWith].
	filesByBase map[string][]*catalogv1.MediaFile

	// The music and audiobook roots' lookups (2026-11-10 design §7.5),
	// built only under [IndexOptions.Music]: a cluster with the roots off
	// lists no Artist, Album, Author or Audiobook and pays nothing for
	// them. music records whether they were built ([Index.Music]).
	music      bool
	artists    map[types.UID]*catalogv1.Artist
	albums     map[types.UID]*catalogv1.Album
	authors    map[types.UID]*catalogv1.Author
	audiobooks map[types.UID]*catalogv1.Audiobook

	// The same objects by namespace and name: an Album's spec.artistRef,
	// an Audiobook's spec.authorRef and a MediaFile's spec.mediaRef name
	// their item that way, not by UID.
	artistByName    map[types.NamespacedName]*catalogv1.Artist
	albumByName     map[types.NamespacedName]*catalogv1.Album
	authorByName    map[types.NamespacedName]*catalogv1.Author
	audiobookByName map[types.NamespacedName]*catalogv1.Audiobook

	// mbArtists and mbAlbums resolve a match request's "mbid://" guid
	// ([Index.ByMBID]): an Artist by spec.musicBrainzID, an Album by
	// spec.releaseGroupID. asins resolves an "asin://" guid
	// ([Index.ByASIN]) by Audiobook spec.asin.
	mbArtists map[string]*catalogv1.Artist
	mbAlbums  map[string]*catalogv1.Album
	asins     map[string]*catalogv1.Audiobook

	// albumsByArtist buckets every Album by its Artist's UID, through
	// spec.artistRef (the Artist's name in the Album's namespace), sorted
	// by release date then title; albumArtist is its reverse. The
	// audiobook pair is the same through spec.authorRef (design D11), and
	// unlinkedAudiobooks buckets the Audiobooks with no authorRef by their
	// first credited author's name, for the audiobooks root's synthetic
	// artist (design §7.2).
	albumsByArtist     map[types.UID][]*catalogv1.Album
	albumArtist        map[types.UID]types.UID
	audiobooksByAuthor map[types.UID][]*catalogv1.Audiobook
	audiobookAuthor    map[types.UID]types.UID
	unlinkedAudiobooks map[string][]*catalogv1.Audiobook

	// mediaFileByName keys the album and audiobook MediaFiles by name, the
	// way an Album's status.tracks[].fileRef and an Audiobook's
	// status.fileRefs name them.
	mediaFileByName map[types.NamespacedName]*catalogv1.MediaFile
}

// IndexOptions says what [BuildIndexWith] builds beyond the movie and TV
// lookups. Music adds the music and audiobook roots' lists and maps
// (2026-11-10 design §7.5), which only `--plex-music-provider` needs.
type IndexOptions struct {
	Music bool
}

// BuildIndex lists every Movie, Series and Episode once each and returns the
// [Index] ui/plex's routes look everything up through. A nil reader (no
// cluster configured) returns an empty, still-usable Index rather than an
// error, matching every other read-only seam in this package (a nil
// Options.Reader renders "nothing to show", never a 500). opts are passed to
// every List ([NewIndexMemo] passes client.UnsafeDisableDeepCopy). It is
// [BuildIndexWith] with no music lookups.
func BuildIndex(ctx context.Context, r client.Reader, opts ...client.ListOption) (*Index, error) {
	return BuildIndexWith(ctx, r, IndexOptions{}, opts...)
}

// BuildIndexWith is [BuildIndex] with [IndexOptions]: under o.Music it
// also lists every Artist, Album, Author and Audiobook, for the music and
// audiobook roots (2026-11-10 design §7.5).
func BuildIndexWith(ctx context.Context, r client.Reader, o IndexOptions, opts ...client.ListOption) (*Index, error) {
	idx := &Index{
		movies:           map[types.UID]*catalogv1.Movie{},
		series:           map[types.UID]*catalogv1.Series{},
		episodes:         map[types.UID]*catalogv1.Episode{},
		tmdbMovies:       map[int64]*catalogv1.Movie{},
		tmdbSeries:       map[int64]*catalogv1.Series{},
		tvdbSeries:       map[int64]*catalogv1.Series{},
		imdbMovies:       map[string]*catalogv1.Movie{},
		imdbSeries:       map[string]*catalogv1.Series{},
		episodesBySeries: map[types.UID][]*catalogv1.Episode{},
		episodeSeries:    map[types.UID]types.UID{},
		movieByName:      map[types.NamespacedName]*catalogv1.Movie{},
		episodeByName:    map[types.NamespacedName]*catalogv1.Episode{},
		filesByBase:      map[string][]*catalogv1.MediaFile{},
		plexIDs:          map[string]plexRef{},
		plexShared:       map[string]bool{},

		collections:          map[int64][]*catalogv1.Movie{},
		collectionPlex:       map[string]int64{},
		collectionPlexShared: map[string]bool{},

		music:              o.Music,
		artists:            map[types.UID]*catalogv1.Artist{},
		albums:             map[types.UID]*catalogv1.Album{},
		authors:            map[types.UID]*catalogv1.Author{},
		audiobooks:         map[types.UID]*catalogv1.Audiobook{},
		artistByName:       map[types.NamespacedName]*catalogv1.Artist{},
		albumByName:        map[types.NamespacedName]*catalogv1.Album{},
		authorByName:       map[types.NamespacedName]*catalogv1.Author{},
		audiobookByName:    map[types.NamespacedName]*catalogv1.Audiobook{},
		mbArtists:          map[string]*catalogv1.Artist{},
		mbAlbums:           map[string]*catalogv1.Album{},
		asins:              map[string]*catalogv1.Audiobook{},
		albumsByArtist:     map[types.UID][]*catalogv1.Album{},
		albumArtist:        map[types.UID]types.UID{},
		audiobooksByAuthor: map[types.UID][]*catalogv1.Audiobook{},
		audiobookAuthor:    map[types.UID]types.UID{},
		unlinkedAudiobooks: map[string][]*catalogv1.Audiobook{},
		mediaFileByName:    map[types.NamespacedName]*catalogv1.MediaFile{},
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
		idx.movieByName[types.NamespacedName{Namespace: m.Namespace, Name: m.Name}] = m
		if m.Spec.TmdbID != 0 {
			idx.tmdbMovies[m.Spec.TmdbID] = m
		}
		if md := m.Status.Metadata; md != nil {
			if imdb := md.ExternalIDs["imdb"]; imdb != "" {
				idx.imdbMovies[imdb] = m
			}
			idx.addPlexID(md.ExternalIDs["plex"], plexRef{uid: m.UID})
			if col := md.Collection; col != nil && col.TmdbID != 0 {
				idx.collections[col.TmdbID] = append(idx.collections[col.TmdbID], m)
				idx.addCollectionPlexID(col.PlexID, col.TmdbID)
			}
		}
	}
	for _, ms := range idx.collections {
		slices.SortFunc(ms, func(a, b *catalogv1.Movie) int {
			if c := cmp.Compare(a.Status.Metadata.Year, b.Status.Metadata.Year); c != 0 {
				return c
			}
			return cmp.Compare(a.Name, b.Name)
		})
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
			if tmdb, err := strconv.ParseInt(md.ExternalIDs["tmdb"], 10, 64); err == nil && tmdb != 0 {
				idx.tmdbSeries[tmdb] = s
			}
			idx.addPlexID(md.ExternalIDs["plex"], plexRef{uid: s.UID})
			for _, ps := range md.PlexSeasons {
				idx.addPlexID(ps.ID, plexRef{uid: s.UID, season: ps.Number, isSeason: true})
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
		idx.addPlexID(ep.Status.PlexID, plexRef{uid: ep.UID})
		idx.episodeByName[types.NamespacedName{Namespace: ep.Namespace, Name: ep.Name}] = ep
		if owner, ok := controllingOwnerUID(ep); ok {
			idx.episodesBySeries[owner] = append(idx.episodesBySeries[owner], ep)
			idx.episodeSeries[ep.UID] = owner
		}
	}

	if o.Music {
		if err := idx.listMusic(ctx, r, opts...); err != nil {
			return nil, err
		}
	}

	var files catalogv1.MediaFileList
	if err := r.List(ctx, &files, opts...); err != nil {
		return nil, fmt.Errorf("projection: list media files: %w", err)
	}
	for i := range files.Items {
		f := &files.Items[i]
		switch f.Spec.MediaRef.Kind {
		case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
		case commonv1.MediaKindAlbum, commonv1.MediaKindAudiobook:
			if !o.Music {
				continue
			}
			idx.mediaFileByName[types.NamespacedName{Namespace: f.Namespace, Name: f.Name}] = f
		default:
			continue
		}
		if f.Spec.Path == "" {
			continue
		}
		base := path.Base(f.Spec.Path)
		idx.filesByBase[base] = append(idx.filesByBase[base], f)
	}

	return idx, nil
}

// listMusic lists every Artist, Album, Author and Audiobook once each into
// the music maps (design §7.5). Artists before Albums and Authors before
// Audiobooks, since a child is bucketed under its container by the name
// its spec names.
func (idx *Index) listMusic(ctx context.Context, r client.Reader, opts ...client.ListOption) error {
	var artists catalogv1.ArtistList
	if err := r.List(ctx, &artists, opts...); err != nil {
		return fmt.Errorf("projection: list artists: %w", err)
	}
	for i := range artists.Items {
		a := &artists.Items[i]
		idx.artists[a.UID] = a
		idx.artistByName[types.NamespacedName{Namespace: a.Namespace, Name: a.Name}] = a
		if a.Spec.MusicBrainzID != "" {
			idx.mbArtists[a.Spec.MusicBrainzID] = a
		}
	}

	var albums catalogv1.AlbumList
	if err := r.List(ctx, &albums, opts...); err != nil {
		return fmt.Errorf("projection: list albums: %w", err)
	}
	for i := range albums.Items {
		al := &albums.Items[i]
		idx.albums[al.UID] = al
		idx.albumByName[types.NamespacedName{Namespace: al.Namespace, Name: al.Name}] = al
		if al.Spec.ReleaseGroupID != "" {
			idx.mbAlbums[al.Spec.ReleaseGroupID] = al
		}
		if artist, ok := idx.artistByName[types.NamespacedName{Namespace: al.Namespace, Name: al.Spec.ArtistRef}]; ok {
			idx.albumsByArtist[artist.UID] = append(idx.albumsByArtist[artist.UID], al)
			idx.albumArtist[al.UID] = artist.UID
		}
	}
	for _, als := range idx.albumsByArtist {
		slices.SortFunc(als, compareAlbums)
	}

	var authors catalogv1.AuthorList
	if err := r.List(ctx, &authors, opts...); err != nil {
		return fmt.Errorf("projection: list authors: %w", err)
	}
	for i := range authors.Items {
		a := &authors.Items[i]
		idx.authors[a.UID] = a
		idx.authorByName[types.NamespacedName{Namespace: a.Namespace, Name: a.Name}] = a
	}

	var audiobooks catalogv1.AudiobookList
	if err := r.List(ctx, &audiobooks, opts...); err != nil {
		return fmt.Errorf("projection: list audiobooks: %w", err)
	}
	for i := range audiobooks.Items {
		ab := &audiobooks.Items[i]
		idx.audiobooks[ab.UID] = ab
		idx.audiobookByName[types.NamespacedName{Namespace: ab.Namespace, Name: ab.Name}] = ab
		if ab.Spec.ASIN != "" {
			idx.asins[ab.Spec.ASIN] = ab
		}
		if ref := ab.Spec.AuthorRef; ref != nil && *ref != "" {
			if author, ok := idx.authorByName[types.NamespacedName{Namespace: ab.Namespace, Name: *ref}]; ok {
				idx.audiobooksByAuthor[author.UID] = append(idx.audiobooksByAuthor[author.UID], ab)
				idx.audiobookAuthor[ab.UID] = author.UID
				continue
			}
		}
		name := FirstAuthorName(ab)
		idx.unlinkedAudiobooks[name] = append(idx.unlinkedAudiobooks[name], ab)
	}
	for _, abs := range idx.audiobooksByAuthor {
		slices.SortFunc(abs, compareAudiobooks)
	}
	for _, abs := range idx.unlinkedAudiobooks {
		slices.SortFunc(abs, compareAudiobooks)
	}
	return nil
}

// FirstAuthorName is an Audiobook's first credited author's name
// (status.metadata.authors[0]), "" when it has none: what the audiobooks
// root's synthetic artist is keyed by for an Audiobook with no authorRef
// (design §7.2).
func FirstAuthorName(ab *catalogv1.Audiobook) string {
	if md := ab.Status.Metadata; md != nil && len(md.Authors) > 0 {
		return md.Authors[0].Name
	}
	return ""
}

// compareAlbums orders an artist's albums by release date, then title, then
// name, so a listing is stable across builds.
func compareAlbums(a, b *catalogv1.Album) int {
	if c := compareDates(albumReleaseDate(a), albumReleaseDate(b)); c != 0 {
		return c
	}
	if c := cmp.Compare(albumTitle(a), albumTitle(b)); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

// compareAudiobooks is [compareAlbums] for an author's audiobooks.
func compareAudiobooks(a, b *catalogv1.Audiobook) int {
	if c := compareDates(audiobookReleaseDate(a), audiobookReleaseDate(b)); c != 0 {
		return c
	}
	if c := cmp.Compare(audiobookTitle(a), audiobookTitle(b)); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

// compareDates orders two optional dates, an unknown one last.
func compareDates(a, b *metav1.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Time.Compare(b.Time)
}

func albumReleaseDate(a *catalogv1.Album) *metav1.Time {
	if a.Status.Metadata == nil {
		return nil
	}
	return a.Status.Metadata.ReleaseDate
}

func albumTitle(a *catalogv1.Album) string {
	if a.Status.Metadata == nil {
		return ""
	}
	return a.Status.Metadata.Title
}

func audiobookReleaseDate(a *catalogv1.Audiobook) *metav1.Time {
	if a.Status.Metadata == nil {
		return nil
	}
	return a.Status.Metadata.ReleaseDate
}

func audiobookTitle(a *catalogv1.Audiobook) string {
	if a.Status.Metadata == nil {
		return ""
	}
	return a.Status.Metadata.Title
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
	if a, ok := idx.artists[uid]; ok {
		return a, true
	}
	if al, ok := idx.albums[uid]; ok {
		return al, true
	}
	if a, ok := idx.authors[uid]; ok {
		return a, true
	}
	if ab, ok := idx.audiobooks[uid]; ok {
		return ab, true
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

// ByTMDB resolves a match request's "tmdb://<id>" guid (D.4 rule 1): a
// Movie by spec.tmdbID, a Series by status.metadata.externalIDs.tmdb (its
// spec identity is the tvdbID, [Index.ByTVDB], but the metadata gateway
// records the TMDB id too, and a Plex library's folder hints or its own
// agent's guids may name a show by either).
func (idx *Index) ByTMDB(kind commonv1.MediaKind, id int64) (client.Object, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		m, ok := idx.tmdbMovies[id]
		return m, ok
	case commonv1.MediaKindSeries:
		s, ok := idx.tmdbSeries[id]
		return s, ok
	default:
		return nil, false
	}
}

// ByTVDB resolves a match request's "tvdb://<id>" guid (D.4 rule 1) against
// spec.tvdbID -- Series only, the one catalog kind with a tvdbID.
func (idx *Index) ByTVDB(id int64) (*catalogv1.Series, bool) {
	s, ok := idx.tvdbSeries[id]
	return s, ok
}

// plexRef is what a Plex id names: an item's UID and, for a season, its
// number.
type plexRef struct {
	uid      types.UID
	season   int32
	isSeason bool
}

// addPlexID records what id names; an id two items claim names neither.
func (idx *Index) addPlexID(id string, ref plexRef) {
	if id == "" {
		return
	}
	if prev, ok := idx.plexIDs[id]; ok && prev != ref {
		idx.plexShared[id] = true
	}
	idx.plexIDs[id] = ref
}

// addCollectionPlexID records that a collection's Plex id names TMDB
// collection tmdbID; an id two collections claim names neither.
func (idx *Index) addCollectionPlexID(id string, tmdbID int64) {
	if id == "" {
		return
	}
	if prev, ok := idx.collectionPlex[id]; ok && prev != tmdbID {
		idx.collectionPlexShared[id] = true
	}
	idx.collectionPlex[id] = tmdbID
}

// CollectionMovies is the library's movies of TMDB collection tmdbID,
// oldest first; none for a collection no Movie belongs to.
func (idx *Index) CollectionMovies(tmdbID int64) []*catalogv1.Movie {
	return idx.collections[tmdbID]
}

// CollectionByPlexID resolves a collection's Plex id -- the 24-hex id of
// its plex://collection/ GUID, which PMS asks with once it holds the
// collection under it -- to its TMDB collection id.
func (idx *Index) CollectionByPlexID(id string) (int64, bool) {
	tmdbID, ok := idx.collectionPlex[id]
	if !ok || idx.collectionPlexShared[id] {
		return 0, false
	}
	return tmdbID, true
}

// ByPlexID resolves a Plex metadata id -- the 24-hex id of a plex:// GUID,
// which PMS sends in place of the ratingKey once it holds an item under
// that GUID -- to what it names, in plex.ParseRatingKey's shape: a Movie,
// Series or Episode UID, or a Series UID and season number. An id two
// items claim resolves to neither: the provider never guesses.
func (idx *Index) ByPlexID(id string) (uid types.UID, season int32, isSeason, ok bool) {
	ref, found := idx.plexIDs[id]
	if !found || idx.plexShared[id] {
		return "", 0, false, false
	}
	return ref.uid, ref.season, ref.isSeason, true
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

// FilesEndingWith returns the movie and episode MediaFiles -- and, under
// [IndexOptions.Music], the album and audiobook ones -- whose spec.path
// ends with "/"+rel. rel is a clean relative path to the file from some
// folder above it, as Plex names a match request's file relative to its
// own library folder; the caller cleans and vets it.
func (idx *Index) FilesEndingWith(rel string) []*catalogv1.MediaFile {
	suffix := "/" + rel
	var out []*catalogv1.MediaFile
	for _, f := range idx.filesByBase[path.Base(rel)] {
		if strings.HasSuffix(f.Spec.Path, suffix) {
			out = append(out, f)
		}
	}
	return out
}

// MovieByName returns the Movie a MediaFile's spec.mediaRef names.
func (idx *Index) MovieByName(namespace, name string) (*catalogv1.Movie, bool) {
	m, ok := idx.movieByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return m, ok
}

// EpisodeByName returns the Episode a MediaFile's spec.mediaRef names.
func (idx *Index) EpisodeByName(namespace, name string) (*catalogv1.Episode, bool) {
	e, ok := idx.episodeByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return e, ok
}

// Music reports whether the music and audiobook lookups below were built
// ([IndexOptions.Music]); without them every one answers "not found".
func (idx *Index) Music() bool { return idx.music }

// ArtistByUID narrows [Index.ByUID] to an Artist.
func (idx *Index) ArtistByUID(uid types.UID) (*catalogv1.Artist, bool) {
	a, ok := idx.artists[uid]
	return a, ok
}

// AlbumByUID narrows [Index.ByUID] to an Album.
func (idx *Index) AlbumByUID(uid types.UID) (*catalogv1.Album, bool) {
	a, ok := idx.albums[uid]
	return a, ok
}

// AuthorByUID narrows [Index.ByUID] to an Author.
func (idx *Index) AuthorByUID(uid types.UID) (*catalogv1.Author, bool) {
	a, ok := idx.authors[uid]
	return a, ok
}

// AudiobookByUID narrows [Index.ByUID] to an Audiobook.
func (idx *Index) AudiobookByUID(uid types.UID) (*catalogv1.Audiobook, bool) {
	a, ok := idx.audiobooks[uid]
	return a, ok
}

// Artists returns every indexed Artist, for the music root's title match.
// Order is unspecified; the caller sorts.
func (idx *Index) Artists() []*catalogv1.Artist {
	out := make([]*catalogv1.Artist, 0, len(idx.artists))
	for _, a := range idx.artists {
		out = append(out, a)
	}
	return out
}

// Albums returns every indexed Album. Order is unspecified.
func (idx *Index) Albums() []*catalogv1.Album {
	out := make([]*catalogv1.Album, 0, len(idx.albums))
	for _, a := range idx.albums {
		out = append(out, a)
	}
	return out
}

// Authors returns every indexed Author. Order is unspecified.
func (idx *Index) Authors() []*catalogv1.Author {
	out := make([]*catalogv1.Author, 0, len(idx.authors))
	for _, a := range idx.authors {
		out = append(out, a)
	}
	return out
}

// Audiobooks returns every indexed Audiobook. Order is unspecified.
func (idx *Index) Audiobooks() []*catalogv1.Audiobook {
	out := make([]*catalogv1.Audiobook, 0, len(idx.audiobooks))
	for _, a := range idx.audiobooks {
		out = append(out, a)
	}
	return out
}

// AlbumsOf returns the Albums whose spec.artistRef names the Artist with
// the given UID, by release date then title (the music root's
// artist->albums children). The slice is a copy, as [Index.Episodes]' is.
func (idx *Index) AlbumsOf(artistUID types.UID) []*catalogv1.Album {
	return append([]*catalogv1.Album(nil), idx.albumsByArtist[artistUID]...)
}

// ArtistOfAlbum returns the Artist an Album's spec.artistRef names,
// [Index.AlbumsOf]'s reverse.
func (idx *Index) ArtistOfAlbum(albumUID types.UID) (*catalogv1.Artist, bool) {
	owner, ok := idx.albumArtist[albumUID]
	if !ok {
		return nil, false
	}
	a, ok := idx.artists[owner]
	return a, ok
}

// AudiobooksOf returns the Audiobooks whose spec.authorRef names the Author
// with the given UID, by release date then title. The slice is a copy.
func (idx *Index) AudiobooksOf(authorUID types.UID) []*catalogv1.Audiobook {
	return append([]*catalogv1.Audiobook(nil), idx.audiobooksByAuthor[authorUID]...)
}

// AuthorOfAudiobook returns the Author an Audiobook's spec.authorRef names,
// false for an Audiobook without one (see [Index.UnlinkedAudiobooks]).
func (idx *Index) AuthorOfAudiobook(audiobookUID types.UID) (*catalogv1.Author, bool) {
	owner, ok := idx.audiobookAuthor[audiobookUID]
	if !ok {
		return nil, false
	}
	a, ok := idx.authors[owner]
	return a, ok
}

// UnlinkedAudiobooks returns the Audiobooks with no resolvable
// spec.authorRef, bucketed by their first credited author's name
// ([FirstAuthorName]; "" for one crediting nobody), each bucket by release
// date then title: the audiobooks root's synthetic artists (design §7.2).
// The map and its slices are copies.
func (idx *Index) UnlinkedAudiobooks() map[string][]*catalogv1.Audiobook {
	out := make(map[string][]*catalogv1.Audiobook, len(idx.unlinkedAudiobooks))
	for name, abs := range idx.unlinkedAudiobooks {
		out[name] = append([]*catalogv1.Audiobook(nil), abs...)
	}
	return out
}

// ByMBID resolves a match request's "mbid://<id>" guid (design §7.3): an
// Artist by spec.musicBrainzID for kind artist, an Album by
// spec.releaseGroupID for kind album. A recording id names no object of
// its own (a track is a slice of its Album's status.tracks), so no other
// kind resolves.
func (idx *Index) ByMBID(kind commonv1.MediaKind, id string) (client.Object, bool) {
	switch kind {
	case commonv1.MediaKindArtist:
		a, ok := idx.mbArtists[id]
		return a, ok
	case commonv1.MediaKindAlbum:
		a, ok := idx.mbAlbums[id]
		return a, ok
	default:
		return nil, false
	}
}

// ByASIN resolves a match request's "asin://<ASIN>" guid against Audiobook
// spec.asin (design §7.3).
func (idx *Index) ByASIN(asin string) (*catalogv1.Audiobook, bool) {
	a, ok := idx.asins[asin]
	return a, ok
}

// ArtistByName returns the Artist an Album's spec.artistRef names.
func (idx *Index) ArtistByName(namespace, name string) (*catalogv1.Artist, bool) {
	a, ok := idx.artistByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return a, ok
}

// AlbumByName returns the Album a MediaFile's spec.mediaRef names.
func (idx *Index) AlbumByName(namespace, name string) (*catalogv1.Album, bool) {
	a, ok := idx.albumByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return a, ok
}

// AuthorByName returns the Author an Audiobook's spec.authorRef names.
func (idx *Index) AuthorByName(namespace, name string) (*catalogv1.Author, bool) {
	a, ok := idx.authorByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return a, ok
}

// AudiobookByName returns the Audiobook a MediaFile's spec.mediaRef names.
func (idx *Index) AudiobookByName(namespace, name string) (*catalogv1.Audiobook, bool) {
	a, ok := idx.audiobookByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return a, ok
}

// MediaFileByName returns the album or audiobook MediaFile an Album's
// status.tracks[].fileRef or an Audiobook's status.fileRefs names, under
// [IndexOptions.Music] only.
func (idx *Index) MediaFileByName(namespace, name string) (*catalogv1.MediaFile, bool) {
	f, ok := idx.mediaFileByName[types.NamespacedName{Namespace: namespace, Name: name}]
	return f, ok
}

// DownloadRow is one grab entry on the Downloads page (ADR-0019 §6.12): the
// owner whose status.downloads holds it, the entry, and for a Series or
// Comic entry the names of the episodes or issues it covers.
type DownloadRow struct {
	Owner     schema.ItemRef
	OwnerKind commonv1.MediaKind
	Entry     catalogv1.DownloadEntry
	Covered   []string
}

// entryIndex reads the grab entries off a round's items: each owner's own
// (one DownloadRow per entry), and an Episode's or Issue's view of its
// Series' or Comic's entries covering it.
type entryIndex struct {
	series   map[types.NamespacedName]*catalogv1.Series
	comics   map[types.NamespacedName]*catalogv1.Comic
	episodes map[types.NamespacedName]map[episodeNo]string // series -> number -> episode name
	issues   map[types.NamespacedName]map[string]string    // comic -> number -> issue name
}

type episodeNo struct{ season, number int32 }

// buildEntryIndex indexes items' Series, Comics and their children.
func buildEntryIndex(items []client.Object) *entryIndex {
	x := &entryIndex{
		series:   map[types.NamespacedName]*catalogv1.Series{},
		comics:   map[types.NamespacedName]*catalogv1.Comic{},
		episodes: map[types.NamespacedName]map[episodeNo]string{},
		issues:   map[types.NamespacedName]map[string]string{},
	}
	for _, it := range items {
		switch t := it.(type) {
		case *catalogv1.Series:
			x.series[types.NamespacedName{Namespace: t.Namespace, Name: t.Name}] = t
		case *catalogv1.Comic:
			x.comics[types.NamespacedName{Namespace: t.Namespace, Name: t.Name}] = t
		case *catalogv1.Episode:
			k := types.NamespacedName{Namespace: t.Namespace, Name: t.Spec.SeriesRef}
			if x.episodes[k] == nil {
				x.episodes[k] = map[episodeNo]string{}
			}
			x.episodes[k][episodeNo{t.Spec.SeasonNumber, t.Spec.EpisodeNumber}] = t.Name
		case *catalogv1.Issue:
			k := types.NamespacedName{Namespace: t.Namespace, Name: t.Spec.ComicRef}
			if x.issues[k] == nil {
				x.issues[k] = map[string]string{}
			}
			x.issues[k][t.Spec.Number] = t.Name
		}
	}
	return x
}

// entriesFor is the grab entries covering item: an owner's own, an
// Episode's Series' entries naming its number, an Issue's Comic's naming
// its number.
func (x *entryIndex) entriesFor(item client.Object) []catalogv1.DownloadEntry {
	switch t := item.(type) {
	case *catalogv1.Episode:
		s := x.series[types.NamespacedName{Namespace: t.Namespace, Name: t.Spec.SeriesRef}]
		if s == nil {
			return nil
		}
		var out []catalogv1.DownloadEntry
		for i := range s.Status.Downloads {
			for _, n := range s.Status.Downloads[i].Episodes {
				if n.Season == t.Spec.SeasonNumber && n.Number == t.Spec.EpisodeNumber {
					out = append(out, s.Status.Downloads[i])
					break
				}
			}
		}
		return out
	case *catalogv1.Issue:
		c := x.comics[types.NamespacedName{Namespace: t.Namespace, Name: t.Spec.ComicRef}]
		if c == nil {
			return nil
		}
		var out []catalogv1.DownloadEntry
		for i := range c.Status.Downloads {
			if slices.Contains(c.Status.Downloads[i].Issues, t.Spec.Number) {
				out = append(out, c.Status.Downloads[i])
			}
		}
		return out
	}
	entries, _ := ownerEntries(item)
	return entries
}

// ownerEntries is an owner kind's status.downloads and its MediaKind.
func ownerEntries(o client.Object) ([]catalogv1.DownloadEntry, commonv1.MediaKind) {
	switch t := o.(type) {
	case *catalogv1.Movie:
		return t.Status.Downloads, commonv1.MediaKindMovie
	case *catalogv1.Series:
		return t.Status.Downloads, commonv1.MediaKindSeries
	case *catalogv1.Album:
		return t.Status.Downloads, commonv1.MediaKindAlbum
	case *catalogv1.Book:
		return t.Status.Downloads, commonv1.MediaKindBook
	case *catalogv1.Audiobook:
		return t.Status.Downloads, commonv1.MediaKindAudiobook
	case *catalogv1.Comic:
		return t.Status.Downloads, commonv1.MediaKindComic
	}
	return nil, ""
}

// rows is one DownloadRow per entry of every owner in items, sorted by
// entry id for a stable render across frames.
func (x *entryIndex) rows(items []client.Object) []DownloadRow {
	var out []DownloadRow
	for _, it := range items {
		entries, kind := ownerEntries(it)
		if kind == "" {
			continue
		}
		owner := schema.ItemRef{Kind: objectKind(kind), Ref: schema.Ref{
			Namespace: it.GetNamespace(), Name: it.GetName(), UID: string(it.GetUID()),
		}}
		key := types.NamespacedName{Namespace: it.GetNamespace(), Name: it.GetName()}
		for i := range entries {
			e := entries[i]
			r := DownloadRow{Owner: owner, OwnerKind: kind, Entry: e}
			for _, n := range e.Episodes {
				if name, ok := x.episodes[key][episodeNo{n.Season, n.Number}]; ok {
					r.Covered = append(r.Covered, name)
				}
			}
			for _, n := range e.Issues {
				if name, ok := x.issues[key][n]; ok {
					r.Covered = append(r.Covered, name)
				}
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Entry.ID != out[j].Entry.ID {
			return out[i].Entry.ID < out[j].Entry.ID
		}
		return out[i].Entry.UID < out[j].Entry.UID
	})
	return out
}

// objectKind is a MediaKind as its object Kind ("audiobook" -> "Audiobook").
func objectKind(k commonv1.MediaKind) string {
	s := string(k)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ListDownloadRows lists the six owner kinds, and the Episodes and Issues
// that name what their entries cover, through r and builds the Downloads
// page's rows: GET /downloads' read when no projection is wired.
func ListDownloadRows(ctx context.Context, r client.Reader) ([]DownloadRow, error) {
	if r == nil {
		return nil, nil
	}
	p := &Projection{reader: r}
	items, err := p.listItems(ctx)
	if err != nil {
		return nil, err
	}
	return buildEntryIndex(items).rows(items), nil
}
