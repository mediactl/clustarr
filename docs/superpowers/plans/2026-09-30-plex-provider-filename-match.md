# Plex Provider Filename Match Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ui/plex` resolves a Plex match request by the file Plex names
(`filename`), through clustarr's own MediaFile records, before the guid
rule or the title rule.

**Architecture:** `ui/projection.Index` gains:

- a MediaFile bucket keyed by the file's base name;
- name lookups for Movies and Episodes.

A new `ui/plex/matchfile.go` holds rule 0: it resolves a filename to
exactly one item or answers nothing. `match()` in `ui/plex/match.go` asks
rule 0 first for every match type and falls through to the existing rules
unchanged.

**Tech Stack:** Go, controller-runtime fake client, testify, `pkg/naming`.

**Spec:** `docs/superpowers/specs/2026-09-30-plex-provider-filename-match-design.md`

## Global Constraints

- Put the GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go
  file.
- `ui/` never writes. `TestUINeverWrites` must pass unchanged, with no new
  exception.
- Rule 0 never guesses. More than one distinct item is no answer.
- Only MediaFiles with `spec.mediaRef.kind` of `movie` or `episode` are
  indexed.
- Build test paths with the real naming engine
  (`naming.NewEngine(naming.Config{Dialect: naming.DialectPlex})`), never
  as typed literals, except the explicitly adversarial ones (`..`, absolute,
  empty).
- Run tests with `make test`, or `go test ./ui/...` for the inner loop. The
  final gate is `make test` and `make lint`.
- Commit on `main` with a pathspec (`git commit -m … -- <paths>`). Never
  push.

## Review Focus

1. **The suffix must meet a `/`.** `filename` `"at (1995).mkv"` must not
   match `/…/Heat (1995).mkv`. Bucketing by base name already excludes it;
   Task 1 pins it with a test.
2. **Traversal.** `filename` `"../../etc/passwd"`, `"/abs/x.mkv"`, `""`
   and `"."` answer nothing from rule 0. Task 2 pins these.
3. **A multi-episode file**, where `mediaRef.name` plus `mediaRef.keys`
   name two Episodes, is not ambiguous for type 2 or type 3: one Series.
   Task 2 pins this.
4. **A MediaFile whose item is not in the Index** (deleted, or in another
   namespace) is skipped, not a panic. Task 2 pins this.
5. **Plex sends a movies-root-relative path under a parent folder**
   (`movies/Heat (1995) {tmdb-1}/…`). The suffix still resolves it. Task 2
   pins this.

---

### Task 1: Index MediaFiles by base name, and items by name

**Files:**
- Modify: `ui/projection/index.go`, adding to the `Index` struct (about
  line 256), `BuildIndex` (about line 287) and new accessors after
  `SeriesOfEpisode`.
- Test: `ui/projection/index_test.go`

**Interfaces:**
- Produces:
  - `func (idx *Index) FilesEndingWith(rel string) []*catalogv1.MediaFile`
  - `func (idx *Index) MovieByName(namespace, name string) (*catalogv1.Movie, bool)`
  - `func (idx *Index) EpisodeByName(namespace, name string) (*catalogv1.Episode, bool)`

- [ ] **Step 1: Write the failing test.** Append to
  `ui/projection/index_test.go`:

```go
// TestIndexFilesEndingWith is the lookup ui/plex's rule 0 reads: a path
// relative to some folder above the file, matched only at a "/" boundary,
// over movie and episode MediaFiles only.
func TestIndexFilesEndingWith(t *testing.T) {
	file := func(name string, kind commonv1.MediaKind, p string) *catalogv1.MediaFile {
		return &catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: kind, Name: "item-" + name},
				Path:     p,
			},
		}
	}
	heat := file("heat", commonv1.MediaKindMovie, "/data/media/movies/Heat (1995)/Heat (1995).mkv")
	other := file("other", commonv1.MediaKindMovie, "/data/media/other/Heat (1995)/Heat (1995).mkv")
	album := file("album", commonv1.MediaKindAlbum, "/data/media/music/Heat (1995)/Heat (1995).mkv")
	movie := &catalogv1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "item-heat", Namespace: "default", UID: "m1"}}
	episode := &catalogv1.Episode{ObjectMeta: metav1.ObjectMeta{Name: "ep", Namespace: "default", UID: "e1"}}

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(heat, other, album, movie, episode).Build()
	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)

	names := func(fs []*catalogv1.MediaFile) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		return out
	}
	require.ElementsMatch(t, []string{"heat"}, names(idx.FilesEndingWith("movies/Heat (1995)/Heat (1995).mkv")))
	require.ElementsMatch(t, []string{"heat", "other"}, names(idx.FilesEndingWith("Heat (1995)/Heat (1995).mkv")),
		"a relative path two folders share names both; the album never")
	require.Empty(t, idx.FilesEndingWith("at (1995).mkv"), "a suffix must start at a path boundary")
	require.Empty(t, idx.FilesEndingWith("nope.mkv"))

	m, ok := idx.MovieByName("default", "item-heat")
	require.True(t, ok)
	require.Equal(t, movie.UID, m.UID)
	_, ok = idx.MovieByName("elsewhere", "item-heat")
	require.False(t, ok, "names are per namespace")
	e, ok := idx.EpisodeByName("default", "ep")
	require.True(t, ok)
	require.Equal(t, episode.UID, e.UID)
}
```

- [ ] **Step 2: Run it and watch it fail.**
  Run `go test ./ui/projection/ -run TestIndexFilesEndingWith`.
  Expected: a compile error, `idx.FilesEndingWith undefined`.

- [ ] **Step 3: Implement.** In `ui/projection/index.go`:

  1. Add `"path"` to the imports (`"strings"` too, if it is not already
     imported).
  2. Add these fields to `Index`, after `episodeSeries`:

```go
	// movieByName and episodeByName key the same objects by namespace and
	// name: a MediaFile's spec.mediaRef names its item that way, not by
	// UID.
	movieByName   map[types.NamespacedName]*catalogv1.Movie
	episodeByName map[types.NamespacedName]*catalogv1.Episode

	// filesByBase buckets every movie and episode MediaFile by its file's
	// base name, for [Index.FilesEndingWith].
	filesByBase map[string][]*catalogv1.MediaFile
```

  3. Initialise all three in `BuildIndex`'s literal:

```go
		movieByName:      map[types.NamespacedName]*catalogv1.Movie{},
		episodeByName:    map[types.NamespacedName]*catalogv1.Episode{},
		filesByBase:      map[string][]*catalogv1.MediaFile{},
```

  4. In the movie loop, after `idx.movies[m.UID] = m`, add:

```go
		idx.movieByName[types.NamespacedName{Namespace: m.Namespace, Name: m.Name}] = m
```

  5. In the episode loop, after `idx.episodes[ep.UID] = ep`, add:

```go
		idx.episodeByName[types.NamespacedName{Namespace: ep.Namespace, Name: ep.Name}] = ep
```

  6. Before `return idx, nil`, add:

```go
	var files catalogv1.MediaFileList
	if err := r.List(ctx, &files, opts...); err != nil {
		return nil, fmt.Errorf("projection: list media files: %w", err)
	}
	for i := range files.Items {
		f := &files.Items[i]
		switch f.Spec.MediaRef.Kind {
		case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
		default:
			continue
		}
		if f.Spec.Path == "" {
			continue
		}
		base := path.Base(f.Spec.Path)
		idx.filesByBase[base] = append(idx.filesByBase[base], f)
	}
```

  7. Add the accessors after `SeriesOfEpisode`:

```go
// FilesEndingWith returns the movie and episode MediaFiles whose spec.path
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
```

  8. Extend `Index`'s doc comment's list of lookups with "and the movie
     and episode files by path suffix (rule 0 of the filename-match
     spec)".

- [ ] **Step 4: Run the tests and watch them pass.**
  Run `go test ./ui/projection/ ./ui/plex/`. Expected: PASS, with every
  existing test unchanged.

- [ ] **Step 5: Commit.**

```bash
git add ui/projection/index.go ui/projection/index_test.go
git commit -m 'feat(ui): the Plex index finds a movie or episode file by the path Plex names it by, and an item by the name its file refers to' -- ui/projection/index.go ui/projection/index_test.go
```

---

### Task 2: Rule 0, matching the file before the guid or the title

**Files:**
- Create: `ui/plex/matchfile.go`
- Modify: `ui/plex/match.go`, the `match` function (about lines 88-139)
- Test: `ui/plex/match_file_test.go` (new)

**Interfaces:**
- Consumes (from Task 1): `Index.FilesEndingWith`, `Index.MovieByName`
  and `Index.EpisodeByName`, plus the existing `Index.SeriesOfEpisode`.
- Produces (package-private):
  - `movieByFile(idx, filename) (*catalogv1.Movie, bool)`
  - `showByFile(idx, filename) (*catalogv1.Series, bool)`
  - `episodeByFile(idx, req) (*catalogv1.Series, *catalogv1.Episode, bool)`

- [ ] **Step 1: Write the failing tests.** Create
  `ui/plex/match_file_test.go`, with the GPL header, then:

```go
package plex_test

import (
	"net/http"
	"path"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// plexEngine renders paths the way importarr names files under a Plex
// RootFolder, so these fixtures are what a real library holds rather than
// strings shaped like the answer.
var plexEngine = naming.NewEngine(naming.Config{Dialect: naming.DialectPlex})

// movieRel is a movie's path relative to its RootFolder:
// "<folder>/<file>.mkv".
func movieRel(t *testing.T, m *catalogv1.Movie) string {
	t.Helper()
	c := naming.Context{
		Kind:   commonv1.MediaKindMovie,
		Title:  m.Status.Metadata.Title,
		Year:   int(m.Status.Metadata.Year),
		TmdbID: strconv.FormatInt(m.Spec.TmdbID, 10),
	}
	folder, err := plexEngine.MovieFolder(c)
	require.NoError(t, err)
	file, err := plexEngine.MovieFile(c)
	require.NoError(t, err)
	return path.Join(folder, file+".mkv")
}

// episodeRel is an episode file's path relative to its RootFolder:
// "<series>/<season>/<file>.mkv", covering every episode number in eps.
func episodeRel(t *testing.T, s *catalogv1.Series, season int32, eps ...int32) string {
	t.Helper()
	nums := make([]int, len(eps))
	for i, e := range eps {
		nums[i] = int(e)
	}
	c := naming.Context{
		Kind:        commonv1.MediaKindEpisode,
		SeriesTitle: s.Status.Metadata.Title,
		SeriesYear:  int(s.Status.Metadata.Year),
		Title:       s.Status.Metadata.Title,
		Year:        int(s.Status.Metadata.Year),
		TvdbID:      strconv.FormatInt(s.Spec.TvdbID, 10),
		Season:      int(season),
		Episodes:    nums,
	}
	seriesFolder, err := plexEngine.SeriesFolder(c)
	require.NoError(t, err)
	seasonFolder, err := plexEngine.SeasonFolder(c)
	require.NoError(t, err)
	file, err := plexEngine.EpisodeFile(c)
	require.NoError(t, err)
	return path.Join(seriesFolder, seasonFolder, file+".mkv")
}

func mediaFile(name string, kind commonv1.MediaKind, item string, p string, keys ...string) *catalogv1.MediaFile {
	return &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: kind, Name: item, Keys: keys},
			Path:     p,
		},
	}
}

func matchedRatingKeys(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(body, &resp))
	var out []string
	for _, m := range resp.MediaContainer.Metadata {
		out = append(out, m.RatingKey)
	}
	return out
}

// TestMatchMovieByFileBeatsTitle is the remake the title rule cannot tell
// apart: two "Skyfall Protocol"s and no year in the request. The file
// decides.
func TestMatchMovieByFileBeatsTitle(t *testing.T) {
	orig, remake := fixtureMovie(), fixtureMovieRemake()
	rel := movieRel(t, remake)
	h := newTestHandler(t, externalURLFixture, orig, remake,
		mediaFile("remake-file", commonv1.MediaKindMovie, remake.Name, "/data/media/movies/"+rel))

	for _, sent := range []string{rel, "movies/" + rel} {
		rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
			"type": 1, "title": "Skyfall Protocol", "filename": sent,
		})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []string{string(remakeMovieUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "filename %q", sent)
	}
}

// TestMatchByFileFallsThrough covers every way rule 0 has no answer: the
// title rule then runs exactly as before.
func TestMatchByFileFallsThrough(t *testing.T) {
	orig, remake := fixtureMovie(), fixtureMovieRemake()
	rel := movieRel(t, remake)
	series, episodes := fixtureSeriesAndEpisodes()
	objs := []client.Object{orig, remake, series,
		// The same relative path under two RootFolders, backing two movies.
		mediaFile("a", commonv1.MediaKindMovie, orig.Name, "/data/media/movies/"+rel),
		mediaFile("b", commonv1.MediaKindMovie, remake.Name, "/data/media/movies-4k/"+rel),
		// An episode file never answers a movie request.
		mediaFile("ep", commonv1.MediaKindEpisode, episodes[0].Name, "/data/media/tv/Show/Season 01/x.mkv"),
		// A file whose item is gone.
		mediaFile("orphan", commonv1.MediaKindMovie, "deleted-movie", "/data/media/movies/Gone (2001)/Gone (2001).mkv"),
	}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h := newTestHandler(t, externalURLFixture, objs...)

	for name, filename := range map[string]string{
		"shared by two roots": rel,
		"episode file":        "Show/Season 01/x.mkv",
		"orphaned file":       "Gone (2001)/Gone (2001).mkv",
		"traversal":           "../../" + rel,
		"absolute":            "/data/media/movies/" + rel,
		"empty":               "",
		"dot":                 ".",
	} {
		t.Run(name, func(t *testing.T) {
			rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
				"type": 1, "title": "Skyfall Protocol", "year": 2015, "filename": filename,
			})
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, []string{string(movieUID)}, matchedRatingKeys(t, rec.Body.Bytes()),
				"the title rule answers, as it did before rule 0")
		})
	}
}

// TestMatchShowSeasonEpisodeByFile: Plex sends the first episode's file
// for a show or a season; for an episode, its own. A two-episode file is
// one Series, and type 4 picks the episode by the request's numbers.
func TestMatchShowSeasonEpisodeByFile(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	// episodes are S01E01..S01E03, S02E01..S02E03 in order.
	single := episodeRel(t, series, 1, 1)
	double := episodeRel(t, series, 2, 1, 2)
	objs := []client.Object{series,
		mediaFile("s01e01", commonv1.MediaKindEpisode, episodes[0].Name, "/data/media/tv/"+single),
		mediaFile("s02e0102", commonv1.MediaKindEpisode, episodes[3].Name, "/data/media/tv/"+double, episodes[4].Name),
	}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h := newTestHandler(t, externalURLFixture, objs...)

	// A wrong title proves the file, not the title, decided.
	rec := postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 2, "title": "Not The Title", "filename": double,
	})
	require.Equal(t, []string{string(seriesUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "show")

	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 3, "parentTitle": "Not The Title", "index": 2, "filename": double,
	})
	require.Len(t, matchedRatingKeys(t, rec.Body.Bytes()), 1, "season")

	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 4, "grandparentTitle": "Not The Title", "parentIndex": 2, "index": 2, "filename": double,
	})
	require.Equal(t, []string{string(episodes[4].UID)}, matchedRatingKeys(t, rec.Body.Bytes()), "episode of a two-episode file")

	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 4, "grandparentTitle": "Not The Title", "filename": single,
	})
	require.Equal(t, []string{string(episodes[0].UID)}, matchedRatingKeys(t, rec.Body.Bytes()), "the file's only episode")
}
```

  Check the fixture constants before running. `movieUID`, `seriesUID` and
  `remakeMovieUID` are defined in `fixtures_test.go`; confirm with
  `grep -n 'UID *=' ui/plex/fixtures_test.go`. A season's `ratingKey` is
  `SeasonKey`'s synthesised form, so the season assertion checks only that
  there is one result.

- [ ] **Step 2: Run them and watch them fail.**
  Run `go test ./ui/plex/ -run 'ByFile'`.
  Expected:
  - `TestMatchMovieByFileBeatsTitle` FAILS: the title rule returns both
    movies, or the wrong one.
  - `TestMatchShowSeasonEpisodeByFile` FAILS: "Not The Title" matches
    nothing.
  - `TestMatchByFileFallsThrough` PASSES already. It is the regression
    guard for Step 3.

- [ ] **Step 3: Implement rule 0.** Create `ui/plex/matchfile.go`, with the
  GPL header, then:

```go
package plex

import (
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// Rule 0 of docs/superpowers/specs/2026-09-30-plex-provider-filename-match-design.md:
// PMS names the file it is matching, relative to its own library folder,
// and clustarr records which item every file backs (MediaFile
// spec.path, spec.mediaRef). A file that resolves to exactly one item is
// the answer; anything else is no answer, and the guid and title rules
// run as before. Never a guess between two items.

// cleanRelative vets a match request's filename: clean, relative, and not
// climbing out of the folder it is relative to.
func cleanRelative(filename string) (string, bool) {
	if filename == "" {
		return "", false
	}
	rel := path.Clean(filename)
	if rel == "." || rel == ".." || path.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// filesFor returns the MediaFiles of kind whose path ends with filename.
func filesFor(idx *projection.Index, filename string, kind commonv1.MediaKind) []*catalogv1.MediaFile {
	rel, ok := cleanRelative(filename)
	if !ok {
		return nil
	}
	var out []*catalogv1.MediaFile
	for _, f := range idx.FilesEndingWith(rel) {
		if f.Spec.MediaRef.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

// movieByFile is rule 0 for type 1.
func movieByFile(idx *projection.Index, filename string) (*catalogv1.Movie, bool) {
	var found *catalogv1.Movie
	for _, f := range filesFor(idx, filename, commonv1.MediaKindMovie) {
		m, ok := idx.MovieByName(f.Namespace, f.Spec.MediaRef.Name)
		if !ok {
			continue
		}
		if found != nil && found.UID != m.UID {
			return nil, false
		}
		found = m
	}
	return found, found != nil
}

// episodesByFile returns every distinct Episode the file backs: the one
// spec.mediaRef names and, for a multi-episode file, those its keys name.
func episodesByFile(idx *projection.Index, filename string) []*catalogv1.Episode {
	seen := map[types.UID]bool{}
	var out []*catalogv1.Episode
	for _, f := range filesFor(idx, filename, commonv1.MediaKindEpisode) {
		names := append([]string{f.Spec.MediaRef.Name}, f.Spec.MediaRef.Keys...)
		for _, n := range names {
			e, ok := idx.EpisodeByName(f.Namespace, n)
			if !ok || seen[e.UID] {
				continue
			}
			seen[e.UID] = true
			out = append(out, e)
		}
	}
	return out
}

// showByFile is rule 0 for types 2 and 3: the one Series every Episode of
// the file belongs to.
func showByFile(idx *projection.Index, filename string) (*catalogv1.Series, bool) {
	var found *catalogv1.Series
	for _, e := range episodesByFile(idx, filename) {
		s, ok := idx.SeriesOfEpisode(e.UID)
		if !ok {
			continue
		}
		if found != nil && found.UID != s.UID {
			return nil, false
		}
		found = s
	}
	return found, found != nil
}

// episodeByFile is rule 0 for type 4: the file's Episode numbered as the
// request asks, else the file's only Episode.
func episodeByFile(idx *projection.Index, req matchRequest) (*catalogv1.Series, *catalogv1.Episode, bool) {
	s, ok := showByFile(idx, req.Filename)
	if !ok {
		return nil, nil, false
	}
	eps := episodesByFile(idx, req.Filename)
	if req.Index != nil && req.ParentIndex != nil {
		for _, e := range eps {
			if e.Spec.SeasonNumber == *req.ParentIndex && e.Spec.EpisodeNumber == *req.Index {
				return s, e, true
			}
		}
	}
	if len(eps) == 1 {
		return s, eps[0], true
	}
	return nil, nil, false
}
```

- [ ] **Step 4: Wire rule 0 into `match`.** In `ui/plex/match.go`,
  replace the four cases of `switch req.Type` with the code below. It
  changes only rule 0; everything after each `if` is today's code:

```go
	case typeMovie:
		if m, ok := movieByFile(idx, req.Filename); ok {
			return []Metadata{buildMovieMetadata(root, h.opts.ExternalURL, m)}
		}
		movies := matchMovies(idx, req, manual)
		out := make([]Metadata, len(movies))
		for i, m := range movies {
			out[i] = buildMovieMetadata(root, h.opts.ExternalURL, m)
		}
		return out

	case typeShow:
		if s, ok := showByFile(idx, req.Filename); ok {
			return []Metadata{buildShowMetadata(root, h.opts.ExternalURL, s, idx, includeChildren)}
		}
		shows := matchShows(idx, req.Title, req.Year, req.Guid, manual, !manual)
		out := make([]Metadata, len(shows))
		for i, s := range shows {
			out[i] = buildShowMetadata(root, h.opts.ExternalURL, s, idx, includeChildren)
		}
		return out

	case typeSeason:
		s, ok := showByFile(idx, req.Filename)
		if !ok {
			s = resolveShow(idx, req.ParentTitle, req.Year, req.Guid)
		}
		if s == nil || req.Index == nil {
			return nil
		}
		md, ok := buildSeasonMetadata(root, h.opts.ExternalURL, s, *req.Index, idx, includeChildren)
		if !ok {
			return nil
		}
		return []Metadata{md}

	case typeEpisode:
		if s, e, ok := episodeByFile(idx, req); ok {
			return []Metadata{buildEpisodeMetadata(root, h.opts.ExternalURL, s, e)}
		}
		s := resolveShow(idx, req.GrandparentTitle, req.Year, req.Guid)
		if s == nil {
			return nil
		}
		e := resolveEpisode(idx.Episodes(s.UID), req)
		if e == nil {
			return nil
		}
		return []Metadata{buildEpisodeMetadata(root, h.opts.ExternalURL, s, e)}
```

  Extend `match`'s doc comment with one sentence: "Every type asks rule 0,
  the file (matchfile.go), before its guid and title rules."

- [ ] **Step 5: Run the tests and watch them pass.**
  Run `go test ./ui/...`. Expected: PASS, including `TestUINeverWrites`
  and every existing match golden.

- [ ] **Step 6: Falsify.** Temporarily replace the body of `movieByFile`
  with `return nil, false`. Run
  `go test ./ui/plex/ -run TestMatchMovieByFileBeatsTitle`. It must FAIL by
  name. Restore the body.

- [ ] **Step 7: Commit.**

```bash
git add ui/plex/matchfile.go ui/plex/match.go ui/plex/match_file_test.go
git commit -m 'feat(ui): the Plex provider matches the file Plex names before its guid or title -- a remake, or a show whose title Plex misread, resolves to the item clustarr recorded for that file, and a path two items share is still left to the title rule' -- ui/plex/matchfile.go ui/plex/match.go ui/plex/match_file_test.go
```

---

### Task 3: Documentation, and the full gate

**Files:**
- Modify: `docs/research/plex-metadata-provider.md`, §10 (about line 390),
  and the matching paragraph in §11
- Modify: `CLAUDE.md`, the UI section

- [ ] **Step 1: Answer the research note's open question.** In §10,
  replace the bullet "Whether PMS sends `filename` for movies in practice
  (docs: support not required, may be sent)." with:

```markdown
- ~~Whether PMS sends `filename`~~ -- answered: it does, "the relative path
  to the base folder configured for the library", and for a show or season
  the first episode's file (Plex staff, forums.plex.tv/t/934384 post #36).
  `ui/plex` matches by it first (rule 0,
  `docs/superpowers/specs/2026-09-30-plex-provider-filename-match-design.md`).
```

  In §11's "Matching against clustarr's spec/status." paragraph, add a
  first sentence:

```markdown
First by file: `filename` suffix against `MediaFile.spec.path`, answering
only when it names exactly one item.
```

- [ ] **Step 2: Record it in CLAUDE.md.** In the UI section, after the
  paragraph ending "…never straight to the browser (ADR-0011).", add:

```markdown
The Plex provider (`ui/plex`, ADR-0012) matches a request by the file Plex
names first (2026-09-30, rule 0 in `ui/plex/matchfile.go`): `filename`,
relative to Plex's library folder, is matched as a path suffix of
`MediaFile.spec.path` (`projection.Index.FilesEndingWith`) and answers only
when it resolves to exactly one item; a path two items share, an unknown
file or an unsafe name falls through to the guid and title rules, so the
provider never guesses between two items.
```

- [ ] **Step 3: Run the full gate.** Run `make test && make lint`.
  Expected: both green. `make test` needs `helm dependency build
  charts/clustarr` in a fresh worktree (see CLAUDE.md, "Gotchas").

- [ ] **Step 4: Commit.**

```bash
git add docs/research/plex-metadata-provider.md CLAUDE.md
git commit -m 'docs: the Plex provider matches by file first' -- docs/research/plex-metadata-provider.md CLAUDE.md
```
