# Live TV: custom guides for every channel, built outside Plex

Date: 2026-11-10. Status: proposed (owner's decisions of 2026-11-10 recorded in
§4; the defaults the owner has not yet ruled on are marked DEFAULT).

Amends `2026-10-07-iptv-live-tv-design.md` (blocks, the XMLTV guide) and
`2026-11-03-livetv-plex-native-guide-design.md` (the Plex cloud lineup, which
stays supported and leaves talos). The cluster-plex half is
`docs/superpowers/specs/2026-11-10-xmltv-built-guides-design.md` in cluster-plex
once written; §5.8 here is its contract.

## 1. Intent

Owner, 2026-11-10: "let's go custom the whole way." Until then the owner
thought only Plex's own (Gracenote) guide could be loaded dynamically, so the
channels outside the DIRECTV Mobile lineup were dropped from Plex (74 of 322 on
talos). Now:

- every channel argon-tv lists "within reason" goes to Plex, split across DVR
  devices by category (sports, news, kids, ...), channels alphabetical within
  each, general categories split into subcategories;
- the guide we provide "*perfectly* follows the Plex guide standards and
  contains **all** images for every listing, channel, background, etc.";
- the work is front-loaded in a pod other than Plex;
- the Plex cloud lineup stays supported, but the DIRECTV mapping on talos is
  redone from scratch: no DIRECTV numbers, no pairing to a Plex DVR;
- the data is accurate ("we always want accurate data"), Gracenote's through
  Plex's guide service where a station exists, fetched quickly.

## 2. What was measured (2026-11-10)

### 2.1 Plex's guide service (epg.provider.plex.tv), the owner's token

- `GET /lineups?country=US&postalCode=<zip>` lists a market's lineups (satellite,
  cable, over-the-air); `country=usa` and `us` are 400. 30 lineups in 9 markets
  gave 4,014 distinct stations.
- `GET /grid/bulk?channelGridKey=<key>&startDate=&endDate=` takes ONE station (a
  comma list or repeated keys get a 400) but any range: 14 days in one answer
  (about 1 MB, 1.2 s), every image inline. A station's grid answers whatever
  lineup it was found in: WNET-DT2 (PBS Kids, New York over-the-air), Comet
  (Verizon Fios New York), ScreenPix (Xfinity Chicago) and NESN (Xfinity Boston)
  all answered two days whole though DIRECTV Mobile lists none of them.
- Per item (66,313 items of the rig's 248 channels, two weeks): a coverPoster on
  66,307, a background on 96%, a summary on 96%, an episode still (snapshot) on
  28,160, season/episode numbers on 46% of episodes, genres on 50%, a
  `plex://` guid, a content rating, a year. Every image is on
  `metadata-static.plex.tv` (public, `cache-control: public`). Station logos:
  2,996 of 4,014 on `provider-static.plex.tv` (public).
- The service limits a token to about 500 calls in 90 s; a full refresh is one
  call per station (none of the per-show `/library/metadata` calls PMS makes,
  4,439 of its 4,900 per load).

### 2.2 iptv-epg.org, as talos serves it

For the 74 off-Plex channels (9,950 programmes): a per-show image on 93% (1,794
images for 1,698 titles), a description on 94%, categories on 94%, credits on
3,185, previously-shown on 3,893, new on 607, live on 864, no content ratings,
and the episode title and numbers only as "S26 E32 — Justice for Nikki" in the
description's first line and an `<episode-num>` naming no system (fixed in
clustarr a2141d09: lifted into sub-title, xmltv_ns and onscreen). Its schedule IS
Gracenote's: 231 of 231 airings on four channels matched the grid's start and
show. Its programme elements are written alphabetically, title last.

### 2.3 PMS 1.43.5.11029's XMLTV reader (the rig, DVR 8, 48 test programmes)

| XMLTV | PMS |
|---|---|
| `episode-num` xmltv_ns `2.4.` / onscreen `S02E07` / dd_progid `EP…` / original-air-date | an episode under show and season (`tv.plex.xmltv://episode/<title>/3/5`); dd_progid EP: season = the current year; original-air-date: keyed by the date; onscreen `Ep. 9`: a movie |
| no episode number, whatever the categories | a movie; the sub-title is dropped |
| xmltv_ns `3..`, `..` | a movie; `.7.` → S1E8; `0.5.0/2` → S1E6 |
| the first `<icon>` | the SHOW's (or the movie's) `user_thumb_url`, whatever its size or order; never on the episode; never `user_art_url` |
| `<image type="poster|backdrop|still">`, further icons | ignored |
| `<sub-title>`, `<desc>` | the episode's title and summary (the movie's summary) |
| first `<rating>` | `content_rating` on the show or movie; `<star-rating>` ignored |
| `<category>` | each a genre tag; `tags_genre` = the first two alphabetically; Sports or News on an EPISODE files it in the Sports (3) or News (4) section; a movie stays in Movies |
| `<credits>` | actor and director → tag 6 ("stars", the first three in `tags_star`), writer → 5, producer → 7; presenter, guest, commentator, composer, editor ignored |
| `<country>` | tag 8 and `tags_country` |
| `<date>` | a movie's year (`20210315` keeps the year; oad = Jan 1); nothing on an episode |
| `original-air-date` (an `episode-num` system) | written only beside an episode's numbers: PMS keys the episode by the numbers and stores the date; alone it would key the episode by date and suppress the dd_progid |
| `<previously-shown>` | clears `at:premiere`; an EPISODE without it is flagged premiere; movies never are |
| `<new>`, `<premiere>`, `<live>`, keyword, language, orig-language, length, url, video, audio, subtitles, review, last-chance, a second `<title>`, `episode-num` systems thetvdb.com / themoviedb.org / imdb.com / plex / tms | ignored |
| channel `<display-name>` (first), `<icon>` (first) | callSign, `at:channelThumb`; channelTitle = "<number> <callSign>"; identifier = vcn = the device's GuideNumber |

Items repeat: the same title and xmltv_ns twice is one episode with two airings;
the same title with no id twice is one movie, the first description kept.

### 2.4 A built guide for an XMLTV DVR (the rig, DVR 8)

PMS serves the XMLTV DVR's guide live from PostgreSQL once cluster-plex's guide
writer holds it. Rows edited in place — an episode's `user_thumb_url` and
`user_art_url`, the show's `user_art_url`, and the guids of a show, season and
episode set to a real `plex://show/…`, `plex://season/…/7`,
`plex://episode/…` — came back at once through
`GET /tv.plex.providers.epg.xmltv:8/grid` as `thumb`, `art`, `grandparentArt`,
`guid` and `grandparentGuid`, and through `/metadata/<plex guid>`. Everything the
Plex-native guide carries, an XMLTV DVR's guide can carry when we write it.

### 2.5 argon-tv (talos, the playlist cached 2026-11-10 05:45Z)

53,141 live entries in 780 groups, no tvg-ids (channels key by name,
`m3u.NameKey`; the guide maps by name, `xmltv.Index.AutoMap`). The `USA:` groups
are 66 with 8,784 entries, of which:

| Group | Entries | What |
|---|---|---|
| USA: TV [1080p] (two groups) | 550 | the broad catalogue: A&E, AMC, Bravo … plus NY affiliates and 146 out-of-market locals ("ATLANTA, GA WSB ABC 2") |
| USA: ENTERTAINMENT | 215 | cable entertainment (what talos admits today) |
| USA: SPORTS NETWORK | 103 | national and regional sports networks |
| USA: NEWS NETWORK | 69 | national news, C-SPAN, some local news |
| USA: MOVIES NETWORK | 74 | premium and movie channels |
| USA: KIDS NETWORK | 32 | kids, 12 of them "[24/7 O-D] KIDS n" loops |
| USA: PBS NETWORK | 33 | PBS by market |
| USA: SPECTRUM NETWORK | 58 | Spectrum News by market |
| USA: BIBLICAL/RELIGIOUS | 10 | |
| USA: {NBC,FOX,CBS,ABC} NETWORK, CW/MY, TELEMUNDO | 1,086 | local affiliates, every market |
| USA: HULU/HBO MAX/PEACOCK/DISNEY+/PARAMOUNT+ NETWORK | 79 | "ORIGINALS n" loops |
| USA: TUBI, PLUTO TV, NETFLIX ON AIR, CINEMANIA | 618 | FAST and single-show loops |
| USA: {NFL,NBA,NHL,MLB,NCAAF,NCAAB,MLS,WNBA} PACKAGE | 532 | per-game event feeds ("NFL : 15 - MNF 8:15pm Bills at Vikings") |
| USA: * [PPV EVENTS], ESPN+/PLUS, FLO, PRIME, DAZN … | 5,300 | event placeholders, mostly idle |

Talos's filters admit ENTERTAINMENT, SPORTS, NEWS, MOVIES and KIDS NETWORK plus
hand picks (the New York network affiliates, WNET): 322 channels in the groups
Entertainment 145, Sports 73, Movies 52, News 25, Kids 14, Networks 7, Music 6.

## 3. Goals, non-goals, anti-goals

Goals:

- G1. Every admitted channel is on a Plex DVR with a complete guide: every
  programme has a poster, a background where one exists, an episode still
  where one exists, its episode title and numbers, a description, genres, a
  rating, and Plex's own identity when Gracenote knows it.
- G2. The channels are split across DVRs by category, alphabetical within each,
  numbered from a per-category base; a channel's category and number are
  predictable and overridable.
- G3. Plex does no guide work but the first fill of a new DVR: clustarr fetches
  and merges the sources and renders; cluster-plex builds the guide database in
  a Job and swaps it in.
- G4. The XMLTV clustarr renders is valid XMLTV that PMS's own reader loads
  correctly (the fallback, and the first fill), while carrying what PMS's reader
  ignores for the builder.
- G5. A lineup change costs Plex at most one channel-map save per changed DVR
  and no guide reload.

Non-goals:

- Recording-rule migration across the talos cutover (owner: redo from scratch).
- Plex's cloud lineup on talos (it stays as a feature; §4 D5).
- Playback changes (the relay, tuners, streams are untouched).
- A guide for the PPV placeholder groups (§4 D3).

Anti-goals:

- Any guide data that is not the source's: no guessed episode numbers, no
  invented images. A programme a source does not describe stays as described
  by the next source down, never embellished.
- PMS parsing a large XMLTV on a schedule (the 10-13 minute loads on the Pi).
- A second fetcher on the same Plex token: clustarr fetches grids; cluster-plex's
  cloud fetcher serves cloud DVRs only.

## 4. Decisions

- D1 (owner). Route B: cluster-plex builds each custom DVR's guide database from
  clustarr's XMLTV, as it builds cloud guides; PMS's own XMLTV load is the DVR's
  first fill only. Route A (PMS's reader) is the fallback when cluster-plex's
  builder is off (`plex.liveTV.xmltvGuide: pms`).
- D2 (owner). Scope: every channel within reason, by category, alphabetical;
  general categories split into subcategories.
  DEFAULT for "within reason": the linear groups of §2.5 (TV, ENTERTAINMENT,
  SPORTS NETWORK, NEWS NETWORK, MOVIES NETWORK, KIDS NETWORK, PBS NETWORK,
  SPECTRUM NETWORK, BIBLICAL/RELIGIOUS, and the owner's market's affiliates:
  Mobile/Pensacola plus the New York feeds already picked), HD over SD and
  East over West when both exist, the "[24/7 O-D]" and "ORIGINALS n" loops
  included under Entertainment / On Demand, adult groups excluded,
  non-English groups excluded, out-of-market affiliates and Spectrum's
  regional news excluded.
- D3 (DEFAULT; deferred — see As built). The sports packages (NFL, NBA, NHL, MLB, NCAA, MLS, WNBA:
  532 per-game feeds) are one category "Sports Events" with their own DVR,
  their guide the event names argon puts in the playlist (no Gracenote
  station) rendered as sports events; the PPV placeholder groups (5,300
  entries) are excluded.
- D4 (owner). The DIRECTV mapping is redone from scratch: no DIRECTV numbers,
  no channel on a Plex cloud DVR. `spec.plexGuide.enabled` goes false on talos;
  the hand picks in `spec.epg.plex.station` stay as station hints for guide
  data (D6), not as DVR pairings.
- D5. The Plex cloud lineup stays a supported block kind (nothing of
  `2026-11-03-…` is removed); talos just stops using it.
- D6 (owner: Gracenote "quick"). Guide data per channel: a Gracenote grid where
  a station is found in any configured lineup (the pairing of
  `2026-11-03-…` §5.3 run over a list of lineups, hand picks first), else
  iptv-epg.org, else the panel's guide, else a dummy. Fetched by clustarr, one
  call per station for 14 days, daily, paced; never by cluster-plex.
- D7 (DEFAULT). Taxonomy and bases, each a DVR: 1000 News, 2000 Sports,
  3000 Sports Events, 4000 Movies, 5000 Kids, 6000 Entertainment / Comedy,
  6500 Entertainment / Drama & Crime, 7000 Entertainment / Reality &
  Lifestyle, 7500 Entertainment / Science, Nature & History, 8000
  Entertainment / Music, 8500 Entertainment / Faith & Family, 9000
  Entertainment / On Demand loops, 9500 Local (the owner's market and the
  New York feeds), 10000 Premium (HBO, Showtime, Starz, Cinemax, MGM+,
  ScreenPix). A category over 400 channels is refused with a condition, not
  split (the owner names a subcategory instead).
- D8. Numbers within a category are alphabetical by the channel's name as
  shown (argon's "[1080p]" and "USA:" trimmings removed), from base+1 with no
  gaps, re-assigned when the category's membership changes. A channel's
  identity is its playlist key, never its number, so a renumbering is a
  channel-map save per affected DVR and nothing else (§5.5).
- D9. Lineups to match against: DEFAULT the owner's market's five (DIRECTV
  Mobile, DISH Mobile, Cox, AT&T U-verse, the local broadcast lineup) plus
  New York's DIRECTV, Fios and local broadcast, Chicago's Xfinity, Boston's
  Xfinity and Los Angeles' Spectrum; the rest of the 30 add nothing the
  matcher found.
- D10. One XMLTV profile (§5.4) serves three readers: PMS's own loader,
  clustarr's TV Guide page and cluster-plex's builder. No second format.
- D11. Plex's images are used as given: `metadata-static.plex.tv` and
  `provider-static.plex.tv` are public; the ui proxies them through
  `/art/search` (allowlist additions), never links them in the browser
  (ADR-0011).

## 5. Design

### 5.1 API (`clustarr.io/v1alpha1`)

IPTVProvider gains `spec.lineup`, the automatic lineup:

```yaml
spec:
  lineup:
    enabled: true                 # off: channels are added by hand as today
    categories:                   # in order; first match wins
      - name: News
        base: 1000
        groups: ["USA: NEWS NETWORK [1080p]"]        # playlist group-title, exact
        names: ["^CNN", "^MSNBC"]                    # regexps on the trimmed name, optional
        exclude: ["✶", "SPECTRUM NEWS"]              # substrings, optional
      - name: Entertainment / Comedy
        base: 6000
        groups: ["USA: ENTERTAINMENT [1080p]", "USA: TV   [1080p]"]
        names: ["COMEDY CENTRAL", "TBS", "TRUTV", "LAFF", "CATCHY COMEDY", ...]
      - name: Entertainment / Drama & Crime
        ...
    prefer: [hd, east]            # among duplicates of one trimmed name
    trim: ["\\s*\\[(1080p|720p|4K[^\\]]*)\\]", "^(USA|US):\\s*", "^TV:\\s*"]
    languages: [en]               # DEFAULT en: a group or name marking another language is left out
  guideSources:
    plex:
      credentialsRef: {name: plex-token, key: token}
      lineups: ["657c8183afd0f02cd353633f", "657cb97c00fec317e3e246e1", ...]   # §4 D9
      refresh: 24h
      rate: 2                     # calls per second, DEFAULT 2
    guideURL: https://iptv-epg.org/...    # as today
```

`spec.filters` keeps its meaning (what is admitted at all); `spec.lineup`
decides the category and number of every admitted entry. `spec.plexGuide` is
unchanged (D5). `spec.channelNumberStart` is ignored while `lineup.enabled`.

IPTVChannel:

- `spec.category` (optional): a person's override of the category rule; an
  empty string means "no category: off every DVR".
- `spec.epg.plex.station` keeps its meaning as a station hint (D4, D6).
- `status.category`, `status.number` as assigned (spec.number is written by the
  lineup controller under its own field manager, see §5.2; status says why:
  `status.lineup.rule`, the category rule that matched, `status.lineup.duplicateOf`
  for an entry left out as a duplicate).
- `status.guide` (exists) gains the source: `plex/<gridKey>`, `xmltv/<id>`,
  `panel/<id>`, `dummy/<minutes>`.

IPTVProvider status: `status.blocks[]` gains `category`; `status.lineup` gains
`categories[]` (name, base, channels, block device id, a `TooMany` condition
when over `dvr.maxChannels`); `status.guideSources.plex` (stations paired,
grids fetched, last fetch, the next hold after a 429).

### 5.2 The lineup controller

A new leader-only runnable in the manager beside the provider reconciler
(`app/livetv/controller/lineup`), woken by the tuner's playlist fetch and by a
provider spec change:

1. Takes the provider's admitted entries (`filter.Candidates`, as the tuner
   does), trims names (`spec.lineup.trim`), groups entries by trimmed name,
   and keeps one per name by `prefer` (HD over SD: the "[1080p]" > "[720p]"
   marker, then the name without "SD"; East over West: "WEST", "(W)", "PACIFIC"
   lose), recording the losers as duplicates.
2. Classifies each kept entry by the first category whose `groups` or `names`
   match and whose `exclude` does not; an entry matching no rule is
   unclassified and off every DVR (its IPTVChannel still exists, inactive,
   `status.lineup.rule: ""`, so the Channels page shows it and a person can
   give it a category).
3. Sorts each category by the trimmed name (`release.TitleNorm` order, then
   the name), numbers base+1.. (D8), and writes every channel's `spec.number`,
   `spec.name` (the trimmed name, when the person set none), `spec.active` and
   `status.category` under field manager `livetv-lineup`; a person's `spec.name`,
   `spec.logo`, `spec.epg`, `spec.language`, `spec.category` and `spec.epg.plex`
   are theirs (co-ownership by field, the SSA split of the catalog's
   `catalog-classify`).
4. Creates IPTVChannels for admitted entries that have none (named by
   `pkg/names` from the key, as `actions.AddChannel` does) and deactivates one
   whose entry left the playlist, deleting it after 7 days absent (the key is
   the playlist's name key, stable across token rotations).
5. Refuses a category over `dvr.maxChannels` (DEFAULT 400) with the condition
   and leaves its channels' numbers as they were.

A person's `spec.category` override moves a channel between categories; it is
renumbered with both. `spec.lineup.enabled: false` stops the controller;
nothing it wrote is undone.

### 5.3 Guide data

The tuner's render (`app/livetv/tuner/render.go`) gains a source step before
the build:

- **Stations.** The pairing of `2026-11-03-…` §5.3 (`plexguide.Pair`) runs over
  each configured lineup in order (hand pick, then the lineups), the first
  Manual or Automatic match wins; `status.plex` on the channel keeps recording
  it (`match`, `station`, and now `lineup`). The lineups are fetched and cached
  as today (`plex-lineup/<ns>/<name>/<lineup>` objects, 24 h).
- **Grids.** For each paired station, one `GET /grid/bulk` for today through
  today+13 (`pkg/iptv/plexgrid`, a client in clustarr with the recorded shapes
  of cluster-plex's `epgcloud` as its fixtures), paced at `guideSources.plex.rate`
  with a hold of max(Retry-After, 1 min) on a 429 recorded in the provider's
  status, refreshed when older than `refresh` or at a station change; the
  answer is reduced to clustarr's programme model and stored as
  `grid/<ns>/<name>/<gridKey>` (about 100 KB per station; 1,500 stations is
  150 MB in the `clustarr-livetv` store). A fetch never holds a render: a render
  takes what the store has.
- **Merge.** A channel's programmes come from its first source with any:
  the grid, else iptv-epg.org (mapped by name as today, with the a2141d09
  lifting), else the panel's guide, else the dummy. Within a channel sources
  are never mixed, so a schedule is one source's.
- **Art for the rest.** The existing `programmeArt` lookups (TMDB/TVDB through
  the metadata gateway) fill posters for programmes of the non-grid sources
  that carry none, as today.

The 14-day window and the daily refresh give `clustarr_livetv_grid_calls_total`
and `_holds_total`; a station's grid that fails three days running drops the
channel to its next source and records `status.guide`.

### 5.4 The XMLTV profile

One XMLTV per block (and the whole provider's, for the ui index), valid against
XMLTV's DTD, elements in DTD order (`dtdOrder` in `pkg/iptv/xmltv/enrich.go`;
the enricher now reorders every programme, not only the ones it changes).

Channel: `<display-name>` the channel's shown name; a second `<display-name>`
"<number> <name>"; `<icon>` the logo (`spec.logo`, else the station's
`provider-static` logo, else the guide's, else the playlist's); `<url>` none.

Programme (every element only when the source has it):

| Element | Content | Who reads it |
|---|---|---|
| `title` | the show's title (a movie's) | PMS, builder, ui |
| `sub-title` | the episode title | PMS, builder, ui |
| `desc` | the description | all |
| `credits` | actor (with `role`), director, writer, producer; others omitted | PMS (actor, director, writer, producer), builder |
| `date` | a movie's year, an episode's air year | PMS (movies), builder |
| `category` | the source's genres, then Plex's classifiers: `Movie` for a movie, `Series` for an episode, `Sports` / `Sports event` and the sport, `News`, `Children` (the enricher's rules of 2026-10-27/28) | PMS (sections), builder, ui |
| `icon` | the poster, first and only | PMS (the show's thumb), builder |
| `image type="poster"` / `"backdrop"` / `"still"` | the poster again, the background, the episode still (XMLTV 0.6 `<image>`; `size="3"`, `orient="P"`/`"L"`) | builder, ui |
| `episode-num system="xmltv_ns"` | season.episode. (zero-based) | PMS, builder, ui |
| `episode-num system="onscreen"` | `S03E05` | PMS, ui |
| `episode-num system="dd_progid"` | `EP…`/`SH…`/`MV…`/`SP…` as the enricher makes them today, for programmes with no numbers (so PMS files them right) | PMS, builder |
| `episode-num system="original-air-date"` | the episode's first air date, only beside `xmltv_ns` and `onscreen` (PMS keys by the numbers and stores the date; alone it keys by date and suppresses the dd_progid) | PMS (oad), builder |
| `episode-num system="plex"` | the programme's `plex://episode/…` or `plex://movie/…` | builder |
| `episode-num system="plex.show"`, `"plex.season"` | the show's and season's `plex://` guids | builder |
| `previously-shown` | on a repeat (Gracenote: not `premiere` and not the first airing of a new episode; iptv-epg.org: its own) | PMS (clears premiere), builder |
| `premiere`, `new`, `live` | as the source says | builder, ui |
| `rating system="VCHIP"` (first), then `"MPAA"` | the content rating | PMS (first), builder |
| `length units="minutes"` | the duration | builder, ui |

`Series` and `Movie` in `category` are genres PMS keeps (the builder's oracle against the rig's DVR 8 dump, 2026-11-10), not classifiers it drops.

The builder keys an item exactly as PMS does for a programme without a
`plex` guid (`tv.plex.xmltv://show/<title>`, `…/season/<title>/<n>`,
`…/episode/<title>/<s>/<e>`, `…/movie/<title>`, the dd_progid and
original-air-date forms of §2.3), and by the `plex` guids when given; so a
DVR built by cluster-plex and one loaded by PMS agree on every programme's
identity and differ only in what PMS's reader cannot carry.

`xmltv.Enricher` is where the profile lives: it takes a programme in the
unified model (not the source's raw XML, as today for iptv-epg.org) and writes
the elements above; `pkg/iptv/livetvstore.IndexGuide` reads the images and the
`plex` ids for the ui.

### 5.5 Blocks and devices

A block per category (D7): `pkg/iptv/blocks` gains a `ByCategory` packing in
which a block's identity is its category name, its numbers the category's, its
device id `BlockDeviceID(ns, name, "cat/"+category)` and its friendly name the
category ("Clustarr Sports"). Within a category the block never splits (D7
refuses instead). `status.blocks[].start` keeps its meaning (the base+1) so the
converger's lease and annotations work unchanged; `status.blocks[].category`
is new.

A renumbering within a category changes the device's lineup.json and so its
`lineupHash`: cluster-plex saves that DVR's channel map again (one PUT), which
PMS follows with a guide rebuild from its own XMLTV load — unless the guide is
built by cluster-plex (§5.8), when the rebuild is cancelled as a cloud block's
is today and the next build is asked for.

### 5.6 The tuner

- The `/hdhr/<id>/` device serves `xmltv.xml` from the block's guide object as
  today (the profile of §5.4), and `lineup.json` from the category's numbers.
- `/livetv/<ns>/<name>/xmltv.xml` (the whole guide) keeps serving the ui's index.
- The render runs when the playlist, a guide source, a lineup or a grid store
  changes, batched (the 2026-10-27 lesson: one render per pass, never one per
  art lookup).
- `guideHash` is the block's XMLTV hash; cluster-plex rebuilds on it with a
  30-minute floor per DVR (§5.8).

### 5.7 The ui

- Channels page: the category and number per channel, the rule that assigned
  them, duplicates left out (with the winner), the guide source, a category
  override control (`spec.category`), and a **dry run** when
  `spec.lineup.enabled` is false but `spec.lineup.categories` are set: the
  controller publishes the plan it would apply into `status.lineup` without
  writing channels, so the owner reviews the whole mapping before the cutover.
- The TV Guide page shows stills, backgrounds and the Plex identity (a link to
  the item's Plex page) from the index.
- M3U/EPG Manager: the guide sources' state (stations paired per lineup,
  grids fetched, holds).

### 5.8 cluster-plex: built XMLTV guides

Config `plex.liveTV.xmltvGuide: pms | manager` (DEFAULT `pms` until talos's
cutover; `manager` on talos from the cutover).

Under `manager`, for an XMLTV block (`Block.Lineup == ""`):

1. The converger registers the device and creates the DVR with the XMLTV URL
   as today; PMS's first load fills the schema and lets the channel map be
   matched (PMS pairs each GuideNumber with its XMLTV channel id itself); the
   map is saved.
2. The converger asks the guide builder for the block
   (`XMLTVGuides.Want(dvrUUID, xmltvURL, guideHash)`), and from then on treats
   PMS's own loads of that guide as it treats a cloud block's: the rebuild a
   map save starts is cancelled (`cancelLoadsUntil`), `reloadGuide` is never
   posted, and the swap hook answers "skip built guide" at PMS's rename, so
   PMS's own refresh never replaces a built guide.
3. The builder fetches the block's XMLTV from the tuner (in-cluster, the same
   URL PMS loads), parses the profile of §5.4 (`pkg/xmltvguide`, its own
   reader: cluster-plex imports nothing of clustarr), plans the rows with
   `guidebuild` through a new input (`InputXMLTV`: channels from the device's
   lineup.json, identifier = number, callSign = the first display-name,
   channelTitle "<number> <callSign>", channelThumb the channel icon; items
   keyed as §5.4 says; sections Movies / Shows / Sports / News as §2.3; the
   premiere flag from `previously-shown`; images from `icon` and `image`;
   genres, credits, rating, year, oad as PMS's own load records them), stages
   it in a Job (`guide-build`, as cloud guides since 601a90c) and swaps it in
   (`StageBuilt` / `SwapStaged`), keeping the live guide's playlists and views.
4. A build is due when `guideHash` moved (30-minute floor per DVR, since
   clustarr re-renders on every guide-source change) and daily otherwise.
5. The oracle: `hack/guidebuild` gains `--xmltv`: built from a block's XMLTV
   and compared with the schema PMS's own load of the same XMLTV produced
   (the rig's DVR 8 recipe), equal on every column PMS's reader fills, and
   listing the additions. Run on the rig before talos.

Two pods: the one-pod Live TV guard stays (the rig: plex-1 crash-looped on it,
2026-11-10); a built guide is the lease holder's, as cloud guides are.

### 5.9 What is measured before the cutover

- The owner's review of the dry-run mapping on the Channels page (§5.7).
- The Gracenote fetch for the whole lineup: stations paired, calls, holds, time
  (expected: about 1,000 calls, 10 minutes at 2/s).
- A PMS first load per DVR on the Pi (expected 2-10 minutes each; 14 DVRs
  loaded serially is about an hour, during which cluster-plex defers DVR
  changes), and the builder's time per DVR in a Job (about a minute).
- The oracle's exactness on every DVR's XMLTV (§5.8 item 5).
- Plex's rendering of one built DVR on the rig: posters, stills, backgrounds,
  Watchlist on a `plex://` item, the What's On hubs.

### 5.10 Rollout (talos)

1. Deploy clustarr with `spec.lineup` set and `enabled: false`: the dry run.
   The owner edits the categories until the mapping reads right.
2. Deploy cluster-plex with `xmltvGuide: manager`, inert while every block is
   the cloud one.
3. Flip: `spec.plexGuide.enabled: false` and `spec.lineup.enabled: true` in one
   edit. The provider renumbers every channel, publishes the category blocks,
   the cloud block goes; cluster-plex removes the DIRECTV device and DVR (Plex
   Live TV is empty for the first PMS load, 2-10 minutes), registers each
   category device, creates its DVR, waits for PMS's first load, saves the map,
   and asks for the build. Recording rules on the DIRECTV DVR are lost (owner).
4. Verify in Plex; the ui's TV Guide shows the same.

Terraform carries the provider's `spec.lineup` (cluster-bootstrap
`resources.yaml`); the categories are values there, so the owner edits them
in one place.

## 6. Left to the builder

- The exact trim and duplicate rules against argon's names (§2.5 has the
  shapes: "[1080p]", "[720p]", "[4K-Q]", "WEST", "(W)", "EAST", "(E)", "SD",
  "(S)", "(F)", "✶⋆✶ GROUP ✶⋆✶" headers).
- The grid-to-programme reduction: which of Gracenote's fields the model keeps
  (all of §2.1's, plus `Team` for sports).
- `plex.season` for a season Gracenote numbers but iptv-epg.org does not.
- Whether a category's guide rebuild after a renumbering can be avoided
  altogether (the airings are keyed by channel identifier, which is the
  number).

## 7. Open questions for the owner

1. D7's taxonomy and bases: edit the list.
2. D2's "within reason": the defaults leave out out-of-market affiliates,
   Spectrum's regional news, non-English groups and adult groups, and keep the
   on-demand loops under Entertainment / On Demand. Confirm or change.
3. D3: the sports packages as a "Sports Events" DVR (532 feeds, event names
   from the playlist, no Gracenote) — wanted, or excluded with the PPV groups?
4. D9: the lineups to match against.
5. The New York network feeds (1001-1004 today): keep them under Local beside
   the owner's market's affiliates?

## As built (2026-11-10)

Built on main, clustarr 4ddb81d4 onward; the plan is
`docs/superpowers/plans/2026-11-10-livetv-custom-guides.md`, the narrative is
CLAUDE.md's Live TV section ("The automatic lineup and the custom guides").
Decisions the plan made where this spec left them open:

- D3's Sports Events category is deferred: argon's package feeds rename per game and a channel's identity is its name key, so the category would churn IPTVChannels and renumber daily; it needs stream-id keys and event-name programmes (a follow-up design).
- `spec.lineup.prefer` and `spec.lineup.languages` are not built: the duplicate rule is fixed (HD over SD, group order, playlist order) and a language is left out with `exclude`.
- The lineup manager (`clustarr-livetv-lineup`) owns `spec.number`, `spec.active`, the `clustarr.io/origin: lineup` label and `status.lineup` of automatic channels, never `spec.name` or `spec.group` (the shown name is `spec.name`, else `status.lineup.name`; the guide group is `spec.group`, else the category).
- A category may be named by several rules; the first carries the base.
- The dry run writes `status.categories`, `status.lineup` on existing channels and the plan as `lineup-plan/<ns>/<name>`; no spec.
- A block per category uses no packer: `start` is base+1, device id `BlockDeviceID(ns, name, "cat/"+category)`.
- Grids are fetched by the tuner (it owns the lineup fetch and the store) under the fetch key `"grids"`.
- cluster-plex's planner takes a provider flavour; the XMLTV a Job builds from is cached under `xmltv/<dvr>/<hash>`; the swap hook treats any DVR the manager builds as its own.

Rulings taken during the build (what, why):

- 2026-11-10, CRD uniqueness rule rewritten as filter/all/size()==1: Kubernetes CEL has no `toSet()`; the kind dry run found it.
- Task 2: a News rule's excludes leave NEWS entries unmatched by design; an override skips the spec exclude; an undefined override category drops the entry unmatched.
- Task 5: lineup entries are computed on demand under the lock, keyed on the playlist fetch, because a cached ok=true was empty before the first render and stale after an Apply.
- Task 3: the plexgrid client keeps the error chain when it redacts, and unvalidated airing times (a missing end gave a 1970 stop) are Important: skipped or duration-derived.
- Task 4: the `plex://movie` guard accepted; the first render moves every XMLTV block's `guideHash` (one reload per XMLTV DVR, none on talos).
- Task 6: `status.lineup` name, group and category reach the tuner only while the lineup is enabled (a dry run renames nothing); blocks are held on an Error plan and on `LineupPending`; an invalid lineup reads `LineupApplied=False` `InvalidLineup` with `Valid` True (the provider still streams); a vanished plan does not re-pack every DVR; a friendly name over 160 is clamped (it refused every status apply).
- Task 7: `status.lineup` rule, name and duplicateOf are clamped to their MaxLengths (an oversized string would hold `LineupPending` for good); a `spec.name` equal to the playlist's raw name is no override; a dropped entry's `rule` is "" and its reason lives in the plan's Dropped; the plan is stored every pass; `LineupCategory.base` has Maximum=99000 (the number pattern overflowed at 99600).
- Task 8: the decoded-grid cache was dropped (it contradicts the 2026-11-03 no-programmes-between-renders rule): about 6 ms and 4 MB a station grid, 1.2 s and 800 MB of churn a render at 200 stations, to be measured on talos; the 429 hold is timed from the answer, not the pass's start; a render follows a no-op pass; a stale pass cannot commit over an Apply or Remove; `force` survives a held pass.
- Task 10: a cloud block takes only pairings of the cloud lineup (`plexguide.Prev.Lineup`, `status.plex.lineup`).
- Task 9: the in-row category select (22.6 KB a row, a component per row) became an on-demand picker dialog; under an enabled lineup the Active switch and bulk enable and disable post the override (Automatic or Off), since the lineup owns `spec.active`; a category may not be named `Automatic` or `Off` (CEL).
- Task 17: `splitBlocks` no longer drops a channel no category lists onto the category block below it.
- XMLTV: `original-air-date` only beside numbers; `Series` and `Movie` stay as genres PMS keeps (the cluster-plex oracle, not the brief's "classifiers dropped"); in cluster-plex a show carries its episodes' categories, credits, countries and rating, a movie's index is -1, media_item_count is the airings, show and season years stay empty.
- Process: no worktree (primary checkout, pathspec commits); a wave's tasks ran in parallel on disjoint files; envtests were written per task and run once in the final gate.
