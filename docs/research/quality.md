# Clustarr research note: Radarr/Sonarr quality model, TRaSH profiles, parsing, and inventory models for music/books/comics/audiobooks

Date: 2026-09-18. All facts below were read from primary sources checked out into the scratchpad
(`$S = /tmp/claude-1000/-home-appkins-src-mediactl-clustarr/55489e0d-8512-4ab3-bc91-253f47ae031c/scratchpad`):

- `$S/src/Radarr/src/NzbDrone.Core/{Qualities,Parser,CustomFormats,Profiles,Movies,DecisionEngine,Languages,MediaFiles,Indexers}` (Radarr `develop`, shallow clone)
- `$S/src/Sonarr/src/NzbDrone.Core/{Qualities,Parser,CustomFormats,Profiles,Tv,DecisionEngine,Languages}` (Sonarr `develop`)
- `$S/src/Lidarr/src/NzbDrone.Core/{Qualities,Parser,Profiles,Music}`; `$S/src/Readarr/src/NzbDrone.Core/{Qualities,Parser,Profiles,Books}`
- `$S/src/Guides/docs/json/{radarr,sonarr}/{cf,cf-groups,quality-profiles,quality-size,naming}` (TRaSH-Guides/Guides `master`: 242 Radarr CFs, 236 Sonarr CFs)
- TRaSH web pages (quality profiles, anime profiles, quality-size), DeepWiki answers for Kapowarr / Mylar3 / Audiobookshelf, anansi-project ComicInfo XSD.
- Go verification program: `$S/gomod/cfcheck/main.go` (compiles every TRaSH regex with `regexp2` and stdlib `regexp`; parses sample titles with `moistari/rls`).

Where I say "unverified" it is from memory and should be checked before relying on it.

---

## 1. Quality definitions

### 1.1 Radarr (`Qualities/Quality.cs`, `QualitySource.cs`, `Modifier.cs`, `Resolution.cs`)

A `Quality` is `(Id, Name, Source, Resolution, Modifier)`. Ordering/"weight" is NOT the Id: it comes from `DefaultQualityDefinitions` (`Weight`), and *inside a profile* ordering is the profile's item order (see 3.2). Groups (`GroupName`) are qualities that tie by default (WEBDL vs WEBRip of the same resolution).

```
enum QualitySource { UNKNOWN=0, CAM=1, TELESYNC=2, TELECINE=3, WORKPRINT=4, DVD=5, TV=6, WEBDL=7, WEBRIP=8, BLURAY=9 }
enum Modifier      { NONE=0, REGIONAL=1, SCREENER=2, RAWHD=3, BRDISK=4, REMUX=5 }
enum Resolution    { Unknown=0, R360p=360, R480p=480, R540p=540, R576p=576, R720p=720, R1080p=1080, R2160p=2160 }
```

| Weight | Id | Name | Source | Res | Modifier | Group | Default Min/Max/Pref MB/min |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 0 | Unknown | UNKNOWN | 0 | NONE | | 0 / 100 / 95 |
| 2 | 24 | WORKPRINT | WORKPRINT | 0 | NONE | | 0 / 100 / 95 |
| 3 | 25 | CAM | CAM | 0 | NONE | | 0 / 100 / 95 |
| 4 | 26 | TELESYNC | TELESYNC | 0 | NONE | | 0 / 100 / 95 |
| 5 | 27 | TELECINE | TELECINE | 0 | NONE | | 0 / 100 / 95 |
| 6 | 29 | REGIONAL | DVD | 480 | REGIONAL | | 0 / 100 / 95 |
| 7 | 28 | DVDSCR | DVD | 480 | SCREENER | | 0 / 100 / 95 |
| 8 | 1 | SDTV | TV | 480 | NONE | | 0 / 100 / 95 |
| 9 | 2 | DVD | DVD | 0 | NONE | | 0 / 100 / 95 |
| 10 | 23 | DVD-R | DVD | 480 | REMUX | | 0 / 100 / 95 |
| 11 | 8 / 12 | WEBDL-480p / WEBRip-480p | WEBDL / WEBRIP | 480 | NONE | WEB 480p | 0 / 100 / 95 |
| 12 | 20 | Bluray-480p | BLURAY | 480 | NONE | | 0 / 100 / 95 |
| 13 | 21 | Bluray-576p | BLURAY | 576 | NONE | | 0 / 100 / 95 |
| 14 | 4 | HDTV-720p | TV | 720 | NONE | | 0 / 100 / 95 |
| 15 | 5 / 14 | WEBDL-720p / WEBRip-720p | WEBDL / WEBRIP | 720 | NONE | WEB 720p | 0 / 100 / 95 |
| 16 | 6 | Bluray-720p | BLURAY | 720 | NONE | | 0 / 100 / 95 |
| 17 | 9 | HDTV-1080p | TV | 1080 | NONE | | 0 / 100 / 95 |
| 18 | 3 / 15 | WEBDL-1080p / WEBRip-1080p | WEBDL / WEBRIP | 1080 | NONE | WEB 1080p | 0 / 100 / 95 |
| 19 | 7 | Bluray-1080p | BLURAY | 1080 | NONE | | 0 / null / null |
| 20 | 30 | Remux-1080p | BLURAY | 1080 | REMUX | | 0 / null / null |
| 21 | 16 | HDTV-2160p | TV | 2160 | NONE | | 0 / null / null |
| 22 | 18 / 17 | WEBDL-2160p / WEBRip-2160p | WEBDL / WEBRIP | 2160 | NONE | WEB 2160p | 0 / null / null |
| 23 | 19 | Bluray-2160p | BLURAY | 2160 | NONE | | 0 / null / null |
| 24 | 31 | Remux-2160p | BLURAY | 2160 | REMUX | | 0 / null / null |
| 25 | 22 | BR-DISK | BLURAY | 1080 | BRDISK | | 0 / null / null |
| 26 | 10 | Raw-HD | TV | 1080 | RAWHD | | 0 / null / null |

`null` MaxSize means unlimited. `QualityDefinition` = `{Quality, Title, GroupName, Weight, MinSize?, MaxSize?, PreferredSize?}` (doubles, MB per minute).

### 1.2 Sonarr (`Qualities/Quality.cs`, `QualitySource.cs`)

Sonarr has no `Modifier`; instead raw/remux are extra *sources*:

```
enum QualitySource { Unknown=0, Television=1, TelevisionRaw=2, Web=3, WebRip=4, DVD=5, Bluray=6, BlurayRaw=7 }
```

| Weight | Id | Name | Source | Res | Group | Default Min/Max/Pref |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 0 | Unknown | Unknown | 0 | | 1 / 199.9 / 95 |
| 2 | 1 | SDTV | Television | 480 | | 2 / 100 / 95 |
| 3 | 12 / 8 | WEBRip-480p / WEBDL-480p | WebRip / Web | 480 | WEB 480p | 2 / 100 / 95 |
| 4 | 2 | DVD | DVD | 480 | | 2 / 100 / 95 |
| 5 | 13 | Bluray-480p | Bluray | 480 | | 2 / 100 / 95 |
| 6 | 22 | Bluray-576p | Bluray | 576 | | 2 / 100 / 95 |
| 7 | 4 | HDTV-720p | Television | 720 | | 3 / 125 / 95 |
| 8 | 9 | HDTV-1080p | Television | 1080 | | 4 / 125 / 95 |
| 9 | 10 | Raw-HD | TelevisionRaw | 1080 | | 4 / null / 95 |
| 10 | 14 / 5 | WEBRip-720p / WEBDL-720p | WebRip / Web | 720 | WEB 720p | 3 / 130 / 95 |
| 11 | 6 | Bluray-720p | Bluray | 720 | | 4 / 130 / 95 |
| 12 | 15 / 3 | WEBRip-1080p / WEBDL-1080p | WebRip / Web | 1080 | WEB 1080p | 4 / 130 / 95 |
| 13 | 7 | Bluray-1080p | Bluray | 1080 | | 4 / 155 / 95 |
| 14 | 20 | Bluray-1080p Remux | BlurayRaw | 1080 | | 35 / null / 95 |
| 15 | 16 | HDTV-2160p | Television | 2160 | | 35 / 199.9 / 95 |
| 16 | 17 / 18 | WEBRip-2160p / WEBDL-2160p | WebRip / Web | 2160 | WEB 2160p | 35 / null / 95 |
| 17 | 19 | Bluray-2160p | Bluray | 2160 | | 35 / null / 95 |
| 18 | 21 | Bluray-2160p Remux | BlurayRaw | 2160 | | 35 / null / 95 |

Important divergences for anyone unifying the two: the numeric Ids differ between apps (Bluray-480p is 20 in Radarr, 13 in Sonarr; Bluray-576p 21 vs 22; Remux-1080p 30 vs "Bluray-1080p Remux" 20). **Do not reuse *arr ids; key qualities by (source, resolution, modifier).** Sonarr places HDTV-1080p *below* WEB-720p, Radarr places HDTV-1080p above Bluray-720p; TRaSH profiles override this by explicit ordering anyway.

### 1.3 Other apps

- Lidarr `Quality` = `(Id, Name)` only, no source/resolution (see section 12).
- Readarr `Quality` = `(Id, Name)`; ebook formats and audiobook formats live in one enum (section 13).

---

## 2. Quality definition size limits (MB/min)

### 2.1 Semantics (`DecisionEngine/Specifications/AcceptableSizeSpecification.cs`)

- Radarr: `minBytes = MinSize(MB) * runtimeMinutes`, `maxBytes = MaxSize(MB) * runtimeMinutes`. If movie runtime is 0 it **substitutes 110 minutes** ("median movie runtime"). `MaxSize == null || 0` = unlimited. Reject reasons: `BelowMinimumSize`, `AboveMaximumSize`, `UnknownRuntime`.
- Sonarr: runtime = sum over all episodes in the release of `episode.Runtime` (fallback `series.Runtime`; if series runtime is 0 and all episodes aired within 24h of the first, fallback **45 minutes**). Season packs therefore scale by episode count. Same min/max formula.
- `PreferredSize` is *not* a filter: `DownloadDecisionComparer.CompareSize` sorts candidates by distance to `PreferredSize * runtime`; when PreferredSize is null it prefers the largest.
- Profiles can override per-quality sizes: `QualityProfileQualityItem{MinSize?, MaxSize?, PreferredSize?}` exists in Sonarr (and the TRaSH JSON has no per-profile sizes; sizes are global per quality definition).

### 2.2 TRaSH recommended tables (`docs/json/{radarr,sonarr}/quality-size/*.json`)

Radarr `movie.json` (trash_id aed34b9f60ee115dfa7918b742336277) min / preferred / max (MB/min). "2000" is the UI value for unlimited, 1999 preferred = "biggest":

| Quality | min | pref | max |
| --- | --- | --- | --- |
| HDTV-720p | 17.1 | 1999 | 2000 |
| WEBDL-720p, WEBRip-720p | 12.5 | 1999 | 2000 |
| Bluray-720p | 25.7 | 1999 | 2000 |
| HDTV-1080p | 33.8 | 1999 | 2000 |
| WEBDL-1080p, WEBRip-1080p | 12.5 | 1999 | 2000 |
| Bluray-1080p | 50.8 | 1999 | 2000 |
| Remux-1080p | 102 | 1999 | 2000 |
| HDTV-2160p | 85 | 1999 | 2000 |
| WEBDL-2160p, WEBRip-2160p | 34.5 | 1999 | 2000 |
| Bluray-2160p | 102 | 1999 | 2000 |
| Remux-2160p | 187.4 | 1999 | 2000 |

Sonarr `series.json` (bef99584217af744e404ed44a33af589), max cap is 1000 in Sonarr's UI:

| Quality | min | pref | max |
| --- | --- | --- | --- |
| HDTV-720p | 10 | 995 | 1000 |
| HDTV-1080p | 15 | 995 | 1000 |
| WEBRip-720p, WEBDL-720p | 10 | 995 | 1000 |
| Bluray-720p | 17.1 | 995 | 1000 |
| WEBRip-1080p, WEBDL-1080p | 15 | 995 | 1000 |
| Bluray-1080p | 50.4 | 995 | 1000 |
| Bluray-1080p Remux | 69.1 | 995 | 1000 |
| HDTV-2160p, WEBRip-2160p, WEBDL-2160p | 25 | 995 | 1000 |
| Bluray-2160p | 94.6 | 995 | 1000 |
| Bluray-2160p Remux | 187.4 | 995 | 1000 |

Anime tables (`anime.json`, both apps): **min 5 for every quality** (SDTV through Remux-2160p), preferred 1999/995, max 2000/1000 — i.e. effectively "only reject absurdly small files". Radarr also ships `sqp-streaming.json` / `sqp-uhd.json` for the SQP profiles (not needed).

---

## 3. Quality profiles

### 3.1 Model

Radarr `Profiles/Qualities/QualityProfile.cs`:

```
Name string
Cutoff int                       // quality Id OR group Id (groups have Id >= 1000 in UI)
Items []QualityProfileQualityItem  // stored WORST -> BEST (index 0 = lowest). UI shows reversed.
MinFormatScore int               // release rejected outright if its CF score < this
CutoffFormatScore int            // stop CF-driven upgrades once existing file score >= this ("Upgrade Until Custom Format Score")
MinUpgradeFormatScore int        // new score must be >= current + this ("Minimum Custom Format Score Increment"), default 1
FormatItems []ProfileFormatItem  // {Format CustomFormat, Score int}
Language Language                // Radarr only (-1 Any, -2 Original, or specific). Sonarr has no profile language; languages are CFs.
UpgradeAllowed bool
```

`QualityProfileQualityItem { Id int (group id, 0 for single), Name string (group name), Quality *Quality, Items []QualityProfileQualityItem (group members), Allowed bool, MinSize?, MaxSize?, PreferredSize? (Sonarr) }`.

`QualityProfile.CalculateCustomFormatScore(formats) = Σ FormatItems[f].Score for matched f`.

Defaults shipped: Radarr and Sonarr both create "Any", "SD", "HD-720p", "HD-1080p", "Ultra-HD", "HD - 720p/1080p" (e.g. Sonarr HD-1080p = cutoff HDTV-1080p, allowed HDTV-1080p, WEBRip-1080p, WEBDL-1080p, Bluray-1080p).

### 3.2 Ordering semantics (`QualityIndex`, `QualityModelComparer`)

`profile.GetIndex(quality)` returns `(Index, GroupIndex)`. `CompareTo(other, respectGroupOrder=false)` compares `Index` only → **all members of a group are equal quality** for upgrade decisions; only with `respectGroupOrder` does WEBDL beat WEBRip inside "WEB 1080p". `FirstAllowedQuality()/LastAllowedQuality()` skip `Allowed=false` entries.

### 3.3 TRaSH profile JSON shape (`quality-profiles/*.json`)

```json
{ "trash_id": "d1d67249d3890e49bc12e275d989a7e9", "name": "HD Bluray + WEB", "group": 1,
  "upgradeAllowed": true, "cutoff": "Bluray-1080p",
  "minFormatScore": 0, "cutoffFormatScore": 10000, "minUpgradeFormatScore": 1, "language": "Original",
  "items": [ {"name":"Bluray-1080p","allowed":true},
             {"name":"WEB 1080p","allowed":true,"items":["WEBRip-1080p","WEBDL-1080p"]},
             {"name":"Bluray-720p","allowed":true}, {"name":"Raw-HD","allowed":false}, ... ],
  "formatItems": { "HD Bluray Tier 01": "ed27ebfef2f323e964fb1f61391bcb35", ... },
  "trash_score_set": "anime-radarr"   // optional: selects which CF score to use
}
```

Note `items` in TRaSH JSON are listed **best first** (opposite of *arr DB order). `cutoffFormatScore: 10000` is the universal TRaSH setting ("keep upgrading on CF score forever, unwanted CFs are -10000 so they can never win").

---

## 4. Custom formats

### 4.1 Model (`CustomFormats/CustomFormat.cs`, `Specifications/*.cs`)

`CustomFormat { Name, IncludeCustomFormatWhenRenaming bool, Specifications []ICustomFormatSpecification }`.
Every specification has `Name, Negate bool, Required bool` plus type-specific fields:

| Implementation | Radarr | Sonarr | Fields | Matches against |
| --- | --- | --- | --- | --- |
| ReleaseTitleSpecification | y | y | `Value` regex | `MovieInfo.SimpleReleaseTitle` OR `Filename` |
| ReleaseGroupSpecification | y | y | `Value` regex | parsed `ReleaseGroup` |
| EditionSpecification | y | - | `Value` regex | parsed `Edition` |
| SourceSpecification | y | y | `Value` int (app's QualitySource) | `Quality.Source` |
| ResolutionSpecification | y | y | `Value` int (360..2160) | `Quality.Resolution` |
| QualityModifierSpecification | y | - | `Value` int (Modifier) | `Quality.Modifier` |
| LanguageSpecification | y | y | `Value` int language id, `ExceptLanguage bool` | parsed `Languages` list; `-2 Original` resolves to the movie/series `OriginalLanguage` |
| IndexerFlagSpecification | y | y | `Value` int (flags bitmask) | `IndexerFlags.HasFlag(Value)` |
| SizeSpecification | y | y | `Min, Max` GB | `Min < size <= Max` |
| YearSpecification | y | - | `Min, Max` | `Min <= year <= Max` |
| ReleaseTypeSpecification | - | y | `Value` (ReleaseType) | `Unknown=0, SingleEpisode=1, MultiEpisode=2, SeasonPack=3` |

Regexes are .NET `Regex(value, Compiled | IgnoreCase)`.

`CustomFormatInput` (Radarr) = `{MovieInfo ParsedMovieInfo, Movie, Size long, IndexerFlags, Languages, Filename}`; Sonarr = `{EpisodeInfo, Series, Size, IndexerFlags, Languages, Filename, ReleaseType}`.

Indexer flags (Radarr `[Flags] IndexerFlags`): `G_Freeleech=1, G_Halfleech=2, G_DoubleUpload=4, PTP_Golden=8, PTP_Approved=16, G_Internal=32, (AHD_Internal=64 obsolete), G_Scene=128, G_Freeleech75=256, G_Freeleech25=512, (AHD_UserRelease=1024 obsolete), Nuked=2048`. Sonarr: `Freeleech=1, Halfleech=2, DoubleUpload=4, Internal=8, Scene=16, Freeleech75=32, Freeleech25=64, Nuked=128`.

### 4.2 Matching algorithm (exact, `CustomFormatCalculationService.ParseCustomFormat` + `SpecificationMatchesGroup`)

```
for each customFormat:
  groups = specifications grouped by concrete type (ReleaseTitle, ReleaseGroup, Source, ...)
  for each group: matches[spec] = spec.IsSatisfiedBy(input)      // IsSatisfiedBy applies Negate: match = raw XOR Negate
  group.DidMatch = !( any(spec.Required && !matches[spec]) || all(!matches[spec]) )
  customFormat matched iff every group DidMatch
score = Σ profile.FormatItems[matchedFormat].Score
```

So: within a type, at least one spec must match AND every `Required` spec must match; across types, all must match. This is what lets TRaSH build "Tier" CFs = `Source=BLURAY (required) AND Modifier!=REMUX (required, negated) AND Resolution!=2160p (required, negated) AND (group==A OR group==B OR ...)`.

### 4.3 TRaSH CF JSON shape and "score sets"

```json
{ "trash_id": "dc98083864ea246d05a42df0d05f81cc",
  "trash_scores": { "default": -10000, "german": 0, "german-anime": 0 },   // may be absent (=> 0 / optional)
  "trash_regex": "https://regex101.com/r/...", "name": "x265 (HD)", "includeCustomFormatWhenRenaming": false,
  "specifications": [
    {"name":"x265/HEVC","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":"[xh][ ._-]?265|\\bHEVC(\\b|\\d)"}},
    {"name":"Not 2160p","implementation":"ResolutionSpecification","negate":true,"required":true,"fields":{"value":2160}} ] }
```

`trash_scores` keys are **score sets**; a profile's `trash_score_set` selects them (e.g. `Remux Tier 01` = 1950 default, 975 in `anime-radarr`, 0 in `french-multi-vf`). This is the mechanism Clustarr should copy to ship several opinionated profiles over one built-in CF catalogue.

Spec-type usage across the 242 Radarr CFs: ReleaseTitle 177, Source 101, ReleaseGroup 62, QualityModifier 27, Resolution 23, Language 16, IndexerFlag 3 (FreeLeech only). Sonarr adds ReleaseType (Season Pack / Single Episode / Multi-Episode CFs). **Edition, Size and Year specs are never used by TRaSH** → the opinionated subset needs only: ReleaseTitle, ReleaseGroup, Source, Resolution, Modifier, Language, ReleaseType (+ IndexerFlag if we support freeleech boosts).

### 4.4 Regex engine verification (Go)

`$S/gomod/cfcheck/main.go` loaded every regex-bearing spec from both catalogues: **2791 regexes; `github.com/dlclark/regexp2` compiled 2791/2791; Go stdlib `regexp` compiled 2634 and rejected 157** (lookbehind/lookahead heavy ones such as `2.0 Stereo`, `3D`, `AAC`, `Anime BD Tier 02`). Runtime spot checks with regexp2 behaved as .NET does, including variable-length lookbehind `(?<!\bweb[ ._-]?(dl|rip)?\b.*)-(EVO)\b` (matches BluRay-EVO, not WEB-DL-EVO). Conclusion: **TRaSH patterns must be evaluated with regexp2 (IgnoreCase), not RE2.** regexp2 supports a match timeout (`SetTimeoutCheckPeriod`, `Regexp.MatchTimeout`) which should be set because these are backtracking patterns.

### 4.5 The opinionated CF subset (definitions as shipped by TRaSH, Radarr ids; Sonarr equivalents have different trash_ids but identical logic)

Unwanted (all `-10000` default; `[Unwanted] Unwanted Formats` group):

| CF | trash_id (Radarr) | Logic |
| --- | --- | --- |
| BR-DISK | ed38b889b31be83fda192888e2286d83 | one ReleaseTitle regex (required): full-disc/BDMV/ISO/COMPLETE.BluRay detection with negative lookaheads for encodes/remux |
| LQ | 90a6f9a284dff5103f6346090e6280c8 | 100 ReleaseGroup specs `^(GROUP)$` (OR) |
| LQ (Release Title) | e204b80c87be9497a8a6eaff48f72905 | 16 ReleaseTitle specs (groups that can't be parsed as groups, e.g. `EVO (no WEBDL)` = `(?<!\bweb[ ._-]?(dl | rip)?\b.*)-(EVO)\b`) |
| x265 (HD) | dc98083864ea246d05a42df0d05f81cc | ReleaseTitle `[xh][ ._-]?265 | \bHEVC(\b | \d)` (req) AND Resolution != 2160 (neg, req) |
| 3D | b8cd450cbfa689c0259a01d9e29ba3d6 | ReleaseTitle `(?<=\b[12]\d{3}\b).*\b(3d | sbs | half[ .-]ou | half[ .-]sbs)\b` / `BluRay3D` / `BD3D` |
| Extras | 0a3f082873eb454bde444150b70253cc | ReleaseTitle `(?<=\b[12]\d{3}\b).*\b(Extras | Bonus | Extended[ ._-]Clip)\b` |
| Upscaled | bfd8eb01832d646a0a89c4deb46f8564 | 7 ReleaseTitle specs (AI upscales, AIUS, GuyZo, Regrade, RW, TheUpscaler, `Upscaled? | UpRez | AI[ ._-]?Enhanced`) |
| AV1 | cae4ca30163749b891686f95532519bd | ReleaseTitle `\bAV1\b` |
| Generated Dynamic HDR | e6886871085226c3da1830830146846c | 9 ReleaseGroup (BiTOR, DepraveD, Flights, GuyZo, SasukeducK, tarunk9c, VD0N, VECTOR, VisionXpert) AND ReleaseTitle (HDR10+ OR DV) |
| Sing-Along Versions | 712d74cd88bceb883ee32f773656b1f5 | ReleaseTitle |
| Bad Dual Groups (optional) | b6832f586342ef70d9c128d40c07b872 | 37 ReleaseGroup specs |
| No-RlsGroup (optional) | ae9b7c9ebde1f3bd336a8cbd1ec4c5e5 | ReleaseGroup `.` **negated** (i.e. no parsed group) |
| Obfuscated (optional) | 7357cf5161efbf8c4d5d0c30b4815ee2 | 17 ReleaseTitle suffixes (`-4P`, `-Obfuscated`, `-xpost`, `-NZBGeek`, `_nzb`, `Scrambled` ...) |
| Retags (optional) | 5c44f52a8714fdd79bb4d98e2673be1f | ReleaseTitle `\[rartv\]`, `\[rarbg\]`, `\[eztvx?...\]`, `\[TGx\]`, `[.]VAV`, `[.]heb`, `ORARBG` |
| Scene (optional) | f537cf427b64c38c8e36298f657e4828 | ReleaseTitle `^(?=.*(\b\d{3,4}p\b).*([*. ]WEB[*. ])(?!DL)\b) | \b(-CAKES | -GGEZ | ... | -STRiKES)` (req) AND NOT INFLATE/DEFLATE AND NOT GERMAN |
| Black and White Editions (optional) | cc444569854e9de0b084ab2b8b1532b2 | 7 ReleaseTitle |
| Line/Mic Dubbed | c465ccc73923871b3eb1802042331306 | ReleaseTitle |

UHD-only unwanted (optional, conflict pairs in `conflicts.json`: pick one of SDR / SDR (no WEBDL); one of x265 (HD) / x265 (no HDR/DV)):

- SDR 9c38ebb7384dada637be8899efa68e6f: Resolution 2160 (req) AND NOT(HDR formats regex) AND `\bSDR\b`… (title group OR).
- SDR (no WEBDL) 25c12f78430a3a23413652cbd1d48d77: same + Source != WEBDL/WEBRIP.
- x265 (no HDR/DV) 839bea857ed2c0a8e084f3cbdbd65ecb: HEVC (req) AND NOT `\b(dv|dovi|dolby[ .]?v(ision)?|hdr(10(P(lus)?)?)?|pq)\b` (req, neg) AND Resolution != 2160.
- DV (w/o HDR fallback) 923b6abef9b17f937fab56cfcf89e1f1 (-10000): DV regex (req) AND Source WEBDL|WEBRIP AND NOT group Flights AND NOT `\bHDR(\b|\d)` AND NOT hulu.

HDR (UHD profiles, "required"): HDR 493b6d1dbec3c3364c59d7607f7e3405 = **+500** (OR of: DV-with-HDR10-fallback regex, `\b(HDR)\b`, HDR10, HDR10+, HLG, PQ, and "RlsGrp missing HDR tag" for FraMeSToR/HQMUX/SiCFoI 2160p); DV Boost b337d6812e06c200ec9a2d3cfa9d20a7 = **+1000** (`\b(dv|dovi|dolby[ .]?v(ision)?)\b`); HDR10+ Boost caa37d0df9c348912df1fb1d88f9273a = **+100**. TRaSH's newer scheme replaced the older individual DV/DV HDR10/HDR10/HDR10+/PQ/HLG scoring in the mainline profiles (those files still exist with `hlg.json` now -10000).

Repack/Proper (required everywhere): Repack/Proper e7718d7a3ce595f289bfee26adc178f5 = 5 (`\b(Repack|Proper|Rerip)\b` AND NOT repack2/3); Repack2 ae43b294509409a6a13919dedd4764c4 = 6; Repack3 5caaaa1c08c1742aa4342d8c4cc463f2 = 7 (anime sets: 1/2/3).

Release-group tiers (shape: source/modifier/resolution gates + `^(GROUP)$` list):

- HD Bluray Tier 01/02/03 (ed27ebfef2f323e964fb1f61391bcb35 / c20c8647f2746a1f4c4262b0fbbeeeae / 5608c71bcebba0a5e666223bae8c9227) = 1800/1750/1700: `Source=BLURAY(9) req; Modifier!=REMUX(5) req; Resolution!=2160 req; groups OR` (Tier 01: ATELiER, BBQ, BMF, c0kE, Chotab, CRiSC, CtrlHD, D-Z0N3, Dariush, decibeL, DON, ... 23 groups).
- UHD Bluray Tier 01/02/03 (4d74ac4c4db0b64bff6ce0cffef99bf0 / a58f517a70193f8e578056642178419d / e71939fae578037e7aed3ee219bbe7c1) = 1800/1750/1700: `Modifier!=REMUX; Source!=WEBDL; Source!=WEBRIP; Resolution==2160 req; groups`.
- Remux Tier 01/02/03 (3a3ff47579026e76d6504ebea39390de / 9f98181fe5a3fbeb0cc29340da2a468a / 8baaf0b3142bf4d94c42a724f034e27a) = 1950/1900/1850: `Modifier==REMUX req; groups` (3L, ATELiER, BiZKiT, BLURANiUM, BMF, FraMeSToR, ...).
- WEB Tier 01/02/03 (c20f169ef63c5f40c2def54abaf4438e / 403816d65392c79236dcb6dd591aeda4 / af94e0fe497124d1f9ce732069ec8c3b) = 1700/1650/1600: `Source WEBDL(7) OR WEBRIP(8); groups` (ABBIE, AJP69, APEX|PAXA|PEXA|XEPA, BLUTONiUM, BYNDR, CMRG, ...).
- Sonarr extra: WEB Scene d0c516558625b04b363fa6c5c2c7cfd4 = 1600 (groups DEFLATE, INFLATE).

Streaming services (Radarr: score 0 except BCORE 15, CRiT 20, MA 20; Sonarr: **75 each** + HD/UHD Streaming Boost 75): shape = `ReleaseTitle \b(amzn|amazon(hd)?)\b (req) AND Source WEBDL|WEBRIP`.

Movie versions (Radarr optional): Hybrid 100, Remaster 25, 4K Remaster 25, Criterion Collection 25, Masters of Cinema 25, Vinegar Syndrome 25, Special Edition 125, IMAX 800, IMAX Enhanced 800.

Audio (optional, UHD/Remux profiles): TrueHD ATMOS 5000, DTS X 4500, ATMOS (undefined) 3000, DD+ ATMOS 3000, TrueHD 2750, DTS-HD MA 2500, FLAC 2250, PCM 2250, DTS-HD HRA 2000, DD+ 1750, DTS-ES 1500, DTS 1250, AAC 1000, DD 750. Each audio CF is a positive regex plus a chain of negated "Not X" regexes so exactly one matches.

Language CFs (LanguageSpecification): "Language: Not Original" d6e9318c875905d6cfb5bee961afcea9 = `{Value:-2, Negate:true}` scored -10000; "Language: Not English" = `{Value:1, Negate:true}` -10000. Combined with profile `Language: Original` in Radarr.

Anime CFs (score set `anime-radarr` / `anime-sonarr`): Anime BD Tier 01..08 = 1400,1300,...,700 (shape: `Source Bluray|BlurayRaw|DVD` OR-group + ReleaseTitle group regexes like `\b(DemiHuman)\b`); Anime Web Tier 01..06 = 600..100 (`Source WEBDL|WEBRIP|WEB` + title regexes); Remux Tier 01/02(/03) = 975/950(/925); Anime Raws -10000 (22 title regexes `X[ ._-]?(Raws)`); Anime LQ Groups -10000 (137 title regexes); Dubs Only -10000; VOSTFR -10000; AV1 -10000; v0 = -51, v1..v4 = 1..4 (`(\b|\d)(v2)\b|\b(Repack|Proper|Rerip)\b` AND NOT higher versions); Anime Dual Audio 0 (options +10 / +101 / +2000, or MinFormatScore 2000 for "must have"): `dual[ ._-]?(audio)|...` (req) AND NOT `\[(JA|ZH|KO)\]` AND Language JA(8)|ZH(10)|KO(21); Uncensored 0 (`\b(Uncut|Unrated|Uncensored|AT[-_. ]?X)\b`); 10bit 0 (`10[.-]?bit|hi10p`); streaming (Sonarr anime set): CR 6, DSNP 5, NF 4, AMZN 3, VRV 3, FUNi 2, ABEMA 1, ADN 1, Bilibili/B-Global/HIDIVE 0; Radarr anime: VRV 10.

Sonarr-only optional: Season Pack 3bc5f395426614e155e585a2f056cdf1 = +10 (`ReleaseTypeSpecification Value=3`); Single Episode / Multi-Episode exist unscored. Sonarr Unwanted adds BR-DISK (BTN), BW.

---

## 5. TRaSH profiles (what to ship as built-ins)

Common settings for every mainline profile: `upgradeAllowed=true, minFormatScore=0, cutoffFormatScore=10000, minUpgradeFormatScore=1`, Radarr `language=Original`. Ordering below is best → worst; only listed tiers are allowed.

### Radarr

| Profile (trash_id) | Allowed tiers (best first) | Cutoff | Required CFs (score) | Unwanted (-10000) | Optional |
| --- | --- | --- | --- | --- | --- |
| HD Bluray + WEB (d1d67249d3890e49bc12e275d989a7e9), 6–15 GB/1080p | Bluray-1080p; WEB 1080p {WEBRip-1080p, WEBDL-1080p}; Bluray-720p | Bluray-1080p | HD Bluray Tier 01/02/03 1800/1750/1700; WEB Tier 01/02/03 1700/1650/1600; Repack/Proper 5, Repack2 6, Repack3 7 | BR-DISK, Generated Dynamic HDR, LQ, LQ (Release Title), x265 (HD), 3D, Extras, Sing-Along Versions, AV1 | streaming svc (0; BCORE 15, CRiT 20, MA 20), Bad Dual Groups, B&W Editions, No-RlsGroup, Obfuscated, Retags, Scene (-10000), movie versions |
| UHD Bluray + WEB (uhd-bluray-web.json), 20–60 GB/2160p | Bluray-2160p; WEB 2160p {WEBRip-2160p, WEBDL-2160p} | Bluray-2160p | UHD Bluray Tier 01/02/03 1800/1750/1700; WEB Tier 01/02/03; Repack ×3; **HDR 500, DV Boost 1000, HDR10+ Boost 100, DV (w/o HDR fallback) -10000** | HD list + Upscaled | audio formats; SDR / SDR (no WEBDL); x265 (no HDR/DV); Hybrid 100; movie versions |
| Remux + WEB 1080p (remux-web-1080p.json), 20–40 GB | Remux-1080p; WEB 1080p | Remux-1080p | Remux Tier 01/02/03 1950/1900/1850; WEB Tier 01/02/03; Repack ×3 | HD list (no Upscaled) | audio; movie versions incl. Hybrid |
| Remux + WEB 2160p (remux-web-2160p.json), 40–100 GB | Remux-2160p; WEB 2160p | Remux-2160p | Remux Tier ×3; WEB Tier ×3; Repack ×3; HDR set | UHD list | audio; SDR/x265 (no HDR/DV); movie versions |
| WEB 1080p (web-1080p.json) | WEB 1080p | WEB 1080p | WEB Tier ×3; Repack ×3 | (unwanted group) | |
| [Anime] Remux-1080p (anime-remux-1080p.json), `minFormatScore=100`, score set `anime-radarr` | Remux 1080p {Remux-1080p, Bluray-1080p}; WEB 1080p {HDTV-1080p, WEBRip-1080p, WEBDL-1080p}; Bluray-720p; WEB 720p {HDTV-720p, WEBRip-720p, WEBDL-720p}; Bluray-576p; Bluray-480p; WEB 480p; DVD; SDTV | Remux 1080p (group) | Anime BD Tier 01–08, Anime Web Tier 01–06, Remux Tier 01–03, v0–v4, VRV 10 | Anime Raws, Anime LQ Groups, Dubs Only, VOSTFR, AV1 | Uncensored, 10bit, Anime Dual Audio |

The TRaSH web text: "Quality Trumps All" – quality tier first, then CF score, then protocol, indexer priority, flags, seeds/peers, age, size. TRaSH also notes that streaming-service CFs at score 0 exist only for renaming.

### Sonarr

| Profile | Allowed tiers (best first) | Cutoff | Required CFs | Notes |
| --- | --- | --- | --- | --- |
| WEB-1080p (web-1080p.json) | WEB 1080p {WEBRip-1080p, WEBDL-1080p} (optionally enable WEB-720p, HDTV, Bluray for old shows) | WEB 1080p | Repack ×3 (5/6/7); WEB Tier 01/02/03 1700/1650/1600; WEB Scene 1600; streaming services 75 each + HD Streaming Boost 75 | Unwanted: BR-DISK, LQ, LQ (Release Title), x265 (HD), Extras, AV1; optional Bad Dual Groups, No-RlsGroup, Obfuscated, Retags, Scene |
| WEB-2160p (web-2160p.json) | WEB 2160p | WEB 2160p | same + HDR 500, DV Boost 1000, HDR10+ Boost 100, DV (w/o HDR fallback) -10000, UHD Streaming Boost 75 | optional SDR / SDR (no WEBDL), x265 (no HDR/DV) |
| Remux + WEB 1080p (remux-web-1080p.json) | Bluray-1080p Remux; WEB 1080p | Bluray-1080p Remux | Remux Tier 01/02 1900/1850 (Sonarr has only two remux tiers); WEB Tier ×3; WEB Scene; Repack ×3 | |
| Remux + WEB 2160p | Bluray-2160p Remux; WEB 2160p | Bluray-2160p Remux | + HDR set | |
| [Anime] Remux-1080p (anime-remux-1080p.json), `minFormatScore=100`, score set `anime-sonarr` | Bluray 1080p {Bluray-1080p Remux, Bluray-1080p}; WEB 1080p {HDTV-1080p, WEBRip-1080p, WEBDL-1080p}; Bluray-720p; WEB 720p {HDTV-720p, WEBRip-720p, WEBDL-720p}; Bluray-480p; WEB 480p; DVD; SDTV | Bluray 1080p (group) | Anime BD Tier 01–08 (1400→700), Anime Web Tier 01–06 (600→100), Remux Tier 01/02 975/950, v0 -51, v1–v4 1–4, CR 6, DSNP 5, NF 4, AMZN 3, VRV 3, FUNi 2, ABEMA 1, ADN 1, B-Global/Bilibili/HIDIVE/WKN 0 | Unwanted: Anime Raws, Anime LQ Groups, AV1, Dubs Only, VOSTFR; optional 10bit, Uncensored, Anime Dual Audio (+10 / +101 / +2000 / MinFormatScore 2000). Series type must be **Anime**. TRaSH recommends a separate anime instance. |

Sonarr has no profile-level language; TRaSH's language handling is via `Language: Not Original` / `Language: Not English` CFs.

---

## 6. Upgrade / decision logic

### 6.1 `UpgradableSpecification.IsUpgradable` (Radarr; Sonarr identical modulo naming)

```
qualityCompare = profileComparer.Compare(new.Quality, current.Quality)     // by profile index; group members tie
if qualityCompare > 0 && QualityCutoffNotMet(profile, current, new): return None (UPGRADE)
if qualityCompare < 0: return BetterQuality
revCompare = new.Revision.CompareTo(current.Revision)                      // Real first, then Version
if propersMode != DoNotPrefer && revCompare > 0: return None (UPGRADE: proper/repack)
if !profile.UpgradeAllowed: return UpgradesNotAllowed
if propersMode != DoNotPrefer && revCompare < 0: return BetterRevision
if qualityCompare > 0: return QualityCutoff                                // higher quality but cutoff already met
curScore = profile.Score(currentFormats); newScore = profile.Score(newFormats)
if newScore <= curScore: return CustomFormatScore
if curScore >= profile.CutoffFormatScore: return CustomFormatCutoff
if newScore < curScore + profile.MinUpgradeFormatScore: return MinCustomFormatScore
return None (UPGRADE on custom format score)
```

`QualityCutoffNotMet` = `profile.GetIndex(current) < profile.GetIndex(profile.Cutoff)` OR (new is a revision upgrade **of the same quality**). `CustomFormatCutoffNotMet` = `score < (UpgradeAllowed ? CutoffFormatScore : MinFormatScore)`. `IsRevisionUpgrade` requires `current.Quality == new.Quality` ("don't upgrade to a proper for a webrip from a webdl or vice versa"). `Revision{Version=1 default, Real int, IsRepack bool}`; `CompareTo` orders by `Real` then `Version`. `ProperDownloadTypes { PreferAndUpgrade, DoNotUpgrade, DoNotPrefer }` is a global config.

Other relevant specifications: `CustomFormatAllowedByProfileSpecification` (reject if score < `MinFormatScore`), `QualityAllowedByProfileSpecification`, `AcceptableSizeSpecification`, `AvailabilitySpecification` (movies), `MonitoredMovieSpecification`, `DelaySpecification`, `ProperSpecification`, `HardcodeSubsSpecification`, `RawDiskSpecification`, `NotSampleSpecification`, `ReleaseRestrictionsSpecification` (release profiles: `Required[]`, `Ignored[]` terms, `IndexerId`, `Tags`), `RequiredIndexerFlagsSpecification`, `TorrentSeedingSpecification`, `MinimumAgeSpecification`, `RetentionSpecification`.

### 6.2 Release prioritization (`DownloadDecisionComparer`)

Radarr order: quality (profile index, then revision) → custom format score → protocol (delay-profile preferred) → indexer priority (reverse) → indexer flags → peers if torrent → age if usenet → size (closest to preferred size × runtime, else largest).
Sonarr order: quality → CF score → protocol → **episode count** (prefer packs when searching seasons) → episode number → indexer priority → peers → age → size.

`DelayProfile { EnableUsenet, EnableTorrent, PreferredProtocol, UsenetDelay, TorrentDelay (minutes), Order, BypassIfHighestQuality, BypassIfAboveCustomFormatScore, MinimumCustomFormatScore, Tags }`.

---

## 7. Release-title parsing

### 7.1 Radarr `ParsedMovieInfo`

`{ MovieTitles []string (primary first), OriginalTitle, ReleaseTitle, SimpleReleaseTitle, Quality QualityModel{Quality, Revision, *DetectionSource}, Languages []Language, ReleaseGroup, ReleaseHash, Edition, Year int, ImdbId, TmdbId int, HardcodedSubs }`. `QualityDetectionSource { Unknown, Name, Extension, MediaInfo }` records where source/resolution/modifier/revision came from (needed because a file's actual mediainfo can override the name).

`Parser.ParseMovieTitle` pipeline: strip extension, reject hashed/obfuscated names (`RejectHashedReleasesRegex`), detect reversed titles (`p027|p0801`), strip `[request info]` prefixes, run `ReportMovieTitleRegex[]` (title + year, incl. anime `[Subgroup] Title (Year)` variants and `tt\d{7,8}` / `tmdb(id)?-\d+` ids), then `ReleaseGroupParser`, `LanguageParser`, `QualityParser`, `ParseEdition`, `ParseHardcodeSubs`. Also `AlternativeTitleRegex` (`AKA`), `MultiRegex` (`[_. ]multi[_. ]`).

Key regexes (`Parser/QualityParser.cs`, all IgnoreCase):

```
SourceRegex named groups:
  bluray: M?Blu[-_. ]?Ray|HD[-_. ]?DVD|BD(?!$)|UHD2?BD|BDISO|BDMux|BD25|BD50|BR[-_. ]?DISK
  webdl : WEB[-_. ]?DL(?:mux)?|AmazonHD|AmazonSD|iTunesHD|MaxdomeHD|NetflixU?HD|WebHD|HBOMaxHD|DisneyHD|[. ]WEB[. ](?:[xh][ .]?26[45]|AVC|HEVC|DDP?5[. ]1)|[. ](?-i:WEB)$|(?:\d{3,4}0p)[-. ](?:Hybrid[-_. ]?)?WEB[-. ]|[-. ]WEB[-. ]\d{3,4}0p|\b\s\/\sWEB\s\/\s\b|(?:AMZN|NF|DP)[. -]WEB[. -](?!Rip)
  webrip: WebRip|Web-Rip|WEBMux
  hdtv  : HDTV
  bdrip : BDRip|BDLight|HD[-_. ]?DVDRip|UHDBDRip      brrip: BRRip
  dvdr  : \d?x?M?DVD-?[R59]                            dvd  : DVD(?!-R)|DVDRip|xvidvd
  dsr   : WS[-_. ]DSR|DSR                              regional: R[0-9]{1}|REGIONAL
  scr   : SCR|SCREENER|DVDSCR|DVDSCREENER              ts   : TS[-_. ]|TELESYNCH?|HD-TS|HDTS|PDVD|TSRip|HDTSRip
  tc    : TC|TELECINE|HD-TC|HDTC                       cam  : CAMRIP|(?:NEW)?CAM|HD-?CAM(?:Rip)?|HQCAM
  wp    : WORKPRINT|WP     pdtv: PDTV    sdtv: SDTV    tvrip: TVRip
ResolutionRegex: (?<R360p>360p)|(?<R480p>480p|480i|640x480|848x480)|(?<R540p>540p)|(?<R576p>576p)|(?<R720p>720p|1280x720|960p)|(?<R1080p>1080p|1920x1080|1440p|FHD|1080i|4kto1080p)|(?<R2160p>2160p|3840x2160|4k[-_. ](?:UHD|HEVC|BD|H\.?265)|(?:UHD|HEVC|BD|H\.?265)[-_. ]4k)
AlternativeResolutionRegex: \b(?<R2160p>UHD)\b|(?<R2160p>\[4K\])
CodecRegex: \b(?:(?<x264>x264)|(?<h264>h264)|(?<xvidhd>XvidHD)|(?<xvid>X-?vid)|(?<divx>divx))\b     // only used for SD heuristics; x265/HEVC/AV1/HDR/audio are NOT parsed into the quality – they are CF territory
RemuxRegex: (?:[_. \[]|\d{4}p-|\bHybrid-)(?<remux>(?:(BD|UHD)[-_. ]?)?Remux)\b|(?<remux>(?:(BD|UHD)[-_. ]?)?Remux[_. ]\d{4}p)
BRDISKRegex: (long; same logic as TRaSH BR-DISK CF)     RawHDRegex: \b(?<rawhd>RawHD|Raw[-_. ]HD)\b     MPEG2Regex
ProperRegex: \b(?<proper>proper)\b   RepackRegex: \b(?<repack>repack\d?|rerip\d?)\b   RealRegex: \b(?<real>REAL)\b (case-sensitive)
VersionRegex: \d[-._ ]?v(?<version>\d)[-._ ]|\[v(?<version>\d)\]|repack(?<version>\d)|rerip(?<version>\d)
AnimeBlurayRegex: bd(?:720|1080|2160)|(?<=[-_. (\[])bd(?=[-_. )\]])    AnimeWebDlRegex: \[WEB\]|[\[\(]WEB[ .]
HighDefPdtvRegex: hr[-_. ]ws   OtherSourceRegex: (?<hdtv>HD[-_. ]TV)|(?<sdtv>SD[-_. ]TV)
EditionRegex (Parser.cs): \(?\b(?<edition>(((Recut.|Extended.|Ultimate.)?(Director.?s|Collector.?s|Theatrical|Ultimate|Extended|Despecialized|(Special|Rouge|Final|Assembly|Imperial|Diamond|Signature|Hunter|Rekall)(?=(.(Cut|Edition|Version)))|\d{2,3}(th)?.Anniversary)(?:.(Cut|Edition|Version))?(.(Extended|Uncensored|Remastered|Unrated|Uncut|Open.?Matte|IMAX|Fan.?Edit))?|((Uncensored|Remastered|Unrated|Uncut|Open?.Matte|IMAX|Fan.?Edit|Restored|((2|3|4)in1))))))\b\)?
HardcodedSubsRegex: \b((?<hcsub>(\w+(?<!SOFT|MULTI|HORRIBLE)SUBS?))|(?<hc>(HC|SUBBED)))\b
ReleaseGroupRegex: -(?<releasegroup>[a-z0-9]+(?<part2>-[a-z0-9]+)?(?!.+?(?:480p|576p|720p|1080p|2160p)))(?<!(?:WEB-(DL|Rip)|Blu-Ray|480p|...|DTS-HD|DTS-X|DTS-MA|DTS-ES|-ES|-EN|-CAT|-ENG|-JAP|-GER|-FRA|-FRE|-ITA|-HDRip|\d{1,2}-bit|[ ._]\d{4}-\d{2}|-\d{2}|tmdb(id)?-(?<tmdbid>\d+)|(?<imdbid>tt\d{7,8}))(?:\k<part2>)?)(?:\b|[-._ ]|$)|[-._ ]\[(?<releasegroup>[a-z0-9]+)\]$
AnimeReleaseGroupRegex: ^(?:\[(?<subgroup>(?!\s).+?(?<!\s))\](?:_|-|\s|\.)?)
InvalidReleaseGroupRegex: ^([se]\d+|[0-9a-f]{8})$
ExceptionReleaseGroupRegexExact: \b(?<releasegroup>KRaLiMaRKo|E\.N\.D|D\-Z0N3|Koten_Gars|BluDragon|ZØNEHD|HQMUX|VARYG|YIFY|YTS(.(MX|LT|AG))?|TMd|Eml HDTeam|LMain|DarQ|BEN THE MEN|TAoE|QxR|126811)\b   (+ ExceptionReleaseGroupRegex for "Silence|afm72|Panda|Ghost|MONOLITH|Tigole|Joy|ImE|UTR|t3nzi..." before `)`/`]`)
```

Source → quality mapping (`ParseQualityName`, in priority order): RawHD (if not BR-DISK) → bluray group: BRDISK if brDiskMatch; 480p/360p→Bluray-480p; 2160p→Remux-2160p|Bluray-2160p; 1080p→Remux-1080p|Bluray-1080p; 720p→Bluray-720p; 576p→Bluray-576p; no resolution: remux→Remux-1080p else Bluray-720p → webdl: 2160/1080/720p→WEBDL-x, else 480p (HR.WS heuristics) → webrip likewise → scr→DVDSCR, cam→CAM, ts→TELESYNC, tc→TELECINE, wp→WORKPRINT, regional→REGIONAL → hdtv: MPEG2→RAWHD, 2160/1080/720p→HDTV-x, else SDTV → bdrip/brrip: by resolution → Bluray-x → dvdr→DVD-R, dvd→DVD → pdtv/sdtv/dsr/tvrip: 1080p→HDTV-1080p, 720p→HDTV-720p, else SDTV. Fallbacks when no source matched: remux+resolution → Remux; anime `BD` token → Bluray-x; anime `[WEB]` → WEBDL-x; bare resolution: 2160p→HDTV-2160p... (with x264/xvid heuristics for SD: DVD vs SDTV; "1080p"+x264 → Bluray-1080p only if `HighDefPdtv`... etc.). Revision: `Real` counts occurrences of the literal `REAL`; `Version` from `VersionRegex`, `IsRepack` from repack/rerip, `Version=2` when `proper` matched.

Language parsing (`LanguageParser`): token regexes for ~50 languages plus subtitle-suffix and ISO-code forms; defaults to `English` when nothing found; `Original` (-2) is produced for e.g. `MULTi` + original-language rules; `Unknown` (0) when nothing matched and title language unknown. Radarr `Language` ids: Unknown 0, English 1, French 2, Spanish 3, German 4, Italian 5, Danish 6, Dutch 7, Japanese 8, Icelandic 9, Chinese 10, Russian 11, Polish 12, Vietnamese 13, Swedish 14, Norwegian 15, Finnish 16, Turkish 17, Portuguese 18, Flemish 19, Greek 20, Korean 21, Hungarian 22, Hebrew 23, Lithuanian 24, Czech 25, Arabic 26, Hindi 27, Bulgarian 28, Malayalam 29, Ukrainian 30, Slovak 31, Thai 32, Portuguese (Brazil) 33, Spanish (Latino) 34, Romanian 35, Latvian 36, Persian 37, Catalan 38, Croatian 39, Serbian 40, Bosnian 41, Estonian 42, Tamil 43, Indonesian 44, Macedonian 45, Slovenian 46, Azerbaijani 47, Uzbek 48, Malay 49, Urdu 50, Romansh 51, Georgian 52, **Any -1, Original -2**. Sonarr shares 0–52 and Original -2 (no Any).

### 7.2 Sonarr `ParsedEpisodeInfo` and episode parsing

```
ReleaseTitle, SeriesTitle, SeriesTitleInfo{Title, TitleWithoutYear, Year, AllTitles}, Quality QualityModel,
SeasonNumbers []int, EpisodeNumbers []int, AbsoluteEpisodeNumbers []int, SpecialAbsoluteEpisodeNumbers []decimal,
AirDate string (yyyy-MM-dd), Languages, FullSeason bool, IsPartialSeason bool, IsMultiSeason (SeasonNumbers>1),
IsSeasonExtra, IsSeasonTitle, IsSplitEpisode, IsMiniSeries, Special bool, ReleaseGroup, ReleaseHash, SeasonPart int,
ReleaseTokens string, DailyPart int?
derived: IsDaily (AirDate set), IsAbsoluteNumbering, IsPossibleSpecialEpisode, IsPossibleSceneSeasonSpecial,
ReleaseType: EpisodeNumbers>1 || AbsoluteEpisodeNumbers>1 -> MultiEpisode; ==1 -> SingleEpisode; FullSeason -> SeasonPack; else Unknown
```

`Parser.ReportTitleRegex` is an ordered array of 142 regexes; the families (from the source comments): daily with year/time (Plex DVR), daily without title (`2018-10-12`), multi-part without title (`S01E05.S01E06`, `1x05.1x06`), multi without title (`S01E04E05`), split episodes (`S01E05a`), single without title, anime `[SubGroup] Title Absolute (S##E##)`, `[SubGroup] Title S##E##`, `[SubGroup] Title Episode 01`, anime multi/batch (`01-12`, `01~12`), anime with season in brackets, anime `Title Absolute [SubGroup] [Hash]`, `Title Absolute (Year) [SubGroup]`, airdate + S/E, absolute + airdate (wrestling), multi-title `/` separated, `S01E99-100`, Chinese+English titles, 4-digit seasons (`S2016E05`, `2016x05`), `s01.05`, multi-season pack, partial season pack (`S01 Part 2`), 4-digit absolute batch, mini-series `E1-E2`, 5-digit episodes, airdate + part, plain airdate, anime 4-digit absolute. Season packs: `FullSeason=true` when a season-only regex matches, when `episodecount` capture equals last episode (`S01E01-24 of 24`), or title-only parse; `Special` when FullSeason and tokens contain "Special". Sonarr `SourceRegex` groups: bluray (`BluRay|Blu-Ray|HD-?DVD|BDMux|BD(?!$)`), webdl (incl. `[. ]WEB[. ](?:[xh][ .]?26[456]|AVC|HEVC|EAC3|DDP?[ .]?5[. ]1)` and `(?:AMZN|ATVP|DP|NF)[. -]WEB[. -](?!Rip)`), webrip, hdtv, bdrip, brrip, dvd (`DVD|DVDRip|NTSC|PAL|xvidvd`), dsr, pdtv, sdtv, tvrip. Quality mapping mirrors Radarr with `Bluray-1080p Remux`/`Bluray-2160p Remux` and no CAM/TS/TC. Series-type dependent: anime titles match absolute-number regexes first; daily series use `AirDate`; `Series.UseSceneNumbering` maps scene S/E to TVDB S/E via `SceneSeasonNumber/SceneEpisodeNumber/SceneAbsoluteEpisodeNumber` on `Episode`.

### 7.3 Go parser reality check (`moistari/rls v0.6.0`)

`rls.ParseString` returned for the sample set: movie (`The Matrix` 1999, src `UHD.BluRay`, res 2160p, codec HEVC, HDR [HDR DV], audio [TrueHD Atmos], ch 7.1, group FraMeSToR, other [REMUX]); WEB-DL movie (group FLUX, audio DDP 5.1); episode `Severance S02E03` (ATVP dropped into nothing — no streaming-service field, but the token remains in Unused()); season pack `Shogun S01` type=series; anime `[SubsPlease] Frieren - 28` → episode 28 (no group extracted for bracket-prefixed groups!); `[Erai-raws] One Piece - 1090` → episode 1090 but group mis-set to `ARA` (a language token); music FLAC 24bit-96kHz (group mis-set to `24bit`); MP3 320; EPUB book; audiobook `Andy Weir - Project Hail Mary (Unabridged) [M4B 64kbps]` **failed** (type unknown, title unparsed); comic `Saga 001 (2012) (Digital) (Zone-Empire).cbz` → type comic, title `Saga 001`, group `Empire`; `PROPER.REPACK2` → other [PROPER REREPACK]. Verdict: `rls` is a good *tokenizer/hint extractor* for CF matching (codec/HDR/audio/edition tags) and for music/book/comic type detection, but Clustarr needs its own anime-group, absolute-episode, audiobook and revision (`REAL`, `v2`) handling on top. Port the *arr `SourceRegex`/`ResolutionRegex`/`RemuxRegex`/`ReleaseGroupRegex` into regexp2 for the quality decision itself — those are the canonical semantics TRaSH CFs assume (e.g. CFs test `Source=WEBDL` as parsed by Radarr's regex, not by rls).

---

## 8. Sonarr-specific inventory semantics

`SeriesTypes { Standard=0, Daily=1, Anime=2 }`; `SeriesStatusType { Deleted=-1, Continuing=0, Ended=1, Upcoming=2 }`.

`Series`: `TvdbId, TvRageId, TvMazeId, ImdbId, TmdbId, MalIds HashSet<int>, AniListIds HashSet<int>, Title, CleanTitle, SortTitle, Status, Overview, AirTime, Monitored, MonitorNewItems (All|None), QualityProfileId, SeasonFolder, LastInfoSync, Runtime, Images, SeriesType, Network, UseSceneNumbering, TitleSlug, Path, Year, Ratings, Genres, Actors, Certification, RootFolderPath, Added, FirstAired, LastAired, OriginalLanguage, OriginalCountry, Seasons []Season{SeasonNumber, Monitored, Images}, Tags, AddOptions`.

`Episode`: `SeriesId, TvdbId, EpisodeFileId, SeasonNumber, EpisodeNumber, Title, AirDate string, AirDateUtc, Overview, Monitored, AbsoluteEpisodeNumber?, SceneAbsoluteEpisodeNumber?, SceneSeasonNumber?, SceneEpisodeNumber?, AiredAfterSeasonNumber?, AiredBeforeSeasonNumber?, AiredBeforeEpisodeNumber?, UnverifiedSceneNumbering, Ratings, LastSearchTime, Runtime, FinaleType ("season"/"series"/"midseason"), HasFile`.

Add-time monitoring (`MonitoringOptions { IgnoreEpisodesWithFiles, IgnoreEpisodesWithoutFiles, Monitor MonitorTypes }`):
`MonitorTypes { Unknown, All, Future, Missing, Existing, FirstSeason, LastSeason, LatestSeason(obsolete), Pilot, Recent, MonitorSpecials, UnmonitorSpecials, None, Skip }`. `EpisodeMonitoredService` semantics: `All` all episodes; `Future` only unaired (and only if series Continuing/Upcoming); `Missing` aired w/o file; `Existing` with file; `FirstSeason`/`LastSeason` one season; `Pilot` S01E01 only; `Recent` aired in last 90 days; `MonitorSpecials`/`UnmonitorSpecials` toggle season 0 only; `None`; `Skip` = don't touch; seasons with no monitored episodes become unmonitored. `MonitorNewItems { All, None }` governs newly discovered episodes/seasons on refresh. Season packs: Sonarr prefers packs when searching a whole season (comparer `CompareEpisodeCount`), CF `Season Pack` (+10 optional) via `ReleaseTypeSpecification`. Sonarr's `EpisodeFile.ReleaseType` is persisted so a later single-episode upgrade can be scored against a season-pack file.

---

## 9. Radarr minimum availability & monitoring

`MovieStatusType { Deleted=-1, TBA=0, Announced=1, InCinemas=2 ("in cinemas < 3 months"), Released=3 ("physical/web release or in cinemas > 3 months") }` — the same enum is used for `Movie.MinimumAvailability` (UI offers Announced / In Cinemas / Released).

`Movie.IsAvailable(delayDays)` (exact):

```
if MinimumAvailability in {TBA, Announced}: date = MinValue   (always available)
elif MinimumAvailability == InCinemas && meta.InCinemas != null: date = meta.InCinemas
else:  # Released
    date = min(PhysicalRelease, DigitalRelease) if both
         | whichever exists
         | InCinemas + 90 days if only InCinemas
         | MaxValue (never)
if date is MinValue or MaxValue: return now >= date
return now >= date + delayDays      # delay = config AvailabilityDelay
```

`AvailabilitySpecification` skips this check for user-invoked searches. `MovieMetadata` dates: `InCinemas?, PhysicalRelease?, DigitalRelease?`; other fields: `TmdbId, ImdbId, Title, CleanTitle, SortTitle, OriginalTitle, OriginalLanguage, Year, SecondaryYear?, Runtime, Status, Overview, Certification, Genres, Keywords, Ratings, Studio, Website, YouTubeTrailerId, Popularity, CollectionTmdbId, CollectionTitle, AlternativeTitles, Translations, Recommendations, Images`. `Movie`: `MovieMetadataId, Monitored, MinimumAvailability, QualityProfileId, Path, RootFolderPath, Added, Tags, AddOptions{Monitor: MovieOnly|MovieAndCollection|None, SearchForMovie, AddMethod: Manual|List|Collection}, LastSearchTime, MovieFileId`. `MovieCollection { TmdbId, Title, Overview, Monitored, QualityProfileId, RootFolderPath, SearchOnAdd, MinimumAvailability, Tags }` (collection-level defaults applied to new members).

`MovieFile { MovieId, RelativePath, Path, Size, DateAdded, OriginalFilePath, SceneName, ReleaseGroup, Quality QualityModel, IndexerFlags, MediaInfo, Edition, Languages }`.

---

## 10. MediaInfo model (needed by inventory + transcoder)

`MediaInfoModel` (Radarr/Sonarr, from ffprobe): `ContainerFormat, VideoFormat, VideoCodecID, VideoProfile, VideoBitrate, VideoBitDepth, VideoMultiViewCount (3D), VideoColourPrimaries, VideoTransferCharacteristics, DoviConfigurationRecord, VideoHdrFormat, Height, Width, AudioFormat, AudioCodecID, AudioProfile, AudioBitrate, RunTime, AudioStreamCount, AudioChannels, AudioChannelPositions, VideoFps, AudioLanguages, Subtitles, ScanType, Title, SchemaRevision, RawStreamData, RawFrameData`.
`HdrFormat { None, Pq10, Hdr10, Hdr10Plus, Hlg10, DolbyVision, DolbyVisionHdr10, DolbyVisionSdr, DolbyVisionHlg, DolbyVisionHdr10Plus }` — derived from colour primaries/transfer (`bt2020`/`smpte2084`/`arib-std-b67`), HDR10+ side data, and the DoVi configuration record (profile 5 = DV-only/no fallback, 7/8 = with HDR10 fallback). This is also exactly the "desired end state" vocabulary the transcoding service needs (10-bit HEVC + AAC, keep HDR metadata).

TRaSH naming (`naming/*.json`): Radarr file `{Movie CleanTitle} {(Release Year)} {tmdb-{TmdbId}} - {edition-{Edition Tags}} {[MediaInfo 3D]}{[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}{-Release Group}`; Sonarr `{Series CleanTitleWithoutYear} {(Series Year)} - S{season:00}E{episode:00} - {Episode CleanTitle:90} {[Custom Formats]}{[Quality Full]}...{-Release Group}`, anime variant adds `{absolute:000}` and `{MediaInfo AudioLanguages}`. `{Quality Full}` = quality name + `Proper`/`Repack`/`v2`.

---

## 11. Music (Lidarr)

Entities (`Music/Model`): `Artist { ArtistMetadataId, CleanName, SortName, Monitored, MonitorNewItems (All|None|New), LastInfoSync, Path, RootFolderPath, Added, QualityProfileId, MetadataProfileId, Tags, AddOptions{Monitor, AlbumsToMonitor, Monitored, SearchForMissingAlbums} }` + `ArtistMetadata { ForeignArtistId (MusicBrainz artist MBID), OldForeignArtistIds, Name, Aliases, Overview, Disambiguation, Type, Status (Deleted=-1, Continuing=0, Ended=1), Links, Genres, Ratings, Members[{Name, Instrument}] }`.
`Album { ArtistMetadataId, ForeignAlbumId (MB release-group id), Title, Overview, Disambiguation, ReleaseDate?, Links, Genres, AlbumType string, SecondaryTypes []SecondaryAlbumType, Ratings, CleanTitle, ProfileId, Monitored, AnyReleaseOk, LastSearchTime, Added, AlbumReleases }`.
`AlbumRelease (Release.cs) { AlbumId, ForeignReleaseId (MB release id), Title, Status, Duration, Label [], Disambiguation, Country [], ReleaseDate?, Media []Medium{Number, Name, Format}, TrackCount, Monitored }` — exactly one release per album is `Monitored`; `AnyReleaseOk=true` lets the importer switch to whichever release matches the files.
`Track { ForeignTrackId, ForeignRecordingId, AlbumReleaseId, ArtistMetadataId, TrackNumber string, AbsoluteTrackNumber, Title, Duration ms, Explicit, Ratings, MediumNumber, TrackFileId }`.
`PrimaryAlbumType { Album 0, EP 1, Single 2, Broadcast 3, Other 4 }`; `SecondaryAlbumType { Studio 0, Compilation 1, Soundtrack 2, Spokenword 3, Interview 4, Audiobook 5, Live 6, Remix 7, DJ-mix 8, Mixtape/Street 9, Demo 10, Audio drama 11 }`; `ReleaseStatus { Official 0, Promotion 1, Bootleg 2, Pseudo-Release 3 }`.
`MetadataProfile { Name, PrimaryAlbumTypes[], SecondaryAlbumTypes[], ReleaseStatuses[] }` = which MusicBrainz release-groups get added as albums (default "Standard": Album + Studio + Official).
`MonitorTypes { All, Future, Missing, Existing, Latest, First, None, Unknown }`.

Qualities (`Quality.cs`, weight order low→high, `GroupName`):
Unknown(0,w1) | Trash Quality Lossy: MP3-8..MP3-80 (w2–10) | Poor Quality Lossy: MP3-96, MP3-112, MP3-128, OGG Vorbis Q5=MP3-160 (w11–14) | Low Quality Lossy: MP3-192=Vorbis Q6=AAC-192=WMA (w15), MP3-224 (w16) | Mid Quality Lossy: Vorbis Q7 (w17), MP3-VBR-V2=MP3-256=Vorbis Q8=AAC-256 (w18) | High Quality Lossy: MP3-VBR-V0=AAC-VBR (w19), MP3-320=Vorbis Q9=AAC-320 (w20), Vorbis Q10 (w21) | Lossless: FLAC=ALAC=APE=WavPack (w22), FLAC 24bit=ALAC 24bit (w23) | WAV (w24). Ids: Unknown 0, MP3-192 1, MP3-VBR-V0 2, MP3-256 3, MP3-320 4, MP3-160 5, FLAC 6, ALAC 7, MP3-VBR-V2 8, AAC-192 9, AAC-256 10, AAC-320 11, AAC-VBR 12, WAV 13, Vorbis Q10..Q5 14–19, WMA 20, FLAC 24bit 21, MP3-128 22, MP3-96..MP3-8 23–32, MP3-112 33, MP3-224 34, APE 35, WavPack 36, ALAC 24bit 37. Sizes are MB/min too (e.g. MP3-320 max 350, FLAC preferred 895/unlimited).
Default profiles: "Any" (cutoff Unknown, everything), "Lossless" (cutoff FLAC; FLAC, ALAC, FLAC 24bit, ALAC 24bit), "Standard" (cutoff MP3-192; MP3-192, MP3-256, MP3-320).
Parser (`QualityParser`): `CodecRegex` (MP1/MP2/MP3VBR/MP3CBR/FLAC(+24bit)/WAVPACK/ALAC/WMA/WAV|PCM/AAC(M4A|M4B|M4P|AAC)/Vorbis/…), `BitRateRegex` (`96|128|160|192|224|256|320|500 kbps`, `V0`, `V2`, `q5..q10`, `itunes plus`=256), `SampleSizeRegex` (`24[-._ ]?bit|flac24|tr24|24-(44|48|96|192)`), `WebRegex`, Proper/Repack/Version/Real. Result = codec + bitrate → one of the enum values; files are re-classified from tags/mediainfo after import. `ParsedAlbumInfo { ArtistName, AlbumTitle, AlbumType, ReleaseDate/ReleaseYear, Discography (+start/end), Quality, ReleaseGroup, ReleaseHash, ReleaseVersion }`.

---

## 12. Books & audiobooks (Readarr) and Audiobookshelf

Readarr entities: `Author { AuthorMetadataId, CleanName, Monitored, MonitorNewItems, Path, RootFolderPath, Added, QualityProfileId, MetadataProfileId, Tags, AddOptions{Monitor, SearchForMissingBooks} }` + `AuthorMetadata { ForeignAuthorId (Goodreads author id), TitleSlug, Name, SortName, NameLastFirst, SortNameLastFirst, Aliases, Overview, Disambiguation, Gender, Hometown, Born, Died, Status (Continuing|Ended), Links, Genres, Ratings }`.
`Book { AuthorMetadataId, ForeignBookId (Goodreads work id), ForeignEditionId, TitleSlug, Title, ReleaseDate?, Links, Genres, RelatedBooks, Ratings, LastSearchTime, CleanTitle, Monitored, AnyEditionOk, Added, AddOptions{AddType Automatic|Manual, SearchForNewBook}, Editions, SeriesLinks }`.
`Edition { BookId, ForeignEditionId (Goodreads book/edition id), TitleSlug, Isbn13, Asin, Title, Language, Overview, Format, IsEbook bool, Disambiguation, Publisher, PageCount, ReleaseDate?, Images, Links, Ratings, Monitored, ManualAdd }` — audiobook vs ebook is **an Edition property** (`IsEbook=false`, `Format` e.g. "Audible Audio", `Asin` set), not a Book property.
`Series { ForeignSeriesId, Title, Description, Numbered, WorkCount, PrimaryWorkCount }`, `SeriesBookLink { Position string ("1", "2.5"), SeriesPosition int, SeriesId, BookId, IsPrimary }`.
`MetadataProfile { Name, MinPopularity double, SkipMissingDate, SkipMissingIsbn, SkipPartsAndSets, SkipSeriesSecondary, AllowedLanguages string (csv), MinPages, Ignored []string }`.
`MonitorTypes { All, Future, Missing, Existing, Latest, First, None, Unknown }`.
Qualities: `Unknown Text 0 (w1)`, `PDF 1 (w5)`, `MOBI 2 (w10)`, `EPUB 3 (w11)`, `AZW3 4 (w12)`, `Unknown Audio 13 (w50)`, `MP3 10 (w100)`, `M4B 12 (w105)`, `FLAC 11 (w110)`. Default profiles: "eBook" (cutoff MOBI; MOBI, EPUB, AZW3) and "Spoken" (cutoff MP3; Unknown Audio, MP3, M4B, FLAC). Parser: `CodecRegex` = PDF|MOBI|EPUB|AZW3?|MP3|FLAC|M4B|... (no bitrate tiers for audiobooks). `BookFile { Quality, Size, MediaInfo, Edition, CalibreId, ... }`; Calibre integration is optional.

Audiobookshelf (advplyr/audiobookshelf) model (for the audiobook inventory shape): `Library { id, name, mediaType: book|podcast, settings{metadataPrecedence: folderStructure|audioMetatags|nfoFile|opfFile|absMetadata} }`; `LibraryItem { id, ino, path, relPath, isFile, mtime, libraryId, libraryFolderId, mediaType, mediaId, libraryFiles[] }`; `Book { title, subtitle, authors[{id,name}], narrators[]string, series[{id,name,sequence}], genres[], tags[], publishedYear, publishedDate, publisher, description, isbn, asin, language, explicit, abridged, audioFiles[] (m4b/mp3 with per-file duration/chapters), chapters[], ebookFile, duration, coverPath }`. Metadata providers: Audible (ASIN, narrator, series/sequence, abridged) primary; also Audnexus/Google Books/OpenLibrary/iTunes (the DeepWiki answer only confirmed Audible from code). Audiobooks are a **folder of audio files**, so inventory must model one "edition" → N files + chapters, unlike a movie's 1 file.

---

## 13. Comics & manga

Kapowarr (`Casvt/Kapowarr`): `Volume { comicvine_id, title, year, publisher, volume_number, description, site_url, monitored, root_folder, folder, custom_folder, special_version, last_cv_fetch, monitor_new_issues }`; `Issue { comicvine_id, volume_id, issue_number string, calculated_issue_number float, title, date, description, monitored, files[] }`; `files { filepath, size }`; `SpecialVersion { tpb, one-shot, hard-cover, omnibus, volume-as-issue, cover, metadata, normal(None) }` — the special version drives file matching, download validation and naming. Filename parser extracts series, year, volume_number, special_version, issue_number, annual. Download sources: GetComics (DDL), torrent, usenet.
Mylar3: `comics { ComicID (ComicVine volume id), ComicName, ComicYear, ComicPublisher, Status (Active/Paused...), Total, Have, ComicLocation, ComicVersion, Type (Print|Digital|TPB|GN|HC|One-Shot), Corrected_Type, ComicImage, LatestIssue, LatestDate, LastUpdated }`; `issues { IssueID, ComicID, Issue_Number, Int_IssueNumber, IssueName, IssueDate, ReleaseDate, DigitalDate, Status, Location, ComicSize, Type }`; `annuals` (same shape + ComicName). Issue `Status ∈ { Wanted, Skipped, Snatched, Downloaded, Archived, Ignored, Failed }`; weekly pull list auto-marks upcoming issues `Wanted` when `AUTOWANT_UPCOMING` (this is the comics equivalent of Sonarr's `MonitorNewItems=All`). `filechecker.py` extracts series_name, issue_number, issue_year, series_volume, booktype (annual/special), scangroup.
ComicInfo.xml (anansi-project, v2.0/v2.1 draft — the de-facto embedded metadata for CBZ, used by ComicTagger/Komga/Kavita/Mylar): elements `Title, Series, Number (string), Count, Volume, AlternateSeries, AlternateNumber, AlternateCount, Summary, Notes, Year, Month, Day, Writer, Penciller, Inker, Colorist, Letterer, CoverArtist, Editor, Translator (2.1), Publisher, Imprint, Genre, Tags (2.1), Web, PageCount, LanguageISO, Format, BlackAndWhite (YesNo), Manga (Unknown|No|Yes|YesAndRightToLeft), Characters, Teams, Locations, ScanInformation, StoryArc, StoryArcNumber (2.1), SeriesGroup, AgeRating (Unknown, Adults Only 18+, Early Childhood, Everyone, Everyone 10+, G, Kids to Adults, M, MA15+, Mature 17+, PG, R18+, Rating Pending, Teen, X18+), Pages[ComicPageInfo{Image, Type: FrontCover|InnerCover|Roundup|Story|Advertisement|Editorial|Letters|Preview|BackCover|Other|Deleted, DoublePage, ImageSize, Key, Bookmark, ImageWidth, ImageHeight}], CommunityRating (0–5), MainCharacterOrTeam, Review, GTIN (2.1)`. Manga is the same model with `Manga=YesAndRightToLeft`, volumes/chapters instead of issues (chapter numbers are decimals like `12.5`), and ids from MangaDex/AniList/MAL rather than ComicVine.

---

## 14. Go-oriented opinionated data model for Clustarr

Design rules derived from the above: (1) key qualities by `(Source, Resolution, Modifier)`, never by *arr ids; (2) profiles are ordered tier lists with groups, cutoff, and CF-score thresholds; (3) custom formats are a **built-in immutable catalogue** with score sets; profiles only reference them; (4) matching uses regexp2 with the exact*arr group semantics; (5) media-specific quality enums for music/books/comics, but the *same* Profile/Format/Upgrade machinery; (6) inventory items carry `Monitored`, profile ref, root folder, and media-specific availability rules.

```go
// pkg/quality/video.go
package quality

type Source uint8

const (
 SourceUnknown Source = iota
 SourceCam
 SourceTelesync
 SourceTelecine
 SourceWorkprint
 SourceDVD
 SourceTV
 SourceWebDL
 SourceWebRip
 SourceBluray
)

type Modifier uint8

const (
 ModNone Modifier = iota
 ModRegional
 ModScreener
 ModRawHD
 ModBRDisk
 ModRemux
)

type Resolution uint16 // 0, 360, 480, 540, 576, 720, 1080, 2160

// Video is the value-type identity of a video quality. Name() renders "Bluray-1080p", "Remux-2160p", "WEBDL-720p" ...
type Video struct {
 Source     Source
 Resolution Resolution
 Modifier   Modifier
}

// Revision mirrors *arr: Version starts at 1; Real counts literal REAL tokens; Repack is informational.
type Revision struct {
 Version int
 Real    int
 Repack  bool
}

func (r Revision) Compare(o Revision) int // Real first, then Version

// Model = quality + revision, exactly *arr QualityModel.
type Model struct {
 Video    Video
 Revision Revision
 Source   DetectionSource // Name | Extension | MediaInfo — where Video came from
}

// Definition is the global table row: canonical ordering weight + size limits (MB per minute of runtime).
type Definition struct {
 Video     Video
 Name      string
 Weight    int      // Radarr weights 1..26; used only for display/sorting outside a profile
 Group     string   // "WEB 1080p" etc. (default tie group)
 MinMBMin  float64
 MaxMBMin  *float64 // nil = unlimited
 PrefMBMin *float64
}

// SizeTable selects TRaSH sizes: "movie" | "series" | "anime". Applied as bytes = MBmin*1024*1024*runtimeMinutes,
// runtime = Σ episode runtimes (fallback series runtime, fallback 45) for TV, movie runtime (fallback 110) for movies.
type SizeTable map[Video]Definition
```

```go
// pkg/quality/audio.go — music (Lidarr vocabulary, grouped into tiers)
type AudioCodec uint8 // MP3, AAC, Vorbis, Opus, WMA, FLAC, ALAC, APE, WavPack, WAV
type Audio struct {
 Codec    AudioCodec
 Bitrate  int    // kbps for CBR (8..320), 0 for VBR/lossless
 Preset   string // "V0","V2","Q5".."Q10","VBR"
 BitDepth int    // 16 or 24 for lossless
}
type AudioTier uint8 // TierTrash < TierPoor < TierLow < TierMid < TierHigh < TierLossless < TierLossless24 (Lidarr GroupWeight 2..7)
func (a Audio) Tier() AudioTier

// pkg/quality/book.go
type BookFormat uint8 // BookUnknown, PDF, MOBI, EPUB, AZW3 (text); AudioUnknown, MP3, M4B, FLAC (spoken) — weights 1,5,10,11,12,50,100,105,110
type ComicFormat uint8 // CBZ, CBR, CB7, PDF, EPUB — no quality ladder; prefer CBZ, keep ScanInfo/Digital hints as tags
```

```go
// pkg/profile/profile.go
package profile

type MediaKind uint8 // Movie, Series, Anime, Music, Book, Audiobook, Comic, Manga

type Tier struct {
 ID        string          // "web-1080p", "bluray-1080p", "remux-1080p"
 Name      string          // "WEB 1080p"
 Qualities []quality.Video // >1 => group; all members compare equal
}

type LanguagePolicy struct {
 Mode     string // "original" | "any" | "specific"
 Language Language
}

type Profile struct {
 Name                  string
 Kind                  MediaKind
 Tiers                 []Tier            // BEST FIRST (TRaSH order). Persist this order; never *arr's reversed order.
 Cutoff                string            // Tier.ID; upgrades on quality stop once file is in this tier or better
 UpgradesAllowed       bool
 MinFormatScore        int               // reject candidates below (TRaSH: 0, anime: 100)
 CutoffFormatScore     int               // stop CF upgrades once existing >= (TRaSH: 10000)
 MinUpgradeFormatScore int               // new >= current + this (TRaSH: 1)
 Language              LanguagePolicy    // movies: Original; series: n/a (language CFs)
 ScoreSet              string            // "default" | "anime-radarr" | "anime-sonarr" ...
 Formats               map[string]int    // FormatID -> score, resolved from catalogue[ScoreSet] + profile overrides
 SizeTable             string            // "movie" | "series" | "anime" | "music" | "book"
 Propers               ProperPolicy      // PreferAndUpgrade | DoNotUpgrade | DoNotPrefer
}

func (p Profile) Index(v quality.Video) (tier int, ok bool)                 // position in Tiers; ok=false => not allowed
func (p Profile) Compare(a, b quality.Video) int                           // by tier index only (group members tie)
func (p Profile) Score(matched []string) int                               // Σ Formats[id]
```

```go
// pkg/format/format.go — built-in custom-format catalogue (not user-editable)
package format

type ConditionKind uint8

const (
 CondReleaseTitle ConditionKind = iota // regex vs release title / filename
 CondReleaseGroup                      // regex vs parsed group
 CondEdition                           // regex vs parsed edition (unused by TRaSH, kept for parity)
 CondSource                            // quality.Source equality
 CondResolution                        // quality.Resolution equality
 CondModifier                          // quality.Modifier equality
 CondLanguage                          // language in parsed languages; Value -2 => item's original language; ExceptLanguage => any language != Value
 CondIndexerFlag                       // flags & Value != 0
 CondSize                              // Min < GB <= Max
 CondYear                              // Min <= year <= Max
 CondReleaseType                       // Sonarr: SingleEpisode | MultiEpisode | SeasonPack
)

type Condition struct {
 Kind     ConditionKind
 Name     string
 Negate   bool
 Required bool
 Pattern  string  // regexp2, compiled with IgnoreCase (+ match timeout) at catalogue load
 Value    int     // enums / language id / flags
 Except   bool    // CondLanguage ExceptLanguage
 Min, Max float64 // size (GB) / year
}

type Format struct {
 ID         string         // stable slug: "x265-hd", "hd-bluray-tier-01"
 Name       string
 TrashID    string         // provenance, per app: {"radarr": "...", "sonarr": "..."}
 Scores     map[string]int // score set -> score; missing set => Scores["default"]; missing default => 0
 Conditions []Condition
 Renaming   bool           // IncludeCustomFormatWhenRenaming
}

// Facts is everything a condition may inspect (union of Radarr/Sonarr CustomFormatInput).
type Facts struct {
 ReleaseTitle     string
 Filename         string
 ReleaseGroup     string
 Edition          string
 Quality          quality.Model
 Languages        []Language
 OriginalLanguage Language
 IndexerFlags     IndexerFlags
 SizeBytes        int64
 Year             int
 ReleaseType      ReleaseType
}

// Match implements *arr semantics exactly:
//   per Kind group: matched[c] = c.eval(f) XOR c.Negate
//   group ok  = !(∃ c: c.Required && !matched[c]) && ∃ c: matched[c]
//   format ok = ∀ groups ok
func (fm Format) Match(f Facts) bool
func MatchAll(cat []Format, f Facts) []string // sorted ids
```

```go
// pkg/decide/upgrade.go
type Verdict uint8

const (
 Upgrade Verdict = iota
 ExistingBetterQuality
 UpgradesNotAllowed
 ExistingBetterRevision
 QualityCutoffMet
 FormatScoreNotHigher
 FormatCutoffMet
 FormatIncrementTooSmall
)

type Candidate struct {
 Quality quality.Model
 Formats []string // matched format ids
}

// IsUpgrade is the *arr UpgradableSpecification decision table (section 6.1).
func IsUpgrade(p profile.Profile, current, candidate Candidate) Verdict

// Rank orders acceptable candidates: tier idx, revision, CF score, protocol pref, (episode count for packs), indexer priority, flags, seeds/age, |size - preferred|.
func Rank(p profile.Profile, cands []ScoredRelease) []ScoredRelease
```

```go
// pkg/parse/release.go — unified parse result (superset of ParsedMovieInfo / ParsedEpisodeInfo / ParsedAlbumInfo / ParsedBookInfo)
type ReleaseType uint8 // Unknown, SingleEpisode, MultiEpisode, SeasonPack

type Parsed struct {
 Kind          MediaKind
 ReleaseTitle  string
 Titles        []string // primary first (AKA handling)
 Year          int
 Edition       string
 Quality       quality.Model
 Languages     []Language
 ReleaseGroup  string
 ReleaseHash   string
 HardcodedSubs string
 IDs           struct{ IMDb, TMDb, TVDb string }
 // TV
 Seasons          []int
 Episodes         []int
 AbsoluteEpisodes []int
 AirDate          string // yyyy-mm-dd for daily series
 FullSeason       bool
 PartialSeason    bool
 Special          bool
 // Music / books
 Artist, Album, Author, Book string
 Audio quality.Audio
 Book  quality.BookFormat
 // Hints for format matching only (never part of Quality): codec, HDR, audio, streaming service, from rls
 Hints struct{ Codec, HDR, AudioCodec []string; Channels, Service string }
}

func (p Parsed) ReleaseType() ReleaseType // >1 episodes -> Multi; ==1 -> Single; FullSeason -> SeasonPack
```

```go
// pkg/inventory — CRD-shaped specs (spec/status split maps to Kubernetes kinds)
type Monitoring struct {
 Monitored       bool
 MonitorNewItems string // "all" | "none" (Sonarr/Lidarr/Readarr); Lidarr adds "new"
 SearchOnAdd     bool
}

type MovieAvailability uint8 // Announced, InCinemas, Released
type MovieStatus int8        // Deleted=-1, TBA, Announced, InCinemas, Released

type MovieSpec struct {
 TMDbID, IMDbID      string
 Title               string
 Year                int
 QualityProfile      string
 RootFolder          string
 MinimumAvailability MovieAvailability
 AvailabilityDelay   int // days (global default)
 Monitoring          Monitoring
 Tags                []string
}
type MovieStatusBlock struct {
 Status          MovieStatus
 InCinemas       *time.Time
 PhysicalRelease *time.Time
 DigitalRelease  *time.Time
 Runtime         int
 OriginalLanguage Language
 File            *MediaFile // Quality, Formats, Languages, ReleaseGroup, Edition, MediaInfo, IndexerFlags, Size
}
func (m MovieStatusBlock) Available(min MovieAvailability, delayDays int, now time.Time) bool // section 9 algorithm

type SeriesType uint8 // Standard, Daily, Anime
type SeriesSpec struct {
 TVDbID, IMDbID, TMDbID string
 MALIDs, AniListIDs     []int
 Title                  string
 SeriesType             SeriesType
 QualityProfile, RootFolder string
 SeasonFolder           bool
 UseSceneNumbering      bool
 Monitoring             Monitoring
 AddMonitor             string // all|future|missing|existing|firstSeason|lastSeason|pilot|recent|monitorSpecials|unmonitorSpecials|none
 Seasons                []SeasonSpec // {Number, Monitored}
 Tags                   []string
}
type Episode struct {
 Season, Number     int
 Absolute           *int
 SceneSeason, SceneNumber, SceneAbsolute *int
 AirDateUTC         *time.Time
 Runtime            int
 Monitored          bool
 FinaleType         string
 File               *MediaFile // includes ReleaseType of the grab it came from
}

type ArtistSpec struct { MBID string; Name string; QualityProfile, MetadataProfile, RootFolder string; Monitoring Monitoring; AddMonitor string /* all|future|missing|existing|latest|first|none */ }
type Album struct { MBReleaseGroupID string; Title string; Type PrimaryAlbumType; SecondaryTypes []SecondaryAlbumType; ReleaseDate *time.Time; Monitored, AnyReleaseOk bool; Releases []AlbumRelease }
type AlbumRelease struct { MBReleaseID string; Title, Status, Country, Label string; Media []Medium; TrackCount int; Monitored bool; Tracks []Track }
type Track struct { MBRecordingID string; Medium, Number int; Title string; DurationMs int; Explicit bool; File *AudioFile /* quality.Audio + tags */ }
type MusicMetadataProfile struct { PrimaryTypes []PrimaryAlbumType; SecondaryTypes []SecondaryAlbumType; ReleaseStatuses []ReleaseStatus }

type AuthorSpec struct { GoodreadsID, OpenLibraryID string; Name string; QualityProfile, MetadataProfile, RootFolder string; Monitoring Monitoring; AddMonitor string }
type Book struct { WorkID string; Title string; ReleaseDate *time.Time; Series []SeriesLink /* {SeriesID, Position "2.5", Primary} */; Monitored, AnyEditionOk bool; Editions []Edition }
type Edition struct { EditionID, ISBN13, ASIN string; Title, Language, Publisher, Format string; IsEbook bool; PageCount int; Narrators []string; Abridged bool; Monitored bool; Files []BookFile /* audiobook = N audio files + chapters */ }
type BookMetadataProfile struct { MinPopularity float64; SkipMissingDate, SkipMissingISBN, SkipPartsAndSets, SkipSeriesSecondary bool; AllowedLanguages []string; MinPages int; Ignored []string }

type SpecialVersion uint8 // Normal, TPB, OneShot, HardCover, Omnibus, VolumeAsIssue, Cover, Metadata (Kapowarr)
type ComicVolumeSpec struct { ComicVineID string; MangaDexID, AniListID string; Title string; Year int; Publisher string; VolumeNumber int; SpecialVersion SpecialVersion; Manga string /* ComicInfo Manga enum */; RootFolder string; Monitoring Monitoring }
type Issue struct { ComicVineID string; Number string; Calculated float64; Title string; Date *time.Time; Annual bool; Monitored bool; Status IssueStatus /* Wanted|Skipped|Snatched|Downloaded|Archived|Ignored|Failed */; File *ComicFile /* ComicFormat + ComicInfo fields */ }
```

Kubernetes mapping suggestion: `QualityProfile` (cluster-scoped, built-ins `hd-bluray-web`, `uhd-bluray-web`, `remux-web-1080p`, `remux-web-2160p`, `web-1080p`, `web-2160p`, `anime-remux-1080p`, `music-lossless`, `music-standard`, `ebook`, `audiobook`, `comic`), `CustomFormat` **not** a CRD (embedded catalogue versioned with the binary; expose read-only via API), `Movie`/`Series`/`Artist`/`Author`/`ComicVolume` namespaced CRDs with the specs above; `status` carries availability/status/files; a `MediaFile` sub-resource (or ConfigMap-free status) records `Quality`, matched `Formats` (ids), `Score`, `MediaInfo`.

---

## 15. Go libraries (versions verified with `go list -m -versions … | tail -1` on 2026-09-18)

| Module | Version | Purpose | Notes |
| --- | --- | --- | --- |
| github.com/dlclark/regexp2 | v1.12.0 | .NET-compatible backtracking regex | Compiled 2791/2791 TRaSH regexes; stdlib rejects 157. Use `regexp2.IgnoreCase`, set `MatchTimeout`. |
| github.com/moistari/rls | v0.6.0 | Release-name tokenizer (movie/series/episode/music/book/comic; codec/HDR/audio/channels/group/edition/cut/language) | Good for CF hints and type detection; weak on anime `[Group]`, audiobooks, `REAL`/`v2` revisions. Used by autobrr. |
| github.com/razsteinmetz/go-ptn | v1.0.0 | Simpler parse-torrent-name port (`ptn.Parse(name) (*TorrentInfo, error)`) | Not evaluated at runtime; fallback only. |
| gopkg.in/vansante/go-ffprobe.v2 | v2.3.1 | ffprobe JSON wrapper (streams, side data) for MediaInfo/HDR detection | HDR/DoVi classification must be written (colour primaries/transfer, `side_data_list` DOVI config). |
| github.com/asticode/go-astiav | v0.42.0 | libav cgo bindings | Alternative to shelling out; heavier build. |
| github.com/asticode/go-astisub | v0.45.0 | Subtitle formats (SRT/SSA/WebVTT/TTML) | Subtitle service. |
| go.uploadedlobster.com/musicbrainzws2 | v0.19.0 | MusicBrainz WS2 client | Artist/release-group/release/recording lookups (Lidarr uses its own metadata proxy on top of MB). |
| github.com/cyruzin/golang-tmdb | v1.9.4 | TMDB client | Movies + TV metadata (Radarr uses a TMDB proxy; Sonarr uses TVDB via Skyhook). |
| github.com/bogem/id3v2/v2 | v2.1.4 | ID3 read/write | Music/audiobook tagging. |
| github.com/dhowden/tag | (untagged, pseudo-version only) | Read tags MP3/M4A/FLAC/OGG | Read-only identification of audio files. |
| github.com/gabriel-vasile/mimetype | v1.4.15 | MIME sniffing | Classify cbz/epub/mkv on import. |
| github.com/goccy/go-yaml | v1.19.2 | YAML | If profiles are authored as YAML. |
| github.com/lithammer/fuzzysearch | v1.1.8 | Fuzzy title matching | Mapping parsed titles to inventory items. |
| github.com/pemistahl/lingua-go | v1.4.0 | Language detection | Subtitle/audio language sanity checks (optional). |
| github.com/recyclarr/recyclarr | v8.7.2+incompatible (C#, not importable) | Reference implementation of TRaSH→*arr sync semantics | Read for score-set / cf-group resolution rules only. |
| github.com/autobrr/autobrr | v1.86.0 (app, not a lib) | Reference Go release-filter engine on top of rls | Shows practical rls usage. |
| github.com/pioz/tvdb, github.com/ryanbradynd05/go-tmdb, github.com/michiwend/gomusicbrainz | untagged | Older clients | Avoid; unversioned. |

---

## 16. Recommendations (short form; see structured output for the full list)

1. Key qualities by `(Source, Resolution, Modifier)`; ship one canonical table (Radarr's) and a Sonarr-name alias map.
2. Vendor the TRaSH JSON (cf, cf-groups, quality-profiles, quality-size) as `embed`-ed test fixtures and generate the Go catalogue from it, keeping `trash_id` for provenance and a sync job to diff upstream.
3. Evaluate CF regexes with regexp2 + timeout; pre-filter by cheap conditions (Source/Resolution/Modifier) before running title regexes.
4. Implement the upgrade decision table and comparer order verbatim; unit-test with Radarr's `UpgradableSpecificationFixture` cases.
5. Parser: port the *arr `SourceRegex`, `ResolutionRegex`, `RemuxRegex`, `BRDISKRegex`, `ReleaseGroupRegex`, `EditionRegex`, proper/repack/version/real regexes; use `rls` only for hint tags. Own the anime and audiobook paths.
6. Inventory CRDs per media kind share `Monitoring`, `QualityProfileRef`, `RootFolder`; movie adds `MinimumAvailability`; series adds `SeriesType` + per-season monitor; music/book add `MetadataProfileRef` and release/edition selection (`AnyReleaseOk`/`AnyEditionOk`); comics add `SpecialVersion` and issue `Status`.
7. MediaInfo/HdrFormat vocabulary is the shared contract between inventory and the transcoder ("HEVC 10-bit + AAC, preserve HDR10/DV metadata, never transcode Remux tiers unless policy says so").
