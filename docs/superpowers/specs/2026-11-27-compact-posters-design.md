# Compact posters: bigger badges and title on a small poster

**Date:** 2026-11-27
**Status:** approved by the owner, 2026-11-28 ("Approve the design"). In build: the clustarr half by `docs/superpowers/plans/2026-11-28-compact-posters-clustarr.md`; the compact sizes are the owner's to settle on mock renderings (§5.5 step 1).
**Owner's ask:** "customize the scaled down posters to make the overlays like
ratings, dubs, and even the title text bigger to match plex's episode count
overlay size"; of the title, "it's actually a title image and is the
icon/title image overlaid onto a textless media poster"; "Comparing on all -
but would like to see same sizing on phone". Of how: "Option 2, bigger only
when small", then "Answer in the proxy inside the Plex pod".
**Repos:** clustarr (the renditions) and cluster-plex (serving them).

## 1. Problem

A poster is drawn once. The metadata agent puts the title image on a textless
poster, the catalog agent draws the rating badges and the `DUB` mark over it,
and every size is a share of the poster's width. Plex downloads that one
image (500 x 750) and shrinks it for each grid. Plex's own episode-count box
is drawn by the client at a fixed size, so the smaller the poster on screen,
the larger Plex's box is beside ours.

Measured on the owner's screenshot of the iOS app (2026-11-27; posters 125
points wide; Breaking Bad and Bob's Burgers):

| | Plex's count box | Our badge | Factor |
| --- | --- | --- | --- |
| Box width, of the poster's | 27-30% | 19% | 1.45 |
| Box height, of the poster's width | 27% | 16% | 1.7 |
| Digit height, of the poster's width | 7.2% | 4% | 1.8 |

## 2. Goals

- On a phone's grid the rating badge and the `DUB` mark are the size of
  Plex's episode-count box, and the title image is larger.
- A poster shown large (a detail page, a television) is the poster it is
  today, byte for byte.
- When anything of ours fails, Plex answers as it does today.

**Not goals.** Season, episode and collection posters. Art and logos.
clustarr's own web UI. A client that reaches Plex over a TLS connection the
in-pod proxy does not end (none is advertised on talos today). Matching
Plex's box on every client at once: phones first, the rest by the same rule
once their requests have been seen (§5.4).

**What would make it a failure.** Posters missing in Plex because clustarr or
the proxy is down or slow. Plex refreshing every item because regular posters
were rewritten. A poster served to someone Plex would not have served it to.

## 3. How it works today (what the design rests on)

clustarr:

- Objects in the `clustarr-artwork` bucket are named
  `<kind>/<uid>/<imageType>/<variant>`, with two variants
  (`pkg/events/subjects.go`): `original`, written only by the metadata agent
  (`app/catalog/metadata/artwork`), and `overlay`, written only by the catalog
  agent's renderer (`app/catalog/worker/artwork`). Invariant "Artwork objects
  have two writers split by variant".
- The titled poster (`title.go`, `TitleVersion` 1): the first candidate
  poster the text detector finds textless takes the item's logo, at most 78%
  of the width and 24% of the height. Only the result is stored; the textless
  canvas exists in memory during the fetch.
- The overlay (`pkg/overlay`, `app/catalog/overlayplan`): badges at the winning
  OverlayProfile's geometry (width 19%, score 27% of the box), then the `DUB`
  box. The stage decides from `status.overlay.renderedFrom` against
  `InputsDigest`; the renderer checks the object's `Clustarr-Rendered-From`.
- `GET /art/{kind}/{uid}/{type}` (`ui/art.go`) serves the overlay if there is
  one, else the original; `?v=<digest>` makes it immutable. It takes no size
  and asks for no credentials, and Plex's public hostname routes `/art` to it.
- Three places assume two variants: `app/catalog/artwork/reaper.go`
  (`countObjects`), `ui/artevents.go` (`parseArtName`), `ui/artindex.go`
  (`chooseArt`).

cluster-plex:

- Clients ask Plex for a sized poster with
  `GET /photo/:/transcode?width=W&height=H&url=/library/metadata/<id>/thumb/<ts>`.
- The manager's proxy holds port 32400 in the pod (`pkg/proxy`). It reads the
  head of every plain-HTTP request, forwards it with `Connection: close`, and
  offers a request whose line matches `divertable` to a `Divert`
  (`divert.go`), which answers it or declines; declined, the request goes to
  Plex as it came. Scans and the Live TV hub list are diverted today. Both
  hostnames reach the pod as plain HTTP through the Gateway, so every request
  is already examined and already on a connection of its own.
- Plex keeps the poster it chose for a row in `metadata_items.user_thumb_url`
  as `metadata://posters/<agent>_<sha1>`, and the SHA-1 is of the URL it
  downloaded. Checked on talos (BEEF, row 35059): the name is the SHA-1 of
  `https://plex.appkins.io:443/art/series/<uid>/poster?v=<overlay digest>`,
  the URL clustarr's provider handed it. So a row says by itself whether it
  shows clustarr's poster, and which version. A poster the user picked in
  Plex or an upload has another name.
- **A row can be stuck on its match result's thumb** (70 on talos on
  2026-11-28: 69 movies and one show). Its `user_thumb_url` is
  `https://images.plex.tv/photo?height=336&width=225&minSize=1&upscale=1&url=<the source poster>`,
  the wrapping ADR-0011's 2026-10-26 amendment records; all 70 also hold a
  downloaded poster (a `taggings` row of tag type 312) that Plex did not
  select. Measured on talos:
  - `GET /library/metadata/<id>/thumb/<ts>` answers `302` to that URL, so a
    client that asks for the poster itself fetches it from Plex's cloud;
  - `GET /photo/:/transcode?...&url=/library/metadata/<id>/thumb/<ts>` makes
    Plex fetch it: one connection from the pod to `images.plex.tv` per image
    its photo cache does not hold (seen in Hubble, 170 ms against 30 ms from
    the cache), then scaled up from 225 x 338 to whatever was asked;
  - `images.plex.tv` needs no token, answers `public, max-age=2592000` from
    Cloudflare, and serves only its own sizes: a request for 200 x 300 is
    redirected to 225 x 336, one for 450 x 672 or 500 x 750 to 400 x 600.
  So those rows show the source's poster, with no badge, at 225 pixels wide
  on every screen. This design declines them (their name is in no pairing
  list); putting them back on clustarr's poster is a repair of its own (§6),
  made on 2026-11-28.
- A movie's or show's guid is `plex://...` on talos (1,280 of 1,281 movies,
  all 549 shows), so the guid does not name clustarr's item.
- The manager holds Plex's database pool and `plex.clustarr.uiURL` (the UI's
  in-cluster address, which it already fetches collection images from, with
  no credentials). It has no image code: `golang.org/x/image` is a new
  dependency there.

## 4. Approaches

| | How | Trade-off |
| --- | --- | --- |
| **A. In-pod proxy (chosen)** | clustarr renders a compact rendition; the proxy answers a small-poster request with it and declines every other | Fails open by construction. Two repos, and a Plex restart to roll out |
| B. Gateway rule to the clustarr UI | `/photo/:/transcode` routed to the UI, which forwards the rest to Plex | One repo, but every Plex poster then depends on the one UI pod being up |
| C. Seed Plex's resize cache | Write compact files under the names Plex would give them | No hop, but rests on Plex's undocumented cache naming, one file per size per client, silent breakage on upgrade; unverified |
| Bigger everywhere | Raise the profile's geometry | No code, but large posters change too; the owner chose "bigger only when small" |

The owner chose A.

## 5. Design

### 5.1 Two more variants, same two writers

| Variant | Writer | Holds | Exists when |
| --- | --- | --- | --- |
| `compact` | metadata agent | the titled poster at the compact title geometry | the `original` is titled |
| `overlay-compact` | renderer | the compact base with badges and `DUB` at the compact geometry | the item has an `overlay` |

The compact base is `compact` when it exists, else `original`. A writer still
writes, clears and deletes only its own variants; the renderer reads the
gateway's as it reads `original` today. The invariant's text becomes "two
writers, each with a regular and a compact variant".

Both are 500 x 750 at most, JPEG, as their regular twins. Expect about the
overlays' present size again in the bucket (3.2 of 5 GiB used on 2026-11-27).

### 5.2 Drawing them without touching the regular poster

- **Title.** `fetchTitled` draws the compact canvas from the same textless
  candidate and logo in the same pass and puts it as `compact`, carrying a
  `clustarr.io/compact` mark: a `CompactTitleVersion` and the item's title
  mark. `PlanFetch` refetches when the original is titled and the compact
  object is missing or its mark is stale; `TitleVersion` is not raised. An
  original that stops being titled has its `compact` deleted.
- **Badges.** The render task draws `overlay-compact` beside `overlay`. Its
  inputs have a digest of their own (`CompactInputsDigest`: a compact render
  version, the compact base's digest, the compact template's hash, the same
  ratings and dub lines), recorded as that object's `Clustarr-Rendered-From`.
  `InputsDigest`, `RenderVersion` and `status.overlay` are unchanged, so no
  regular overlay is redrawn and Plex refreshes nothing. The stage reads the
  compact object's metadata from the object index it already reads, and asks
  for a render when either rendition is stale. No status field is added.
- **Geometry.** `OverlayProfile.spec.compactGeometry`, an optional
  `OverlayGeometry` read through accessors whose defaults are width 28 and
  score 31, the rest as the regular defaults. The compact title's constants
  live beside the regular ones in `title.go`: at most 90% of the width and
  32% of the height to start. A logo smaller than that box is scaled up to
  fill it, at most four times its own size (owner, 2026-11-28: "we should
  make the logo readable in both scales"); the regular poster still never
  scales a logo up, since doing so would change its bytes.
- **The look is settled on mock renderings first** (§5.5): these numbers are
  starting points from the measurements in §1. Owner, 2026-11-28: the sizes
  cannot be judged from a sheet; they are settled by comparing before and
  after on the clients, once `observe` has shown which requests each client
  makes where. The starting numbers stand until then, and a change after
  the first compact poster is stored raises `CompactTitleVersion`.

### 5.3 Serving one from clustarr

`GET /art/{kind}/{uid}/poster?size=compact` answers `overlay-compact`, else
`compact`, else 404, with the same ETag and `?v` rules. No resizing and no
other change to `/art`. The reaper and the UI's object index learn the two
names; the UI's own pages keep choosing between `overlay` and `original`.

**The pairing list.** The UI also answers, on a path outside `/art` (so
Plex's public hostname does not route it), the list of posters that have a
compact twin: for each movie and series, the poster URL exactly as the Plex
provider hands it to Plex (`artworkFor.url`: base, path, `?v=<digest>`,
`&nowebp=1` when it adds it) and the compact rendition's digest and `/art`
path. It carries an ETag. No title and no name is in it.

### 5.4 Answering Plex's request in the proxy (cluster-plex)

A new diverter, offered `GET /photo/:/transcode`. It declines unless every
one of these holds, and declining is always safe:

1. `url` names `/library/metadata/<id>/thumb/<ts>` (relative, or on the local
   Plex address) and the request carries only parameters the diverter
   understands (`width`, `height`, `minSize`, `upscale`, `url`, `X-Plex-*`).
2. The request is small by the configured rules (below).
3. The row shows the poster the compact one is the twin of: the SHA-1 in the
   row's `user_thumb_url` (one lookup by primary key) is the SHA-1 of a URL
   in the pairing list (5.3), which the manager fetches from
   `plex.clustarr.uiURL` and refreshes about once a minute with its ETag.
   This is the whole mapping: no guid is read. A poster the user picked, one
   Plex has not refreshed to yet, or an item with no compact twin has no
   entry and is declined, so Plex serves what it holds. An edition's row, and
   a season's row that fell back to its show's poster, carry the same name
   and get the same twin.
4. Plex would serve this client: Plex is asked for the item with the
   client's own token (from the header or the query), and the answer is kept
   per token and library for ten minutes.
5. The compact bytes are at hand: kept by digest in a bounded cache, fetched
   from `/art` on a miss.

Every step has a deadline and the whole answer a budget of a few hundred
milliseconds; running out declines. A phone's grid asks for dozens at once,
so steps 3 and 5 are memory lookups after the first and step 4 is one call
per client and library.

It then scales the image to the size asked for (never up, the poster's shape
kept; a request whose shape is not a poster's is declined), encodes a JPEG,
and answers with the status, content type and caching headers Plex gives for
the same request. Captured on talos on 2026-11-28 (PMS 1.43.5, through the
manager's port, token in a header): `200`, `X-Plex-Protocol: 1.0`,
`Content-Type: image/jpeg`, `Content-Length`, `Cache-Control:
max-age=259200`, `Date`, and no validator; the image covers the size asked
for with its shape kept (250 x 375 asked, 250 x 376 answered). The response
and its test are built from that transcript. What a phone's own request
carries (its headers, whether the token is in the query) is not on record
until `observe` runs. Scaling is cluster-plex's (`golang.org/x/image`), so
`/art` gains no resizing a stranger could call.

**What counts as small.** A phone asks for more pixels than its poster's
size on screen, and no log holds what clients ask for. So the setting is a
mode and a list of rules:

```yaml
plex:
  compactPosters:
    mode: observe          # off | observe | serve
    rules:
      - platforms: [iOS, Android]
        maxWidth: 450      # pixels asked for
```

The platform is `X-Plex-Platform`, read from the header and from the query
(clients send either). In `observe` the diverter declines everything and
counts each request by platform and width (a metric with a bounded width bucket, never a title or a
key), logging one sample line per distinct platform, product and size. The
first rules are written from that, phones first.

`observe` also says what each poster request would have met, as a second
label on the same counter: `paired` (the row shows clustarr's poster and a
compact twin exists), `unpaired` (clustarr's poster, no twin yet),
`cloud` (the row is stuck on an `images.plex.tv` thumb, §3), `other` (a pick
or an upload), `not-a-poster` (any other `url`, counted by its host class
only: local path, `images.plex.tv`, other public host). The `cloud` and
`images.plex.tv` counts are the watch the owner asked for on 2026-11-28
("watch for calls to the images.plex.tv endpoint from within the
cluster-plex pod"): together with Hubble's flows from the pod they say
which client action makes Plex call its cloud.

### 5.5 Order

1. **Mock renderings.** A throwaway program draws the compact rendition of
   the three posters in the owner's screenshot with the real renderer and the
   real title code, scaled to the phone's 125 points beside Plex's box. The
   owner adjusts the numbers on those. Nothing deployed.
2. **clustarr.** Variants, geometry field (CRD applied by hand on talos, then
   the shared roll), `/art?size=compact`. No client sees a difference.
   Step 3 does not wait for this one (owner, 2026-11-28: "Ship the observe
   mode after the sizing round"): `observe` needs nothing of clustarr's, and
   counts every downloaded poster as `unpaired` until the pairing list is
   served.
3. **cluster-plex in `observe`.** One Plex restart, at a quiet time and
   announced. It can carry the shim's `.62` pin in the same restart. Then a
   session with the owner on the iOS app (offered 2026-11-28): the library
   grid, a hub row, a detail page, a search and a season list, one at a
   time, while the counters and the pod's flows to `images.plex.tv` are
   read after each. It settles the sizes a phone asks for, where its token
   and platform travel, and which screens bypass the proxy.
4. **Rules, then `serve`.** A second restart (the manager reads its
   configuration at start). Checked on the owner's phone with a screenshot
   against §1's table.

## 6. Limits to know

- A client keeps a sized poster under its URL, whose `<ts>` changes only when
  Plex refreshes the item. A rating change redraws both renditions and
  refreshes the item. A change to the compact look alone does not, so phones
  keep the old compact image until their cache turns over: the reason the
  look is settled on mock renderings.
- A client on a TLS connection the proxy does not end gets the regular
  poster.
- A row stuck on its match result's thumb (§3) gets neither rendition, and
  no badge at any size, until it is back on clustarr's poster. Repaired
  beside this design (owner, 2026-11-28: "Yes, fix the 70 posters stuck on
  the cloud thumbnail"): the 70 on talos were given the poster each already
  held through PMS's own API that day (select, then the lock the select
  sets taken off), and cluster-plex's `plexproject.ReconcilePosters` does
  the same for a row that gets stuck later, in the pass that writes the
  ratings. `observe`'s `cloud` count is then the measure of how often new
  ones appear between passes.

## 7. Left to the builder

- Names inside packages, the metric's name and buckets, cache sizes and the
  deadline in 5.4 (5), kept within the manager's memory limit.
- The pairing list's path and shape, and how often the manager refreshes it.
- The exact Plex call in 5.4 (4): the cheapest one that enforces a shared
  user's library access.
- Whether a `DUB`-only or badge-less titled item needs any special case
  beyond 5.1's table.
- Tests as the repos require: renders through the real producers and
  falsified; a divert transcript per decline reason; the three two-variant
  assumptions in §3 each covered; kind end-to-end for the clustarr half.
