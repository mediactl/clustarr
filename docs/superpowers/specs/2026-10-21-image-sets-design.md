# Image sets: MediUX and ThePosterDB artwork for a series, its seasons and episodes

Status: built 2026-10-21, from the owner's request the same day ("replace
images using theposterdb and mediux; start with Cyberpunk: Edgerunners").

## Problem

A Movie's or Series' `spec.artwork` already replaces its own poster or
background with any URL: the metadata agent fetches it into the artwork
store and the Plex provider serves it (M7, ADR-0011). A season's poster and
an episode's still have no such override: they come from TheTVDB through
`status.metadata.seasonImages` and `Episode.status.images`, served through
the ui's signed `/art/search` proxy, which fetches from the four providers'
image hosts alone.

MediUX (mediux.pro) and ThePosterDB publish curated sets per show -- a
poster, a background, one poster per season and one thumbnail per episode --
and export them in Kometa's image-files YAML, keyed by the show's TheTVDB id
(a movie's TMDB id):

```yaml
384541:
  url_poster: https://api.mediux.pro/assets/<uuid>
  url_background: https://api.mediux.pro/assets/<uuid>
  seasons:
    1:
      url_poster: https://api.mediux.pro/assets/<uuid>
      episodes:
        1:
          url_poster: https://api.mediux.pro/assets/<uuid>
```

Both hosts serve their assets anonymously (verified 2026-10-21:
`api.mediux.pro/assets/<uuid>` answers `image/webp`,
`theposterdb.com/api/assets/<id>` `image/jpeg`).

## Design

### The spec

`Series.spec.seasonArtwork[]` is the per-season and per-episode counterpart
of `spec.artwork`: `{season, episode?, type, url}`, at most 1000 entries
(JoJo's 190 episodes and 6 seasons in two orders fit). `type` is `poster`
or `fanart` for a season and `screenshot` for an episode. The series' own
poster and background stay `spec.artwork`, where they already work.

The lists are the owner's (ui/actions patches them as `clustarr-ui`;
`kubectl` can too). They are not fetched into the artwork store: the Plex
provider serves them through the signed proxy, as it serves TheTVDB's
season posters and stills today, so no new store keys or writers are
needed. The proxy's allowlist gains `api.mediux.pro` and
`theposterdb.com`; an override on another host is not served until its host
is added, which the Series page says.

### The Plex provider

A season answers its `thumb` from `spec.seasonArtwork` first (its poster of
the series' own order; the list carries no order, since a set names
seasons by number), else TheTVDB's; its `art` from a `fanart` override, else
the series'. An episode's `thumb` is its `screenshot` override, else its
TheTVDB still. The series' own poster and background are `spec.artwork`'s,
as before.

### Importing a set

`pkg/imageset` parses Kometa's image-files YAML (`url_poster`,
`url_background`, `tpdb_poster`, `tpdb_background` -- a ThePosterDB asset
id, `https://theposterdb.com/api/assets/<id>` -- `seasons{n}` and
`episodes{n}`, keys that are numbers or strings) into `imageset.Entry` per
id: the item's overrides and its season/episode overrides.

The series page's and the movie page's toolbar gain **Image Set**, a dialog
with one textarea: paste a set's YAML, Apply. `handleImageSet` picks the
entry for the item's own provider id (TheTVDB for a series, TMDB for a
movie), or the only entry when the YAML holds one, and
`ui/actions.SetArtwork` patches `spec.artwork` and `spec.seasonArtwork` in
one merge patch -- the set's entries for the types it names, the item's
existing overrides of other types kept. An empty set clears nothing: there
is nothing to apply.

### Not in this cut

- MediUX's own API (a set's id instead of its YAML): the export is what
  the site gives and what Kometa users have.
- Fetching season and episode overrides into the artwork store.
- Plex's refresh: PMS fetches a season's or episode's images on its next
  refresh of the show, which cluster-plex triggers on a metadata change of
  the item, not on a spec change; the owner refreshes the show in Plex or
  waits for its next refresh.
