# ADR-0023: An item Plex has no trailer for gets one from the page TMDB or TheTVDB names, fetched with yt-dlp by an optional agent in its own image

**Status:** Accepted, 2026-11-17 (owner: "tmdb and tvdb have trailers we can use - determine how
to provide the trailers to Plex", then "Yes, do all" to the downloader as a separate optional
image). Built the same day: `pkg/trailers`, `pkg/ytdlp`, `app/trailers/agent`,
`engine.MuxPair`, `rpc.catalog.metadata.trailers`, the provider's own-trailer clip
(`ui/plex/trailer.go`), the `trailers` image and the chart's `agents.trailers`.

## Context

Trailers reach Plex through the provider's extras route (`ui/plex/extras.go`), which answers
what Plex's own metadata service holds for the item's Plex id: Internet Video Archive clips.
Measured on talos that day, Plex's service has none for 203 of 1,273 movies and, at show
level, for 390 of 541 shows (most of them anime); asked directly, it answers an empty list for
*Lamb* (2021) and *Funny Games* (1997). TMDB and TheTVDB name a trailer for most of those,
but only by its page: TMDB's `/videos` gives a site and an id (YouTube's, nearly always),
TheTVDB's `trailers[]` a page address. Plex cannot play a page.

Three things were tested before anything was built:

1. **What PMS takes from a provider's extra** (PMS 1.43.5.11079, one obscure film, the
   test clips removed after). A clip whose part names a **file** (`Part.file`, a path on the
   volume PMS mounts) is stored as a local part: PMS read the file itself (its real
   duration, width and size, not the ones answered) and served it at
   `/library/parts/<id>/0/file.mp4`, HTTP 206. A part naming an outside `https` address was
   stored too, but a transcode of it failed (`TPU: Failed to download sub-stream`), and it
   would need an address every client can reach. A part naming a provider path answered 404.
2. **Whether the page can be fetched without ffmpeg.** yt-dlp's release build, alone in a
   read-only container as uid 1000, fetched a 1080p H.264 picture and an AAC sound as two
   DASH files; it joins them with ffmpeg, which no image holds.
3. **Whether the two can be joined in process.** `engine.MuxPair` (ffgo, every packet
   copied, the index first) made one MP4 of the real pair, and it decodes clean.

## Decision

1. **The trailer is a file on the media volume, answered to Plex by its path.** It lies
   under the item's root folder at `.clustarr/trailers/<kind>-<uid>.mp4`, a directory no
   library walk enters. The provider's extras route answers it as a clip whose part is that
   file where Plex mounts it (`--plex-path-map`, the chart's `ui.plex.pathMappings`), only
   for a movie or a show whose Plex extras hold no trailer, after them.
2. **A new, optional agent domain fetches it: `agent --domain=trailers`**
   (`app/trailers/agent`), one replica, off by default. It passes over the Movies and
   Series, and for one whose metadata has come and that is monitored or holds a file it:
   asks the gateway for Plex's extras of the item's Plex id and its seasons' (a trailer
   there ends it); asks the gateway which videos the trailer providers name
   (`rpc.catalog.metadata.trailers`: TMDB, then TheTVDB for a series) and picks one
   (`trailers.Pick`: a trailer before a teaser, the rights holder's, the language, the
   sharpest, the newest); fetches it, joins it, reads it, places it; and records the
   outcome in the `clustarr-trailers` bucket, which it alone writes. It writes no Kubernetes
   object (ADR-0019) and reads Movies, Series and RootFolders.
3. **yt-dlp is the one outside program clustarr runs, in the one image that holds it.**
   ADR-0017's rule stands for everything else. `pkg/ytdlp/exec.go` is the second file the
   exec guard allows (`TestNoProductionCodeStartsAProcess`), and only `app/trailers` may
   link the package (`TestOnlyTheTrailersAgentLinksYtDlp`). The program gets none of the
   agent's environment, a page clustarr composed from a site and an id that is nothing but
   an id, and a deadline. The image is `trailers`: the native image, yt-dlp's own release
   build for the architecture (pinned and checked by SHA-256) and the five Debian libraries
   it loads. It still holds no ffmpeg, ffprobe or par2.
4. **Nothing closes the loop to Plex but Plex's next refresh of the item.** Extras reach
   PMS only when it refreshes an item, and only cluster-plex asks for that, on a change of
   the item's resource. The agent therefore fetches as soon as an item's metadata has come
   -- for a monitored item before its file exists -- so the trailer is there when
   cluster-plex inserts the row and asks for its first refresh. An item already in Plex
   shows a trailer fetched later at its next refresh.

## Consequences

- YouTube changes what a client must do several times a year; an old yt-dlp then fetches
  nothing, and every fetch fails until `YTDLP_VERSION` is raised and the image rebuilt. A
  refusal of the client (a rate limit, a check for a person) pauses every fetch for an hour
  and counts against no video. yt-dlp warns that extraction without a JavaScript runtime is
  deprecated; the image holds none, and may need one.
- The `agent` binary links `pkg/ytdlp` in every image, since it is one binary; the program
  is in the trailers image alone, and the domain does not start without it.
- A trailer averaged 38 MB over the first thirteen on talos (up to 1080p, two to five
  minutes); the gap there is about 350 movies and 390 shows.
- As run on talos (rev 268, 2026-11-17): a trailer every 30 seconds or so. Of the first
  sixteen fetches two pages were held for an age and one was Vimeo's, which serves nothing
  to a client that is not signed in; an unavailable page now gives way to the item's next
  best video (`trailers.Rank`, three at most). The first pass ran while the metadata agent
  was rolling and warned once per item; ten undecided items in a row now end a pass.
- A status field on Movie and Series, written by the manager from the bucket, would let
  cluster-plex refresh an item the moment its trailer lands. It was not built: it is a CRD
  field and an item stage for a gap the early fetch mostly covers.
