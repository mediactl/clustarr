# Clustarr research note: Prowlarr indexer model, Cardigann, Torznab/Newznab, Go libraries, "Elasticsearch-like" cache

Date: 2026-09-18. Everything below was verified against Prowlarr `develop` sources, the Prowlarr/Indexers repo (schema v11), the Torznab 1.3 draft spec, the Newznab API reference, the Jackett wiki/README, and `go list -m -versions` on the dev box (Go 1.27). Where something is inferred rather than verified it is marked *(inferred)*.

Companion files in this directory (downloaded raw, public):
- `schema-v11.json` – the Prowlarr/Indexers v11 JSON schema (1024 lines)
- `1337x.yml` – an HTML-scraping public-tracker definition (301 lines)
- `0dayfiles-api.yml` – a JSON-API (UNIT3D) private-tracker definition (216 lines)

---

## 1. Executive summary

- **Prowlarr is three things**: (1) a *definition engine* ("Cardigann") that turns YAML files into HTTP request generators + HTML/JSON/XML parsers, (2) a *Newznab/Torznab façade* (`/{indexerId}/api`, `/{indexerId}/download`) that downstream apps talk to, and (3) a *fan-out search service* (`ReleaseSearchService`) plus per-indexer health/limit bookkeeping. Clustarr's indexer service should mirror this split: `definition` (engine) / `torznab` (protocol) / `search` (fan-out + cache) / `status` (health, limits, backoff).
- **Cardigann is Go-native by origin.** The format was created by the Go project `cardigann/cardigann` (MIT, 2016–2021); its templates *are* Go `text/template` syntax (`{{ .Config.x }}`, `{{ if }}`, `{{ range }}`, `{{ join }}`, `{{ re_replace }}`). Prowlarr/Jackett re-implement a regex subset in C#. In Go we can execute the real thing with a `FuncMap`. The corpus today is **547 YAML definitions at schema v11** served from `https://indexers.prowlarr.com/master/11` (JSON listing, `package.zip`, and per-id files).
- **The protocol is small.** Torznab = Newznab RSS 2.0 + `<torznab:attr name= value=>` items, `t=caps|search|tvsearch|movie|music|book`, comma-joined `cat`, external IDs (`imdbid`, `tmdbid`, `tvdbid`, `tvmazeid`, `traktid`, `doubanid`, `rid`), `season`/`ep`, `limit`/`offset`, `apikey`, `extended=1`. Errors are `<error code="…" description="…"/>` with HTTP 200 (Newznab) – Prowlarr additionally uses HTTP 410 (indexer disabled) and 429 + `Retry-After` (limits).
- **Prowlarr aggregation is simple**: `Task.WhenAll` over enabled indexers, **no per-indexer timeout** other than the 100 s HTTP default, dedup by `Guid` keeping the lowest `IndexerPriority`, results capped at `MaxNumResultsPerQuery = 1000`. Prowlarr has **no** aggregate Torznab endpoint (Jackett does: `/api/v2.0/indexers/all/results/torznab`, plus a filter grammar). Clustarr should do better: bounded fan-out with per-indexer deadlines, dedup by infohash *and* guid, and streaming/partial results.
- **Go library verdict**: nothing off-the-shelf is adoptable as a dependency. `Kcchouette/cardigann-go v0.4.0` is a serious, recent v11 engine but **LGPL-3.0** and incomplete (no `case`/`remove`/`default`/`dateheaders`/`count`, ~22 of 25 filters). `mrobinsn/go-newznab v1.2.0` (MIT) is stale (2021), Newznab-centric and not context-aware. `autobrr/pkg/torznab` lives inside a GPL-2.0 application module. **Recommendation: write our own `pkg/cardigann` and `pkg/torznab` (Apache-2.0)** using `goquery v1.13.0`, `tidwall/gjson v1.19.0`, `antchfx/xmlquery v1.5.1`, `gopkg.in/yaml.v3`, `text/template`, `x/time/rate v0.16.0`, `x/net/proxy v0.59.0`, and `anacrolix/torrent/metainfo` for magnet parsing; use cardigann-go and Prowlarr's `CardigannBase.cs`/`CardigannParser.cs` as behavioural references.
- **"Elasticsearch-like"** should mean: a *continuously ingested, shared, TTL'd, deduplicated index of releases* (RSS/`t=search` with empty `q` every N minutes + every interactive result), queryable by parsed attributes (title tokens, resolution, source, codec, group, size, seeders, age, category, external IDs). For a distributed K8s deployment the index must be shared, so: **Postgres FTS (tsvector/GIN + pg_trgm) as the primary index**, `moistari/rls v0.6.0` to parse release names into structured columns, `NATS KV` (bucket TTL) for the Jackett-style per-(indexer, query-hash) result cache, and **bleve v2.6.1 only as an optional per-pod accelerator** (it has no TTL and is per-process).

---

## 2. Prowlarr's indexer model (C# → what to borrow)

### 2.1 `IndexerDefinition : ProviderDefinition`
Verified via deepwiki + `IndexerDefinition.cs`:

| Property | Type | Notes |
|---|---|---|
| Id, Name | int, string | |
| Implementation | string | `"Cardigann"`, `"Newznab"`, `"Torznab"`, or a native class (e.g. `PassThePopcorn`) |
| ConfigContract | string | settings type name |
| Settings | object | for Cardigann: `CardigannSettings` = `ExtraFieldData` dict + `BaseSettings` |
| Enable, Redirect | bool | Redirect = hand the download URL to the app instead of proxying |
| Priority | int | 1 (highest) … 50; used as the dedup tiebreaker |
| Privacy | enum | `public`, `private`, `semiPrivate` |
| Protocol | enum | `usenet`, `torrent` |
| SupportsRss, SupportsSearch, SupportsRedirect, SupportsPagination | bool | |
| Capabilities | IndexerCapabilities | see 2.3 |
| Tags | []int | drive proxy selection and app-sync |
| AppProfileId, DownloadClientId | int | |
| Language, Encoding, Description, DefinitionName | string | |
| IndexerUrls, LegacyUrls | []string | `links` / `legacylinks` |
| Added | DateTime | |

`IndexerBaseSettings` (embedded in every indexer's settings): `QueryLimit int?`, `GrabLimit int?`, `LimitsUnit enum {Day=0, Hour=1}`.

Upstream Newznab/Torznab settings (`NewznabSettings.cs`): `BaseUrl`, `ApiPath` (default `/api`), `ApiKey`, `AdditionalParameters` (validated against `(&.+?\=.+?)+`), `VipExpiration` (date, must be future), `BaseSettings`. API key is only *required* for an allow-list of hosts (nzbs.org, nzb.life, dognzb.cr, …).

### 2.2 `IndexerBase` / `HttpIndexerBase`
- `RateLimit` default `TimeSpan.FromSeconds(2)`; for Cardigann it is raised to the definition's `requestDelay` when that is larger.
- `MaxNumResultsPerQuery = 1000` (const), `PageSize` per indexer (Newznab = 100).
- Retry: Polly `RetryStrategy`, exponential backoff + jitter, **max 2 retries**, only on 5xx/timeouts.
- Exception → status mapping in `FetchReleases`: `TooManyRequestsException` → `RecordFailure(id, RetryAfter ≥ 1h)`; `RequestLimitReachedException` → 1 h min backoff; `IndexerAuthException`, `CloudFlareProtectionException`, `HttpException`, `WebException` (DNS/connect → `RecordConnectionFailure`), `TaskCanceledException` (timeout) → `RecordFailure`. Success → `RecordSuccess`.
- 401 → re-login; cookies re-stored with **30-day** expiry.
- `CleanupReleases`: set `Guid` if empty (DownloadUrl → MagnetUrl → InfoUrl), stamp `IndexerId/Indexer/IndexerPriority/IndexerPrivacy/DownloadProtocol`, derive flags (`DownloadVolumeFactor==0 → FreeLeech`, `==0.5 → HalfLeech`, `UploadVolumeFactor==0 → NeutralLeech`, `==2 → DoubleUpload`, `Scene → Scene`), then `DistinctBy(Guid)`.
- `FilterReleasesByQuery`: splits the term on non-word chars, drops stopwords `and the an of`, keeps releases whose Title/Description contain the terms (used by indexers that cannot filter server-side).
- HTTP defaults (`ManagedHttpDispatcher.cs`): **100 s request timeout** when unset, `SocketsHttpHandler{AutomaticDecompression=GZip|Brotli, UseCookies=false, AllowAutoRedirect=false, MaxConnectionsPerServer=12, PooledConnectionLifetime=10min}`.

### 2.3 `IndexerCapabilities`
```
LimitsMax int?, LimitsDefault int?, SupportsRawSearch bool
Categories  IndexerCapabilitiesCategories   // tracker↔newznab mapping + tree
SearchParams      []SearchParam      // {Q}
TvSearchParams    []TvSearchParam    // {Q, Season, Ep, ImdbId, TvdbId, RId, TvMazeId, TraktId, TmdbId, DoubanId, Genre, Year}
MovieSearchParams []MovieSearchParam // {Q, ImdbId, TmdbId, ImdbTitle, ImdbYear, TraktId, Genre, DoubanId, Year}
MusicSearchParams []MusicSearchParam // {Q, Album, Artist, Label, Year, Genre, Track}
BookSearchParams  []BookSearchParam  // {Q, Title, Author, Publisher, Genre, Year}
Flags []IndexerFlag
computed: SearchAvailable, TvSearchAvailable, MovieSearchAvailable, MusicSearchAvailable, BookSearchAvailable
```
Caps XML emitted by `GetXDocument()`:
```xml
<caps>
  <server title="Prowlarr"/>
  <limits default="100" max="100"/>
  <searching>
    <search available="yes" supportedParams="q" searchEngine="raw"/>   <!-- searchEngine only when SupportsRawSearch -->
    <tv-search available="yes" supportedParams="q,season,ep,imdbid,tvdbid"/>
    <movie-search available="yes" supportedParams="q,imdbid,tmdbid"/>
    <music-search available="no" supportedParams="q"/>
    <audio-search available="no" supportedParams="q"/>                 <!-- legacy alias -->
    <book-search available="no" supportedParams="q"/>
  </searching>
  <categories><category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/></category>…</categories>
  <tags><tag name="freeleech" description="Download doesn't count toward ratio"/>…</tags>
</caps>
```

`IndexerCapabilitiesCategories` (important algorithm):
- `_categoryMapping []CategoryMapping{TrackerCategory string, NewznabCategory IndexerCategory, TrackerCategoryDesc string}` and `_torznabCategoryTree []IndexerCategory`.
- `AddCategoryMapping(trackerCat, newznabCat, desc)`: if `desc` given, also creates a **custom category `100000 + trackerCatInt`** (string tracker ids are SHA1-hashed to 0..65535 first) named `desc`, so apps can target tracker-native categories.
- `ExpandTorznabQueryCategories(cats, mapChildrenToParent)`: parent (e.g. 2000) expands to all its subcats; ids ≥ 100000 are not expanded.
- `MapTorznabCapsToTrackers(query)`: expanded newznab cats → distinct tracker category ids (what goes in `{{ .Categories }}`).
- `MapTrackerCatToNewznab(trackerCat)` / `MapTrackerCatDescToNewznab(desc)`: reverse, case-insensitive; a result gets **both** the standard cat and the custom 100000+ cat.
- `SupportedCategories(cats)`: filters a query's cats to those the indexer has (used to skip indexers in fan-out).

### 2.4 `IndexerFlag` (Prowlarr/Indexers/IndexerFlag.cs)
`Internal` ("Uploader is an internal release group"), `Exclusive`, `FreeLeech` ("Download doesn't count toward ratio"), `NeutralLeech` ("Download and upload doesn't count toward ratio"), `HalfLeech` ("Release counts 50% to ratio"), `Scene` ("Uploader follows scene rules"), `DoubleUpload` ("Seeding counts double for release"). Native indexers add e.g. `Golden`/`Approved` (PassThePopcorn). Flags are emitted as `<torznab:attr name="tag" value="freeleech"/>`-style tags and in caps `<tags>`.

### 2.5 `ReleaseInfo` / `TorrentInfo` (Parser/Model)
```
ReleaseInfo:
  Guid, Title, Description string; Size long?; DownloadUrl, InfoUrl, CommentUrl string
  IndexerId int; Indexer string; IndexerPriority int; IndexerPrivacy IndexerPrivacy; DownloadProtocol DownloadProtocol
  Grabs int?, Files int?
  TvdbId, TvRageId, ImdbId, TmdbId, TraktId, TvMazeId, DoubanId int; Year int
  Author, BookTitle, Publisher, Artist, Album, Label, Track string
  PublishDate DateTime; PosterUrl, Origin, Source, Container, Codec, Resolution string
  Genres, Languages, Subs []string; Categories []IndexerCategory; IndexerFlags set<IndexerFlag>
  Age (days), AgeHours, AgeMinutes (computed)
TorrentInfo : ReleaseInfo:
  MagnetUrl, InfoHash string; Seeders, Peers int?; MinimumRatio double?; MinimumSeedTime long?
  DownloadVolumeFactor, UploadVolumeFactor double?; Scene bool?; SeedConfiguration TorrentSeedConfiguration
  static GetSeeders(ReleaseInfo), GetPeers(ReleaseInfo)
```
`Peers` in Cardigann = seeders + leechers (leechers are not stored separately; `NewznabResults.ToXml` emits `peers`, and Torznab `leechers` = peers − seeders).

### 2.6 Search criteria (`IndexerSearch/Definitions`)
```
SearchCriteriaBase: InteractiveSearch bool; IndexerIds []int; SearchTerm string; Categories []int; SearchType string
                    Limit, Offset, MinAge, MaxAge int?; MinSize, MaxSize long?; Source, Host string
                    computed: SearchQuery, IsRssSearch (SearchTerm blank), IsIdSearch, SanitizedSearchTerm
MovieSearchCriteria: ImdbId string, TmdbId, TraktId, DoubanId, Year int?, Genre string
TvSearchCriteria:    ImdbId string, Season int?, Episode string, TvdbId, TraktId, TmdbId, DoubanId, RId, TvMazeId, Year int?, Genre string
MusicSearchCriteria: Artist, Album, Label, Track, Genre string, Year int?
BookSearchCriteria:  Author, Title, Publisher, Genre string, Year int?
```
Sanitization: `\p{Pd}+` → `-`; `` [`´‘’] `` → `'`; keep only `[\p{L}\p{N}\s\-._()@/'\[\]+%]`.

`NewznabRequest` (the inbound query object, `IndexerSearch/NewznabRequest.cs`): `t, q, cat string; imdbid string; tmdbid, tvdbid, rid, tvmazeid, traktid, doubanid, season int?; ep string; limit, offset, minage, maxage int?; minsize, maxsize long?; artist, album, track, label, author, title, publisher, genre string; year int?; extended, configured, source, host, server string`. `QueryToParams()` also parses `{tvdbid:12345}` / `{imdbid:tt0068646}` tokens embedded in `q` (the UI's interactive search syntax) and strips them.

---

## 3. Cardigann YAML definitions (schema v11)

### 3.1 Distribution and versioning
- Repo `Prowlarr/Indexers` → `definitions/v1 … v11/` each with `schema.json`; **v11 is active (547 `.yml` today)**, v10 deprecated, v1–v9 frozen. Synced daily from Jackett with a blocklist; validated with `validate.py` (Python 3.11+).
- Prowlarr fetches `https://indexers.prowlarr.com/{branch=master}/{version=11}` (JSON list of `CardigannMetaDefinition`: `id, file, implementation, name, protocol, description, type, language, links, settings, …`), `…/11/package.zip` (all YAML, extracted to `AppData/Definitions/`), `…/11/{id}` (single file). Users may drop files in `AppData/Definitions/Custom/`. YAML is deserialized with YamlDotNet `CamelCaseNamingConvention` + `IgnoreUnmatchedProperties`. Refresh on startup and on the `IndexerDefinitionUpdateCommand` task.
- Fallback: a Prowlarr build asks for its own max version; older builds keep reading older directories.

| Ver | Prowlarr | What changed |
|---|---|---|
| v1 | – | base YAML |
| v2 | – | regex removal for `size`, multiple download selectors, optional selectors, `testlinktorrent`, infohash links, `allowrawsearch` |
| v3 | – | API/JSON support (`response.type`), `imdb:` → `imdbid:`, description optional |
| v4 | 0.2.0.1678 | `tmdbid`, `genre`, `traktid`, `categorydesc` |
| v5 | 0.2.0.1678 | JSON filters |
| v6 | 0.4.2.1879 | `doubanid`; `tmdbid` in tv-search |
| v7 | 0.4.4.1947 | `publisher`, `year`, `genre` query params |
| v8 | 1.1.0.2322 | `htmlencode`/`htmldecode` filters |
| v9 | 1.4.0.3230 | `allowEmptyInputs`, `default` values, `missingAttributeEqualsNoResults` |
| v10 | 1.18.0.4543 | `info_cookie`, `info_flaresolverr`, `info_useragent` setting types; conditional login validation |
| v11 | 1.20.0.4590 | `info_category_8000`; optional `selectorinputs`/`getselectorinputs`; 170+ language codes; SelectorBlock dependency rules |

### 3.2 Root fields (schema v11; required: `caps, description, encoding, id, language, links, name, search, type`)
`id`, `replaces []string`, `name`, `description`, `language` (enum of locales), `type` ∈ `public|semi-private|private`, `encoding`, `followredirect bool`, `testlinktorrent bool`, `requestDelay number` (seconds; e.g. 1337x uses 3), `links []uri` (first is default; must end with `/`), `legacylinks []uri`, `certificates []string` (SHA-1 fingerprints), `caps`, `settings`, `login`, `search`, `download`.

### 3.3 `settings[]` (required `name`, `type`)
`type` ∈ `info | text | password | checkbox | select | info_category_8000 | info_cookie | info_flaresolverr | info_useragent` (Jackett additionally has `multi-select`; Prowlarr adds a synthetic `cardigannCaptcha` when `login.captcha` exists). Fields: `label`, `default (string|int|bool)`, `options {key:label}`, `defaults []string`. Template exposure (`GetBaseTemplateVariables`): text/password → string; checkbox → `.True` sentinel or null; select → the option **key**; `info_*` → no-op. Always present: `.Config.sitelink` (chosen base URL), `.True`, `.False`, `.Today.Year`.

### 3.4 `caps` (required `modes`; one of `categories` | `categorymappings`)
- `categorymappings[]{id int|string, cat <IndexerCategories enum>, desc string, default bool}`; `cat` values are the standard names (`Movies`, `Movies/HD`, `TV/Anime`, `Audio/Lossless`, `Books/Comics`, `Other/Misc`, … full enum in §4.4).
- `modes{ search: [q]; tv-search: [q, season, ep, imdbid, tvdbid, tmdbid, tvmazeid, traktid, doubanid, year, genre]; movie-search: [q, imdbid, tmdbid, traktid, doubanid, year, genre]; music-search: [q, album, artist, label, track, year, genre]; book-search: [q, title, author, publisher, year, genre] }` (each is the *allowed* superset).
- `allowrawsearch bool`, `allowtvsearchimdb bool`.

### 3.5 `login`
`method` ∈ `form | post | cookie | get | oneurl` (default `form`), `path`, `submitpath`, `form` (CSS selector of the form), `inputs {k: v|number|bool}`, `selectors bool`, `selectorinputs {name: SelectorBlock}` (values scraped from the login page, e.g. CSRF tokens), `getselectorinputs {…}` (same, for GET query), `cookies []string`, `headers {k: [v]}`, `captcha {type: image|text, selector, input}`, `error [] {path?, selector, message: SelectorBlock}`, `test {path, selector?}`.
Semantics: after login, `CheckIfLoginIsNeeded` returns true on cross-domain redirect, HTTP error, or when `test.selector` finds nothing in the `test.path` page → re-login; cookies persisted 30 days.

### 3.6 `search` (required `rows`, `fields`; one of `paths` | `path`)
- `paths[] {path (template), method get|post, followredirect, categories [int|string] (leading "!" = exclude), inputs {k: v}, inheritinputs bool, queryseparator, response {type: json|xml, noResultsMessage}}`. A path is selected when its `categories` intersect the mapped query categories (inverted with `!`); **each matching path yields one request**.
- `inputs {k: v}` merged with the path's; `$raw` is appended verbatim to the query string; empty values skipped unless `allowEmptyInputs`.
- `headers {k: [v]}`, `keywordsfilters [FilterBlock]` (applied to the combined keywords → `.Keywords`), `error [ErrorBlock]`, `preprocessingfilters [FilterBlock]` (on the raw body).
- `rows {selector, attribute, after int, dateheaders SelectorBlock, count SelectorBlock, optional, multiple, missingAttributeEqualsNoResults, case, remove, text, filters [andmatch|strdump]}`. `after: N` merges the N following sibling rows into the row; `dateheaders` picks a date from the nearest preceding header row when the row has no date; `andmatch` drops rows whose title doesn't contain all keywords (arg = max length to compare); JSON: `selector` is a path (`data.torrents`), `attribute` descends one more level, `multiple` = the selected node is itself a list, `count.selector` short-circuits empty results.
- `fields` (required `title`, `size`, `seeders`; one of `category|categorydesc`; any of `download|infohash|magnet`). Allowed names: `title, description, category, categorydesc, download, magnet, infohash, details, comments, size, leechers, seeders, date, files, grabs, downloadvolumefactor, uploadvolumefactor, minimumratio, minimumseedtime, imdb, imdbid, tmdbid, rageid, tvdbid, tvmazeid, traktid, doubanid, poster, genre, year, author, booktitle, publisher, album, artist, label, track`, any `_custom` (underscore prefix), and any of those with a `_suffix` (e.g. `title_default`, `date_year`) as intermediate variables; `|append`/`|noappend` modifiers on title/description/category. **Fields are evaluated in file order** and each value is stored as `.Result.<name>` for later templates (see `title: text: "{{ if .Result.title_optional }}…"` in `1337x.yml`).
- `SelectorBlock {selector, attribute, optional, default (requires optional), case {cssSelector: value, "*": fallback}, remove (selector to strip first), text (literal/template instead of selector), filters []}`.
- `FilterBlock {name, args (string|int|[…])}`, names: `querystring, timeparse, dateparse, regexp, re_replace, split, replace, trim, prepend, append, tolower, toupper, urldecode, urlencode, htmldecode, htmlencode, timeago, reltime, fuzzytime, validfilename, diacritics, jsonjoinarray, hexdump, strdump, validate`.

Filter semantics (from `CardigannBase.ApplyFilters`):
| filter | args | behaviour |
|---|---|---|
| querystring | `param` | value of query-string param in the (URL) value |
| regexp | `pattern` | returns capture group 1 |
| re_replace | `[pattern, replacement]` | regex replace; replacement is templated; `$1` groups |
| replace | `[from, to]` | plain replace (templated) |
| split | `[sep, index]` | negative index from end |
| trim | `[cutset?]` | |
| prepend / append | `text` (templated) | |
| tolower / toupper | – | |
| dateparse / timeparse | `format` | **.NET custom format** (`yyyy-MM-dd HH:mm:ss zzz`, `MMM. d yy`, `htt MMM. d`, `ddd dd MMM`); Prowlarr's `ParseDateTimeGoLang` first rewrites Go layout tokens (`2006`,`01`,`Jan`,`15`,`04`,`05`,`-0700`,`PM`) into .NET, so both dialects appear in the corpus → Go needs a .NET-format→Go-layout translator |
| timeago / reltime | – | "2 hours ago", "3 days" → time |
| fuzzytime | – | "Today 12:25", "Yesterday", "now" → time |
| validfilename | – | strip `?<>:\*` etc. |
| diacritics | `replace` | NFD→strip marks→NFC |
| jsonjoinarray | `[jsonpath, sep]` | join JSON array |
| urldecode/urlencode/htmldecode/htmlencode | – | |
| validate | `"a,b,c"` | keep only tokens present in the allow-list (genres) |
| hexdump / strdump | – | debug logging only |

Template dialect (`ApplyGoTemplateText` in C# supports exactly): `{{ .Var }}`, `{{ if .X }}…{{ else }}…{{ end }}`, `{{ range .Categories }}…{{.}}…{{ end }}` / `{{ range $i, $e := .X }}`, `{{ join .Categories "," }}`, `{{ re_replace .X "pat" "repl" }}`, `and`, `or`, `eq`, `ne`. Variables: `.Config.*`, `.Keywords` (post-filter), `.Query.Keywords` (pre-filter), `.Query.Q`, `.Query.Type` (`search|tv-search|movie-search|music-search|book-search`), `.Query.IMDBID` (`tt…`), `.Query.IMDBIDShort` (digits), `.Query.TVDBID`, `.Query.TMDBID`, `.Query.TVMazeID`, `.Query.TraktID`, `.Query.DoubanID`, `.Query.Season`, `.Query.Ep`/`.Query.Episode`, `.Query.Year`, `.Query.Genre`, `.Query.Album/.Artist/.Label/.Track`, `.Query.Author/.Title/.Publisher`, `.Categories` (tracker ids), `.Result.*`, `.DownloadUri.Query.*` (download block), `.True/.False`, `.Today.Year`. Keywords = `Q` + (series/movie/year/`SxxEyy` when the mode lacks id params).

### 3.7 `download`
`method`, `before {path|pathselector SelectorField, method, inputs, queryseparator}` (a request to make first, e.g. "thanks"), `selectors [] {selector, attribute, usebeforeresponse, filters}` (tried in order on the details page; first hit wins – see 1337x primary/fallback), `infohash {usebeforeresponse, hash SelectorField, title SelectorField}` (build a magnet from hash+title+trackers), `headers`.

### 3.8 Parser → release mapping (`CardigannParser.ParseFields`)
`title`→Title; `details`→InfoUrl (absolutized against sitelink); `comments`→CommentUrl; `download`→DownloadUrl, or MagnetUrl when it starts with `magnet:`; `magnet`→MagnetUrl; `infohash`→InfoHash; `size`→`ParseUtil.GetBytes` ("1.5 GB", "700 MiB", "1,234 KB"); `seeders`/`leechers`→Seeders, Peers=seeders+leechers; `date`→`DateTimeUtil.FromUnknown` (fallback now); `category`/`categorydesc`→Categories via mapping (missing mapping + `info_category_8000` → `8000 Other`); `grabs`, `files`; `downloadvolumefactor`/`uploadvolumefactor` (default 1.0 each); `minimumratio`, `minimumseedtime`; `description`; `imdbid` (parses `tt\d+` or digits), `tmdbid`, `tvdbid`, `rageid`, `tvmazeid`, `traktid`, `doubanid`; `poster`→PosterUrl; `genre`→Genres split on `,`/`|`; `year`, `author`, `booktitle`, `publisher`, `artist`, `album`, `label`, `track`. Non-optional field with no match → `CardigannException` (row dropped / indexer error); optional → `default` or skipped.

### 3.9 Real examples (abridged; full files alongside this note)
HTML public tracker (`1337x.yml`): `requestDelay: 3`; 4 search paths (Movies/TV/Music/Other pages) whose `path` is a template on `.Keywords` and `.Config.sort`; `rows.selector: tr:has(a[href^="/torrent/"])…`; `title` built from `title_optional`/`title_default` with a chain of `re_replace` "cleanup for Sonarr" filters; three `date_*` optional variants (`dateparse "htt MMM. d"`, `"MMM. d yy"`, `fuzzytime`) merged by `date: text: "{{ or .Result.date_year .Result.date_years .Result.date_today }}"`; `download.selectors` with primary/fallback picked by `.Config.primarydownloadlink`.

JSON private tracker (`0dayfiles-api.yml`, UNIT3D): `login.method: get` against `api/torrents` with `error` selectors; `search.headers: Authorization: ["Bearer {{ .Config.apikey }}"]`; `inputs.$raw: "{{ range .Categories }}&categories[]={{.}}{{end}}"`, `name: "{{ .Keywords }}"`, `seasonNumber: "{{ .Query.Season }}"`, `imdbId: "{{ .Query.IMDBIDShort }}"`, `perPage: 100`; `rows.selector: data`, `attribute: attributes`; fields via JSON paths (`meta.poster`, `files[0].name`) and `case` maps (`freeleech: {0%: 1, 25%: 0.75, …, "*": 0}`); `minimumseedtime: text: 604800`.

---

## 4. Torznab / Newznab protocol

### 4.1 Functions and parameters
Common: `t` (required), `apikey`, `cat` (comma list), `limit`, `offset`, `extended=1` (return all attrs), `attrs` (comma list of attrs), `o=json|xml` (Newznab; Prowlarr only XML), `maxage` (days), `del`, plus Prowlarr extras `minage, maxage, minsize, maxsize`.

| t | extra params (Torznab spec / Prowlarr) |
|---|---|
| `caps` | – (apikey usually optional) |
| `search` | `q`, `tag` (spec), `group` (Newznab) |
| `tvsearch` | `q, season, ep, rid, tvdbid, tvmazeid, traktid, imdbid, tmdbid, doubanid, year, genre` |
| `movie` | `q, imdbid, tmdbid, traktid, doubanid, year, genre` |
| `music` (spec alias `audio`) | `q, album, artist, label, track, year, genre` |
| `book` | `q, title, author, publisher, year, genre` |
| Newznab-only | `details(id)`, `getnfo(id, raw)`, `get(id, del)`, `cartadd/cartdel(id)`, `comments(guid)`, `commentadd(guid,text)`, `user(username)`, `register(email)` |

Prowlarr → upstream request (`NewznabRequestGenerator`): `{BaseUrl}{ApiPath}?t={type}&extended=1&cat={cats}&apikey={key}&{params}`, `PageSize = 100`, each id param gated by caps (`TvSearchImdbAvailable`, …); **if no id param applies it falls back to `t=search&q=`**; `q` is only added when the mode supports `q`.

### 4.2 Response XML
```xml
<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"
     xmlns:torznab="http://torznab.com/schemas/2015/feed"
     xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <atom:link rel="self" type="application/rss+xml"/>
    <title>Prowlarr</title>
    <item>
      <title>Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP</title>
      <guid>https://tracker/details/123</guid>          <!-- mandatory, unique per indexer -->
      <link>https://tracker/download/123</link>         <!-- or magnet when preferMagnetUrl -->
      <comments>https://tracker/details/123#comments</comments>
      <pubDate>Wed, 17 Sep 2026 10:11:12 +0000</pubDate>   <!-- RFC 2822 -->
      <size>1234567890</size>
      <description>…</description>
      <category>2000</category><category>2040</category>
      <enclosure url="https://tracker/download/123" length="1234567890" type="application/x-bittorrent"/> <!-- usenet: application/x-nzb -->
      <torznab:attr name="category" value="2000"/>
      <torznab:attr name="category" value="2040"/>
      <torznab:attr name="seeders" value="12"/>
      <torznab:attr name="peers" value="15"/>
      <torznab:attr name="infohash" value="0123…abcd"/>   <!-- lower-case hex -->
      <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:…"/>
      <torznab:attr name="downloadvolumefactor" value="0"/>
      <torznab:attr name="uploadvolumefactor" value="1"/>
      <torznab:attr name="minimumratio" value="1.0"/>
      <torznab:attr name="minimumseedtime" value="604800"/>
      <torznab:attr name="imdb" value="0133093"/>          <!-- Prowlarr pads to 7 digits; also emits imdbid -->
      <torznab:attr name="tmdbid" value="603"/>
      <torznab:attr name="tvdbid" value="…"/> <torznab:attr name="rageid"/> <torznab:attr name="tvmazeid"/> <torznab:attr name="traktid"/> <torznab:attr name="doubanid"/>
      <torznab:attr name="tag" value="freeleech"/>         <!-- IndexerFlags -->
      <torznab:attr name="grabs" value="7"/> <torznab:attr name="files" value="3"/>
      <torznab:attr name="poster" value="https://…jpg"/>
      <torznab:attr name="genre" value="Action, Sci-Fi"/> <torznab:attr name="year" value="2024"/>
      <torznab:attr name="language" value="English"/> <torznab:attr name="subs" value="English, Spanish"/>
      <torznab:attr name="author"/> <torznab:attr name="booktitle"/> <torznab:attr name="publisher"/>
      <torznab:attr name="artist"/> <torznab:attr name="album"/> <torznab:attr name="label"/> <torznab:attr name="track"/>
      <torznab:attr name="prowlarrindexer" id="1" type="private" value="MyTracker"/>
    </item>
  </channel>
</rss>
```
Prowlarr rewrites `link`/`enclosure` to its own proxy `/{indexerId}/download?apikey=…&link=<encrypted>&file=<title>` (`DownloadMappingService`), unless `Redirect` is on; the download endpoint decrypts, records the grab (history + grab limit), sniffs magnet bytes → 302, otherwise streams with `application/x-bittorrent` / `application/x-nzb`.

### 4.3 Predefined attributes (Torznab 1.3 draft; `size` and `category` mandatory)
`size int, category int (repeatable), tag string (repeatable), guid, files int, poster (uploader), group (NNTP), team (release group), grabs int, seeders int, leechers int, peers int, infohash (lower hex), magneturl, seedtype (ratio|seedtime|both|either), minimumratio decimal, minimumseedtime decimal(seconds), downloadvolumefactor decimal, uploadvolumefactor decimal, password int (0 no,1 rar,2 inner), comments int, usenetdate, nfo int, info (nfo url), year int, coverurl, backdropurl, review, season int, episode int, rageid int, tvtitle, tvairdate, tvdbid int, tvmazeid int, genre, video, audio, resolution, framerate, language, subs, imdb (7-digit string), imdbscore, imdbtitle, imdbtagline, imdbplot, imdbyear, imdbdirector, imdbactors, artist, album, publisher, tracks, booktitle, publishdate, author, pages`. Prowlarr adds `imdbid`, `tmdbid`, `traktid`, `doubanid`, `prowlarrindexer`.

### 4.4 Categories (Newznab standard; Prowlarr `NewznabStandardCategory`)
```
0000 Other(Zed)  0010 Other/Misc 0020 Other/Hashed                       (Prowlarr-only; usually hidden)
1000 Console     1010 NDS 1020 PSP 1030 Wii 1040 XBox 1050 XBox 360 1060 Wiiware 1070 XBox 360 DLC 1080 PS3 1090 Other
                 1110 3DS 1120 PS Vita 1130 WiiU 1140 XBox One 1180 PS4
2000 Movies      2010 Foreign 2020 Other 2030 SD 2040 HD 2045 UHD 2050 BluRay 2060 3D 2070 DVD 2080 WEB-DL 2090 x265(Prowlarr)
3000 Audio       3010 MP3 3020 Video 3030 Audiobook 3040 Lossless 3050 Other 3060 Foreign
4000 PC          4010 0day 4020 ISO 4030 Mac 4040 Mobile-Other 4050 Games 4060 Mobile-iOS 4070 Mobile-Android
5000 TV          5010 WEB-DL 5020 Foreign 5030 SD 5040 HD 5045 UHD 5050 Other 5060 Sport 5070 Anime 5080 Documentary 5090 x265(Prowlarr)
6000 XXX         6010 DVD 6020 WMV 6030 XviD 6040 x264 6045 UHD 6050 Pack 6060 ImageSet 6070 Other 6080 SD 6090 WEB-DL
7000 Books       7010 Mags 7020 EBook 7030 Comics 7040 Technical 7050 Other 7060 Foreign
8000 Other       8010 Misc 8020 Hashed
100000+          indexer-specific custom categories (100000 + tracker id; cannot be used with Jackett's aggregate)
```
Clustarr mapping: movies→2000*, tv→5000*, music→3000* (audiobooks 3030), books/comics/manga→7000* (7020 ebook, 7030 comics), anime→5070; audiobooks live under Audio, not Books.

### 4.5 Errors
Newznab always answers HTTP 200 with `<error code="N" description="…"/>`:
`100 Incorrect user credentials · 101 Account suspended · 102 Insufficient privileges/not authorized · 103 Registration denied · 104 Registrations are closed · 105/106/107 registration errors · 200 Missing parameter · 201 Incorrect parameter · 202 No such function · 203 Function not available · 300 No such item / Item already exists · 500 Request limit reached (Torznab) · 501 Download limit reached (Torznab) · 900 Unknown error · 910 API Disabled`.
Prowlarr: `200` missing `t`, `201` bad `imdbid`, `202` unknown function, **HTTP 410** when the indexer is disabled, **HTTP 429 + `Retry-After: <seconds>`** when `AtQueryLimit`/`AtDownloadLimit`.

### 4.6 Jackett aggregate / filter endpoints (for feature parity ideas)
`/api/v2.0/indexers/all/results/torznab/api` (results capped at **1000**, slow indexers slow the whole response) and `/api/v2.0/indexers/<filter>/results/torznab` with grammar `type:public|private|semi-private`, `tag:<tag>`, `lang:<prefix>`, `test:passed|failed`, `status:healthy|failing|unknown`, operators `!` (not), `+` (and), `,` (or); e.g. `tag:group1,!type:private+lang:en`. Jackett result cache: `CacheEnabled=true`, `CacheTtl=2100 s`, `CacheMaxResultsPerIndexer=1000`, key = `indexerId` + SHA-256(JSON(query)), invalidated per indexer on config change, bypassed for tests. `FlareSolverrMaxTimeout=55000 ms`.

---

## 5. Aggregated search in Prowlarr (and what Clustarr should do instead)

`ReleaseSearchService` (verified source):
```csharp
Search(request, indexerIds, interactive) => request.t switch { "movie"→MovieSearch, "music"→MusicSearch, "tvsearch"→TvSearch, "book"→BookSearch, _→BasicSearch }
Dispatch(searchAction, criteria):
  indexers = _indexerFactory.Enabled()
  if (criteria.IndexerIds?.Count > 0)
     indexers = indexers.Where(i => ids.Contains(i.Definition.Id)
                              || (ids.Contains(-1) && i.Protocol == Usenet)
                              || (ids.Contains(-2) && i.Protocol == Torrent))
  // + filtered to indexers whose SupportedCategories intersect the query cats
  batch = await Task.WhenAll(indexers.Select(x => DispatchIndexer(searchAction, x, criteria)))
  return batch.SelectMany(x => x)
DeDupeReleases(releases) => releases.GroupBy(r => r.Guid).Select(g => g.OrderBy(v => v.IndexerPriority).First())
```
Observations: no per-indexer deadline (only the 100 s HTTP timeout and Polly's 2 retries), an exception in one indexer only drops that indexer, the merged list is capped at 1000, `Limit/Offset` are passed through to each indexer (so "offset" over an aggregate is not stable). Dedup is by `Guid` only — the same torrent on two trackers is *not* collapsed (different guids); Sonarr/Radarr later dedup by infohash when grabbing.

REST: `GET /api/v1/search?query=&type=search|tvsearch|movie|music|book&indexerIds=&categories=&limit=&offset=` → `[]ReleaseResource`; `POST /api/v1/search` `{indexerId, guid, downloadClientId?}` grabs; `POST /api/v1/search/bulk`.

**Clustarr design implications**
1. Fan-out with `errgroup` + per-indexer `context.WithTimeout` (e.g. 20–30 s, override per indexer), collect partial results, report per-indexer outcome (`ok|timeout|error|skipped(limit|disabled|no-caps)`), optionally stream via NATS subject per search id.
2. Dedup keys in order: `infohash` (torrent, lower hex, parsed from `magneturl` when absent with `metainfo.ParseMagnetUri`), then `(indexerId, guid)`; keep the candidate with the best `(priority, seeders)`; retain the list of `also_on` indexers for the UI.
3. Deterministic ordering (`publishDate desc` default; the caller sorts by quality later) and server-side `limit` after merge; do not forward `offset` unless a single indexer is targeted.
4. Skip indexers whose caps cannot serve the request (categories, mode, id params) exactly like Prowlarr's `SupportedCategories`.

---

## 6. Status, limits, backoff, rate limiting

- `ProviderStatusServiceBase`: `EscalationBackOff.Periods = [0, 60, 300, 900, 1800, 3600, 10800, 21600, 43200, 86400]` seconds. `RecordFailure(id, minimumBackOff)`: increments `EscalationLevel` (capped at `Periods.Length-1`) unless in the 15-minute startup grace (`MinimumTimeSinceStartup`), and keeps incrementing until the period ≥ `minimumBackOff`; `DisabledTill = now + Periods[level]`. `RecordSuccess`: level−1, clear `DisabledTill`. Status row: `ProviderId, InitialFailure, MostRecentFailure, EscalationLevel, DisabledTill` + `IndexerStatus`-specific `LastRssSyncReleaseInfo, Cookies, CookiesExpirationDate`.
- `IndexerLimitService`: window = 1 h (`LimitsUnit.Hour`) or 24 h; `AtQueryLimit` counts History events `IndexerQuery` + `IndexerRss` ≥ `QueryLimit`; `AtDownloadLimit` counts `ReleaseGrabbed` ≥ `GrabLimit`; `CalculateRetryAfter…` = seconds until the oldest event in the window ages out.
- Per-request pacing: `RateLimit` (2 s default / `requestDelay`) keyed per indexer host; 429 → `Retry-After` honoured (≥ 1 h).
- Clustarr: keep counters in a shared store (Postgres or NATS KV) not per pod; expose the same four numbers in `Indexer.status` (`queriesInWindow`, `grabsInWindow`, `escalationLevel`, `disabledUntil`) and emit K8s Events on escalation.

---

## 7. Indexer proxies

- Types: `FlareSolverr` (`Host` default `http://localhost:8191/`, `RequestTimeout` 60 s, valid 1..180), `Http` (`Host, Port, Username, Password`), `Socks4`, `Socks5` (same 4 fields). Each proxy has `Tags`.
- Selection (`IndexerHttpClient`): `proxies.Where(p => indexer.Tags ∩ p.Tags ≠ ∅)`; an indexer with **no tags gets no proxy**; a brand-new (unsaved, id 0) indexer defaults to the FlareSolverr proxy so tests work; at most one FlareSolverr, and it is ordered **last**; request goes through `Aggregate(PreRequest)` then response through `Aggregate(PostResponse)`.
- FlareSolverr protocol: `PreRequest` injects the cached User-Agent for the host; `PostResponse` detects a Cloudflare/DDoS-Guard challenge (403/503 + `cf-mitigated`/`server: cloudflare` markers *(inferred from Jackett)*), POSTs `{"cmd":"request.get"|"request.post","url":…,"maxTimeout":RequestTimeout*1000,"postData":…,"proxy":{"url","username","password"}}` to `/v1`, reads `{status,message,version,startTimestamp,endTimestamp,solution:{url,status,headers,response,cookies:[{name,value,domain,path,expires,httpOnly,secure,session}],userAgent}}`, caches the UA per host, injects cookies into the original request and re-executes it.
- Go: `golang.org/x/net/proxy` (SOCKS5 dialer; `proxy.FromURL`), `http.Transport.Proxy` for HTTP; FlareSolverr is a ~150-line client. Model proxies as an `IndexerProxy` CRD selected by label selector instead of Prowlarr's numeric tags.

---

## 8. Sync to apps (Prowlarr → Sonarr/Radarr/Lidarr/Readarr/Mylar/LazyLibrarian/Whisparr)

`ApplicationSettings`: `ProwlarrUrl`, `BaseUrl`, `ApiKey`, `SyncCategories []int`, `SyncLevel ∈ {disabled, addOnly, fullSync}`, `SyncRejectBlocklistedTorrentHashesWhileGrabbing`. `AppSyncProfile`: `EnableRss, EnableAutomaticSearch, EnableInteractiveSearch, MinimumSeeders`. Pushed indexer (`BuildSonarrIndexer`): `Name "{name} (Prowlarr)"`, `Implementation Newznab|Torznab`, `Priority`, fields `baseUrl={ProwlarrUrl}/{indexerId}/`, `apiPath=/api`, `apiKey=<Prowlarr key>`, `categories`, `animeCategories`, `animeStandardFormatSearch`, `minimumSeeders`, `seedCriteria.seedRatio/seedTime/seasonPackSeedTime`, `rejectBlocklistedTorrentHashesWhileGrabbing`, legacy `additionalParameters`; user-owned fields preserved on update: `DownloadClientId`, `SeasonSearchMaximumSingleEpisodeAge`.
Clustarr: internal consumers (inventory service) should call the search API/NATS directly; keep the per-indexer Torznab façade for third-party apps and for compatibility testing with Sonarr/Radarr.

---

## 9. Go libraries (versions verified with `go list -m -versions … | tail -1` on 2026-09-18)

| Module | Version | License | Verdict |
|---|---|---|---|
| github.com/Kcchouette/cardigann-go | v0.4.0 (pushed 2026-08-02) | **LGPL-3.0** | Best existing Go v11 engine; packages `definition, engine, selector (goquery/gjson/etree), template (text/template + FuncMap), template/filters (22 filters), login (form/cookie/get/oneurl), httpclient (x/time/rate, retries, proxy, cookie persister; 20 s timeout, 3 retries), torznab (XML writer; note it uses namespace `http://torznab.github.io/schemas/2015/feed`, not the canonical `torznab.com` one), defs (tarball fetch of Prowlarr/Indexers)`. Gaps: `Setting` lacks `options/defaults`; `Field` lacks `case/remove/default`; `Rows` lacks `attribute/multiple/count/dateheaders`; `Query` lacks TVMaze/Trakt/music/book params; `Release` lacks magnet/leechers/volume factors/flags; missing filters `hexdump, strdump, jsonjoinarray, validfilename, htmlencode, reltime`. **Reference only** (license + gaps). |
| github.com/cardigann/cardigann | v1.10.2 (last push 2021-02) | MIT | The origin of the format; Go `text/template`, goquery. Dead; useful for historical semantics only. |
| github.com/mrobinsn/go-newznab | v1.2.0 (last push 2021-06) | MIT | `newznab.New(baseURL, apikey, userID, insecure)`, `Capabilities()`, `SearchWithQuery/IMDB/TVDB/TVMaze/TVRage`, `LoadRSSFeed`, `DownloadNZB`; `NZB` struct has `Seeders/Peers/InfoHash/DownloadURL/IsTorrent`. No `context`, no music/book, partial attrs. Not recommended. |
| github.com/autobrr/autobrr (`pkg/torznab`) | v1.86.0 | GPL-2.0 | Good `FeedItem`/`Caps` field reference (`ProwlarrIndexer`, `JackettIndexer`, `Freeleech`, `DownloadVolumeFactor`…); cannot import into a non-GPL project. |
| github.com/PuerkitoBio/goquery | v1.13.0 | BSD-3 | CSS selectors over HTML (cascadia v1.3.5). Needs `:contains()`/`:has()` (cascadia supports both). |
| github.com/tidwall/gjson | v1.19.0 | MIT | JSON path selectors for `response.type: json` (not full JSONPath; enough for the corpus per cardigann-go) |
| github.com/antchfx/xmlquery | v1.5.1 | MIT | XPath over XML for `response.type: xml` (alternative: beevik/etree v1.8.0) |
| gopkg.in/yaml.v3 | v3.0.1 | MIT/Apache | definition parsing (Prowlarr definitions rely on lenient parsing: `args` may be scalar or list) |
| github.com/santhosh-tekuri/jsonschema/v6 | v6.0.3 | Apache-2.0 | validate definitions against the upstream `schema.json` at sync time |
| golang.org/x/time | v0.16.0 | BSD | `rate.Limiter` per indexer host |
| golang.org/x/net | v0.59.0 | BSD | `proxy` (SOCKS5), `html` |
| github.com/moistari/rls | v0.6.0 | MIT | release-name parser: `rls.ParseString(title)` → `Release{Type, Title, Year, Series, Episode, Resolution, Source, Codec, HDR, Audio, Channels, Language, Group, Container, …}` — feeds the search index columns and quality profile matching |
| github.com/anacrolix/torrent | v1.61.0 | MPL-2.0 | `metainfo.ParseMagnetUri` / `ParseMagnetV2Uri` → `Magnet{InfoHash, Trackers, DisplayName, Params}`; `metainfo.Load` for `.torrent` bytes |
| github.com/blevesearch/bleve/v2 | v2.6.1 | Apache-2.0 | local full-text index; `NewMemOnly` = upsidedown+gtreap (legacy), scorch in-memory via `NewUsing("", m, scorch.Name, scorch.Name, nil)` (sets `unsafeBatch`); 14 third-party module deps; no TTL |
| github.com/jackc/pgx/v5 | v5.11.0 | MIT | Postgres driver for the shared release index |
| github.com/nats-io/nats.go | v1.53.1 | Apache-2.0 | JetStream + KV (bucket TTL) for the query cache / RSS ingestion (already chosen elsewhere) |
| github.com/dgraph-io/ristretto/v2, hashicorp/golang-lru/v2 | v2.4.2, v2.0.7 | Apache/MPL | per-pod caches (caps, cookies, UA) |
| github.com/Masterminds/sprig/v3 | v3.3.0 | MIT | optional extra template funcs (do **not** expose to definitions; keep the Cardigann FuncMap minimal for compatibility) |

Not found as Go modules: `hbollon/go-torznab`, `Skarlso/torznab`, `jamesmoriarty/go-torznab`, `tinnamchoi/torznab` (`go list` returns "found" without a version → no tagged module).

---

## 10. "Elasticsearch-like": what it should mean and how to build it

> **SUPERSEDED for Clustarr, 2026-09-19.** This section marks Postgres FTS the
> "best fit for a distributed K8s service" and sketches its schema, TTL policy
> and `release_cache` DDL. That comparison predates the decision.
> `docs/adr/0003-release-index-sqlite-fts5.md` chose **SQLite FTS5 via
> `modernc.org/sqlite`** on indexarr's RWO PVC, and spec §6.2, §12 and §16 all
> pin it; Postgres is recorded there as the rejected alternative. indexarr runs
> as exactly one replica with `strategy: Recreate`, which removes the
> multi-writer problem this section was weighing, and the pure-Go driver
> matters because the image is distroless-static and a cgo driver will not
> link.
>
> The table below is still worth reading for *why* each option was weighed —
> just do not implement from it. Phase D1 Task D1-2 implements ADR-0003.


Meaning for Clustarr: Prowlarr is stateless per query; every app search hits every tracker. An Elastic-like indexer instead **ingests** (RSS polling of every indexer every ~15 min = `t=search` with empty `q`, plus every interactive result) into a **shared, TTL'd, deduplicated release index** with structured fields, and serves most inventory searches from it (fast, no tracker load, respects query limits), falling back to live fan-out when the index has no hit or the caller asks for `fresh=true`.

| Option | Pros | Cons | Fit |
|---|---|---|---|
| In-process map + hand-rolled inverted index, TTL sweep | zero deps, trivial | per-pod (inconsistent across replicas), lost on restart, no ranking/fuzzy, everything hand-written | only as the Jackett-style *query cache*; better done in NATS KV with bucket TTL |
| **bleve v2.6.1** (scorch, on PVC or in-memory) | rich queries (match/phrase/fuzzy/prefix/wildcard/regexp/numeric+date range/boolean/facets/highlight), Go-native, Apache-2.0, index aliases for sharding | **no TTL** (must track expiry and `Delete` in batches), single-writer per index dir, per-pod unless one "index" Deployment with a PVC (then it is a SPOF and a second network hop), mapping/analyzer tuning for release titles (`Some.Movie.2024.1080p` needs a custom tokenizer on `.`/`-`/`_`) | good for a single-node "search pod" or a UI autocomplete; not as system of record |
| **Postgres FTS** (`tsvector` GENERATED column + GIN, `websearch_to_tsquery('simple', …)`, `pg_trgm` for fuzzy/ILIKE, btree on `(infohash)`, `(indexer_id, guid)` UNIQUE, `expires_at`) | shared by all replicas, transactional dedup via `INSERT … ON CONFLICT`, TTL = `DELETE WHERE expires_at < now()` cron, structured filters (resolution/source/codec/seeders/size/age/category/ids) are ordinary indexed columns, one dependency the platform already needs | ranking is simpler than Lucene (`ts_rank`), no facets without `GROUP BY`, must normalize titles ourselves (`rls` + a `.`/`-` → space tokenizer before `to_tsvector`) | **best fit** for a distributed K8s service |
| External engines (Elasticsearch/OpenSearch, Meilisearch v0.36.3 client, Typesense v3.2.0 client) | true Elastic semantics, TTL via ILM/aging | another stateful system to operate; overkill for ≤ a few million rows | no (maybe later as a pluggable backend) |

Recommended shape:
- Table `release_cache(id bigserial, indexer_id uuid, guid text, infohash bytea NULL, title text, title_norm text, tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', title_norm)) STORED, protocol, categories int[], size bigint, seeders int, peers int, grabs int, publish_at timestamptz, fetched_at timestamptz, expires_at timestamptz, download_url text, magnet_url text, info_url text, imdb_id int, tmdb_id int, tvdb_id int, tvmaze_id int, trakt_id int, year int, season int, episode int, resolution text, source text, codec text[], hdr text[], audio text[], languages text[], release_group text, dvf real, uvf real, min_ratio real, min_seed_time int, flags text[], attrs jsonb, UNIQUE(indexer_id, guid))` with GIN on `tsv`, btree on `infohash`, `(tvdb_id, season, episode)`, `(imdb_id)`, `(expires_at)`.
- TTL policy: default 24 h for RSS/basic results, 2 h for seeders/peers freshness (re-fetch on grab), bump `expires_at` when re-seen; per-indexer override.
- Dedup at query time: `DISTINCT ON (COALESCE(infohash, indexer_id||guid))` ordered by indexer priority, seeders.
- Query cache (Jackett-style): NATS KV bucket `indexer-query-cache` with TTL 35 min, key = `<indexerId>/<sha256(canonical JSON of SearchRequest)>`, value = compressed `[]Release`.
- Optional bleve: one `indexarr-search` pod building a scorch index from the same Postgres rows (rebuildable), exposed for fuzzy/autocomplete only.

---

## 11. Go-oriented data model for Clustarr (`indexarr`)

Package layout suggestion: `pkg/newznab` (categories + protocol types, no I/O), `pkg/cardigann` (definition structs + engine), `pkg/indexer` (interfaces, release model, search request), `app/indexer/` (controllers, fan-out, cache, HTTP façade).

```go
// pkg/newznab/category.go
package newznab

type CategoryID int

type Category struct {
	ID          CategoryID  `json:"id"`
	Name        string      `json:"name"`        // "Movies/HD"
	Description string      `json:"description,omitempty"`
	SubCategories []Category `json:"subCategories,omitempty"`
}

const (
	CatMovies CategoryID = 2000; CatMoviesHD CategoryID = 2040; CatMoviesUHD CategoryID = 2045 // … full table in §4.4
	CatTV CategoryID = 5000; CatTVAnime CategoryID = 5070
	CatAudio CategoryID = 3000; CatAudioAudiobook CategoryID = 3030
	CatBooks CategoryID = 7000; CatBooksComics CategoryID = 7030
	CatOther CategoryID = 8000
	CustomCategoryOffset CategoryID = 100000 // indexer-specific: offset + tracker id (string ids: sha1→uint16)
)

func (c CategoryID) Parent() CategoryID { if c >= CustomCategoryOffset { return c }; return c / 1000 * 1000 }
func ParseCategoryName(name string) (CategoryID, bool) // "TV/Anime" -> 5070
func Expand(ids []CategoryID) []CategoryID              // parents -> parent+all subcats; >=100000 untouched

// pkg/indexer/capabilities.go
type SearchMode string // "search" | "tv-search" | "movie-search" | "music-search" | "book-search"
type SearchParam string // "q","season","ep","imdbid","tvdbid","tmdbid","tvmazeid","traktid","doubanid","rid","year","genre","album","artist","label","track","title","author","publisher"

type Capabilities struct {
	LimitsMax        int                        `json:"limitsMax,omitempty"`     // caps <limits max>
	LimitsDefault    int                        `json:"limitsDefault,omitempty"`
	Modes            map[SearchMode][]SearchParam `json:"modes"`
	SupportsRawSearch bool                      `json:"supportsRawSearch"`
	Categories       []newznab.Category         `json:"categories"`              // tree exposed in caps
	Flags            []Flag                     `json:"flags,omitempty"`
}

// CategoryMapper is the tracker<->newznab table (Prowlarr IndexerCapabilitiesCategories).
type CategoryMapping struct {
	TrackerCategory string             // "28" or "movies_hd"
	Newznab         newznab.CategoryID // 5070
	Description     string             // "Anime/Dubbed" -> also custom 100000+id
	Default         bool
}
type CategoryMapper interface {
	ToTracker(query []newznab.CategoryID) []string                 // MapTorznabCapsToTrackers
	FromTracker(trackerCat string) []newznab.CategoryID            // MapTrackerCatToNewznab (std + custom)
	FromTrackerDesc(desc string) []newznab.CategoryID
	Supports(query []newznab.CategoryID) bool
	Tree() []newznab.Category
}

type Flag string
const (
	FlagFreeLeech Flag = "freeleech"; FlagHalfLeech Flag = "halfleech"; FlagNeutralLeech Flag = "neutralleech"
	FlagDoubleUpload Flag = "doubleupload"; FlagInternal Flag = "internal"; FlagExclusive Flag = "exclusive"; FlagScene Flag = "scene"
)

// pkg/indexer/search.go
type SearchType string // "search","tvsearch","movie","music","book"  (Torznab t=)

type SearchRequest struct {
	ID         string          `json:"id"`                    // uuid; NATS reply subject key
	Type       SearchType      `json:"type"`
	Query      string          `json:"q,omitempty"`           // free text (sanitized like Prowlarr)
	Categories []newznab.CategoryID `json:"cat,omitempty"`
	Limit      int             `json:"limit,omitempty"`
	Offset     int             `json:"offset,omitempty"`      // only honoured for single-indexer searches

	// external ids
	IMDBID   string `json:"imdbid,omitempty"`   // "tt0133093" (canonical with tt)
	TMDBID   int    `json:"tmdbid,omitempty"`
	TVDBID   int    `json:"tvdbid,omitempty"`
	TVMazeID int    `json:"tvmazeid,omitempty"`
	TraktID  int    `json:"traktid,omitempty"`
	DoubanID int    `json:"doubanid,omitempty"`
	TVRageID int    `json:"rid,omitempty"`
	// tv
	Season  *int   `json:"season,omitempty"`
	Episode string `json:"ep,omitempty"`        // "5", "5-8", or daily "2024/09/17"
	// shared
	Year  int    `json:"year,omitempty"`
	Genre string `json:"genre,omitempty"`
	// music
	Artist, Album, Label, Track string
	// book
	Author, Title, Publisher string
	// filters (Prowlarr extras)
	MinAge, MaxAge int   // days
	MinSize, MaxSize int64
	// routing
	Indexers   IndexerSelector `json:"indexers"`   // by name list, protocol, labels (k8s selector), "all"
	Interactive bool           `json:"interactive"` // vs automatic/rss (affects limit accounting & priority)
	Source     string          `json:"source,omitempty"` // "inventorri", "ui", "sonarr" (Prowlarr Source/Host)
	Fresh      bool            `json:"fresh,omitempty"`  // bypass cache
	Deadline   time.Duration   `json:"deadline,omitempty"` // per-indexer timeout override
}

type IndexerSelector struct {
	Names    []string          `json:"names,omitempty"`
	Protocol Protocol          `json:"protocol,omitempty"` // "" | torrent | usenet  (Prowlarr -1/-2)
	Labels   map[string]string `json:"labels,omitempty"`   // metav1.LabelSelector in the CRD world
	All      bool              `json:"all,omitempty"`
}

func (r SearchRequest) CacheKey() string // sha256 of canonical JSON minus ID/Fresh/Deadline/Source  (Jackett style)
func (r SearchRequest) IsRSS() bool      // Query=="" && no ids

// pkg/indexer/release.go
type Protocol string // "torrent" | "usenet"
type Privacy string  // "public" | "semi-private" | "private"

type Release struct {
	// identity
	GUID        string   `json:"guid"`               // unique per indexer (details url or id)
	IndexerID   string   `json:"indexerId"`          // k8s name or uid
	IndexerName string   `json:"indexer"`
	IndexerPriority int  `json:"indexerPriority"`
	IndexerPrivacy  Privacy `json:"indexerPrivacy"`
	Protocol    Protocol `json:"protocol"`

	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Size        int64     `json:"size"`
	PublishDate time.Time `json:"publishDate"`
	Categories  []newznab.CategoryID `json:"categories"` // std + custom 100000+

	DownloadURL string `json:"downloadUrl,omitempty"` // .torrent / .nzb (indexer URL; façade rewrites to /download)
	MagnetURL   string `json:"magnetUrl,omitempty"`
	InfoHash    string `json:"infoHash,omitempty"`    // lower-case hex, v1 (40) or hybrid
	InfoURL     string `json:"infoUrl,omitempty"`
	CommentURL  string `json:"commentUrl,omitempty"`
	PosterURL   string `json:"posterUrl,omitempty"`

	// torrent
	Seeders  *int `json:"seeders,omitempty"`
	Leechers *int `json:"leechers,omitempty"`
	Peers    *int `json:"peers,omitempty"`     // seeders+leechers
	Grabs    *int `json:"grabs,omitempty"`
	Files    *int `json:"files,omitempty"`
	DownloadVolumeFactor *float64 `json:"downloadVolumeFactor,omitempty"` // 0 = freeleech
	UploadVolumeFactor   *float64 `json:"uploadVolumeFactor,omitempty"`
	MinimumRatio    *float64 `json:"minimumRatio,omitempty"`
	MinimumSeedTime *int64   `json:"minimumSeedTime,omitempty"` // seconds
	Flags []Flag `json:"flags,omitempty"`

	// external ids & media meta
	IMDBID, TMDBID, TVDBID, TVMazeID, TraktID, DoubanID, TVRageID int
	Year   int      `json:"year,omitempty"`
	Genres []string `json:"genres,omitempty"`
	Languages, Subs []string
	Author, BookTitle, Publisher string
	Artist, Album, Label, Track string

	// parsed (rls) – populated by indexarr before caching
	Parsed *ParsedRelease `json:"parsed,omitempty"`
	Attrs  map[string]string `json:"attrs,omitempty"` // any other torznab:attr / _custom fields

	FetchedAt time.Time `json:"fetchedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ParsedRelease struct { // subset of rls.Release
	Type string; Title string; Year int; Season, Episode int
	Resolution, Source string; Codec, HDR, Audio, Language []string; Channels string
	Container, Group string; Edition, Cut []string
}

func (r *Release) DedupKey() string // infohash if set (or parsed from MagnetURL), else indexerId+"|"+guid
func (r *Release) Age() time.Duration

type SearchResult struct {
	Request   SearchRequest
	Releases  []Release            // merged, deduped, sorted
	PerIndexer []IndexerOutcome
	Cached    bool
}
type IndexerOutcome struct {
	IndexerID string; Status string /* ok|timeout|error|skipped */; Reason string
	Count int; Elapsed time.Duration; FromCache bool
}

// pkg/indexer/indexer.go  – the runtime object built from a CR + definition
type Indexer interface {
	ID() string
	Definition() Definition          // metadata (name, privacy, protocol, links, language, encoding)
	Capabilities() Capabilities
	Categories() CategoryMapper
	Search(ctx context.Context, req SearchRequest) ([]Release, error)
	Download(ctx context.Context, rel Release) (Download, error) // resolves download block / magnet / nzb bytes
	Test(ctx context.Context) error
}
type Download struct { Magnet string; Body []byte; ContentType string; Filename string }

// implementations: cardigann.Indexer (YAML engine), newznab.Indexer (upstream Newznab/Torznab incl. Prowlarr/Jackett), native (rare)
```

Cardigann definition structs (mirror of schema v11; yaml tags are the keys):
```go
// pkg/cardigann/definition.go
type Definition struct {
	ID string `yaml:"id"`; Replaces []string `yaml:"replaces"`; Name, Description, Language string
	Type string `yaml:"type"`            // public | semi-private | private
	Encoding string `yaml:"encoding"`; FollowRedirect bool `yaml:"followredirect"`; TestLinkTorrent *bool `yaml:"testlinktorrent"`
	RequestDelay float64 `yaml:"requestDelay"` // seconds
	Links, LegacyLinks, Certificates []string
	Caps     Caps            `yaml:"caps"`
	Settings []SettingsField `yaml:"settings"`
	Login    *LoginBlock     `yaml:"login"`
	Search   SearchBlock     `yaml:"search"`
	Download *DownloadBlock  `yaml:"download"`
}
type SettingsField struct {
	Name, Type, Label string           // type enum in §3.3
	Default  any `yaml:"default"`      // string|int|bool
	Options  map[string]string `yaml:"options"` // key -> label (ordered by key)
	Defaults []string `yaml:"defaults"`
}
type Caps struct {
	CategoryMappings []CategoryMapping `yaml:"categorymappings"`
	Categories       map[string]string `yaml:"categories"` // legacy {trackerId: "Movies/HD"}
	Modes            map[string][]string `yaml:"modes"`
	AllowRawSearch   bool `yaml:"allowrawsearch"`; AllowTVSearchIMDB bool `yaml:"allowtvsearchimdb"`
}
type CategoryMapping struct { ID StringOrInt `yaml:"id"`; Cat string `yaml:"cat"`; Desc string `yaml:"desc"`; Default bool `yaml:"default"` }
type LoginBlock struct {
	Method string `yaml:"method"` // form|post|cookie|get|oneurl
	Path, SubmitPath, Form string
	Inputs map[string]StringOrScalar `yaml:"inputs"`
	SelectorInputs, GetSelectorInputs map[string]SelectorBlock
	Selectors bool; Cookies []string; Headers map[string][]string
	Captcha *CaptchaBlock; Error []ErrorBlock; Test *PageTestBlock
}
type CaptchaBlock struct { Type, Selector, Input string }
type ErrorBlock struct { Path, Selector string; Message *SelectorBlock }
type PageTestBlock struct { Path, Selector string }
type SearchBlock struct {
	Path string; Paths []SearchPathBlock
	AllowEmptyInputs bool `yaml:"allowEmptyInputs"`
	Inputs map[string]StringOrScalar; Headers map[string][]string
	KeywordsFilters, PreprocessingFilters []FilterBlock
	Error []ErrorBlock
	Rows RowsBlock; Fields OrderedFields // ordered! map keeps file order
}
type SearchPathBlock struct {
	Path, Method string; FollowRedirect bool; Categories []StringOrInt; Inputs map[string]StringOrScalar
	InheritInputs bool `yaml:"inheritinputs"`; QuerySeparator string; Response *ResponseBlock
}
type ResponseBlock struct { Type string /* json|xml */; NoResultsMessage string }
type SelectorBlock struct {
	Selector, Attribute string; Optional bool; Default *StringOrScalar
	Case map[string]StringOrScalar; Remove string; Text *StringOrScalar
	Filters []FilterBlock
}
type RowsBlock struct {
	SelectorBlock `yaml:",inline"`
	After int; Multiple bool; MissingAttributeEqualsNoResults bool
	DateHeaders *SelectorBlock `yaml:"dateheaders"`; Count *SelectorBlock
}
type FilterBlock struct { Name string; Args []string /* custom UnmarshalYAML: scalar or list */ }
type DownloadBlock struct {
	Method string; Before *BeforeBlock; Selectors []SelectorField; InfoHash *InfoHashBlock; Headers map[string][]string
}
type BeforeBlock struct { Path string; PathSelector *SelectorField; Method string; Inputs map[string]StringOrScalar; QuerySeparator string }
type InfoHashBlock struct { Hash, Title SelectorField; UseBeforeResponse bool }
type SelectorField struct { Selector, Attribute string; UseBeforeResponse bool; Filters []FilterBlock }

// engine
type TemplateContext struct {
	Config map[string]any // sitelink + settings (checkbox -> ".True"/nil, select -> key)
	Keywords string; Query QueryVars; Categories []string; Result map[string]string
	DownloadURI *url.URL; True, False string; Today struct{ Year int }
}
type QueryVars struct{ Type, Q, Keywords, IMDBID, IMDBIDShort, TVDBID, TMDBID, TVMazeID, TraktID, DoubanID, Season, Ep, Episode, Year, Genre, Album, Artist, Label, Track, Author, Title, Publisher string }
type Filter func(value string, args []string, tc *TemplateContext) (string, error)
var Filters = map[string]Filter{ /* the 25 names of §3.6 */ }
```

Kubernetes CRDs (`api/v1alpha1`) — sketch:
```go
// IndexerDefinition: cluster-scoped, synced from Prowlarr/Indexers by a controller (or user-supplied)
type IndexerDefinitionSpec struct {
	SchemaVersion int    `json:"schemaVersion"` // 11
	Source string        `json:"source"`        // "prowlarr" | "custom"
	YAML   string        `json:"yaml"`          // raw definition (validated against schema.json at admission)
}
type IndexerDefinitionStatus struct { SHA256 string; Name, Type, Language string; Protocol Protocol; Modes []string; CategoryCount int; Valid bool; Message string }

// Indexer: namespaced instance = definition + user settings
type IndexerSpec struct {
	DefinitionRef  string            `json:"definitionRef"`         // IndexerDefinition name, or "newznab"/"torznab" builtins
	BaseURL        string            `json:"baseURL,omitempty"`     // one of definition links
	Settings       map[string]string `json:"settings,omitempty"`    // non-secret setting values
	SecretRef      *corev1.LocalObjectReference `json:"secretRef,omitempty"` // keys = setting names (password, apikey, cookie)
	Enabled        bool  `json:"enabled"`
	Priority       int   `json:"priority"`       // 1..50, default 25
	Redirect       bool  `json:"redirect"`
	Limits         Limits `json:"limits,omitempty"`
	RateLimit      metav1.Duration `json:"rateLimit,omitempty"`   // default 2s or definition requestDelay
	Timeout        metav1.Duration `json:"timeout,omitempty"`     // per-search deadline (default 30s)
	ProxySelector  *metav1.LabelSelector `json:"proxySelector,omitempty"` // IndexerProxy objects
	CacheTTL       metav1.Duration `json:"cacheTTL,omitempty"`
	RSSInterval    metav1.Duration `json:"rssInterval,omitempty"`  // 0 = disabled
	SyncCategories []newznab.CategoryID `json:"syncCategories,omitempty"`
}
type Limits struct { QueryLimit, GrabLimit int; Unit string /* day|hour */ }
type IndexerStatus struct {
	Conditions []metav1.Condition // Ready, Authenticated, RateLimited, Disabled
	Capabilities Capabilities
	Protocol Protocol; Privacy Privacy
	EscalationLevel int; DisabledUntil *metav1.Time; LastFailure, LastSuccess *metav1.Time; LastError string
	QueriesInWindow, GrabsInWindow int; WindowResetAt *metav1.Time
	CookiesExpireAt *metav1.Time  // cookies themselves stored in a Secret owned by the Indexer
	LastRSSAt *metav1.Time; LastRSSCount int
}

// IndexerProxy: flaresolverr | http | socks4 | socks5
type IndexerProxySpec struct { Type string; Host string; Port int; RequestTimeout metav1.Duration; SecretRef *corev1.LocalObjectReference /* username/password */ }
```

Torznab façade (`app/indexer/torznab`): `GET /{indexerName}/api?t=…` and `GET /{indexerName}/download?link=&file=`; `GET /search/api?t=…&indexers=name1,name2` for the aggregate (Jackett-style filter grammar optional). `pkg/torznab` holds `Caps`, `Feed`, `Item`, `Attr` XML structs (both namespaces), `ParseFeed(r io.Reader) ([]Release, error)` for consuming upstream Torznab/Newznab (incl. Prowlarr/Jackett), and `WriteFeed`.

---

## 12. Recommendations (concrete)

1. **Adopt Cardigann v11 verbatim as the definition format** (`IndexerDefinition` CRD carrying the YAML); do not invent a new schema. Sync the 547 definitions from `https://indexers.prowlarr.com/master/11/package.zip` (or the GitHub tarball, as cardigann-go does) with SHA-based change detection, and validate with `santhosh-tekuri/jsonschema/v6` against `definitions/v11/schema.json`.
2. **Write `pkg/cardigann` in Go (Apache-2.0)** with Go `text/template` + a small FuncMap (`re_replace`, `join`, plus builtin `and/or/eq/ne/if/range`), goquery for HTML, gjson for JSON, xmlquery/etree for XML, and the 25 filters. Implement a **.NET-format→Go-layout date translator** and a `GetBytes` size parser; port Prowlarr's `CardigannRequestGenerator` semantics (`$raw`, `inheritinputs`, `!` category exclusion, one request per matching path, `allowEmptyInputs`, keyword building, `.Result.*` ordering). Use cardigann-go v0.4.0 and Prowlarr's C# as test oracles; run the whole corpus through the parser in CI (parse-only) and a handful of public trackers (1337x, etc.) as opt-in e2e tests.
3. **Write `pkg/torznab`** (client + server types; both `torznab.com` and `newznab.com` namespaces; caps/search/download; error XML; 410/429+Retry-After like Prowlarr) instead of importing go-newznab.
4. **Model state as CRDs + shared stores**: `IndexerDefinition` (cluster), `Indexer` (namespaced; secrets in a `Secret`; cookies in an owned `Secret`), `IndexerProxy` (label-selected, FlareSolverr last in chain). Health/backoff uses Prowlarr's exact `Periods` table and 15-minute startup grace; query/grab limits are counted in Postgres (history table) so all replicas agree.
5. **Fan-out**: `errgroup` + per-indexer `context.WithTimeout` (default 30 s), skip indexers by caps/categories/limits/disabled, dedup by infohash then guid, keep provenance (`alsoOn`), stream partials over NATS (`indexarr.search.<id>.results`), final merge capped at 1000.
6. **Elastic-like layer**: Postgres FTS table `release_cache` (schema in §10) fed by (a) RSS polling per indexer every 15 min (`t=search`, empty `q`, respecting `QueryLimit`), (b) every interactive/automatic search result; TTL sweep; `rls` parse at ingest; queries by structured filters + `websearch_to_tsquery`; NATS KV (35-min TTL) for the per-(indexer, request-hash) raw result cache. Add bleve later only for fuzzy/autocomplete.
7. **Categories**: ship the full Newznab table (§4.4) in `pkg/newznab` with `Expand`, `Parent`, custom `100000+` offset, and the Cardigann `cat` name enum; map Clustarr media kinds → default category sets (movies 2000, tv 5000, music 3000, audiobooks 3030, books 7020, comics/manga 7030, anime 5070).
8. **Proxy compatibility**: keep a `/{indexer}/api` Torznab façade so Sonarr/Radarr (and Prowlarr itself, as an upstream) interoperate during migration; support Prowlarr/Jackett *as upstream indexers* via the generic Newznab/Torznab implementation (`prowlarrindexer` attr → provenance).

---

## 13. Sources
- Prowlarr sources (develop): `src/NzbDrone.Core/IndexerSearch/ReleaseSearchService.cs`, `…/IndexerSearch/NewznabRequest.cs`, `…/IndexerSearch/NewznabResults.cs`, `…/IndexerSearch/Definitions/SearchCriteriaBase.cs`, `…/Indexers/HttpIndexerBase.cs`, `…/Indexers/IndexerBase.cs`, `…/Indexers/IndexerCapabilities.cs`, `…/Indexers/IndexerCapabilitiesCategories.cs`, `…/Indexers/IndexerFlag.cs`, `…/Indexers/IndexerLimitService.cs`, `…/Indexers/IndexerHttpClient.cs`, `…/Indexers/Definitions/Cardigann/{CardigannDefinition,CardigannBase}.cs`, `…/Indexers/Definitions/Newznab/{NewznabRequestGenerator,NewznabSettings}.cs`, `…/IndexerProxies/{FlareSolverr/FlareSolverr.cs,FlareSolverr/FlareSolverrSettings.cs,Http/HttpSettings.cs,Socks5/Socks5Settings.cs}`, `…/IndexerVersions/IndexerDefinitionUpdateService.cs`, `…/ThingiProvider/Status/{ProviderStatusServiceBase,EscalationBackOff}.cs`, `…/Applications/Sonarr/Sonarr.cs`, `src/NzbDrone.Common/Http/{HttpRequest.cs,Dispatchers/ManagedHttpDispatcher.cs}`, `src/Prowlarr.Api.V1/{Indexers/NewznabController.cs,Search/SearchController.cs}` (https://github.com/Prowlarr/Prowlarr)
- DeepWiki Q&A on Prowlarr/Prowlarr and Prowlarr/Indexers (Cardigann framework, request generator, parser, sync)
- Prowlarr/Indexers: README, `definitions/v11/schema.json`, `definitions/v11/1337x.yml`, `definitions/v11/0dayfiles-api.yml`; live listing https://indexers.prowlarr.com/master/11 (547 entries)
- Torznab spec 1.3 draft: https://torznab.github.io/spec-1.3-draft/torznab/Specification-v1.3.html
- Newznab API reference mirror: https://inhies.github.io/Newznab-API/ (functions, errors)
- Jackett: README (aggregate/filter indexers, FlareSolverr), wiki `Definition-format`, wiki `Jackett-Categories`, `src/Jackett.Common/Services/CacheService.cs`, `src/Jackett.Common/Models/Config/ServerConfig.cs`
- Go: pkg.go.dev + `go doc` for github.com/Kcchouette/cardigann-go v0.4.0, github.com/mrobinsn/go-newznab v1.2.0, github.com/blevesearch/bleve/v2 v2.6.1 (`index.go`, `config.go`, `index/scorch/scorch.go`), github.com/anacrolix/torrent/metainfo v1.61.0, github.com/moistari/rls v0.6.0; GitHub API for repo freshness/licenses; `go list -m -versions` for all versions in §9
