# The Plex provider matches a file by its path before anything else

**Date:** 2026-09-30
**Status:** Proposed.

**Companion:** cluster-plex
`docs/superpowers/specs/2026-09-30-clustarr-library-integration-design.md`.
That spec provisions Plex libraries on clustarr's providers (ADR-0012)
with nothing set in the Plex UI, and rescans the folders clustarr changes.
Once every Movies and TV library item comes through `ui/plex`'s match, a
wrong match is a wrong library, so the match has to be exact.

## 1. Problem

For a match request, PMS sends `filename`: "the relative path to the base
folder configured for the library". For a show or a season it sends the first
episode's file (Plex staff, forums.plex.tv/t/934384, post #36).

- `ui/plex/match.go` decodes it (`matchRequest.Filename`) and never reads it.
- It matches by guid, then by title and year.

clustarr already knows exactly which item every file backs:
`MediaFile.spec.path` and `spec.mediaRef`. The title fallback guesses. It
cannot tell apart two films with one title and no year in the folder name,
and a guid Plex parsed from a folder tag is Plex's reading, not clustarr's
record. The scanner's rule applies here too: never guess when the answer is on
record.

## 2. Rule 0: the file

Rule 0 runs before the guid rule, for every match type.

1. **Clean `filename`.** Refuse it — skip to the next rule — if it is empty,
   absolute, or contains a `..` element after `path.Clean`.
2. **Find the MediaFiles** whose `spec.path` ends with `"/" + filename`, using
   a new index keyed by base name (§3). A suffix is used instead of joining
   onto a RootFolder because:
   - PMS sends the path relative to the *Plex* library folder;
   - that folder can be a RootFolder, its parent, or behind a different mount
     prefix (`/media` versus `/data/media`);
   - a suffix needs no path mapping and no knowledge of Plex's mounts.
3. **Keep only the MediaFiles of the request's kind:**
   - type 1: `mediaRef.kind == movie`;
   - types 2, 3 and 4: `mediaRef.kind == episode`.
4. **Resolve each to its item:**
   - A movie file gives its Movie.
   - An episode file gives the Episode named by `mediaRef.name`, or, for a
     multi-episode file, each Episode in `mediaRef.keys`. From an Episode:
     - **type 2** takes its Series (`Index.SeriesOfEpisode`);
     - **type 3** takes its Series and the request's season `index`;
     - **type 4** takes the Episode whose season and episode numbers equal the
       request's `parentIndex` and `index`. If none does, it takes the
       file's only Episode when there is exactly one.
5. **Decide:**
   - **Exactly one item** is the answer: the single result, whether
     `manual` is 0 or 1.
   - **More than one distinct item** is ambiguous, and so is none. Fall
     through to the existing guid and title rules unchanged. Ambiguity can
     come from two RootFolders holding the same relative path, or a suffix
     too short to be unique.

Consistent with the never-guess rule, a path that clustarr's records place
ambiguously is never resolved by preferring one side.

## 3. The index

`ui/projection.Index` gains `filesByBase map[string][]*catalogv1.MediaFile`.

- It holds only MediaFiles whose `mediaRef.kind` is `movie` or `episode`,
  keyed by `path.Base(spec.path)`.
- `BuildIndex` lists MediaFiles through the same reader and the same `opts`
  (`UnsafeDisableDeepCopy` from `NewIndexMemo`) it uses for Movies, Series and
  Episodes. The ui's cache already watches MediaFiles for the projection, so
  there is no new watch.
- It gains one accessor, `Index.FilesEndingWith(rel string)
  []*catalogv1.MediaFile`, which filters the bucket for `path.Base(rel)` by
  the `"/" + rel` suffix.

The cost is one map entry per video file (about 14,000 on the owner's
library), rebuilt at most once per `projection.IndexTTL`.

A MediaFile names its item by `mediaRef.name` in its own namespace. The
index's maps are keyed by UID, so it also needs
`movieByName`/`episodeByName` keyed by namespace and name. They are built in
the same loops that already fill `movies` and `episodes`.

## 4. What does not change

- Guid and title matching, their order, and `manual: 1`'s ranked list when
  rule 0 falls through.
- Rating keys, guids and every metadata route.
- `ui/plex` stays read-only. It reads the Index only, so `TestUINeverWrites`
  needs no exception.

## 5. Tests

`ui/plex/match_test.go`, table-driven, with paths produced by the real
naming presets (`pkg/naming`) rather than typed out, per the
fixture-shaped-like-the-answer gotcha:

- **A remake:** two Movies titled "Heat", no year in the request. Rule 0
  picks the one whose file it is, where the title rule would return both.
- **A mount prefix:** the MediaFile is `/data/media/movies/Heat (1995)/…` and
  PMS sends `Heat (1995)/…`, and also `movies/Heat (1995)/…`. Both resolve.
- **Type 2** from the first episode's file resolves the Series; **type 3**
  the Series plus the season; **type 4** the Episode.
- **A multi-episode file** (`mediaRef.keys` of two Episodes) resolves type 4
  by the request's episode number.
- **A path that two RootFolders share** falls through to the title rule.
- **Kind mismatch:** an episode file sent on a type 1 request falls through.
- `..`, an absolute path and an empty `filename` fall through.

**Falsification:** remove rule 0 and the remake case must fail by name.

## 6. Documentation

- Research note `docs/research/plex-metadata-provider.md` §10's open question
  about whether PMS sends `filename` is answered, citing the forum post.
- CLAUDE.md's UI section records that the provider matches by file first.
