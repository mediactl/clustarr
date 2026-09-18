# Subtitles research note — Bazarr functionality, subtitle providers, and a Go design for Clustarr `pkg/subtitles`

Date: 2026-09-18. Verified against Bazarr **v1.6.1** (released 2026-09-15) source pulled from GitHub, the official OpenSubtitles Kodi add-on request models, the Jellyfin OpenSubtitles plugin models, upstream subliminal, and `go doc` on the Go modules named below (toolchain Go 1.27, module proxy reachable). Where a claim is *not* verified it is labelled as such.

Scratch artefacts used for verification (all under the session scratchpad, nothing written to the project):
- `scratchpad/bazarr_src/*.py` — raw copies of the Bazarr files quoted here (`score.py`, `core.py`, `indexer/movies.py`, `adaptive_searching.py`, `upgrade.py`, `get_providers.py`, `opensubtitlescom.py`, `whisperai.py`, `subsyncer.py`, `video_analyzer.py`, `subtitle.py`, `hearing_impaired.py`, `language.py`, `utils.py`).
- `scratchpad/gomod/` — temp Go module where `go list -m`, `go doc`, and a moviehash cross-check test were run.
- `scratchpad/sample.mkv` — ffmpeg-generated MKV with one `forced` and one `hearing_impaired` SubRip stream, probed with ffprobe 9.0.1.

---

## 0. TL;DR

1. Bazarr is a **planner + provider pool + post-processor**. The planner turns (media item, language profile, existing subtitles) into a *wanted* list of `lang[:forced|:hi]` keys; the provider pool searches all enabled providers, scores every candidate against the video (weights below, max 360 for episodes / 180 for movies), downloads the best per language above a minimum score; post-processing re-encodes to UTF-8, applies text "mods" (HI removal etc.), converts to SRT, writes `Video.<lang>[.forced|.hi].srt`, optionally runs ffsubsync, and records a history row. Two periodic loops re-run this: **wanted search** (default every 6 h, with adaptive per-language backoff) and **upgrade** (every 12 h, only items downloaded in the last 7 days whose score < max-3, requiring a strictly better score).
2. The provider landscape in 2026: **OpenSubtitles.com REST v1** (API key + user account, 20/day free, 1000/day VIP, 5 req/s) is the anchor. **OpenSubtitles.org XML-RPC is dead** (deprecated 2023-12-31, VIP-only through 2024/2025, final shutdown notice January 2026), so `github.com/oz/osdb` is only useful for its `Hash()` function. **Subscene died May 2024**; **Podnapisi was deleted from Bazarr in 1.6.0 because the site went offline**. Viable free/JSON providers: **Gestdown** (Addic7ed mirror for TV, TVDB id, no auth), **SubDL** (API key), **SubSource** (API key). **Addic7ed direct** still exists but needs an account + paid anti-captcha and is repeatedly reported broken. YIFY (movies, scraping) and TVSubtitles (episodes, scraping) survive but are fragile. **Whisper generation** is a first-class fallback provider with a stable HTTP contract (`/asr`, `/detect-language`) implemented by whisper-asr-webservice, subgen and go-whisper.
3. Go libs that check out: `github.com/asticode/go-astisub v0.45.0` (SRT/SSA/ASS/WebVTT/TTML/STL read+write, `RemoveStyling`, `Add`, `ApplyLinearCorrection`), `gopkg.in/vansante/go-ffprobe.v2 v2.3.1` (typed `disposition.forced/hearing_impaired`, `tags.language`), `github.com/odwrtw/opensubtitles` (a small REST-v1 client, 2022 pseudo-version, use as a reference not a dependency), `github.com/ggerganov/whisper.cpp/bindings/go` (cgo; in-process whisper), `github.com/gogs/chardet` + `golang.org/x/text v0.42.0` (charset detection/transcoding), `github.com/dlclark/regexp2 v1.12.0` (Bazarr's HI regexes use lookbehind, which Go's RE2 lacks), `github.com/nats-io/nats.go v1.53.1` JetStream for the task model.
4. Recommended Clustarr model: **one task per (media item × language key)**, published to a JetStream work-queue stream with `Nats-Msg-Id` dedupe, per-provider throttle/quota state in a NATS KV bucket shared by all workers, one **provider gateway** per remote provider that owns the login token and the rate limiter, and separate streams for search/fetch, sync, and whisper generation so each scales on its own (whisper on GPU nodes).

---

## 1. Bazarr end-to-end pipeline (what we are reproducing)

```
Sonarr/Radarr sync ──> media rows (path, file_size, audio_language, profileId, ffprobe_cache)
        │
        ▼
Index existing subtitles: ffprobe/mediainfo (via knowit) for embedded streams
                          + directory scan for sidecar files (naming rules §3.2)
        │
        ▼
list_missing_subtitles(): desired(profile) − actual(existing) with cutoff / audio_exclude /
                          language_equals / HI rules  ──> missing_subtitles = ["en", "fr:forced", "de:hi"]
        │
        ▼ (scheduler: wanted_search_frequency, adaptive search gate per language)
SZProviderPool.list_subtitles(video, languages)  (all enabled, non-throttled providers, threaded)
        │  each Subtitle: get_matches(video) + guess_matches(release_info)
        ▼
compute_score() ──> sort (score, score_without_hash) desc ──> banlist / blacklist / HI checks
        │
        ▼
download_best_subtitles(): first candidate per language with score >= min_score that downloads OK
        │
        ▼
Subtitle.guess_encoding() → mods (subzero) → to SRT (pysubs2) → get_subtitle_path() → write
        │
        ├─> optional ffsubsync (SubSyncer) if use_subsync and score < threshold
        ├─> optional post-processing shell command with {{variables}}
        └─> TableHistory row (action code, score, score_out_of, provider, subs_id, ...)
        
Periodic: upgrade_subtitles() every upgrade_frequency h: re-search recent history rows with
          score < score_out_of − 3, forced_minimum_score = score + 1, is_upgrade=True
Provider exceptions ──> provider_throttle() → throttled_providers.dat (name → (reason, until, text))
```

---

## 2. Language profiles — exact model and evaluation

### 2.1 Storage (Bazarr `TableLanguagesProfiles`)

| column | type | meaning |
|---|---|---|
| `profileId` | int PK | |
| `name` | text | |
| `cutoff` | int | the `id` of the item that, when present, stops all further searching; `65535` = "Any" |
| `items` | JSON text | list of profile items (below) |
| `mustContain` | JSON list of strings | regexes; a candidate's `release_info` must match **every** one (`re.search`, IGNORECASE) |
| `mustNotContain` | JSON list of strings | regexes; candidate rejected if **any** matches |
| `originalFormat` | int/bool | keep the provider's original format (e.g. ASS) instead of converting to SRT |
| `tag` | text | optional Sonarr/Radarr tag auto-assigning this profile |

Profile item (`Language.ProfileItem` in the frontend types):

```jsonc
{ "id": 1, "language": "en", "forced": "False", "hi": "False",
  "audio_exclude": "False", "audio_only_include": "False" }
```

- `language`: IETF-ish code as Bazarr's `Language.basename` prints it: `en`, `pt-BR`, `zh-TW`, `sr-Latn`... (alpha2 + optional country/script).
- `forced`: `"True"` → want a *forced* (foreign-parts-only) subtitle.
- `hi`: three-valued (`bazarr/constants.py`: `HI_EXCLUDED = "Excluded"`): `"True"` = HI required (wanted key `de:hi`, file gets `.hi`), `"False"` = either is fine (non-HI preferred via the 1-point `hearing_impaired` score), `"Excluded"` = non-HI only.
- `audio_exclude`: skip this item when the media's audio already has this language.
- `audio_only_include`: only want this item when the audio has this language (e.g. "English CC only for English audio").

Global related settings: `language_equals` (list of `"from:to"` strings, e.g. `"pt-BR:pt"`; `_lang_from_str` also accepts `"es-MX"`, `"en@hi"`, `"es-MX@forced"`), `single_language` (default False), `use_embedded_subs` (embedded streams count as existing), `embedded_subs_show_desired` (True), `ignore_pgs_subs`/`ignore_vobsub_subs`/`ignore_ass_subs` (all False), `hi_extension` ∈ {`hi`, `cc`, `sdh`} (default `hi`), `subfolder` ∈ {`current`, `absolute`, `relative`} (+ `subfolder_custom`).

### 2.2 Missing-subtitle computation (`list_missing_subtitles_movies`, verbatim logic)

```
desired = []
for item in profile.items:
    if item.audio_exclude and audio_has(item.language): continue
    if item.audio_only_include and not audio_has(item.language): continue
    desired += {language, forced, hi}
actual = [{code2, forced, hi} for every embedded (if use_embedded_subs) or sidecar subtitle]
expanded_actual = language_equals.check_set(actual)     # adds the "to" side of each from:to pair

cutoff_met = false
for c in profile.cutoff_items:                            # 1 item, or all items when cutoff == 65535
    if c.audio_only_include and not audio_has(c): continue
    elif c.audio_exclude and audio_has(c):         cutoff_met = true
    elif c in actual or c in expanded_actual:      cutoff_met = true
    elif c.hi == "False" and {c.language, forced:false, hi:true} in actual: cutoff_met = true   # an HI file satisfies a non-HI cutoff
if cutoff_met: missing = []
else:
    missing = [d for d in desired if d not in actual and d not in expanded_actual]
    hi_setting = {item.language: item.hi for item in profile.items if not item.forced}
    for a in actual:
        if a.hi and hi_setting[a.language] != "Excluded":  missing.remove({a.language, forced:false, hi:false})   # HI file satisfies a plain request
        elif not a.hi and hi_setting[a.language] == "Excluded": missing.remove({a.language, forced:false, hi:"Excluded"})
    output = [lang + (":forced" if forced else ":hi" if hi=="True" else "") for each missing]
```

`Language.__eq__` compares `(alpha3, country, script, hi, forced)` — so `en`, `en:forced`, `en:hi` are three distinct wanted keys, and a search task is naturally keyed by that string.

---

## 3. Detecting what already exists

### 3.1 Embedded streams via ffprobe

Command shape Bazarr (through knowit) and we should use:

```
ffprobe -v error -print_format json -show_streams -show_format <file>
```

Real output from the generated sample (ffprobe **n9.0.1**), one forced and one SDH SubRip stream:

```json
{"index": 2, "codec_name": "subrip", "codec_type": "subtitle",
 "disposition": {"default": 0, "forced": 1, "hearing_impaired": 0, "captions": 0, "descriptions": 0, ...},
 "tags": {"language": "eng"}}
{"index": 3, "codec_name": "subrip", "codec_type": "subtitle",
 "disposition": {"default": 0, "forced": 0, "hearing_impaired": 1, ...},
 "tags": {"language": "eng", "title": "English (SDH)"}}
```

Facts to encode:
- `codec_name` values: text — `subrip`, `ass`, `ssa`, `webvtt`, `mov_text`, `text`; bitmap — `hdmv_pgs_subtitle`, `dvd_subtitle`, `dvb_subtitle`, `xsub`; broadcast — `eia_608`, `dvb_teletext`. Bazarr's `embeddedsubtitles` provider can only extract `ass, subrip, webvtt, mov_text` (no OCR); its sync-reference enumeration skips names containing `dvd`/`pgs`.
- `tags.language` is ISO 639-2/B (`eng`, `fre`, `ger`, `chi`, `dut`...) — normalise to 639-3/BCP-47 (Bazarr has a `chi→zh` style map and a `CustomLanguage` table for `pt-BR`, `zh-TW`, `sr-Latn`, `es-MX`, `pb`...). `und`/missing → "Undefined" (whisper maps `und→eng`, `gsw→deu`).
- Forced = `disposition.forced == 1`; HI = `disposition.hearing_impaired == 1` (knowit exposes both). Bazarr drops any stream whose `title` contains `commentary`. Recommendation (not Bazarr behaviour, but matches how rips are actually tagged): also treat `title` matching `(?i)\bforced\b` as forced and `(?i)\b(sdh|hi|cc)\b` as HI, since many muxers only set the title.
- Cache: Bazarr pickles the probe result into `ffprobe_cache` keyed by `file_size` + `file_id`; invalidate on size change. Clustarr should key on `(path, size, mtime)` or the inventory's file id.
- Audio languages come from the same probe (`codec_type == "audio"`, `tags.language`) and feed `audio_exclude` / `audio_only_include` and the subsync reference-stream choice.

Go: `gopkg.in/vansante/go-ffprobe.v2 v2.3.1` — `ffprobe.ProbeURL(ctx, path)` returns `*ProbeData{Streams []*Stream, Format *Format}`; `Stream{Index, CodecName, CodecType, Disposition StreamDisposition{Default, Forced, HearingImpaired, VisualImpaired, Dub, Original, Comment, ...}, TagList Tags}` (`Tags` is a map wrapper with `GetString("language")`).

### 3.2 Sidecar files — naming rules (Bazarr `_search_external_subtitles`, verbatim behaviour)

- `SUBTITLE_EXTENSIONS = ('.srt', '.sub', '.smi', '.txt', '.ssa', '.ass', '.mpl', '.vtt')`, but with `INCLUDE_EXOTIC_SUBS` off only `.srt .ass .ssa .vtt` are considered.
- Only files in the video's directory (plus configured custom sub-folders) whose stem, after stripping the tag and language segments, equals the video stem (`match_strictness="strict"`; `"loose"` allows "contains").
- Parsing order (right to left): `<stem>[.<lang>][.<tag>].<ext>`
  - `tag` (last dot-segment, lowercase) ∈ `forced, normal, default, embedded, embedded-forced, custom, hi, cc, sdh`. `forced = "forced" in tag`; `hi = tag contains hi|cc|sdh`.
  - `lang` = next segment with `_`→`-`, parsed as IETF (`Language.fromietf`): `en`, `eng`, `pt-BR`, `zh-Hant`... `zh-TW` is rewritten to `zht`. Unparseable codes fall back to a Simplified/Traditional Chinese alias table (`chs, sc, zhs, hans, gb, 简体...` → zh; `cht, tc, zht, hant, big5, 繁體...` → zh).
  - `Movie.srt` (stem identical to video) → language unknown → `None`; if `only_one` (single_language) it is assumed to be the one desired language, else Bazarr later guesses from content.
- Examples that parse: `Movie.en.srt`, `Movie.en.forced.srt`, `Movie.en.hi.srt`, `Movie.eng.sdh.srt`, `Movie.en.cc.srt`, `Movie.pt-BR.srt`, `Movie.zh-TW.srt`, `Movie.es-MX.forced.srt`. `Movie.forced.en.srt` does **not** parse as forced (tag must be last).
- Writing (`get_subtitle_path`): `<video-stem>.<lang.basename>[.forced | .<hi_extension>]<ext>`; forced wins over HI (a forced file never gets an HI tag). Extra custom tags are joined with `-`. Encoding of output defaults to UTF-8 (`utf8_encode=True`).

Go regex equivalent (RE2-safe):

```go
var sidecarRe = regexp.MustCompile(
  `^(?P<stem>.+?)` +
  `(?:\.(?P<lang>[A-Za-z]{2,3}(?:[-_][A-Za-z]{2,4})?))?` +
  `(?:\.(?P<tag>forced|normal|default|embedded|embedded-forced|custom|hi|cc|sdh))?` +
  `\.(?P<ext>srt|ass|ssa|vtt|sub|smi|txt|mpl)$`)
```
(after matching, verify `stem == videoStem` and that `lang` parses with `golang.org/x/text/language.Parse`; if `lang` fails to parse, treat it as part of the stem and re-check.)

Content-based language guess for untagged files: `github.com/pemistahl/lingua-go v1.4.0` (accurate, larger) or `github.com/abadojack/whatlanggo v1.0.1` (small).

---

## 4. Providers

### 4.1 Bazarr v1.6.1 provider inventory (`custom_libs/subliminal_patch/providers/`, 64 providers)

addic7ed, animekalesi, animesubinfo, animetosho, animetosho_xyz, assrt, avistaz, avistaz_network, bayflix, betaseries, bsplayer, cinemaz, **embeddedsubtitles**, **gestdown**, greeksubs, greeksubtitles, hdbits, hosszupuska, jimaku, karagarga, ktuvit, legendasdivx, legendasnet, napiprojekt, napisy24, nekur, **opensubtitlescom**, pipocas, prijevodionline, regielive, shooter, soustitreseu, subclub, **subdl**, subf2m, subs4free, subs4series, subsarr, subscenter, subsdump, **subsource**, subsro, subssabbz, subsunacs, subsynchro, subtis, subtitlecat, subtitrarinoi, subtitriid, subtitulamostv, subx, supersubtitles, titlovi, titrari, titulky, turkcealtyaziorg, **tvsubtitles**, vladoonmooo, **whisperai**, wizdom, xsubs, yavkanet, **yifysubtitles**, zimuku.

Absent (removed): `podnapisi` (removed in 1.6.0, "no longer online"), `subscene` (site shut down May 2024), `opensubtitles` (.org XML-RPC).

`PROVIDERS_FORCED_OFF` (cannot serve *forced* requests): addic7ed, tvsubtitles, legendasdivx, napiprojekt, shooter, hosszupuska, supersubtitles, titlovi, assrt.

Providers that set `hearing_impaired_verifiable = True` (their HI flag is trustworthy, so "force HI/non-HI" filtering applies): opensubtitlescom, gestdown, hdbits, subdl, subsource, embeddedsubtitles.

### 4.2 Viability table for a 2026 Go implementation

| provider | type | auth | movies/TV | status 2026 | notes |
|---|---|---|---|---|---|
| OpenSubtitles.com REST v1 | JSON API | API key (consumer) + user login (JWT) | both | **primary** | quotas 5/day anon per IP, 20/day registered, 1000/day VIP; 5 req/s/IP; hash search |
| OpenSubtitles.org XML-RPC | XML-RPC | user agent | both | **dead** (deprecated 2023-12-31; VIP-only 2024; final shutdown notice Jan 2026) | `oz/osdb` targets this |
| Gestdown (api.gestdown.info) | JSON API | none | TV only | viable | Addic7ed mirror; lookup by TVDB id; 423 = "refreshing, retry in 30s" |
| SubDL (api.subdl.com) | JSON API | `api_key` query param | both | viable | Subscene successor; 429 with `error: daily_limit` → quota, resets midnight GMT |
| SubSource (api.subsource.net) | JSON API | `api_key` | both | viable | `X-RateLimit-Reset` header; 401/403/429 mapped |
| Addic7ed | HTML scraping | account + reCAPTCHA solver (anti-captcha.com / DBC) | both | fragile | 40/day (80 VIP) rolling 24 h; "relax, slow down" → TooManyRequests; repeated "broken provider" issues in 2026 |
| YIFY (yifysubtitles.ch) | HTML scraping | none | movies | fragile | search `/movie-imdb/tt…`; zip download |
| TVSubtitles (tvsubtitles.net) | HTML scraping | none | TV | fragile | `search1.php`, `tvshow-<id>-<season>.html`, `episode-<id>.html`; zip |
| Podnapisi | — | — | — | **gone** | removed from Bazarr 1.6.0 |
| Subscene | — | — | — | **gone** | shut down May 2024 |
| Whisper (subgen / whisper-asr-webservice / go-whisper) | HTTP generation | none | both | viable, GPU recommended | translate only *into* English |
| Embedded extraction | local ffmpeg | none | both | viable | text codecs only |
| Anime: animetosho, jimaku (API key) | API/scrape | — | TV | niche | |

The Bazarr+ community catalog (`LavX/bazarr-provider-catalog`, 60+ scrapers as stdlib plugins) is a sign the ecosystem expects providers to be **pluggable**; Clustarr should make `Provider` a plugin boundary (Go plugin via gRPC or in-tree registry) rather than hard-coding.

### 4.3 OpenSubtitles.com REST v1 — verified contract

Base: `https://api.opensubtitles.com/api/v1/`. VIP accounts get `base_url` in the login response (`vip-api.opensubtitles.com`) — Bazarr stores it as `server_hostname` and uses it for later calls.

Headers: `Api-Key: <consumer key>` (required on every call), `User-Agent: <AppName> v<ver>` (or `X-User-Agent`), `Content-Type: application/json`, `Authorization: Bearer <jwt>` after login. Rate limit **5 req/s per IP**, `/login` **1 req/s**; response headers `X-RateLimit-Remaining`, `x-ratelimit-remaining-second`, `x-ratelimit-remaining-minute`, and `Retry-After` on 429.

`POST /login` `{"username","password"}` → `{"user":{"allowed_downloads":int,"allowed_translations":int,"level":"VIP Member","user_id":int,"ext_installed":bool,"vip":bool},"base_url":"api.opensubtitles.com","token":"<jwt>","status":200}`. JWT valid **24 h** (Bazarr caches 12 h, `TOKEN_EXPIRATION_TIME`).

`GET /infos/user` → `{"data":{"allowed_downloads","level","user_id","ext_installed","vip","downloads_count","remaining_downloads","reset_time_utc"}}`.

`GET /subtitles` query parameters (names, allowed values from the official Kodi add-on's `OpenSubtitlesSubtitlesRequest`; Bazarr sorts params alphabetically and lowercases values because the API caches on the exact query string):

| param | type / values | default |
|---|---|---|
| `id` | feature id (int) | |
| `imdb_id`, `tmdb_id` | int, **no `tt` prefix** | |
| `parent_imdb_id`, `parent_tmdb_id`, `parent_feature_id` | int (series ids for episodes) | |
| `season_number`, `episode_number`, `year` | int | |
| `type` | `movie` \| `episode` \| `all` | `all` |
| `query` | text (NFC-normalised) | |
| `languages` | comma-separated lowercase (`en,pt-br,zh-tw`) | |
| `moviehash` | 16 hex chars | |
| `moviehash_match` | `include` \| `only` | `include` |
| `hearing_impaired` | `include` \| `exclude` \| `only` | `include` |
| `foreign_parts_only` | `include` \| `exclude` \| `only` | `include` |
| `trusted_sources` | `include` \| `only` | `include` |
| `machine_translated` | `include` \| `exclude` | `exclude` |
| `ai_translated` | `include` \| `exclude` | `include` |
| `order_by` | `language, download_count, new_download_count, hearing_impaired, hd, fps, votes, points, ratings, from_trusted, foreign_parts_only, upload_date, ai_translated, machine_translated` | download_count |
| `order_direction` | `asc` \| `desc` | `desc` |
| `uploader_id`, `page` | int | |

Response (typed fields from the Jellyfin plugin + `odwrtw/opensubtitles` + subliminal):

```jsonc
{ "total_pages": 3, "total_count": 120, "per_page": 50, "page": 1,
  "data": [ { "id": "1234567", "type": "subtitle", "attributes": {
      "subtitle_id": "1234567", "language": "en",
      "download_count": 500000, "new_download_count": 1200,
      "hearing_impaired": false, "hd": true, "fps": 23.976, "votes": 3, "points": 0, "ratings": 8.5,
      "from_trusted": true, "foreign_parts_only": false, "ai_translated": false, "machine_translated": false,
      "upload_date": "2010-09-01T00:00:00Z", "release": "Inception.2010.720p.BluRay.x264-REWARD",
      "comments": "", "legacy_subtitle_id": 3874251,
      "uploader": { "uploader_id": 1, "name": "os-user", "rank": "trusted" },
      "feature_details": { "feature_id": 1, "feature_type": "Movie", "year": 2010, "title": "Inception",
         "movie_name": "Inception (2010)", "imdb_id": 1375666, "tmdb_id": 27205,
         "season_number": null, "episode_number": null,
         "parent_imdb_id": null, "parent_title": null, "parent_tmdb_id": null, "parent_feature_id": null },
      "url": "https://www.opensubtitles.com/en/subtitles/…",
      "related_links": [ { "label": "…", "url": "…", "img_url": "…" } ],
      "files": [ { "file_id": 998877, "cd_number": 1, "file_name": "Inception.2010.720p.BluRay.x264-REWARD.srt" } ],
      "moviehash_match": true } } ] }
```

`POST /download` `{"file_id": 998877, "sub_format": "srt", "file_name": "...", "in_fps": 23.976, "out_fps": 25, "timeshift": 0.5, "force_download": false}` → `{"link":"https://www.opensubtitles.com/download/…","file_name":"…","requests":3,"remaining":17,"message":"Your quota will be renewed in 20 hours and 12 minutes (2026-09-19 03:12:00 UTC)","reset_time":"20 hours and 12 minutes","reset_time_utc":"2026-09-19T03:12:00.000Z"}`. The `link` is **valid ~3 h** and is fetched with a plain GET (no auth). Each successful `/download` consumes one unit of the daily quota.

Status → behaviour (Bazarr `opensubtitlescom.py`): 400 `ConfigurationError` (message from JSON); 401 reset token, re-login once; 403 "API key problem" `ProviderError`; **406 `DownloadLimitExceeded`** (body has `remaining`, `reset_time`); 410 "download link expired"; **429 `TooManyRequests`**; 502 `APIThrottled` (sleep 15 s, up to 3 retries); other 5xx `ProviderError`. Forced detection: `is_real_forced = foreign_parts_only && !hearing_impaired`. Bazarr filters `ai_translated`/`machine_translated` both in the query and again on the results. Feature lookups (`GET /features?imdb_id=`) are cached for a week.

Other endpoints worth using: `GET /features?imdb_id|tmdb_id|query=` (feature/episode ids, `feature_type`), `GET /utilities/guessit?filename=` (server-side release parsing), `GET /infos/languages`, `DELETE /logout`.

### 4.4 OpenSubtitles `moviehash` — verified algorithm

`hash = uint64(filesize) + Σ(first 65536 bytes as 8192 little-endian uint64) + Σ(last 65536 bytes as 8192 LE uint64)`, wrapping mod 2^64, printed as 16 lowercase hex digits. Files smaller than 128 KiB should not be hashed (the Python reference raises `SizeError` below 131072; `oz/osdb` only rejects < 65536, which is looser than the spec).

Verified: a 300000-byte random file hashed to `1606fd38140b6f23` with (a) the Python reference implementation, (b) `github.com/oz/osdb.Hash`, and (c) the Go function below.

```go
const osChunk = 64 * 1024

func MovieHash(f *os.File) (uint64, error) {
	st, err := f.Stat()
	if err != nil { return 0, err }
	if st.Size() < 2*osChunk { return 0, ErrFileTooSmall }
	sum := uint64(st.Size())
	buf := make([]byte, osChunk)
	for _, off := range []int64{0, st.Size() - osChunk} {
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF { return 0, err }
		for i := 0; i < osChunk; i += 8 { sum += binary.LittleEndian.Uint64(buf[i:]) }
	}
	return sum, nil
}
func MovieHashString(h uint64) string { return fmt.Sprintf("%016x", h) }
```

### 4.5 Whisper as a provider (contract Bazarr relies on)

Server contract (whisper-asr-webservice, mirrored by subgen; Bazarr wiki now recommends **subgen**):
- `POST /asr?task=transcribe|translate&language=<alpha2>&output=srt&encode=false[&video_file=<name>]` multipart `audio_file` → SRT body. Other `output`: `txt, vtt, tsv, json`; other flags: `word_timestamps`, `vad_filter`, `diarize`, `min_speakers`, `max_speakers`, `initial_prompt` (webservice).
- `POST /detect-language?encode=false` multipart `audio_file` → `{"detected_language":"english","language_code":"en","confidence":0.98}`.
- Images: `mccloud/subgen:latest` (GPU+CPU) / `mccloud/subgen:cpu`; `onerahmet/openai-whisper-asr-webservice:latest|latest-gpu` (env `ASR_MODEL`, `ASR_ENGINE=openai_whisper|faster_whisper|whisperx`); `ghcr.io/mutablelogic/go-whisper` (cpu/cuda/vulkan; OpenAI-compatible `/v1/audio/transcriptions|translations`, `response_format=srt|vtt|json`). subgen also exposes OpenAI-compatible `/v1/audio/transcriptions`, `/status`, `/batch`, and webhooks for Plex/Jellyfin/Emby/Tautulli.
- Bazarr feeds **raw PCM**, not the container: `ffmpeg -nostdin -threads 0 -i <file> -map 0:a:<idx> -f s16le -acodec pcm_s16le -ac 1 -ar 16000 [-af adelay=…|atrim=…] -` (delay/trim compensation when the audio `start_time` > 20 ms, e.g. Amazon WEB-DL). Config: `endpoint` (default `http://127.0.0.1:9000`), `response` (connect timeout, 5 s), `timeout` (read timeout, **3600 s**), `loglevel`, `pass_video_name`. Throttle: `ConnectionError` → 5 min.
- Task decision: if the file has no audio-language tags → `/detect-language`; map `gsw→deu`, `und→eng`; if an audio track's language equals the wanted language → `transcribe` on that track (`force_audio_stream`), else `translate` (Whisper can only translate **into English**; a request for non-English from foreign audio is rejected upstream). Ambiguous language codes force detection.
- Score: `get_matches` yields `series+season+episode` (220/360 = **61 %**) for episodes and `title` (60/180 = **33 %**) for movies — fixed; Bazarr uses whisper only as a **fallback when no provider returned anything** (`fallback_allowed`), and users must lower min-score for it to auto-accept.
- In-process alternative (Go): `github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper` (cgo; `make whisper` in `bindings/go`, `C_INCLUDE_PATH`/`LIBRARY_PATH` to the built `libwhisper.a`; `GGML_CUDA=1` for CUDA). API: `whisper.New(modelPath) Model`, `model.NewContext() Context`, `ctx.SetLanguage("auto"|"en")`, `SetTranslate(bool)`, `SetThreads`, `SetVAD/SetVADModelPath/SetVADThreshold…`, `SetInitialPrompt`, `Process(samples []float32, encoderBegin, segmentCb, progressCb)`, `NextSegment() (Segment{Num, Start, End time.Duration, Text, Tokens}, error)`; audio must be `whisper.SampleRate` (16 kHz) mono float32. Module go.mod says `go 1.23`, no semver tags (pseudo-version `v0.0.0-20260918050654-7a2ceef966cd`).

### 4.6 Embedded-extraction provider

Bazarr's `embeddedsubtitles` provider treats the file's own text streams as candidates: `included_codecs = ("ass","subrip","webvtt","mov_text")`, extracts with ffmpeg (`fese` lib, `copy_subtitles(fallback_to_convert=True)`), marks forced from `disposition.forced`, HI from disposition, `hi_fallback` (treat HI-only track as plain), `unknown_as_fallback`+`fallback_lang` for `und` streams, `timeout` 600 s, and reports matches `{"hash", "hearing_impaired"}` (i.e. full score). Worth replicating: it lets a language profile be satisfied by muxed subs turned into sidecars (players and sync tools prefer sidecars).

---

## 5. Scoring and matching (verified weights)

`custom_libs/subliminal_patch/score.py`:

```python
DEFAULT_SCORES = {
  "episode": {"hash": 359, "series": 160, "year": 90, "season": 30, "episode": 30, "source": 25,
              "release_group": 20, "audio_codec": 1, "resolution": 1, "video_codec": 1,
              "hearing_impaired": 1, "streaming_service": 1},
  "movie":   {"hash": 179, "title": 60, "year": 40, "source": 30, "edition": 30, "release_group": 15,
              "audio_codec": 1, "resolution": 1, "video_codec": 1, "hearing_impaired": 1, "streaming_service": 1},
}
MAX_SCORES = {"episode": 360, "movie": 180}   # sum of all non-hash weights
# invariant checked at load: hash == (sum of non-hash weights) - 1  (so a hash match == "everything but HI")
```

`compute_score(matches, subtitle, video, hearing_impaired)`:
1. If `subtitle.hash_verifiable` and `"hash" in matches`: the hash is only trusted when the corroborating matches are present — episodes need `{series, season, episode, source}` (specials skip season/episode validation), movies need `{video_codec, source}`; if so `matches = {"hash"}` else drop `hash`. If the provider is not hash-verifiable, a `hash` match also collapses to `{"hash"}`.
2. If `hearing_impaired` preference is set and `subtitle.hearing_impaired == preference` add `hearing_impaired` (+1).
3. `score = Σ weights[m]`; `score_without_hash` computed too (sorting key 2).

Minimum score: settings are **percentages** — `minimum_score` (episodes) default **90** → `int(360*90/100) = 324`; `minimum_score_movie` default **70** → `126`. Whisper at 61 %/33 % therefore never auto-passes with defaults.

Where matches come from:
- Provider `get_matches(video)`: e.g. OpenSubtitles adds `series/season/episode/series_imdb_id` or `title/imdb_id`, `year` (if imdb matched or equal), `hash` (from `moviehash_match`), `release_group` (sanitised + "equivalent release groups").
- Generic `guess_matches(video, guessit(release_info))`: `series` (title vs series + alternative titles), `title` (episode title), `season`, `episode` (handles lists/packs), `year` (or absent year for original-series), movie `title`/`year`, `release_group` (sanitised), `source` with `MERGED_FORMATS` buckets (`TV`={HDTV,SDTV,AHDTV,Ultra HDTV}, `Air`={SATRip,DVB,PPV,Digital TV}, `Disk-HD`={HD-DVD,Blu-ray,Ultra HD Blu-ray}, `Disk-SD`={DVD,VHS}, `Web`={Web}), `resolution` (guessit `screen_size`), `video_codec`, `audio_codec`, `streaming_service`, `edition`. Implication for Clustarr: the inventory/indexer's release parser must expose those same fields for the local file so the scorer can compare.

Selection (`download_best_subtitles`): sort by `(score, score_without_hash)` desc → for each candidate: stop if `score < min_score`; skip if its language already got a file; if `hearing_impaired_verifiable` and the HI preference is "force HI"/"force non-HI" and HI does not match, skip; for episodes skip if `season/episode/series/imdb_id` verification fails; try download — on failure fall through to the next; `only_one` stops after the first success; if nothing downloaded and whisper is enabled, run whisper as fallback. Before that, `list_subtitles` applies `_Banlist` (must_contain / must_not_contain regexes on `release_info`) and `_Blacklist` (`(provider, subtitle_id)` pairs from `TableBlacklist`).

---

## 6. Post-processing (verbatim behaviours worth porting)

1. **Encoding** (`Subtitle.guess_encoding`): BOM sniff → try `utf-8` → language-specific candidates → `chardet`. The language table (alpha3 → encodings): `zho`: cp936, gb2312, gbk, hz, iso2022_jp_2, cp950, big5hkscs, big5, gb18030, utf-16; `jpn`: shift-jis, cp932, euc_jp, iso2022_jp(_1,_2); `tha`: tis-620, cp874; `ara/fas/per`: windows-1256, utf-16(le), ascii, iso-8859-6; `heb`: windows-1255, iso-8859-8; `tur`: windows-1254, iso-8859-9, iso-8859-3; `ell/gre/grc`: windows-1253, cp1253, cp737, iso8859-7, cp875, cp869…; Central-European (`pol, ces, slk, slv, hun, bos, hrv, hbs, rsb…`): windows-1250, iso-8859-2 (+ iso-8859-4 for slv; Albanian gets windows-1252 family); Cyrillic (`bul, mkd, rus, ukr`): windows-1251, iso-8859-5; `srp`: by script (Latn → 1250/8859-2, Cyrl → 1251/8859-5, else both); default Western: windows-1252, iso-8859-15, iso-8859-9, iso-8859-4, iso-8859-1. Output is always UTF-8 when `utf8_encode` is on. Go: `github.com/gogs/chardet` (`NewTextDetector().DetectBest(b) → Result{Charset, Language, Confidence}`) + `golang.org/x/text/encoding/ianaindex` (`ianaindex.IANA.Encoding(name)`) + `transform.NewReader`.
2. **Mojibake fix**: `ftfy.fix_text` on every write — no Go equivalent; skip (low value once encoding detection is right).
3. **Mods** (subzero): `remove_HI`, `remove_tags` (font/bold/italic/color tags), `OCR_fixes`, `common` (whitespace/punctuation), `fix_uppercase`, `reverse_rtl`, `color`. The HI processor list, verbatim (Python `re`, `%(t)s` = optional style tag pattern):
   - `HI_brackets_full`: `(?sux)^-?TAG[([].+(?=[^)\]]{3,}).+[)\]]TAG$` (whole entry is a bracketed cue → drop entry)
   - `HI_before_colon_caps`: `(?u)(?:(?<=^)|(?<=[.\-!?\"\'])\s)([\s\->~]*(?=[A-ZÀ-Ž&+]\s*[A-ZÀ-Ž&+]\s*[A-ZÀ-Ž&+])[A-zÀ-ž-_0-9\s\"\'&+()\[\],:]+:(?![\"\'’ʼ❜‘‛”“‟„])(?:\s+|$))(?![0-9])` (uppercase speaker label `JOHN:` → remove)
   - `HI_before_colon_noncaps`: same idea, mixed case, guarded by a "contains lowercase" heuristic
   - `HI_brackets`: `(?sux)-?TAG["\']*\[(?=[^\[\]]{3,})[A-Za-zÀ-ž0-9\s\'".:-_&+]+[)\]]["\']*[\s:]*TAG` (inline `[door slams]`)
   - `HI_all_caps`: `(?u)(^(?=.*[A-ZÀ-Ž&+]{4,})[A-ZÀ-Ž-_\s&+]+$)` — only removed when the line contains sound cues (`LAUGH, APPLAU, CHEER, MUSIC, GASP, SIGH, GROAN, COUGH, SCREAM, SHOUT, WHISPER, PHONE, DOOR, KNOCK, FOOTSTEP, THUNDER, EXPLOSION, GUNSHOT, SIREN`)
   - `HI_remove_man`: `(?suxi)(\b(?:WO)MAN:\s*)`
   - `HI_starting_upper_then_sentence`: `(?u)^(?=[A-ZÀ-Ž]{4,})[A-ZÀ-Ž-_\s]+\s([A-ZÀ-Ž][a-zà-ž].+)` → `\1`
   - `JP_parentheses`: `(?u)（.+）|\(.+\)` (only for Japanese)
   - last: `HI_music_symbols_only`: `(?u)(^TAG[*#¶♫♪\s]*TAG[*#¶♫♪\s]+TAG[*#¶♫♪\s]*TAG$)`; `HI_music`: `(?ums)(^[-\s>~]*[*#¶♫♪]+\s*.+|.+\s*[*#¶♫♪]+\s*$|.+\s*[*#¶♫♪]+[)\]])`
   Several use lookbehind/lookahead — Go's `regexp` (RE2) cannot; use `github.com/dlclark/regexp2 v1.12.0` (.NET-style engine, supports lookaround) for a faithful port, or rewrite with token scanning.
4. **Format**: SRT is the output unless `originalFormat` is set on the profile; ASS/SSA are converted via pysubs2. Go: `github.com/asticode/go-astisub v0.45.0` — `ReadFromSRT/ReadFromSSA/ReadFromWebVTT/ReadFromTTML/ReadFromSTL/ReadFromTeletext`, `Subtitles.WriteToSRT/WriteToSSA/WriteToWebVTT/WriteToTTML/WriteToSTL`, `RemoveStyling()`, `Add(d)` (shift), `ApplyLinearCorrection(actual1, desired1, actual2, desired2)`, `Fragment/Unfragment`, `Optimize()`, `Order()`; items are `Item{Index, StartAt, EndAt, Lines []Line{Items []LineItem{Text, InlineStyle}}, Style, Region}`. It has no charset handling — decode to UTF-8 first.
5. **Post-processing command** variables: `{{directory}} {{episode}} {{episode_name}} {{subtitles}} {{subtitles_language}} {{subtitles_language_code2}} {{subtitles_language_code3}} {{episode_language}} {{episode_language_code2}} {{episode_language_code3}} {{score}} {{subtitle_id}} {{provider}} {{uploader}} {{release_info}} {{series_id}} {{episode_id}}`.

---

## 7. Sync (ffsubsync / alass)

**ffsubsync 0.5.1** (2026-07-24, Python, pip `ffsubsync`, needs ffmpeg). Algorithm: discretise audio and subtitles into 10 ms windows → VAD (`--vad webrtc` default, `auditok`, `silero` (PyTorch), and `subs_then_*` fused variants) → FFT cross-correlation of the two binary speech signals → global offset; framerate ratio search (`--gss` golden-section search, `--no-fix-framerate`); 0.5.x adds `--split-penalty` for alass-style piecewise alignment. Other flags: `--max-offset-seconds` (default 60), `--reference-stream a:N`, `--output-encoding same|utf-8`, `--ffmpegpath`, `--overwrite-input`, `--apply-offset-seconds`, `--start-seconds/--max-duration-seconds`, `--extract-subs-from`. Usage: `ffs video.mkv -i in.srt -o out.srt` or `ffs reference.srt -i in.srt -o out.srt`.

Bazarr's exact invocation (`SubSyncer`): `[reference, '-i', srtin, '-o', srtout, '--ffmpegpath', ffmpeg, '--vad', vad, '--log-dir-path', dir, '--max-offset-seconds', N, '--output-encoding', 'same'] + ['--no-fix-framerate'] + ['--gss'] + ['--reference-stream', 'a:N']`. Reference stream = the audio track whose language equals the item's *original language* (from Sonarr/Radarr), otherwise `a:0`; an explicit `reference` (`a:1`, `s:0`, or a path to another subtitle) overrides. It reads back `offset_seconds` and `framerate_scale_factor` and logs history action 5. Settings: `use_subsync` False, `use_subsync_threshold`/`subsync_threshold` 90 (episodes), `use_subsync_movie_threshold`/`subsync_movie_threshold` 70 — when enabled, only subtitles scoring **below** the threshold are synced (hash matches are assumed in sync), `max_offset_seconds` 60, `no_fix_framerate` True, `gss` True.

**alass v2.0.0** (Rust; last tag 2019-10, last push 2023-12, 1.4 k stars; not in Bazarr, feature request open). `alass <video.mkv | ref.srt> in.srt out.srt [--split-penalty 7] [--no-splits]`; needs `ffmpeg`/`ffprobe` on PATH (`ALASS_FFMPEG_PATH`, `ALASS_FFPROBE_PATH`); handles constant offsets, **ad-break splits**, and 23.976↔25 fps mismatches; formats srt/ssa/ass/idx. Split penalty 0–1000, useful 5–20.

Go options: (a) wrap the CLIs in the worker image (Python ffsubsync + static alass binary) behind a `Syncer` interface — cheapest and proven; (b) later, a pure-Go syncer: extract 16 kHz PCM with ffmpeg, VAD (Silero ONNX via `onnxruntime` or webrtcvad cgo), FFT cross-correlation (`gonum/fourier`), then apply with `astisub.Add` / `ApplyLinearCorrection`. Not verified: any maintained pure-Go VAD.

---

## 8. Upgrade behaviour (verbatim rules)

- Runs every `upgrade_frequency` hours (default 12) when `upgrade_subs` (default True).
- Candidates: `TableHistory` rows with `timestamp > now − days_to_upgrade_subs` (default 7), `action ∈ [1,3]` (downloaded, upgraded) or `[1,2,3,4,6]` when `upgrade_manual` (default True), and `score < score_out_of − 3` (or score NULL with action 6). Latest row per (media, language) wins; the chain is kept via `upgradedFromId`.
- For each: `generate_subtitles(..., forced_minimum_score = score + 1, is_upgrade = True, previous_subtitles_to_delete = <old path>)` — a strictly better candidate replaces the file and logs action 3.
- There is no explicit "deadline"; the 7-day window is the deadline (a subtitle older than `days_to_upgrade_subs` is never upgraded again unless re-downloaded).

## 9. Search scheduling and adaptive back-off (verbatim)

- Wanted search jobs: `wanted_search_frequency` (series) and `wanted_search_frequency_movie`, both default **6 h**; also triggered on new file / upgrade events.
- Per-item column `failedAttempts` = `[[lang, unix_ts], ...]`; `updateFailedAttempts` keeps only the **initial** and **latest** timestamp per language.
- `is_search_active(lang, attempts)` with `adaptive_searching` (True), `adaptive_searching_delay` ∈ {`1w,2w,3w,4w`} (default `3w`), `adaptive_searching_delta` ∈ {`3d,1w,2w,3w,4w`} (default `1w`):
  - no attempts for that language → search;
  - `initial + delay > now` → search on every scheduled run;
  - else search only if `latest + delta <= now`.
  So: full cadence for 3 weeks after first failure, then once a week forever.
- Attempts are stamped only when a search returns nothing; success clears the item from the wanted list.

## 10. Provider throttling / back-off (verbatim map)

Persisted `throttled_providers.dat`: `{provider: (exception_name, until_datetime, human_text)}`; `get_providers()` skips a provider while `now < until`; expired entries are cleared. In addition a **5-strikes-in-120 s** in-memory counter (`throttled_count`) throttles a provider immediately on repeated `TooManyRequests/ServiceUnavailable/APIThrottled/Timeout`. HTTP sessions honour `Retry-After` on 429 (`RateLimiting` in `subliminal_patch/http.py`).

| exception | default | opensubtitlescom | addic7ed | subdl | subsource | others |
|---|---|---|---|---|---|---|
| TooManyRequests | 1 h | **1 min** | 5 min | — | 1 h | titlovi 5 min, titrari 10 min, titulky 1 min, legendasdivx 3 h, regielive 5 min |
| DownloadLimitExceeded | 3 h | **6 h** | 3 h | until 00:00 GMT +1 h | — | titulky until 00:00 Europe/Prague; legendasdivx until 00:00 Europe/Lisbon +1 h |
| ServiceUnavailable | 20 min | | | | | |
| APIThrottled | 10 min | | | 15 min | 10 min | regielive 1 h |
| ParseResponseError | 6 h | | | | | |
| Timeout / ConnectTimeout / ReadTimeout / socket.timeout / ProxyError | 1 h | | | | | whisperai ConnectionError 5 min |
| ConfigurationError / PermissionError / AuthenticationError | 12 h | | | | subsource AuthenticationError 1 h, ForbiddenError 15 min | |
| ProviderError | not throttled | 1 min | | 1 h | | regielive 10 min |
| IPAddressBlocked / SearchLimitReached | — | | 1 h | | | legendasdivx until midnight Lisbon +1 h |

Exception taxonomy to keep: `ProviderError` (base), `ConfigurationError`, `AuthenticationError`, `ForbiddenError`, `ServiceUnavailable`, `DownloadLimitExceeded`, `TooManyRequests`, `APIThrottled`, `ParseResponseError`, `IPAddressBlocked`, `SearchLimitReached`, `MustGetBlacklisted` (bad file → blacklist that subtitle id), plus network timeouts.

## 11. History action codes (`TableHistory.action`)

`0` erased, `1` downloaded, `2` manual download, `3` upgraded, `4` manual upload, `5` synced, `6` translated. Row also stores `score`, `score_out_of`, `provider`, `subs_id`, `subtitles_path`, `language`, `matched`/`not_matched` (match sets), `upgradedFromId`.

---

## 12. Go libraries (versions verified with `go list -m` on 2026-09-18)

| module | version | role | verdict |
|---|---|---|---|
| `github.com/asticode/go-astisub` | v0.45.0 (2026-09-17) | parse/convert SRT, SSA/ASS, WebVTT, TTML, STL, Teletext; shift/linear-correct; strip styling | **use** |
| `gopkg.in/vansante/go-ffprobe.v2` (= `github.com/vansante/go-ffprobe/v2`) | v2.3.1 (2026-09-14) | typed ffprobe JSON incl. `disposition.forced/hearing_impaired`, tags | **use** |
| `github.com/oz/osdb` | v0.0.0-20221214175751-f169057712ec (2022-12-14, 76★) | XML-RPC client for the **dead** .org API; `Hash()` is correct | **do not depend**; copy the 20-line hash |
| `github.com/odwrtw/opensubtitles` | v0.0.0-20221028215647-e762df0d6349 (2022-10-28) | minimal REST v1 client (`NewClient(apiKey,user,pass)`, `Login`, `Search`, `SearchByFile`, `DownloadSearch`, `Download`, `UserInfo`, `Hash`) — note its JSON tags `hearing_impared`/`movie_hash_match` are **wrong** vs the API (`hearing_impaired`, `moviehash_match`) | reference only; write our own client |
| `github.com/ggerganov/whisper.cpp/bindings/go` (repo now `ggml-org/whisper.cpp`) | v0.0.0-20260918050654-7a2ceef966cd | in-process whisper (cgo, needs built `libwhisper.a`) | optional; prefer HTTP to a GPU service |
| `github.com/mutablelogic/go-whisper` | v0.0.39 (2026-01-27) | Go whisper server/CLI/lib (cgo), OpenAI-compatible HTTP, docker cpu/cuda/vulkan | candidate whisper backend image |
| `github.com/gogs/chardet` | v0.0.0-20211120154057-b7413eaefb8f | charset detection (port of ICU/juniversalchardet) | **use** (or `saintfish/chardet` v0.0.0-20230101081208) |
| `golang.org/x/text` | v0.42.0 | `language.Tag` for BCP-47, `encoding/*` + `ianaindex` transcoding | **use** |
| `github.com/dlclark/regexp2` | v1.12.0 | lookaround regexes for HI-removal mods | **use** for mods |
| `github.com/pemistahl/lingua-go` | v1.4.0 | content-based language detection for untagged sidecars | use |
| `github.com/abadojack/whatlanggo` | v1.0.1 | lighter alternative | alt |
| `golang.org/x/time` | v0.16.0 | `rate.Limiter` token bucket (5 req/s OpenSubtitles) | **use** |
| `github.com/nats-io/nats.go` | v1.53.1 | JetStream work queues + KV (`jetstream.New(nc)`, `CreateOrUpdateStream`, `CreateOrUpdateConsumer`, `Consumer.Consume/Fetch`, `Msg.Ack/Nak/NakWithDelay/InProgress/Term`, `MsgIDHeader = "Nats-Msg-Id"`, `WorkQueuePolicy`, `AckExplicitPolicy`, `ConsumerConfig{AckWait, MaxDeliver, BackOff, MaxAckPending, FilterSubjects}`, `StreamConfig{Duplicates}`) | **use** (given) |
| `github.com/razsteinmetz/go-ptn` | v1.0.0 (2022) / `github.com/middelink/go-parse-torrent-name` v0.0.0-20190301 | release-name parsers (guessit-lite) | weak; the indexer/inventory research should pick the parser; the scorer only needs the parsed fields |
| `github.com/asticode/go-astiav` | v0.42.0 | libav bindings (cgo) if we ever want in-process demux/decode instead of exec ffmpeg | later |
| `github.com/kalafut/imohash` | v1.1.1 | fast sampled hash for change detection (not the OS hash) | optional |

Not found / not viable: no maintained Go client for Addic7ed/Gestdown/SubDL/SubSource (all trivial HTTP+JSON, write in-tree); `tympanix/supper` (Go subtitle downloader, v1.0.5 2020) and `matcornic/subify` (v0.6.0 2025) exist but target dead APIs/scrapers.

---

## 13. Go design for `pkg/subtitles`

### 13.1 Core types

```go
package subtitles

import ("time"; "golang.org/x/text/language")

// Lang is Bazarr's Language: a BCP-47 tag plus the two flags that make distinct "wanted" keys.
type Lang struct {
	Tag    language.Tag // "en", "pt-BR", "zh-Hant", "sr-Latn"
	Forced bool
	HI     bool
}
// Key renders the Bazarr wire form used in wanted lists and task subjects: "en", "en:forced", "en:hi".
func (l Lang) Key() string
func ParseLangKey(s string) (Lang, error)      // accepts "en", "en:forced", "en:hi", "en@hi", "es-MX@forced"
func (l Lang) Base() Lang                       // flags cleared
func (l Lang) Alpha2() string                   // "en"   (for providers that want 639-1)
func (l Lang) Alpha3() string                   // "eng"  (639-2/T; providers/ffprobe use 639-2/B for some — map fre/ger/chi/dut...)
func (l Lang) OpenSubtitles() string            // "pt-br", "zh-tw", "pb"… provider-specific casing

type HIMode string
const (
	HIAny      HIMode = "any"      // Bazarr "False": prefer non-HI, accept HI
	HIRequired HIMode = "required" // Bazarr "True"
	HIExcluded HIMode = "excluded" // Bazarr "Excluded"
)

type ProfileItem struct {
	Lang             language.Tag `json:"language"`
	Forced           bool         `json:"forced"`
	HI               HIMode       `json:"hi"`
	AudioExclude     bool         `json:"audioExclude"`
	AudioOnlyInclude bool         `json:"audioOnlyInclude"`
}

// LanguageProfile is the CRD spec (namespaced), 1:1 with Bazarr's TableLanguagesProfiles.
type LanguageProfile struct {
	Name           string        `json:"name"`
	Items          []ProfileItem `json:"items"`
	Cutoff         *int          `json:"cutoff,omitempty"`        // index into Items; nil = none; -1 = "any"
	MustContain    []string      `json:"mustContain,omitempty"`   // RE2 regexes, case-insensitive, all must match release
	MustNotContain []string      `json:"mustNotContain,omitempty"`
	OriginalFormat bool          `json:"originalFormat"`          // keep ASS/SSA instead of converting to SRT
	MinScorePct    *int          `json:"minScorePercent,omitempty"` // default 90 episodes / 70 movies
	Sync           *SyncPolicy   `json:"sync,omitempty"`
	Upgrade        *UpgradePolicy `json:"upgrade,omitempty"`
	LanguageEquals []LangEqual   `json:"languageEquals,omitempty"` // {From:"pt-BR", To:"pt"}
	Mods           []string      `json:"mods,omitempty"`           // "remove_HI","remove_tags","OCR_fixes","common","fix_uppercase","reverse_rtl"
	HIExtension    string        `json:"hiExtension,omitempty"`    // "hi" | "cc" | "sdh"
}
type SyncPolicy struct { Enabled bool; Engine string /* ffsubsync|alass */; ScoreBelowPct int /* 90/70 */; MaxOffsetSeconds int /* 60 */; FixFramerate bool; GSS bool; SplitPenalty *int }
type UpgradePolicy struct { Enabled bool; WindowDays int /* 7 */; IncludeManual bool /* true */; Every time.Duration /* 12h */ }
type LangEqual struct { From, To language.Tag }

// MediaRef is what the inventory service must hand us (subset of Radarr/Sonarr video model + parsed release).
type MediaKind string
const (MediaMovie MediaKind = "movie"; MediaEpisode MediaKind = "episode")
type MediaRef struct {
	ID        string    // inventory id (stable)
	Kind      MediaKind
	Path      string    // absolute path on the shared volume
	Size      int64
	MovieHash string    // 16 hex, "" if < 128 KiB
	Title     string    // movie title or episode title
	Year      int
	Series    string; AltSeries []string; Season, Episode int; AbsoluteEpisode int; SeriesYear int
	IMDBID, TMDBID, TVDBID string; ParentIMDBID, ParentTMDBID, ParentTVDBID string
	OriginalLanguage language.Tag
	Release   Release  // parsed from the file name / scene name
	Audio     []AudioStream
	Existing  []ExistingSubtitle
	ProfileRef string
}
type Release struct { Group, Source, Resolution, VideoCodec, AudioCodec, StreamingService, Edition, SceneName string }
type AudioStream struct { Index int; Spec string /* "a:0" */; Lang language.Tag; Title string; Default bool }
type ExistingSubtitle struct { Lang Lang; Path string /* "" for embedded */; Stream int; Codec string; Embedded bool; Size int64 }
```

### 13.2 Planner (missing computation)

```go
// Wanted implements §2.2 exactly; returns the ordered list of Lang keys still missing.
func Wanted(p LanguageProfile, m MediaRef, opts PlanOptions) []Lang
type PlanOptions struct { UseEmbedded bool; IgnoreCodecs []string /* hdmv_pgs_subtitle, dvd_subtitle, ass */ }
```

### 13.3 Provider interface and candidates

```go
type Capabilities struct {
	Movies, Episodes    bool
	ForcedSearch        bool          // false for Bazarr's PROVIDERS_FORCED_OFF
	HashSearch          bool          // OpenSubtitles
	HashVerifiable      bool          // sets subtitle.hash_verifiable
	HIVerifiable        bool          // opensubtitlescom, gestdown, subdl, subsource, embedded
	Languages           func(Lang) bool
	NeedsSecrets        []string      // "apiKey","username","password","cookie"
	Generates           bool          // whisper/embedded: produces rather than fetches
}

type Query struct {
	Media     MediaRef
	Languages []Lang            // all wanted keys for this media (providers may batch)
	MinScore  int               // absolute; upgrade sets score+1
	HIMode    map[language.Tag]HIMode
}

type Match uint32
const (
	MatchHash Match = 1 << iota; MatchSeries; MatchTitle; MatchYear; MatchSeason; MatchEpisode
	MatchSource; MatchReleaseGroup; MatchAudioCodec; MatchResolution; MatchVideoCodec
	MatchHearingImpaired; MatchStreamingService; MatchEdition; MatchIMDB; MatchSeriesIMDB; MatchTVDB
)

type Candidate struct {
	Provider   string
	ID         string           // provider-scoped id (blacklist key = Provider+ID)
	Lang       Lang             // provider-declared language + forced/hi flags
	Release    string           // release_info used by must(Not)Contain and guess matching
	Matches    Match            // provider-asserted matches (hash, imdb…) — guessMatches() adds the rest
	Uploader   string; Trusted bool; Downloads int; Votes int; FPS float64
	AITranslated, MachineTranslated bool
	Fetch      FetchHandle      // opaque: {FileID int, URL string, Extra map[string]string}
	Format     string           // "srt","ass","zip" if known
}

type Payload struct { Content []byte; Format string; DeclaredEncoding string; FileName string }

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Search(ctx context.Context, q Query) ([]Candidate, error)
	Fetch(ctx context.Context, c Candidate) (Payload, error)
}

// ProviderError carries the Bazarr taxonomy so the pool can throttle uniformly.
type ErrKind string
const (
	ErrTooManyRequests ErrKind = "too_many_requests"; ErrDownloadLimit ErrKind = "download_limit"
	ErrServiceUnavailable ErrKind = "service_unavailable"; ErrAPIThrottled ErrKind = "api_throttled"
	ErrParseResponse ErrKind = "parse_response"; ErrAuth ErrKind = "auth"; ErrForbidden ErrKind = "forbidden"
	ErrConfig ErrKind = "config"; ErrIPBlocked ErrKind = "ip_blocked"; ErrSearchLimit ErrKind = "search_limit"
	ErrTimeout ErrKind = "timeout"; ErrBlacklist ErrKind = "must_blacklist"; ErrProvider ErrKind = "provider"
)
type ProviderError struct { Provider string; Kind ErrKind; RetryAfter time.Duration; ResetAt time.Time; Remaining int; Err error }
```

### 13.4 Scoring

```go
var EpisodeWeights = map[Match]int{MatchHash: 359, MatchSeries: 160, MatchYear: 90, MatchSeason: 30, MatchEpisode: 30,
	MatchSource: 25, MatchReleaseGroup: 20, MatchAudioCodec: 1, MatchResolution: 1, MatchVideoCodec: 1,
	MatchHearingImpaired: 1, MatchStreamingService: 1}
var MovieWeights = map[Match]int{MatchHash: 179, MatchTitle: 60, MatchYear: 40, MatchSource: 30, MatchEdition: 30,
	MatchReleaseGroup: 15, MatchAudioCodec: 1, MatchResolution: 1, MatchVideoCodec: 1,
	MatchHearingImpaired: 1, MatchStreamingService: 1}
const (MaxEpisodeScore = 360; MaxMovieScore = 180)

// Score implements compute_score(): hash validation, HI bonus, score & scoreWithoutHash.
func Score(kind MediaKind, m Match, c Candidate, wantHI HIMode, hashVerifiable bool) (score, withoutHash int)
func MinScore(kind MediaKind, pct int) int  // int(max*pct/100)
// GuessMatches derives Match bits from Release vs MediaRef (series/title/year/season/episode/group/source/resolution/codecs/service/edition).
func GuessMatches(m MediaRef, releaseInfo string) Match
// Select picks, per wanted Lang, the best candidate honouring HIMode, banlist, blacklist, forced-capability and min score.
func Select(kind MediaKind, want []Lang, cands []Candidate, p LanguageProfile, bl Blacklist) map[Lang]Ranked
type Ranked struct { Candidate; Score, WithoutHash int; Matches Match }
```

### 13.5 Fetch / normalise / write / sync pipeline

```go
type Normalizer interface {
	// Decode → UTF-8, unzip if needed, convert to SRT unless keepFormat, apply mods.
	Normalize(ctx context.Context, p Payload, lang Lang, keepFormat bool, mods []string) (Payload, error)
}
type Writer interface {
	// SidecarPath: <stem>.<lang.basename>[.forced|.<hiExt>].<ext>; atomic temp+rename.
	SidecarPath(m MediaRef, l Lang, ext, hiExt string, layout SubfolderLayout) string
	Write(ctx context.Context, path string, p Payload) error
}
type Syncer interface { // ffsubsync / alass wrappers; both take a reference stream or subtitle
	Sync(ctx context.Context, m MediaRef, subtitlePath string, ref string /* "a:0" or path */, pol SyncPolicy) (SyncResult, error)
}
type SyncResult struct { OffsetSeconds float64; FramerateScale float64; Splits int; Engine string }

type Pipeline struct { Providers []Provider; Throttle ThrottleStore; Quota QuotaStore; Norm Normalizer; W Writer; Sync Syncer; Hist HistoryStore }
// Run executes one task: search → select → fetch(fallthrough) → normalise → write → sync? → history.
func (p *Pipeline) Run(ctx context.Context, t Task) (Outcome, error)
```

### 13.6 Provider implementations to ship first

1. `opensubtitles` — REST v1 client per §4.3 (own code; `rate.Limiter(5/s)`, single shared JWT via `QuotaStore`, `moviehash` when `Size >= 128 KiB`, `hearing_impaired`/`foreign_parts_only` derived from `HIMode`/`Forced`, `ai_translated=exclude` by default, `order_by=download_count`, map 406→ErrDownloadLimit with `ResetAt`, 429→ErrTooManyRequests with `Retry-After`).
2. `gestdown` (TV, TVDB id) and `subdl`, `subsource` (API keys) — trivial JSON.
3. `embedded` — ffmpeg `-map 0:s:N -c:s srt` extraction of text streams to sidecars (scores as hash).
4. `whisper` — HTTP client for the `/asr` + `/detect-language` contract (subgen / whisper-asr-webservice / go-whisper), PCM extraction exactly as §4.5, task decision (transcribe vs translate-to-English), fixed matches (`series+season+episode` / `title`), used only as fallback unless the profile opts in.
5. Later/opt-in: `addic7ed` (needs captcha solver secret), `yify`, `tvsubtitles`, anime (`jimaku`, `animetosho`).

---

## 14. Distributed task model (Kubernetes + NATS JetStream)

Principles: configuration lives in CRDs, **work lives in JetStream**, mutable per-item state lives in a NATS KV bucket (not in CRs — there will be hundreds of thousands of (item × language) pairs).

### 14.1 CRDs (config only)

- `SubtitleProfile` (namespaced) — spec = `LanguageProfile` above; status = counts of items bound / missing.
- `SubtitleProvider` (namespaced) — `spec: {type: opensubtitles|gestdown|subdl|subsource|whisper|embedded|addic7ed, secretRef, endpoint, options{useHash, includeAITranslated, timeout…}, priority, languages}`; `status: {throttledUntil, throttleReason, quota{allowed, remaining, resetAt}, lastError, lastSuccess}` — the status is a *mirror* of the KV entries for kubectl visibility; the controller updates it from KV watches.
- `SubtitleSyncBackend` (optional) — image/args for ffsubsync/alass.
- No per-task CRD. A `SubtitleRequest` CR can exist for manual/one-off requests (manual search UI, "upgrade this one now"); the controller turns it into a task message.

### 14.2 Streams, subjects, consumers

```
stream SUBTITLES_WANTED   retention=WorkQueue  subjects: subtitles.wanted.>   Duplicates=24h
   subject: subtitles.wanted.<kind>.<mediaID>.<langKey>          langKey: en | en-forced | en-hi (":" is not subject-safe)
   header : Nats-Msg-Id = <mediaID>/<langKey>/<fileSize>          (dedupe; size changes = new task)
   consumer "searcher"   AckExplicit, AckWait=15m, MaxDeliver=6, BackOff=[1m,10m,1h,6h,24h], MaxAckPending=<workers*2>
stream SUBTITLES_UPGRADE  retention=WorkQueue  subjects: subtitles.upgrade.>   (same key, body carries MinScore=score+1, PreviousPath)
stream SUBTITLES_SYNC     retention=WorkQueue  subjects: subtitles.sync.<mediaID>.<langKey>   consumer "syncer" (CPU-heavy, AckWait=30m)
stream SUBTITLES_GENERATE retention=WorkQueue  subjects: subtitles.generate.whisper.<mediaID>.<langKey>  consumer "whisper" (GPU pool, AckWait=2h, MaxAckPending=<gpus>)
stream SUBTITLES_EVENTS   retention=Limits (MaxAge 30d) subjects: subtitles.events.>   (downloaded/upgraded/synced/failed → history, UI, other services)
KV  subtitle-state     key <mediaID>/<langKey>   → {status, score, scoreOutOf, provider, subtitleID, path, size, attempts:{initial,latest,count}, nextSearchAt, lastError, upgradedFrom}
KV  provider-throttle  key <provider>            → {until, reason, text}      (TTL = until-now)
KV  provider-quota     key <provider>/<account>  → {allowed, remaining, resetAt, token, tokenExpiresAt}
KV  blacklist          key <provider>/<subtitleID> → {mediaID, langKey, reason, at}
```

Task message (JSON):

```jsonc
{ "mediaId": "mv_01J…", "kind": "movie", "lang": "en:forced", "profile": "default",
  "reason": "wanted|upgrade|manual|new-file", "minScore": 126, "previousPath": null,
  "attempt": { "initial": "2026-09-01T00:00:00Z", "latest": "2026-09-15T00:00:00Z", "count": 3 },
  "deadline": "2026-10-06T00:00:00Z" }
```

### 14.3 Who publishes

- **Planner controller** (controller-runtime): watches inventory media-item events (new file, file replaced, profile changed) and runs `Wanted()`; publishes one task per missing `Lang`. A **cron reconciler** (every `wantedSearchFrequency`, default 6 h) re-publishes tasks for KV entries with `status=missing` whose `nextSearchAt <= now` (adaptive gate computed from `attempts` exactly as §9 — no need for `failedAttempts` strings; store the two timestamps). Another cron (every 12 h) publishes `subtitles.upgrade` for entries `status=downloaded && downloadedAt > now-7d && score < scoreOutOf-3`.
- Dedupe means a re-publish while a task is in flight is dropped by the server (same `Nats-Msg-Id` within the 24 h window); to force a re-search, include a nonce (manual requests).

### 14.4 Worker behaviour

1. Load KV state (`status` must be `missing|upgradeable`; if `downloaded` with score ≥ target → `Term()` — idempotency guard against redeliveries).
2. Filter providers: enabled, supports kind/forced/language, not throttled in `provider-throttle` (all workers see the same throttle map — Bazarr's file becomes a KV).
3. Search providers concurrently with a per-call timeout; on `ProviderError` write `provider-throttle[<provider>] = now + map[kind]` (table §10) — first writer wins via KV `Create/Update` with revision.
4. Select → fetch (fall through) → normalise → write sidecar atomically → optional sync (publish to `subtitles.sync` instead of doing inline when engine is heavy) → `subtitle-state` update with CAS → publish event → `Ack()`.
5. Nothing found: update `attempts` (initial/latest), compute `nextSearchAt` (adaptive gate), `Ack()` (the cron will re-publish; do **not** rely on NAK for week-long delays).
6. Provider-wide throttling with nothing else to try: `NakWithDelay(min(untilAll, 1h))`.
7. Long fetches/whisper: call `InProgress()` periodically to extend `AckWait`.
8. Permanent failure (file missing, profile deleted): `TermWithReason()` + event.

### 14.5 Provider gateways and cluster-wide limits

- Run **one gateway Deployment per remote provider account** (e.g. `opensubtitles-gateway`), exposing an internal gRPC/HTTP `Search/Fetch`; it owns the JWT (refresh at 12 h, honour `base_url` for VIP), enforces `rate.Limiter(5, burst 5)` and the `/login` 1 r/s limit, tracks `remaining/reset_time_utc` from every `/download`, and pre-emptively throttles at `remaining == 0` (writes `provider-throttle` until `resetAt`). Workers call the gateway, so N workers never multiply the per-IP limit. For scrapers (Addic7ed) the gateway also holds the session cookie and captcha solver.
- Whisper: workers publish to `subtitles.generate.whisper`; a GPU-node consumer (subgen/go-whisper sidecar or in-process whisper.cpp) processes with `MaxAckPending = number of GPUs`; KEDA scales on `nats` JetStream consumer lag. Alternative: keep `whisper` as a normal provider whose `Fetch` blocks on the HTTP call, but then `AckWait` must cover up to `timeout=3600 s`.

### 14.6 Storage layout and safety

- Sidecars are written next to the media on the shared volume (RWX PVC) with Bazarr's naming; `subfolder` variants (`current|absolute|relative`) are a profile option.
- Write = temp file + `rename`; record `size` in KV so a later index pass can detect external changes.
- Keep a `history` (event stream → DB) with Bazarr's action codes so upgrade chains and UI parity are trivial.

---

## 15. Recommendations for Clustarr

1. **Model wanted work as `(mediaID × langKey)` tasks** with `langKey ∈ {xx, xx-forced, xx-hi}`; this is exactly Bazarr's unit of work and maps 1:1 onto JetStream subjects and KV keys.
2. **Port the language-profile semantics verbatim** (three-valued `hi`, `audio_exclude`, `audio_only_include`, cutoff incl. the "HI file satisfies non-HI cutoff" rule, `language_equals`). These are the behaviours users know from Bazarr/Trash guides.
3. **Adopt Bazarr's scoring table and hash-validation rules unchanged** (360/180 max, hash = rest−1, minimum 90 %/70 %); expose `scorePercent` in the API. Make the inventory's release parser emit `group/source/resolution/videoCodec/audioCodec/streamingService/edition` so `GuessMatches` works.
4. **Ship providers in this order**: OpenSubtitles.com (REST v1, own client), Gestdown, SubDL, SubSource, Embedded, Whisper. Treat Addic7ed/YIFY/TVSubtitles as opt-in plugins; do not build on `oz/osdb` or any .org XML-RPC code.
5. **Centralise per-provider auth, rate limits and quota in one gateway per provider**; keep throttle state (§10 table) and quotas in a NATS KV visible to every worker and mirrored into `SubtitleProvider.status`.
6. **Re-implement adaptive search as timestamps in KV + cron re-publish** (initial/latest attempt, delay 3 w, delta 1 w) rather than long NAK delays; re-implement upgrade as a 12 h cron over KV with `minScore = score+1` and a 7-day window.
7. **Sync via wrapped CLIs first** (ffsubsync 0.5.1 with `--vad webrtc --max-offset-seconds 60 --gss --no-fix-framerate --reference-stream a:N`, alass 2.0 for ad-break splits), selected by policy `scoreBelowPct`; run in its own stream so CPU-heavy sync does not block searches.
8. **Post-processing in Go**: decode with `gogs/chardet` + `x/text/encoding` using Bazarr's language→encoding hints, convert with `go-astisub`, port the HI regexes with `dlclark/regexp2`, always write UTF-8 SRT unless `originalFormat`.
9. **Sidecar naming**: `<stem>.<lang>[.forced|.hi|.cc|.sdh].srt`, forced beats HI, `hiExtension` configurable; parse with the RE2 pattern in §3.2; also honour ffprobe `disposition.forced/hearing_impaired` and title hints for embedded streams; cache probes by `(path,size,mtime)`.
10. **Whisper is a fallback, not a peer**: it can only translate into English, scores 61 %/33 % by construction, and needs GPU scheduling; give it its own stream/consumer and a profile-level opt-in.

---

## 16. Sources

- Bazarr source (v1.6.1, 2026-09-15): https://github.com/morpheus65535/bazarr — files `custom_libs/subliminal_patch/score.py`, `core.py`, `subtitle.py`, `providers/{opensubtitlescom,whisperai,embeddedsubtitles,addic7ed,gestdown,subdl,subsource,yifysubtitles,tvsubtitles}.py`, `custom_libs/subzero/modification/mods/hearing_impaired.py`, `custom_libs/subzero/language.py`, `bazarr/app/{config,get_providers}.py`, `bazarr/subtitles/{adaptive_searching,upgrade,utils}.py`, `bazarr/subtitles/indexer/movies.py`, `bazarr/subtitles/tools/subsyncer.py`, `bazarr/utilities/video_analyzer.py`, `bazarr/constants.py`, `frontend/src/pages/Settings/Languages/table.tsx`.
- DeepWiki Q&A on morpheus65535/bazarr (language profiles, throttling, upgrade, selection, action codes, language_equals).
- Bazarr wiki, Whisper provider: https://wiki.bazarr.media/Additional-Configuration/Whisper-Provider/
- OpenSubtitles REST API docs (Stoplight; JS shell, content confirmed via search snippets): https://opensubtitles.stoplight.io/docs/opensubtitles-api/e3750fd63a100-getting-started , …/6ef2e232095c7-best-practices , …/ea912bb244ef0-user-informations
- Official OpenSubtitles Kodi add-on request models: https://github.com/opensubtitles/service.subtitles.opensubtitles-com (`resources/lib/osclient/model/request/{subtitles,download}.py`, `provider.py`)
- Jellyfin OpenSubtitles plugin response models: https://github.com/jellyfin/jellyfin-plugin-opensubtitles
- Upstream subliminal opensubtitlescom provider: https://github.com/Diaoul/subliminal/blob/main/src/subliminal/providers/opensubtitlescom.py
- OpenSubtitles.org API shutdown: https://forum.opensubtitles.org/viewtopic.php?t=19471 , https://forum.opensubtitles.org/viewtopic.php?t=17930 , https://www.filebot.net/forums/viewtopic.php?t=13399
- OpenSubtitles quotas/rate limits: https://opensubtitles.tawk.help/article/getting-started , https://forum.opensubtitles.org/viewtopic.php?t=16072 , https://github.com/morpheus65535/bazarr/issues/2179
- Subscene shutdown: https://torrentfreak.com/subscenes-demise-is-no-surprise-but-millions-of-app-users-face-disruption-240505/
- Podnapisi removal (Bazarr 1.6.0 changelog via linuxserver release notes): https://github.com/linuxserver/docker-bazarr/releases
- Addic7ed status: https://github.com/morpheus65535/bazarr/issues (2026 "broken provider" issues), https://github.com/morpheus65535/bazarr/issues/1637
- Bazarr+ provider catalog: https://github.com/LavX/bazarr-provider-catalog
- ffsubsync: https://github.com/smacke/ffsubsync (0.5.1, 2026-07-24)
- alass: https://github.com/kaegi/alass (v2.0.0)
- whisper-asr-webservice: https://github.com/ahmetoner/whisper-asr-webservice (`docs/endpoints.md`)
- subgen: https://github.com/McCloudS/subgen
- whisper.cpp Go bindings: https://github.com/ggml-org/whisper.cpp/tree/master/bindings/go
- go-whisper: https://github.com/mutablelogic/go-whisper
- Go modules inspected with `go list -m` / `go doc`: go-astisub, go-ffprobe.v2, oz/osdb, odwrtw/opensubtitles, gogs/chardet, x/text, dlclark/regexp2, x/time, lingua-go, whatlanggo, nats.go/jetstream, whisper.cpp bindings, go-ptn, go-parse-torrent-name.
