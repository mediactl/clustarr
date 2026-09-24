# Phase D1 — exact API reference for the `indexarr` library layer

Research note, gathered 2026-09-19 against `main` @ `dc36655`. Everything below is
transcribed from the source or from `go doc -all`; nothing is paraphrased into a
signature. File:line references are to the working tree at that commit.

**Scope.** `pkg/torznab`, `pkg/newznab`, `pkg/cardigann`, `pkg/ratelimit`, the
indexer-relevant part of `pkg/release`; plus the three contract questions about
`app/catalog/worker/search`, `app/catalog/worker/rssmatcher` and the repo's
outbound-HTTP conventions.

---

## 0. Executive summary — read this before planning

Six things the plan author must not get wrong:

1. **`pkg/newznab` is not a Newznab client.** It is the category table only. Its
   package doc: *"It does no I/O and parses no XML -- that is pkg/torznab's
   job."* The wire client for **both** Torznab and Newznab is `pkg/torznab`
   (`pkg/torznab/doc` header: *"Package torznab is the Torznab/Newznab wire
   client"*). There is no separate usenet HTTP client to wire.
2. **There are no `panic(`, `log.Fatal` or `os.Exit` calls anywhere in these five
   packages**, not even in their tests. Verified by grep (§6.1).
3. **There is exactly one `ErrUnsupported`-family sentinel in the five packages**:
   `cardigann.ErrUnsupportedRowFeature`, and it is *not* a one-line stub — it is
   a real guard on a genuinely unimplemented feature
   (`pkg/cardigann/search.go:371`). The two one-line `return nil, ErrUnsupported`
   methods that Phase B shipped are in **`pkg/metadata`**, not here — see §6.2.
   They are still live and still unimplemented.
4. **`pkg/cardigann` has a much larger "decoded but never read" surface than its
   one documented gap.** Eleven `Definition` fields are parsed, schema-validated
   and exposed on the public struct while no code in the package ever reads them
   — including `search.error` (tracker error pages are *not* detected during
   search), `search.preprocessingfilters`, `Definition.Encoding` and
   `Definition.RequestDelay`. Full list with evidence in §6.3. **This is the most
   valuable finding in this note.**
5. **`pkg/cardigann` embeds only the JSON schema, not any indexer definitions.**
   `//go:embed schema.json` is the only embed (`pkg/cardigann/schema.go:31`).
   `1337x.yml` and `0dayfiles-api.yml` live in `test/data/cardigann/` and are test
   fixtures. Sourcing the definition corpus is unbuilt work.
6. **`torznab.WithRateLimit` does not take a `ratelimit.Limiter`.** It takes
   `(rate.Limit, int)` from `golang.org/x/time/rate` and constructs a private
   `*rate.Limiter` internally. `pkg/ratelimit.Limiter` is keyed and is a
   *different* type that no library package accepts. A caller wanting one keyed
   limiter shared across indexers cannot hand it to `torznab.Client` today
   (§7.2).

---

## 1. `pkg/torznab` — the Torznab/Newznab wire client

`go doc ./pkg/torznab` package comment, verbatim:

> Package torznab is the Torznab/Newznab wire client: caps, search and error
> parsing, an HTTP client and the server-side encoders that are exact inverses
> of the parsers.
>
> The caller owns pacing. Client does not rate-limit unless built with
> WithRateLimit: the controller that drives it holds one limiter per indexer
> host (Indexer.spec.rateLimit), and a library-side default would sit in series
> underneath it and silently halve the configured rate.

> **Doc/CRD mismatch to flag:** that package doc names `Indexer.spec.rateLimit`.
> No such field exists. `api/index/v1alpha1/indexer_types.go` has
> `spec.requestDelay` (`metav1.Duration`) and `spec.limits` (`*Limits{QueryLimit,
> GrabLimit *int32; Unit LimitUnit}`). The plan should derive the limiter from
> `spec.requestDelay` / `spec.limits`, not from a field that does not exist.

### 1.1 Construction

```go
func NewClient(baseURL, apikey string, opts ...ClientOption) (*Client, error)
```
`pkg/torznab/client.go:94`. Parses `baseURL`; errors with
`"torznab: parse base URL %q: %w"` on a parse failure and
`"torznab: base URL %q must be absolute"` when scheme or host is empty. Builds:

```go
hc: &http.Client{Timeout: defaultTimeout, Transport: &http.Transport{}}
```
`const defaultTimeout = 30 * time.Second` (`client.go:40`) — "matches
Indexer.spec.timeout's default in api/index/v1alpha1". Options are applied after,
in order.

```go
type Client struct {
	baseURL *url.URL
	apikey  string
	hc      *http.Client
	limiter *rate.Limiter
}
```
`client.go:86`. Doc: *"Client is a Torznab/Newznab HTTP client for one indexer
host: context aware, API-key authenticated, with an optional caller-supplied dial
function for proxying and an optional caller-supplied rate limiter
(WithRateLimit). It does not pace itself."*

### 1.2 Options — `type ClientOption func(*Client)`

| Option | Signature | File:line |
| --- | --- | --- |
| `WithTimeout` | `func WithTimeout(d time.Duration) ClientOption` | `client.go:46` |
| `WithRateLimit` | `func WithRateLimit(r rate.Limit, burst int) ClientOption` | `client.go:55` |
| `WithProxy` | `func WithProxy(dial func(ctx context.Context, network, addr string) (net.Conn, error)) ClientOption` | `client.go:65` |
| `WithHTTPClient` | `func WithHTTPClient(hc *http.Client) ClientOption` | `client.go:78` |

`WithRateLimit` doc, verbatim (`client.go:50-54`):

> WithRateLimit paces every request this client issues. There is no default: a
> Client built without this option does not rate-limit at all, because the caller
> owns pacing (see the package doc). Prowlarr's own default, for a caller that
> wants it, is one request every two seconds (docs/research/indexers.md §6):
> `WithRateLimit(rate.Every(2*time.Second), 1)`.

Implementation is one line: `c.limiter = rate.NewLimiter(r, burst)`. **The limiter
is constructed inside the package and is unreachable from outside** — you cannot
share one `*rate.Limiter` across two `Client`s, and you cannot hand it a
`ratelimit.Limiter`.

`WithProxy` doc: *"It is a no-op -- fails closed rather than panicking -- when the
client's http.Client.Transport (set by an earlier WithHTTPClient) is not an
\*http.Transport."* It `Clone()`s the transport and sets `DialContext`; it never
mutates the caller's transport in place. **Ordering matters: `WithHTTPClient` must
come before `WithProxy`, and `WithHTTPClient` after `WithTimeout` discards the
timeout** (it replaces `c.hc` outright).

### 1.3 Calls

```go
func (c *Client) Caps(ctx context.Context) (Caps, error)     // client.go:169
func (c *Client) Search(ctx context.Context, q Query) ([]Release, error) // client.go:194
```

`Caps` builds `url.Values{"t": {"caps"}}` and sets `apikey` when non-empty.
`Search` uses `q.Values(c.apikey)`.

Both open a span (`tracing.Start(ctx, "torznab.caps")` / `"torznab.search"`), call
`c.do`, `defer resp.Body.Close()`, read via `readOrError` and parse. `c.do`
(`client.go:120`) opens its own `"torznab.request"` span, logs at Debug with
`"url", c.baseURL.String(), "t", values.Get("t")` (**never the full query string —
the query carries the apikey**), waits on the limiter if one is set, otherwise
checks `ctx.Err()` directly so *"cancellation propagates out of Search/Caps
unwrapped"*, then issues a plain `GET`. **No User-Agent is set.**

### 1.4 Response cap and error mapping

```go
const maxResponseBodyBytes = 8 << 20 // 8 MiB                     // client.go:162
var ErrResponseTooLarge = errors.New("torznab: response body exceeds size limit") // client.go:166
```

`readOrError` (`client.go:221`) is the whole convention in one function:

1. `httpStatusError(resp)` first — maps **410 → `&Error{HTTPStatus: 410,
   Description: "indexer disabled"}`** and **429 → `&Error{HTTPStatus: 429,
   Description: "rate limited", RetryAfter: retryAfter(resp)}`**. Every other
   status returns nil here so the body is inspected.
2. `io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))`; if
   `len(body) > maxResponseBodyBytes`, returns
   `fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(body))`.
3. `ParseError(bytes.NewReader(body))` — if it yields a non-nil `*Error`, stamps
   `e.HTTPStatus = resp.StatusCode` and returns it as the error. (Newznab answers
   errors with HTTP 200 and an XML `<error>` body.)
4. Any remaining non-200 → `fmt.Errorf("torznab: unexpected status %d", …)`.

`retryAfter` (`client.go:267`) parses `Retry-After` as a whole number of seconds
only; the HTTP-date form yields 0.

### 1.5 Types

```go
type SearchMode string
const (
	ModeSearch      SearchMode = "search"
	ModeTVSearch    SearchMode = "tvsearch"
	ModeMovieSearch SearchMode = "movie"
	ModeMusicSearch SearchMode = "music"
	ModeAudioSearch SearchMode = "audio" // spec alias of music
	ModeBookSearch  SearchMode = "book"
)
```

```go
type Query struct {
	Type                             SearchMode
	Q                                string
	Categories                       []newznab.CategoryID
	IMDBID, TMDBID, TVDBID, TVMazeID string
	Season                           *int
	Episode                          string
	Artist, Album                    string
	Author, Title                    string
	Limit, Offset                    int
}
func (q Query) Values(apikey string) url.Values   // query.go:45
func (q Query) Validate(caps Caps) error          // query.go:103
```
`Values` always sets `t` and `extended=1`; `cat` is comma-joined; `imdbid` is sent
with the `tt` prefix **stripped** (`strings.TrimPrefix(q.IMDBID, "tt")`,
`query.go:63`); `season` from `*int`, `ep` from the string. `Limit`/`Offset` are
omitted when zero. `Validate` returns
`"torznab: search mode %q is not available on this indexer"` when
`caps.Modes[q.Type]` is missing or not `Available`. **`Validate` checks the mode
only — it does not check `SupportedParams`.** Use `Caps.Supports` for that.

```go
type Caps struct {
	ServerTitle   string
	LimitsDefault int
	LimitsMax     int
	Modes         map[SearchMode]Searching
	Categories    []newznab.Category
	Tags          map[string]string // flag name -> description
}
func ParseCaps(r io.Reader) (Caps, error)
func (c Caps) Supports(mode SearchMode, param string) bool

type Searching struct {
	Available       bool
	SupportedParams []string
	SearchEngine    string // "raw" when the indexer accepts free-text q
}
```

```go
type Release struct {
	Title       string
	GUID        string
	Link        string
	CommentURL  string
	PubDate     time.Time
	Size        int64
	Description string
	Categories  []newznab.CategoryID

	// torrent
	Seeders              *int32
	Leechers             *int32
	Peers                *int32
	Grabs                *int32
	Files                *int32
	InfoHash             string
	MagnetURL            string
	DownloadVolumeFactor *float64
	UploadVolumeFactor   *float64
	MinimumRatio         *float64
	MinimumSeedTime      *int64 // seconds

	// usenet (Newznab-native)
	Group      string
	Poster     string
	UsenetDate *time.Time
	Password   *int32
	NFO        *int32
	Info       string // nfo url

	IDs   map[string]string
	Attrs map[string][]string
}
func ParseResults(r io.Reader) ([]Release, error)
func ParseItem(r io.Reader) (Release, error)
```
`release.go:34`. The `*float64` fields carry an explicit CLAUDE.md carve-out in
the source comment: *"they are wire-protocol decimals carried verbatim from the
indexer, not a human-typed config value, and this package never persists a Release
to a CRD."* `IDs` is keyed by `commonv1alpha1.IDKeyIMDB/IDKeyTMDB/IDKeyTVDB` plus
`"tvmazeid"`; **the IMDb value carries the canonical `tt` prefix** (note the
asymmetry with `Query.Values`, which strips it on the way out). `Attrs` holds
every `torznab:`/`newznab:` attr verbatim as `name -> []string`, including
`season`, `episode`, `genre`, `year`, `language`.

Robustness note from `release.go:102-110`: `wireItem.Size` and `Categories` are
decoded as strings, not numerics, because *"encoding/xml aborts the ENTIRE Decode
… the moment any element it is unmarshaling into a numeric Go field fails strconv
parsing"* — so one malformed `<size>` cannot fail the whole feed.

```go
type Error struct {
	Code        ErrorCode // 0 when the failure was HTTP-level only
	Description string
	HTTPStatus  int
	RetryAfter  time.Duration // set from the Retry-After header on 429
}
func ParseError(r io.Reader) (*Error, error)
func (e *Error) Error() string
```
`ParseError` returns `(nil, nil)` — not an error — for well-formed XML that is not
an `<error>` element, so a caller can try it first on a document of unknown shape.

```go
type ErrorCode int
const (
	ErrIncorrectCredentials   ErrorCode = 100
	ErrAccountSuspended       ErrorCode = 101
	ErrInsufficientPrivileges ErrorCode = 102
	ErrRegistrationDenied     ErrorCode = 103
	ErrRegistrationsClosed    ErrorCode = 104
	ErrMissingParameter       ErrorCode = 200
	ErrIncorrectParameter     ErrorCode = 201
	ErrNoSuchFunction         ErrorCode = 202
	ErrFunctionNotAvailable   ErrorCode = 203
	ErrNoSuchItem             ErrorCode = 300
	ErrRequestLimitReached    ErrorCode = 500 // Torznab-specific
	ErrDownloadLimitReached   ErrorCode = 501 // Torznab-specific
	ErrUnknown                ErrorCode = 900
	ErrAPIDisabled            ErrorCode = 910
)
```

### 1.6 Server-side encoders — the Torznab facade's half

```go
func WriteCaps(w io.Writer, c Caps) error              // exact inverse of ParseCaps
func WriteResults(w io.Writer, rels []Release) error   // exact inverse of ParseResults
func WriteError(w io.Writer, err *Error) error         // exact inverse of ParseError
```
These are what `indexarr`'s facade (`DefaultFacadeBindAddress = ":9696"`,
`app/indexer/run.go:56`) serves. `WriteResults`' doc notes it round-trips a `Release`
*including its raw `Attrs`*, "because every typed field is written from the
original wire string in `Release.Attrs` whenever one is available and only falls
back to reformatting the typed value when it is not (a Release built without going
through ParseItem, e.g. by pkg/cardigann)."

### 1.7 Stubs in `pkg/torznab`

**None.** No `ErrUnsupported`/`ErrNotImplemented`, no `panic(`, no `TODO`/`FIXME`
in non-test files. Grep transcript in §6.

---

## 2. `pkg/newznab` — the category table (no I/O)

Package doc, verbatim:

> Package newznab is the Newznab category table: the fixed, five-family standard
> tree Clustarr uses (movies/audio/tv/books/other), plus the conversions between
> a category id and a Clustarr MediaKind. It does no I/O and parses no XML -- that
> is pkg/torznab's job.

### 2.1 Full exported surface

```go
type CategoryID int32
type Category    struct{ ID CategoryID; Name string; Sub []SubCategory }
type SubCategory struct{ ID CategoryID; Name string }
type CategoryMapper struct{}   // stateless; zero value ready to use

func Tree() []Category
func ByKind(kind commonv1.MediaKind) []CategoryID
func Custom(trackerID string) CategoryID
func Expand(ids []CategoryID) []CategoryID
func (c CategoryID) Parent() CategoryID
func (CategoryMapper) Kind(ids []CategoryID) (kind commonv1.MediaKind, ok bool)
```

Constants: the five families in full —
`CatMovies 2000`, `CatMoviesForeign 2010`, `CatMoviesOther 2020`, `CatMoviesSD 2030`,
`CatMoviesHD 2040`, `CatMoviesUHD 2045`, `CatMoviesBluRay 2050`, `CatMovies3D 2060`,
`CatMoviesDVD 2070`, `CatMoviesWEBDL 2080`, `CatMoviesX265 2090`;
`CatAudio 3000`, `CatAudioMP3 3010`, `CatAudioVideo 3020`, `CatAudioAudiobook 3030`,
`CatAudioLossless 3040`, `CatAudioOther 3050`, `CatAudioForeign 3060`;
`CatTV 5000`, `CatTVWEBDL 5010`, `CatTVForeign 5020`, `CatTVSD 5030`, `CatTVHD 5040`,
`CatTVUHD 5045`, `CatTVOther 5050`, `CatTVSport 5060`, `CatTVAnime 5070`,
`CatTVDocumentary 5080`, `CatTVX265 5090`;
`CatBooks 7000`, `CatBooksMags 7010`, `CatBooksEBook 7020`, `CatBooksComics 7030`,
`CatBooksTechnical 7040`, `CatBooksOther 7050`, `CatBooksForeign 7060`;
`CatOther 8000`, `CatOtherMisc 8010`, `CatOtherHashed 8020`;
and `CustomCategoryOffset CategoryID = 100000`.

`ByKind` mapping, from its doc: *"movie->2000, series/episode->5000,
artist/album->3000, audiobook->3030, author/book->7020, comic/issue->7030. Anime
(5070) is not reachable from a MediaKind -- it is a search-time overlay
(Indexer.spec.animeCategories), not a catalog kind."*

`Custom(trackerID)` = `100000 + the first two bytes of sha1(trackerID) as a
big-endian uint16`. Ids `>= CustomCategoryOffset` are never expanded and their
`Parent()` is themselves.

`Tree()` returns an **independent deep copy** on every call (`Sub` included), so a
mutating caller cannot corrupt the package-level table.

`CategoryMapper.Kind` checks each id's `Parent()` against the five family roots and
returns the first match; `ok` is false for a custom id or for 1000/4000/6000.

### 2.2 Stubs in `pkg/newznab`

**None.** No `ErrUnsupported`, no `panic(`, no `TODO`. `pkg/newznab/zz_scratch_verify/`
exists on disk but is **empty** (verified with `ls -la`); it is not a Go package and
contains no files.

---

## 3. `pkg/cardigann` — the v11 definition engine

Package doc, verbatim:

> Package cardigann implements the Cardigann **v11** indexer-definition engine:
> YAML definitions validated against the bundled JSON schema, selectors over
> HTML/JSON/XML bodies, the 25 filter pipeline, login, search and download.

**Definition-format version: v11**, enforced by `schema.json` (33.5 KB, embedded at
`pkg/cardigann/schema.go:31` via `//go:embed schema.json`). A byte-identical copy
lives at `test/data/cardigann/schema-v11.json` and
`EmbeddedSchemaForTest() []byte` exists solely to assert the two never drift.

### 3.1 Load and validate

```go
func Validate(data []byte) error          // schema-only check, no decode
func Load(data []byte) (*Definition, error) // Validate + decode
```
`Load` is "the only public entry point that combines the two — the indexer
controller calls it once per `IndexerDefinition.spec.yaml` and once per bundled
definition at startup." `Validate` returns a wrapped
`*jsonschema.ValidationError`.

**Decoding gotcha carried in the source:** YAML→JSON conversion goes through
`yaml.Unmarshal` into a generic `any` and back out via `encoding/json`, *not*
`yaml.YAMLToJSON`, because goccy/go-yaml v1.19.2 mis-encodes the U+000F control
character in `1337x.yml`'s title filter chain as the invalid JSON escape `\x0f`.
The same bug drove `Scalar`, `ScalarList` and `OrderedFields` to implement
`goccy/go-yaml`'s `NodeUnmarshaler` (decode from the AST node) rather than
`BytesUnmarshaler`.

### 3.2 `Definition` and its blocks

```go
type Definition struct {
	ID              string         `yaml:"id"`
	Name            string         `yaml:"name"`
	Description     string         `yaml:"description"`
	Language        string         `yaml:"language"`
	Type            DefinitionType `yaml:"type"`
	Replaces        []string       `yaml:"replaces"`
	Encoding        string         `yaml:"encoding"`
	FollowRedirect  bool           `yaml:"followredirect"`
	TestLinkTorrent *bool          `yaml:"testlinktorrent"`
	RequestDelay    float64        `yaml:"requestDelay"` // seconds

	Links        []string `yaml:"links"`
	LegacyLinks  []string `yaml:"legacylinks"`
	Certificates []string `yaml:"certificates"`

	Caps     Caps            `yaml:"caps"`
	Settings []SettingsField `yaml:"settings"`
	Login    *LoginBlock     `yaml:"login"`
	Search   SearchBlock     `yaml:"search"`
	Download *DownloadBlock  `yaml:"download"`
}
func (d *Definition) Capabilities() Capabilities
func (d *Definition) ResolveSettings(baseURL string, raw map[string]string) (map[string]any, error)

type DefinitionType string // "public" | "semi-private" | "private", the schema enum verbatim.
                           // NOT the IndexerDefinitionStatus CRD's camelCase equivalent;
                           // the indexer controller maps between them.
```

> `Definition.RequestDelay` is a `float64`. That is legal here (this struct is not
> under `api/`), but the controller must convert it before anything reaches a CRD.

```go
type Caps struct {
	Categories       map[string]string `yaml:"categories"`       // legacy: trackerID -> IndexerCategories name
	CategoryMappings []CategoryMapping `yaml:"categorymappings"` // mutually exclusive with the above (schema oneOf)
	Modes             map[string][]string `yaml:"modes"`
	AllowRawSearch    bool                `yaml:"allowrawsearch"`
	AllowTVSearchIMDB bool                `yaml:"allowtvsearchimdb"`
}
type CategoryMapping struct {
	ID      Scalar `yaml:"id"`  // tracker's own id, string or int in the YAML
	Cat     string `yaml:"cat"` // one of the 71 canonical IndexerCategories enum names
	Desc    string `yaml:"desc"`
	Default bool   `yaml:"default"`
}
type Capabilities struct {
	Modes             map[string][]string
	Categories        []newznab.CategoryID // resolved, deduped; Console/PC/XXX-family fold to newznab.CatOther
	AllowRawSearch    bool
	AllowTVSearchIMDB bool
}
```
`Capabilities` is *"the shape IndexerDefinitionStatus.Caps and Indexer.status.caps
(spec §4.3) are populated from."* `Caps.Modes` keys are the Cardigann spellings —
`"search" | "tv-search" | "movie-search" | "music-search" | "book-search"` — which
are **not** `torznab.SearchMode` values (`"tvsearch"`, `"movie"`, …). A translation
layer is required.

```go
type CategoryMapper struct{ /* unexported */ }
func NewCategoryMapper(def *Definition) CategoryMapper
func (m CategoryMapper) FromTracker(trackerID string) []newznab.CategoryID
func (m CategoryMapper) FromTrackerDesc(desc string) []newznab.CategoryID
func (m CategoryMapper) ToTracker(categories []newznab.CategoryID) []string
```

```go
type SearchBlock struct {
	Path             string              `yaml:"path"`
	Paths            []SearchPathBlock   `yaml:"paths"` // exactly one of Path/Paths (schema oneOf)
	AllowEmptyInputs bool                `yaml:"allowEmptyInputs"`
	Inputs           map[string]Scalar   `yaml:"inputs"`
	Headers          map[string][]string `yaml:"headers"`
	KeywordsFilters      []FilterBlock `yaml:"keywordsfilters"`
	PreprocessingFilters []FilterBlock `yaml:"preprocessingfilters"`
	Error []ErrorBlock `yaml:"error"`
	Rows  RowsBlock    `yaml:"rows"`
	Fields OrderedFields `yaml:"fields"`
}
type SearchPathBlock struct {
	Path           string            `yaml:"path"`
	Method         string            `yaml:"method"` // default GET
	FollowRedirect bool              `yaml:"followredirect"`
	Categories     []Scalar          `yaml:"categories"` // leading "!" excludes; empty = always matches
	Inputs         map[string]Scalar `yaml:"inputs"`
	InheritInputs  bool              `yaml:"inheritinputs"`
	QuerySeparator string            `yaml:"queryseparator"`
	Response       *ResponseBlock    `yaml:"response"` // nil = HTML
}
type ResponseBlock struct {
	Type             string `yaml:"type"` // "json"|"xml"
	NoResultsMessage string `yaml:"noResultsMessage"`
}
type RowsBlock struct {
	SelectorBlock `yaml:",inline"`
	After                           int            `yaml:"after"`       // KNOWN GAP
	Multiple                        bool           `yaml:"multiple"`
	MissingAttributeEqualsNoResults bool           `yaml:"missingAttributeEqualsNoResults"`
	DateHeaders                     *SelectorBlock `yaml:"dateheaders"` // KNOWN GAP
	Count                           *SelectorBlock `yaml:"count"`
}
type OrderedFields []FieldEntry
type FieldEntry struct { Name string; Block SelectorBlock }
func (f *OrderedFields) UnmarshalYAML(node ast.Node) error
```

```go
type SelectorBlock struct {
	Selector  string            `yaml:"selector"`
	Attribute string            `yaml:"attribute"`
	Optional  bool              `yaml:"optional"`
	Default   *Scalar           `yaml:"default"` // requires Optional per schema dependentRequired
	Case      map[string]Scalar `yaml:"case"`
	Remove    string            `yaml:"remove"`  // nested selector stripped before reading text (HTML only)
	Text      *Scalar           `yaml:"text"`    // literal or template, replaces Selector entirely
	Filters   []FilterBlock     `yaml:"filters"`
}
func (b SelectorBlock) Extract(ctx context.Context, d Doc, tc *TemplateContext) (string, bool, error)

type SelectorField struct { // DownloadBlock's smaller shape
	Selector          string        `yaml:"selector"`
	Attribute         string        `yaml:"attribute"`
	UseBeforeResponse bool          `yaml:"usebeforeresponse"`
	Filters           []FilterBlock `yaml:"filters"`
}
type FilterBlock struct { Name string `yaml:"name"`; Args ScalarList `yaml:"args"` }
type Scalar     string             ; func (s *Scalar) UnmarshalYAML(node ast.Node) error
type ScalarList []string           ; func (l *ScalarList) UnmarshalYAML(node ast.Node) error
type SettingsField struct {
	Name string; Label string; Type string // info|text|password|checkbox|select|info_category_8000|info_cookie|info_flaresolverr|info_useragent
	Default Scalar; Options map[string]string; Defaults []string
}
```

```go
type LoginBlock struct {
	Method  string   `yaml:"method"` // ""(=form)|form|post|cookie|get|oneurl
	Cookies []string `yaml:"cookies"`
	Path       string `yaml:"path"`
	SubmitPath string `yaml:"submitpath"`
	Form       string `yaml:"form"`
	Captcha *CaptchaBlock `yaml:"captcha"`
	Inputs    map[string]Scalar `yaml:"inputs"`
	Selectors bool              `yaml:"selectors"`
	SelectorInputs    map[string]SelectorBlock `yaml:"selectorinputs"`
	GetSelectorInputs map[string]SelectorBlock `yaml:"getselectorinputs"`
	Error []ErrorBlock   `yaml:"error"`
	Test  *PageTestBlock `yaml:"test"`
	Headers map[string][]string `yaml:"headers"`
}
type CaptchaBlock  struct{ Type string; Selector string; Input string } // Type is "image"|"text"
type PageTestBlock struct{ Path string; Selector string }
type ErrorBlock    struct{ Path string; Selector string; Message *SelectorBlock }

type DownloadBlock struct {
	Method    string              `yaml:"method"`
	Before    *BeforeBlock        `yaml:"before"`
	Selectors []SelectorField     `yaml:"selectors"`
	InfoHash  *InfoHashBlock      `yaml:"infohash"`
	Headers   map[string][]string `yaml:"headers"`
}
type BeforeBlock   struct{ Path string; PathSelector *SelectorField; Method string; Inputs map[string]Scalar; QuerySeparator string }
type InfoHashBlock struct{ Hash SelectorField; Title SelectorField; UseBeforeResponse bool }
```

### 3.3 Config, session, engine

```go
type Config struct {
	BaseURL string
	Values  map[string]any // from Definition.ResolveSettings
	Session *Session       // nil until Engine.Login; required by Search/Download when Definition.Login != nil
}
func NewConfig(def *Definition, baseURL string, raw map[string]string) (Config, error)

type Session struct {
	Cookies   []*http.Cookie
	Headers   http.Header // rare; most definitions authenticate via search.headers instead
	ExpiresAt time.Time
}
```
`Config` doc: *"The indexer controller builds it from Indexer.spec.settings merged
with the decoded SecretRef Secret; pkg/cardigann never reads a Kubernetes object."*

`ResolveSettings` semantics, verbatim: *"checkbox -> "true"/"" (matching
TemplateContext.True/False so `eq .Config.x .False` type-checks), select -> the
chosen option key verbatim, text/password -> verbatim, info* -> never present in
Config. Always injects "sitelink" = baseURL."* Any raw key that is not a declared
setting passes through unchanged, because `login.cookies` names arbitrary cookie
names that `loginCookie` reads straight out of `Config.Values`.

```go
type Engine struct {
	HTTP  *http.Client
	Proxy http.RoundTripper
	Now   func() time.Time
}
func (e Engine) Caps(def *Definition) (Capabilities, error)
func (e Engine) Login(ctx context.Context, def *Definition, cfg Config) (*Session, error)
func (e Engine) Search(ctx context.Context, def *Definition, cfg Config, query Query) ([]torznab.Release, error)
func (e Engine) Download(ctx context.Context, def *Definition, cfg Config, link string) (io.ReadCloser, error)
```
`engine.go:43`. **The zero value works** with `http.DefaultClient`. The controller
is expected to set `HTTP` (timeouts, a cookie jar per Indexer) and, when an
`IndexerProxy` selects the Indexer, `Proxy` — *"a RoundTripper that speaks SOCKS5
or FlareSolverr — built and owned entirely outside this package, which never dials
a proxy itself."* `httpClient()` shallow-copies the client to swap the transport
and **never mutates a caller-owned `*http.Client`** (`engine.go:71`).

**`Engine` has no rate limiter at all.** There is no `WithRateLimit` equivalent; the
caller is entirely responsible for pacing every `Login`/`Search`/`Download`.

```go
type Query struct {
	Type       string // "search"|"tv-search"|"movie-search"|"music-search"|"book-search"
	Q          string
	Categories []newznab.CategoryID
	IMDBID, TMDBID, TVDBID, TVMazeID, TraktID, DoubanID string
	Season, Ep                                          string
	Year                                                int
	Genre                                               string
	Album, Artist, Label, Track string
	Author, Title, Publisher    string
}
```
Note `Season`/`Ep` are **strings** here, and `torznab.Query.Season` is a `*int`.

### 3.4 Errors

```go
var ErrResponseTooLarge   = errors.New("cardigann: response body exceeds size limit")
var ErrSessionRequired    = errors.New("cardigann: definition requires login; call Engine.Login first")
var ErrUnsupportedRowFeature = errors.New("cardigann: rows.after/dateheaders or a field's |append modifier is not yet supported")

type CaptchaRequiredError     struct{ Type string } // "image" | "text"
type CloudflareChallengeError struct{ StatusCode int }
type LoginError               struct{ Message string }
```
All three struct errors implement `Error() string` on the pointer receiver, so
callers use `errors.As(err, &e)` with `*CaptchaRequiredError` etc.

`ErrSessionRequired` applies only to login methods that produce a session —
`form`/`post`/`cookie` and the `""` default. `get` and `oneurl` authenticate every
request from `Config` (an API key rendered into `search.headers` or
`search.inputs`), so `Search` runs without a `Session`. The predicate is
`loginRequiresSession` (`login.go:81`):

```go
func loginRequiresSession(lb *LoginBlock) bool {
	if lb == nil { return false }
	switch lb.Method {
	case "get", "oneurl": return false
	default:               return true // "", "form", "post", "cookie"
	}
}
```

`CloudflareChallengeError` is raised on **403 or 503** carrying a `cf-mitigated`
header or a `Server:` header containing `cloudflare` or `ddos-guard`
(`looksLikeCloudflare`, `engine.go:115`). *"pkg/cardigann never solves the challenge
itself (that is IndexerProxy's FlareSolverr client, a later indexarr task)."*

`CaptchaRequiredError` is raised **before** the submit: `Login` first GETs
`lb.Path` and checks whether `lb.Captcha.Selector` actually matches this time. The
doc names the intended surfacing: *"Indexer.status condition Authenticated=False,
reason CaptchaRequired."*

### 3.5 Response cap and the one shared request function

```go
const maxResponseBodyBytes = 8 << 20 // 8 MiB   // engine.go:127
```
`Engine.do` (`engine.go:188`) is *"the one shared low-level request function every
outbound call (login page fetch, submit, search request, download fetch) goes
through, so Cloudflare detection, the size cap and tracing all apply everywhere for
free."* It opens a span named `"cardigann." + strings.ToLower(req.Method)`, reads
`io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))`, and returns
`ErrResponseTooLarge` wrapped with the byte count and a **redacted** URL.

Redaction is deliberate and worth copying: `redactURL` / `redactRawURL` /
`redactErr` (`engine.go:145`, `:161`, `:176`) strip the query string, fragment and
userinfo from every diagnostic, because *"real definitions put apikey, passkey and
rsskey in the query string; those errors reach Indexer.status and the logs."*
`redactErr` unwraps `*url.Error` so `errors.Is(err, context.Canceled)` still works.

### 3.6 HTML / JSON / XML — one `Doc` abstraction

```go
type ResponseType int
const ( ResponseHTML ResponseType = iota; ResponseJSON; ResponseXML )

type Doc struct{ /* unexported */ }
func ParseDoc(rt ResponseType, body []byte) (Doc, error)
func (d Doc) Select(selector string) (Doc, bool)
func (d Doc) Rows(selector string) []Doc
func (d Doc) Text(attribute string) (string, bool)
```
Selector dialect per backend: **CSS for HTML** (goquery), **a gjson path for JSON**,
**an XPath expression for XML** (xmlquery). `Rows` is the equivalent of
`*Selection.Each` / gjson array iteration / `xmlquery.Find`. `Text(attribute)` reads
text when `attribute == ""` (HTML) or a named HTML attribute, and for JSON/XML the
attribute is "one more gjson/xpath descent step". For a JSON array with no further
descent, elements are **joined with `","`** rather than returned as raw JSON, which
is what makes a plain `re_replace`/`split` chain work on a genre-like field.

`ResponseBlock.Type` selects the backend per search path; `nil` Response = HTML.

### 3.7 The filter set — exactly 25

```go
var Filters = map[string]Filter{ ... }
type Filter func(ctx context.Context, value string, args []string, tc *TemplateContext) (string, error)
```
`Filters` doc: *"holds exactly the 25 names FilterBlock's schema enum allows — no
other name is ever added; a filter name outside this set is a schema violation."*

The 25 names: `querystring`, `timeparse`, `dateparse`, `regexp`, `re_replace`,
`split`, `replace`, `trim`, `prepend`, `append`, `tolower`, `toupper`, `urldecode`,
`urlencode`, `htmldecode`, `htmlencode`, `timeago`, `reltime`, `fuzzytime`,
`validfilename`, `diacritics`, `jsonjoinarray`, `hexdump`, `strdump`, `validate`.
(`timeparse` and `dateparse` share `filterDateparse`; `hexdump`/`strdump` are
debug-only and log via `logging.FromContext(ctx)`.)

Two exported helpers the filters rely on, usable directly:
```go
func GetBytes(s string) (int64, error)            // "1.5 GB", "700 MiB", "1,234 KB", bare int; plain units are BINARY (1 GB == 2^30)
func TranslateDateFormat(dotnet string) string    // ".NET custom format" -> Go reference layout; Go-form tokens pass through
```

### 3.8 The template context

```go
type TemplateContext struct {
	Config map[string]any // resolved settings, always including "sitelink" = Config.BaseURL
	Keywords string
	Query    QueryVars
	Categories []string    // tracker category ids for the current request, as strings
	Result map[string]string
	DownloadURI *url.URL   // only set while evaluating the download block
	True, False string     // True="true", False=""
	Today struct{ Year int }
	Now time.Time          // from Engine.Now, defaulting to time.Now
}
type QueryVars struct {
	Type, Q, Keywords string
	IMDBID, IMDBIDShort, TVDBID, TMDBID, TVMazeID string
	TraktID, DoubanID                             string
	Season, Ep, Episode, Year, Genre string
	Album, Artist, Label, Track      string
	Author, Title, Publisher         string
}
```
`Engine.Now` is read in exactly one place (`templateContext`, `engine.go:99`), so
setting it makes every date-relative filter and `.Today.Year` deterministic.

### 3.9 What `Engine.Search` actually does — and what it produces

`Search`'s doc comment (`search.go:176-186`), verbatim:

> Search executes every SearchBlock.Paths entry whose Categories intersect
> query.Categories (via the CategoryMapper; a path with no Categories always
> matches; "!id" excludes), or the single SearchBlock.Path when Paths is empty,
> builds .Keywords from Query, applies KeywordsFilters, sends each request (GET
> query string or POST form, per path.Method), parses the response per
> path.Response.Type (default HTML), iterates Rows (After == 0 and DateHeaders ==
> nil, else ErrUnsupportedRowFeature), evaluates Fields in file order into a
> per-row Result, and maps the canonical field names (note §3.8) onto a
> torznab.Release. A non-optional field with no match drops that row (logged at
> Debug via logging.FromContext, not returned as an error) rather than failing the
> whole search.

The canonical field-name set that drives row-dropping and the `torznab.Release`
mapping (`search.go:66`):

```go
var canonicalFieldNames = map[string]bool{
	"download": true, "magnet": true, "infohash": true, "details": true, "comments": true,
	"title": true, "description": true, "category": true, "categorydesc": true, "size": true,
	"leechers": true, "seeders": true, "date": true, "files": true, "grabs": true,
	"downloadvolumefactor": true, "uploadvolumefactor": true, "minimumratio": true, "minimumseedtime": true,
	"imdb": true, "imdbid": true, "tmdbid": true, "rageid": true, "tvdbid": true, "tvmazeid": true,
	"traktid": true, "doubanid": true, "poster": true, "genre": true, "year": true, "author": true,
	"booktitle": true, "publisher": true, "album": true, "artist": true, "label": true, "track": true,
}
```

`mapResultToRelease` (`search.go:497`) — the exact mapping onto `torznab.Release`:

- `Title` ← `title`; `Description` ← `description`.
- `GUID` ← `firstNonEmpty(details, download, magnet)`.
- `CommentURL` ← `details` resolved against `cfg.BaseURL`.
- `Link` ← `download` resolved against `cfg.BaseURL`, **unless** it starts with
  `magnet:`, in which case it goes to `MagnetURL`.
- `MagnetURL` ← `magnet` (overrides the above); `InfoHash` ← `infohash`.
- `Size` ← `GetBytes(size)` (parse failure leaves 0, silently).
- `Seeders`/`Leechers` ← `ParseInt(…,10,32)`; `Peers` = seeders+leechers **only when
  both are present**. `Grabs`, `Files` likewise.
- `PubDate` ← `time.Parse(time.RFC3339, date)` — **the `date` field must already be
  RFC3339 by the time the filter chain finishes**; a parse failure silently leaves
  the zero time.
- `Categories` ← `mapper.FromTracker(category)` ++ `mapper.FromTrackerDesc(categorydesc)`.
- `DownloadVolumeFactor`/`UploadVolumeFactor`/`MinimumRatio` ← `ratioField`: the
  row's value when parseable; **`1.0` when the field is entirely absent from the
  definition** (not merely empty for this row); else nil. `MinimumSeedTime` ←
  `int64(ParseFloat(minimumseedtime))` seconds.
- `IDs["imdb"]` ← `normalizeIMDB(imdbid ?? imdb)` → canonical `tt%07d`;
  `IDs[name]` for `tmdbid, tvdbid, rageid, tvmazeid, traktid, doubanid` verbatim.
- `Poster` ← `poster`; `Attrs["genre"]` ← `splitGenre` on `,` or `|`;
  `Attrs[name] = []string{v}` for `year, author, booktitle, publisher, artist,
  album, label, track`.

Source comment worth quoting (`search.go:492-496`): *"torznab.Release has no
music/book fields (unlike the research note's older §11 sketch — confirmed against
B6's actual struct): year/author/booktitle/publisher/artist/album/label/track all
land in Attrs instead; a later phase may extend torznab.Release itself."*

`buildKeywords` (`search.go:87`) folds a zero-padded `" S%02dE%02d"` suffix into
`.Keywords` when `Query.Season` and `Query.Ep` are both set and `Q` does not
already match `(?i)\bS\d{1,2}E\d{1,3}\b`.

### 3.10 `Engine.Login` — the five flows

Dispatch (`login.go:95`):

```go
switch lb.Method {
case "", "form": return e.loginForm(...)
case "cookie":   return e.loginCookie(...)
case "post":     return e.loginPost(...)
case "get":      return e.loginGet(...)
case "oneurl":   return e.loginOneURL(...)
default:         return nil, fmt.Errorf("cardigann: unknown login method %q", lb.Method)
}
```
`Login` returns `(nil, nil)` when `def.Login == nil` (public tracker). Captcha
detection runs first when `lb.Captcha != nil`. `checkLoginErrors(respBody,
lb.Error, tc)` is applied at `login.go:185`, `:211`, `:252` (form, post, cookie
paths) and yields a `*LoginError`. `lb.Test` (`PageTestBlock`) is honoured at
`login.go:282` via `runLoginTest`.

`loginForm` GETs `lb.Path` and scrapes `lb.SelectorInputs` (e.g. a CSRF token)
before submitting (`login.go:144-166`).

### 3.11 `Engine.Download`

```go
func (e Engine) Download(ctx context.Context, def *Definition, cfg Config, link string) (io.ReadCloser, error)
```
Doc, verbatim: *"Download resolves link (a search result's Details/Download field,
or already a "magnet:" URI, in which case it is returned unread) into the
downloadable content: with no DownloadBlock, link is fetched directly; otherwise
Before (if set) runs first, link's page is fetched and each Selectors entry is
tried in file order (selector strings are themselves templates — see 1337x's
`a[href*="{{ .Config.primarydownloadlink }}"]`); the first non-empty match is
fetched (or returned as-is when it is itself a "magnet:" URI); with no Selectors
match, InfoHash (if set) builds a magnet URI from the extracted hash and title."*
The returned body is bounded by `do`'s 8 MiB cap (`download.go:110`).

### 3.12 **Deferred: do NOT wire these in this phase**

The task brief already scopes `IndexerDefinition`/`IndexerProxy` wiring out. What
follows is the precise boundary, so the plan can state it without hand-waving:

| Deferred thing | Where the source says so |
| --- | --- |
| `IndexerProxy` → SOCKS5 / FlareSolverr `http.RoundTripper` | `Engine.Proxy` doc, `engine.go:35-37`: *"built and owned entirely outside this package, which never dials a proxy itself"* |
| Cloudflare challenge solving | `CloudflareChallengeError` doc, `engine.go:58-61`: *"that is IndexerProxy's FlareSolverr client, **a later indexarr task**"* |
| `IndexerDefinition.spec.yaml` ingestion | `Load` doc: *"the indexer controller calls it once per IndexerDefinition.spec.yaml and once per bundled definition at startup"* — no such controller exists |
| Sourcing the definition corpus | Only `schema.json` is embedded (`schema.go:31`); `1337x.yml`/`0dayfiles-api.yml` are `test/data/` fixtures |
| `DefinitionType` ↔ CRD camelCase mapping | `DefinitionType` doc: *"NOT the IndexerDefinitionStatus CRD's camelCase equivalent; the indexer controller maps between them"* |
| Captcha solving | `CaptchaRequiredError` doc: *"pkg/cardigann never solves captchas"* |
| `clustarr-indexer-sessions` KV mirroring | `ErrSessionRequired` doc names the intended flow (login once → owned Secret → KV bucket → reconstructed `Session`), which does not exist |

The library side of all of these is **done and callable**; only the controller-side
wiring is deferred. `Engine.Proxy` in particular is just a field — a plan that
wants to leave proxying out simply leaves it nil.

---

## 4. `pkg/ratelimit`

Package doc: *"a keyed token-bucket limiter over `golang.org/x/time/rate`, an
exponential Backoff with jitter and a server Retry-After override, and a
closed/open/half-open CircuitBreaker … None of the three types talk to each other;
a caller composes them (limiter for steady-state pacing, breaker for outright
failures, backoff for the delay between retries) the way indexarr's health/limit
bookkeeping does per docs/research/indexers.md §6."*

### 4.1 `Limiter` — the type callers inject

```go
type Config struct {
	RPS   float64 // <= 0 means unlimited (Wait/Allow always succeed immediately)
	Burst int     // <= 0 is treated as 1
}
type Limiter struct{ /* unexported */ }

func New(defaults Config) *Limiter
func (l *Limiter) Wait(ctx context.Context, key string) error
func (l *Limiter) Allow(key string) bool
func (l *Limiter) SetConfig(key string, cfg Config)
func (l *Limiter) Remove(key string)
```
One `rate.Limiter` per key, created lazily from the key's `SetConfig` or the
default. Safe for concurrent use. `SetConfig` applies in place via
`SetLimit`/`SetBurst`, so accumulated tokens survive a reconfiguration. `Remove`
exists so *"the map does not grow without bound over the process lifetime"* — call
it when an `Indexer`/`DownloadClient` is deleted.

`Config.Burst`'s `<= 0 → 1` rule is load-bearing: *"a real Burst of 0 makes
x/time/rate refuse every request, never what a caller who only set RPS wants."*

### 4.2 `Backoff`

```go
type Backoff struct {
	Base       time.Duration // attempt 0's delay before jitter; must be > 0
	Max        time.Duration // ceiling after exponentiation, before jitter; <= 0 means no ceiling
	Multiplier float64       // growth per attempt; <= 0 defaults to 2
	Jitter     float64       // randomised fraction, clamped to [0,1]; 0 disables
}
func (b Backoff) Next(attempt int, retryAfter time.Duration) time.Duration
```
`retryAfter > 0` is returned **completely unchanged** — *"a server's Retry-After is
authoritative in both directions (never lowered by Max, never raised by Base)."*
Otherwise `min(Base * Multiplier^attempt, Max)` ±`Jitter/2`, using `math/rand/v2`.

### 4.3 `CircuitBreaker`

```go
type BreakerState int
const ( BreakerClosed BreakerState = iota; BreakerOpen; BreakerHalfOpen )
func (s BreakerState) String() string

type BreakerConfig struct {
	FailureThreshold  int           // <= 0 defaults to 1
	Cooldown          time.Duration
	HalfOpenSuccesses int           // <= 0 defaults to 1
}
type CircuitBreaker struct{ /* unexported */ }
func NewCircuitBreaker(cfg BreakerConfig, clock clockwork.Clock) *CircuitBreaker
func (b *CircuitBreaker) Allow() bool
func (b *CircuitBreaker) Success()
func (b *CircuitBreaker) Failure()
func (b *CircuitBreaker) State() BreakerState
```
`nil` clock → `clockwork.NewRealClock()`. *"one per indexer or provider; callers key
their own map by name — this type itself is not keyed."* `Allow` lets exactly one
probe through when `Cooldown` expires; a concurrent second `Allow` returns false,
so a recovering dependency is not stampeded.

### 4.4 Stubs in `pkg/ratelimit`

**None.** No `ErrUnsupported`, no `panic(`, no `TODO`.

---

## 5. `pkg/release` — the indexer-path subset

### 5.1 Title → parsed release

```go
type Options struct {
	Kind       commonv1.MediaKind // zero value ("") means auto-detect via ClassifyKind
	SeriesType string             // "standard" (default when empty) | "daily" | "anime"; ignored for non-TV kinds
}

func Parse(title string, o Options) (*ParsedRelease, error)
func ParseKind(title string, kind commonv1.MediaKind) (*ParsedRelease, error) // sugar for Parse(title, Options{Kind: kind})
func ParsePath(path string, o Options) (*ParsedRelease, error)
func ClassifyKind(title string) commonv1.MediaKind
```
`ClassifyKind` is *"a heuristic, not a parse: Parse still runs the real per-kind
regex family and returns an error if the guess was wrong for the actual title
shape."*

For an indexer row, `Parse(title, Options{})` with no pinned kind is the natural
call — the classifier decides. `ParsePath` is for the library scanner
(`importarr`), not the indexer path.

`Parse`'s shared pre/post-dispatch steps: `extractIDs` (`ids.go`) strips embedded
`[tmdbid-N]`/`{tvdb-N}` tokens before kind dispatch so *"an embedded id token can
never leak into a title, season/episode match or release-group pattern for any
kind"*, and `buildTitles` (`titles.go`) splits `AKA`/` / ` alternate titles.

### 5.2 `ParsedRelease`

```go
type ParsedRelease struct {
	Title       string
	Titles      []string
	Year        int
	Quality     commonv1.Quality
	Revision    commonv1.Revision
	Languages   []string
	Group       string
	Hash        string // scene obfuscation hash token, e.g. trailing -a1b2c3d4; "" normally
	Edition     string
	Seasons     []int
	Episodes    []int
	Absolute    []int
	AirDate     *time.Time
	FullSeason  bool
	Partial     bool
	MultiSeason bool
	Special     bool
	ReleaseType commonv1.ReleaseType
	Music       *MusicInfo
	Book        *BookInfo
	Comic       *ComicInfo
	Hints       Hints
	IDs         map[string]string
}
type Hints struct {
	Codec     []string // x264, x265, h264, h265, AV1, XviD, VC1, MPEG2
	HDR       []string // HDR10, HDR10+, DV, HLG, PQ, SDR
	Audio     []string // DTS-X, DTS-HD MA, TrueHD, Atmos, EAC3, AC3, AAC, FLAC, Opus, MP3
	Channels  string   // "7.1", "5.1", "2.0"; "" when not present
	Streaming []string // AMZN, NF, DSNP, ATVP, HULU, HMAX, ...
	Container string   // mkv, mp4, avi; "" when not present
}
```
(`MusicInfo`, `BookInfo`, `ComicInfo` are populated per kind; see §5.6 for their
fields. `Seasons`/`Episodes`/`Absolute` are `[]int`, while `schema.Release`'s
equivalents are `[]int32` — the conversion is the caller's.)

### 5.3 `ApplyTo` — what the parser fills on a `ReleaseInfo`

```go
func (p *ParsedRelease) ApplyTo(ri *commonv1.ReleaseInfo)
```
`pkg/release/convert.go:24`, the whole body:

```go
ri.Quality      = p.Quality
ri.Revision     = p.Revision
ri.ReleaseGroup = p.Group
ri.Edition      = p.Edition
ri.Languages    = p.Languages
ri.ReleaseType  = p.ReleaseType
if len(p.IDs) > 0 { /* merge p.IDs into ri.IDs, allocating if nil */ }
```

**That is six fields plus IDs. Everything else on `ReleaseInfo` is the indexer
path's responsibility.** The doc says so explicitly: *"leaving every
indexer-sourced field (GUID, DownloadURL, Seeders, ...) untouched."*

So `indexarr` must fill, by hand, from the wire release:

`GUID`, `IndexerRef`, `IndexerName`, `Title` (raw), `Protocol`, `SizeBytes`,
`PublishedAt` (**`*metav1.Time` — pass absence through, never backfill**),
`DownloadURL`, `MagnetURL`, `InfoHash`, `InfoURL`, `Seeders`, `Leechers`,
`IndexerFlags`, `Categories`, `IDs` (merged with the parser's).

And it must **not** fill `FormatScore` / `MatchedFormats`: those are written by
`pkg/decision.Evaluate` on the consumer side
(`app/catalog/worker/rssmatcher/handler.go:270-272`: *"Decision.Release already
carries the resolved FormatScore and MatchedFormats — pkg/decision.Evaluate writes
both onto it before scoring — so there is nothing to fold back on"*).

The full `commonv1.ReleaseInfo` field list, with the enum constraint that matters:

```go
IndexerFlags []string `json:"indexerFlags,omitempty"`
// +kubebuilder:validation:items:Enum=freeleech;halfleech;neutralleech;doubleupload;internal;exclusive;scene
```
Anything outside that seven-value enum will be rejected by the apiserver when a
`Download.spec.release` or a `Search` result is persisted.

`PublishedAt`'s doc comment is a Phase-C scar worth reading in full before writing
any code that touches it (`api/common/v1alpha1`, `ReleaseInfo.PublishedAt`): a
non-pointer `metav1.Time` was unpersistable, and *"ranking uses publish age as the
usenet tiebreaker, so substituting "now" makes a dateless release sort as brand new
and substituting the zero time makes it sort as ancient. Neither is true."*

### 5.4 Matching and dedup helpers

```go
func CleanTitle(title string) string
func Normalize(title string) string
func MatchTitle(p *ParsedRelease, cands []TitleCandidate) (best int, score float64)
type TitleCandidate struct{ Title string; Year int }
func (p *ParsedRelease) Fingerprint() string
func Age(publishedAt, now time.Time) (days int32, hours int32, minutes int64)
func SizePerMinuteCentiMB(sizeBytes int64, runtimeMinutes int32) int64 // -1 for unknown runtime
```

`CleanTitle` is *"the key pkg/decision and the metadata gateway match parsed titles
against inventory titles with"* — and it is what
`rssmatcher.TitleYearKey` calls (`app/catalog/worker/rssmatcher/index.go:104`). Use
`CleanTitle`, never `Normalize`, for equality.

`Fingerprint()` is *"a deterministic 16-hex-char sha256 prefix of
CleanTitle+Year+Seasons+Episodes+Quality.Name+Group"* — and its doc draws the exact
distinction `indexarr` needs:

> It is a content-level dedup key, distinct from indexarr's own **sha1(indexer:guid)
> Msg-Id (spec §8.7)**: that one dedupes repeat sightings of the *same*
> indexer/guid pair; this one dedupes the *same release* seen through two different
> indexers, which neither guid nor infohash can do when only one of the two carries
> an infohash (a usenet mirror of a scene release, for example).

The Msg-Id half already exists in `pkg/events`:
```go
func MsgIDForRelease(indexerName, guid string) string // sha1("<indexerName>:<guid>")
```

### 5.5 Stubs in `pkg/release`

**None.** No `ErrUnsupported`, no `panic(`, no `TODO`.

### 5.6 Per-kind sub-structs (for completeness)

```go
type MusicInfo struct { Artist, Album string; Year int; Codec string; BitrateKbps int32; VBR bool; SampleBits int32 }
type BookInfo  struct { Author, Title string; Year int; Format, Narrator, ASIN string; Unabridged bool }
type ComicInfo struct { Series, Issue string; Volume int; Year int; Format string; Manga bool }
```

---

## 6. Stub / panic audit — the grep transcript

### 6.1 `panic(`, `log.Fatal`, `os.Exit`

```
$ grep -rn --include='*.go' 'panic(' pkg/torznab pkg/newznab pkg/cardigann pkg/ratelimit pkg/release
(no matches — including test files)

$ grep -rn --include='*.go' 'log.Fatal\|os.Exit' pkg/torznab pkg/newznab pkg/cardigann pkg/ratelimit pkg/release
(no matches)
```

**Clean.** Nothing in these five packages panics.

### 6.2 The two one-line `ErrUnsupported` stubs Phase B shipped

They are **not** in the five packages under review. They are in `pkg/metadata`,
and both are still one-liners:

```go
// pkg/metadata/clients/tmdb/tmdb.go:197-200
// SearchMovies is not implemented by this task; it returns
// metadata.ErrUnsupported until a later task needs it.
func (c *Client) SearchMovies(context.Context, string, int) ([]metadata.MovieHit, error) {
	return nil, metadata.ErrUnsupported
}
```

```go
// pkg/metadata/clients/musicbrainz/musicbrainz.go:101-106
// SearchArtists is not implemented by this task: MusicBrainz search uses
// Lucene query syntax, which is out of scope here (see the brief). It
// returns metadata.ErrUnsupported until a later task needs it.
func (c *Client) SearchArtists(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, metadata.ErrUnsupported
}
```

Neither blocks the `indexarr` path. Two nearby things a planner should know exist
but not confuse with these:

- `pkg/metadata/clients/tmdb/tmdb.go:164` — `return nil, metadata.ErrUnsupported`
  inside `FindMovie`'s `default:` branch. **Not a stub**; it is the correct answer
  for an `ExternalIDs` carrying none of tmdb/imdb/tvdb.
- `pkg/importlist/errors.go:25` — `ErrNotImplemented` plus a
  `NotImplementedError{Provider, TODO}` that `Unwrap()`s to it. This is the
  *pattern* for deferred import-list providers (tmdb, custom, arr per spec §17);
  the `indexarr` path does not touch it.
- `app/catalog/controller/metadataprovider/prober.go:146,180` — a deliberate
  Phase-C prober stub that returns `metadata.ErrUnsupported` without a call.

### 6.3 `pkg/cardigann`'s real gap list — decoded, exposed, never read

This is the finding with teeth. Three gaps are documented in the source; **eight
more are not.** Verified by counting non-test, non-`yaml:`-tag references to each
field name within `pkg/cardigann/`:

| `Definition` field | Refs outside the struct tag | Documented as a gap? | Consequence |
| --- | ---: | --- | --- |
| `RowsBlock.After` | guard only | **yes** — `// KNOWN GAP`, `definition.go:312` | `ErrUnsupportedRowFeature`, `search.go:371` |
| `RowsBlock.DateHeaders` | guard only | **yes** — `// KNOWN GAP`, `definition.go:315` | same |
| field `\|append`/`\|noappend` key form | n/a | **yes** — `search.go:53-58` | same sentinel |
| `SearchPathBlock.InheritInputs` | 0 | **yes**, TODO only — `search.go:291` | always-merge, path wins; no distinct behaviour |
| **`SearchBlock.Error`** | **0** | **no** | **a tracker's error page during search is never detected.** `ErrorBlock` is only evaluated on login (`login.go:185,211,252`). A rate-limit or "login expired" HTML page parses as zero rows and reads as "no results". |
| **`SearchBlock.PreprocessingFilters`** | **0** | **no** | declared preprocessing is silently skipped. (`KeywordsFilters` *is* applied, `search.go:198`.) |
| **`ResponseBlock.NoResultsMessage`** | **0** | **no** | cannot distinguish "genuinely no results" from a failure |
| **`Definition.Encoding`** | **0** | **no** | non-UTF-8 tracker pages are not transcoded |
| **`Definition.RequestDelay`** | **0** | **no** | the definition's own pacing hint is ignored; the caller must supply it |
| **`Definition.FollowRedirect`** (and `SearchPathBlock.FollowRedirect`) | **0** | **no** | Go's default redirect policy applies unconditionally |
| **`Definition.Certificates`** | **0** | **no** | pinned certs are not installed |
| **`Definition.TestLinkTorrent`** | **0** | **no** | no download-link validation |
| **`Definition.LegacyLinks` / `Replaces`** | **0** | **no** | no legacy-host fallback, no definition-supersedes chain |
| **`RowsBlock.Multiple`** | **0** | **no** | — |
| **`LoginBlock.Selectors` (bool)** | **0** | **no** | — |
| **`LoginBlock.GetSelectorInputs`** | **0** | **no** | only `SelectorInputs` is scraped (`login.go:166`) |

Reproduce with:

```bash
for f in RequestDelay FollowRedirect Encoding Certificates TestLinkTorrent \
         LegacyLinks Replaces PreprocessingFilters Multiple NoResultsMessage; do
  printf "%-24s " "$f"
  grep -rn --include='*.go' "\.$f\b" pkg/cardigann/ \
    | grep -v '_test.go' | grep -v 'yaml:' | wc -l
done
```

**Planning implication.** Every one of these is a field a real-world definition may
set, that `Load` will happily accept, and that the engine will then ignore without
a word. `SearchBlock.Error` is the dangerous one: it turns an authentication or
rate-limit failure into a silent zero-result search, which is exactly the class of
bug CLAUDE.md's "the scanner never guesses" invariant exists to prevent. The plan
should either (a) implement `search.error` evaluation in this phase, or (b) state
explicitly that an indexer returning zero rows cannot be distinguished from one
returning an error page, and gate `Indexer.status` health on something else.

### 6.4 `TODO` / `FIXME` in the five packages (non-test)

```
pkg/cardigann/definition.go:312  // KNOWN GAP: decoded, not implemented — see search.go
pkg/cardigann/definition.go:315  // KNOWN GAP: decoded, not implemented
pkg/cardigann/search.go:288      // path wins; see the TODO(inheritinputs) note below
pkg/cardigann/search.go:291      // TODO(inheritinputs): ...
```
That is the complete list. `pkg/torznab`, `pkg/newznab`, `pkg/ratelimit` and
`pkg/release` have none.

---

## 7. The three contract questions

### 7.1 How `app/catalog/worker/search` calls the indexer RPC

**The caller already ships. This is the contract `indexarr` must satisfy exactly.**

#### The interface

`app/catalog/worker/search/rpc.go:38-45`:

```go
// SearchRPC is the federated-search half of clustarr.rpc.indexarr.search that
// this package depends on. indexarr (Phase D) MUST serve that subject with
// exactly schema.SearchRequest -> schema.SearchResponse, already pinned in
// pkg/events/schema/index.go; this package adds no new payload type, only
// this narrow client interface plus a fake for tests.
type SearchRPC interface {
	Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error)
}
```

#### The subject and transport

`events.RPCIndexSearch = "clustarr.rpc.indexarr.search"` (`pkg/events/subjects.go:156`),
queue group `events.QueueGroupIndexarr = "indexarr"` (`subjects.go:162`).

`busSearchRPC.Search` (`rpc.go:58-67`), whole body:

```go
var resp schema.SearchResponse
if err := b.r.Request(ctx, events.RPCIndexSearch, req, &resp); err != nil {
	if errors.Is(err, events.ErrNoResponders) {
		return schema.SearchResponse{}, events.Retry(noRespondersRetryAfter, fmt.Errorf("search RPC: %w", err))
	}
	return schema.SearchResponse{}, fmt.Errorf("search RPC: %w", err)
}
return resp, nil
```

`const noRespondersRetryAfter = 15 * time.Second` (`rpc.go:36`) — *"short because
the usual cause is a rolling restart of indexarr, and long enough that a full
search backlog does not hammer the subject while indexarr is down or, **before
Phase D, not built at all**."*

The responder side is `events.Requester.Serve` (`pkg/events/bus.go:159-161`):

```go
Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) error
```

There is a working example to copy at `app/catalog/metadata/rpc.go:57-91`
(`ServeRPC`): one `bus.Serve` per subject, each handler opening its own span
(`tracing.Start(ctx, "metadata.rpc.serve."+verb)`), `json.Unmarshal` the request,
call, `tracing.RecordError` on a non-empty `resp.Error`, `json.Marshal` the
response. **Note its doc's caveat, which applies to `indexarr` too:** *"The bus
hands every inbound RPC request a bare context.Background(): pkg/events' Requester
.Serve does not yet extract Clustarr-Trace from the request onto the context it
passes handlers (a gap tracked and fixed separately, by the task that owns
pkg/events)."* Wiring trace propagation into `pkg/events` is listed in CLAUDE.md as
part of Phase C.

#### The exact request built

`SearchDeadline` (`app/catalog/worker/search/request.go:29-31`):

```go
// SearchDeadline is the RPC deadline every federated search carries. Spec §5's
// clustarr.rpc.indexarr.search row: "single reply at min(deadline, 45s)".
const SearchDeadline = 45 * time.Second
```

**There is no `context.WithTimeout` anywhere in `app/catalog/worker/search`.** The
deadline is transmitted as data only, in `SearchRequest.DeadlineMillis = 45000`.
Verified:

```
$ grep -n 'context.WithTimeout\|WithDeadline\|SearchDeadline' app/catalog/worker/search/*.go
app/catalog/worker/search/request.go:29  // SearchDeadline is the RPC deadline ...
app/catalog/worker/search/request.go:31  const SearchDeadline = 45 * time.Second
app/catalog/worker/search/request.go:69  DeadlineMillis: SearchDeadline.Milliseconds(),
```

The outer bound is therefore the consumer's `AckWait`: `ConsumerCatalogSearchHigh`
and `ConsumerCatalogSearchNorm` both use `AckWait: 120 * s`
(`pkg/events/topology.go:451`, `:462`). **`indexarr` owns the 45 s budget; nothing
on the caller side will cut the request off before 120 s.**

`BuildSearchRequest` (`request.go:65-102`), verbatim:

```go
func BuildSearchRequest(kind commonv1.MediaKind, ids TargetIDs, limit int32, userInvoked bool, indexerRefs []schema.Ref, categories []int32) schema.SearchRequest {
	req := schema.SearchRequest{
		Kind:           kind,
		Limit:          limit,
		DeadlineMillis: SearchDeadline.Milliseconds(),
		UserInvoked:    userInvoked,
		Year:           ids.Year,
		IndexerRefs:    indexerRefs,
	}
	if len(categories) > 0 {
		req.Categories = categories
	} else {
		req.Categories = expandCategoryIDs(newznab.ByKind(kind))
	}

	idmap := map[string]string{}
	switch kind {
	case commonv1.MediaKindMovie:
		if ids.TmdbID != 0 { idmap[commonv1.IDKeyTMDB] = strconv.FormatInt(ids.TmdbID, 10) }
		if ids.ImdbID != ""  { idmap[commonv1.IDKeyIMDB] = ids.ImdbID }
	case commonv1.MediaKindEpisode:
		if ids.TvdbID != 0 { idmap[commonv1.IDKeyTVDB] = strconv.FormatInt(ids.TvdbID, 10) }
		req.Episode = ids.Episode
		if !ids.Anime { req.Season = ids.Season }
	}
	if len(idmap) > 0 { req.IDs = idmap }
	return req
}
```

Five concrete facts `indexarr` must honour:

1. **`Kind` is only ever `movie` or `episode`.** Every other kind is discarded
   before the RPC (`worker.go:252-259`: *"§16 scopes catalogarr's non-video kinds
   to M6"*).
2. **`Text` is never set.** The request is ids-only (`IDs`) plus `Year`, `Season`,
   `Episode`, `Categories`. `indexarr` must be able to search from ids alone, or
   resolve a title itself.
3. **Anime: the absolute number goes in `Episode` and `Season` stays nil.** The
   source comment (`request.go:59-64`) explains: *"schema.SearchRequest … has
   Season and Episode but no Absolute field, unlike spec §8.2's "tvdb plus
   season/episode/absolute". For an anime episode this function puts the absolute
   number in Episode and leaves Season nil, which is how *arr indexers key anime
   releases."* **There is no flag in the payload saying "this is absolute
   numbering."** `indexarr` sees `Episode=137, Season=nil` and must infer.
4. **`Categories` are already `Expand`ed** — `newznab.ByKind(kind)` cast to
   `[]int32`. Wait: `expandCategoryIDs` (`request.go:104`) is a *type cast*, not
   `newznab.Expand`. So a default movie search arrives as `[2000]`, a default
   episode search as `[5000]` — parent ids only, **not expanded to leaves**. An
   interactive `Search.spec.categories` overrides wholesale.
5. **`Limit` is always `schema.MaxSearchReleases` (500)**, never
   `Search.spec.limit`. `buildRequest` (`worker.go:471-482`) and its doc:
   *"The RPC limit is always schema.MaxSearchReleases -- spec §5's cap on the
   federated reply -- and deliberately NOT Search.spec.limit; see keepLimit for why
   conflating "fetch" with "keep" narrows recall to the indexer's own ordering."*
   `IndexerRefs` is built as `schema.Ref{Namespace: srch.Namespace, Name: name}`
   from `Search.spec.indexerRefs` — **namespaced**.

#### The response shape expected

`pkg/events/schema/index.go:224-238`:

```go
const MaxSearchReleases = 500 // "indexarr truncates beyond this, reporting the truncation per indexer"

type SearchResponse struct {
	Releases  []Release       `json:"releases,omitempty"`
	Outcomes  []SearchOutcome `json:"outcomes,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}
func (SearchResponse) Schema() string { return "index.SearchResponse.v1" }

type SearchOutcome struct {
	IndexerRef    Ref                 `json:"indexerRef"`
	IndexerName   string              `json:"indexerName,omitempty"`
	Status        SearchOutcomeStatus `json:"status"`
	Releases      int32               `json:"releases"`
	ElapsedMillis int64               `json:"elapsedMillis,omitempty"`
	Error         string              `json:"error,omitempty"`
}
type SearchOutcomeStatus string
const (
	SearchOutcomeOK      SearchOutcomeStatus = "ok"      // answered within the deadline
	SearchOutcomeTimeout SearchOutcomeStatus = "timeout" // still running when the single reply had to be sent
	SearchOutcomeError   SearchOutcomeStatus = "error"
	SearchOutcomeSkipped SearchOutcomeStatus = "skipped" // disabled, rate limited, or did not support the query
)
```

`schema.Release` (`index.go:32-75`) is the same struct the firehose carries:

```go
type Release struct {
	Info        commonv1.ReleaseInfo `json:"info"`            // "the release as the indexer reported it, already normalised"
	ParsedTitle string               `json:"parsedTitle,omitempty"`
	Year        int32                `json:"year,omitempty"`
	Seasons     []int32              `json:"seasons,omitempty"`
	Episodes    []int32              `json:"episodes,omitempty"`
	Absolute    []int32              `json:"absolute,omitempty"`
	AirDate     *time.Time           `json:"airDate,omitempty"`
	FullSeason  bool                 `json:"fullSeason,omitempty"`
	MultiSeason bool                 `json:"multiSeason,omitempty"`
	Special     bool                 `json:"special,omitempty"`
	Kind        commonv1.MediaKind   `json:"kind,omitempty"`
	Hints       map[string][]string  `json:"hints,omitempty"`
	FetchedAt   time.Time            `json:"fetchedAt"`
}
func (Release) Schema() string { return "index.Release.v1" }
```

Note `Hints` is `map[string][]string` on the wire while `release.Hints` is a struct
with a `Channels string` and a `Container string` — a flattening step is needed.

#### What the caller does with the response

`worker.go:282-323`:

```go
req := w.buildRequest(task, snap, srch)
resp, err := w.RPC.Search(ctx, req)
if err != nil {
	tracing.RecordError(span, err)
	return err // already an events.RetryError from busSearchRPC, or a plain error -> backoff nak
}
w.recordAttempt(ctx, ns, grabTarget(task))   // stamps lastSearchedAt/searchAttempts
opts, err := w.decisionOptions(ctx, ns, task)
if err != nil { return err }
rels := releaseInfos(resp.Releases)          // projects r.Info out, verbatim
decisions := w.evaluate()(ctx, snap.Target, profile, w.Catalogue, rels, opts)
recordDecisionMetrics(task.MediaRef.Kind, decisions)
ranked := RankAndCap(decisions, opts, keepLimit(srch))
w.log(ctx).Info("search: decided", "releases", len(rels), "approved", countApproved(decisions),
	"kept", len(ranked), "truncated", resp.Truncated)
if srch != nil { return w.writeResults(ctx, srch, resp.Outcomes, ranked) }
return w.sink().Deliver(ctx, ns, grabTarget(task), ranked)
```

**Only `Release.Info` is consumed by the decision engine.** `releaseInfos`
(`worker.go:544-551`) is a straight projection — everything else on
`schema.Release` (`ParsedTitle`, `Seasons`, `Kind`, `Hints`, …) is **unused by the
search path**. It matters for the firehose (§7.2), not here. Its doc:

> PublishedAt is passed through exactly as the indexer reported it, including
> absence. It used to be backfilled here … It is a \*metav1.Time now, so absence
> round-trips, and backfilling would be a lie with consequences.

Error handling, exhaustively:

| Failure | Caller's behaviour | Evidence |
| --- | --- | --- |
| `events.ErrNoResponders` | `events.Retry(15s, …)` — redelivery in 15 s, does **not** consume the backoff ladder | `rpc.go:61-63` |
| Any other RPC error | returned bare → nak → the subscription's own backoff (`30s, 2m, 10m`, `MaxDeliver: 5`) | `rpc.go:64`, `worker.go:284-287`, `topology.go:449-462` |
| `resp.Truncated == true` | **logged only.** No error, no status condition, no retry | `worker.go:316-318` |
| `resp.Outcomes` | on an interactive search, mapped to `catalogv1alpha1.IndexerOutcome` and written to `Search.status.indexerOutcomes`; on an automatic search, **discarded** | `worker.go:321`, `:587`, `:661` |
| A nameless `SearchOutcome` | **dropped.** `status.indexerOutcomes` is `listType=map` keyed by name | `worker.go:658-661` |
| More than 100 outcomes | truncated by `capOutcomes`; **first entry wins on a duplicate name** | `worker.go:650`, `:682-708` |

`MaxIndexerOutcomes = 100` (`worker.go:650`). **So `indexarr` should send at most
100 distinct, uniquely-named outcomes, and must set `IndexerName` on every one or
it vanishes silently.**

`recordAttempt` runs **after** the RPC and **before** the sink, deliberately, and
never fails the task (`worker.go:326-354`). That ordering is the caller's problem,
not `indexarr`'s, but it means a slow `indexarr` delays the per-item backoff stamp.

`FakeSearchRPC` (`rpc.go:74-98`) is exported precisely so downstream tasks reuse it:
`Response`, `Err`, and `Requests() []schema.SearchRequest`. `indexarr`'s own tests
can assert against the same double.

### 7.2 How `app/catalog/worker/rssmatcher` consumes the firehose

#### Subject filter and consumer tuning

`Handler.Subscription()` (`handler.go:117-126`) reads
`events.Default().Consumer(events.ConsumerCatalogRSSMatcher)` rather than restating
the tuning. That spec (`pkg/events/topology.go:441-447`):

```go
{
	Name: ConsumerCatalogRSSMatcher, Stream: StreamReleases,
	Filters: []string{FilterAllReleases},
	AckWait: 30 * s, MaxDeliver: 6,
	BackOff:       []time.Duration{1 * s, 5 * s, 30 * s, 2 * m, 10 * m},
	MaxAckPending: 256,
},
```

- Stream: `StreamReleases = "CLUSTARR_RELEASES"` (`subjects.go:30`)
- Filter: `FilterAllReleases = "clustarr.rel.>"` (`subjects.go:52`) — **the whole
  firehose, unfiltered**
- Publish subject builder (`subjects.go:189-193`):
  ```go
  // ReleaseSubject builds clustarr.rel.<protocol>.<indexerName>.<newznabTop>,
  // the RSS fan-out subject indexarr publishes parsed releases on.
  func ReleaseSubject(protocol, indexerName string, newznabTop int) string {
  	return fmt.Sprintf("clustarr.rel.%s.%s.%d", tok(protocol), tok(indexerName), newznabTop)
  }
  ```
  `<newznabTop>` is the **top-level** category (2000/3000/5000/7000/8000), i.e.
  `CategoryID.Parent()`.
- Dedup id: `events.MsgIDForRelease(indexerName, guid)` = `sha1("<indexerName>:<guid>")`.
- **`AckWait` is 30 s and `MaxAckPending` is 256** — the matcher holds only 256 in
  flight, which is why `matchRetry = 5 * time.Second` is short (`handler.go:45-49`).

`SetupWithManager` wraps the subscription in `k8s.EveryReplica`, not a bare
`manager.RunnableFunc`, because *"a bare RunnableFunc has no NeedLeaderElection
method, so controller-runtime puts it behind the leader lease … Before Task C12a's
review that meant exactly one replica consumed the release firehose, however many
were scaled up"* (`handler.go:132-136`).

#### Payload

`handler.go:162-165`:

```go
var rel schema.Release
if err := schema.Decode(env.Schema, env.Data, &rel); err != nil {
	return events.Discard("rssmatcher: malformed Release", err)
}
```

So `Envelope.Schema` must be `"index.Release.v1"` and `Envelope.Data` the
JSON-encoded `schema.Release`. Use `schema.Encode(p Payload) (schema string, data
[]byte, err error)` to build both.

#### **What it requires of `Envelope.Key` — the quote the brief asks for**

`app/catalog/worker/rssmatcher/handler.go:166-174`, verbatim:

```go
// Every catalogarr consumer splits the envelope key on "/" for its
// namespace, and indexarr must follow the same convention on
// clustarr.rel.> -- "<namespace>/<indexerName>". Guessing a namespace
// instead would fan one indexer's releases across the whole cluster.
ns, _, ok := strings.Cut(env.Key, "/")
if !ok || ns == "" {
	return events.Discard("rssmatcher: envelope key is not <namespace>/<indexerName>",
		fmt.Errorf("key=%q", env.Key))
}
```

**`Envelope.Key` MUST be exactly `"<namespace>/<indexerName>"`.** A key with no
`"/"`, or with an empty namespace, is **dead-lettered immediately** — `Discard`
copies the payload to `clustarr.dlq.<service>.<task>.<id>` and terminates the
delivery, bypassing `MaxDeliver`. This is the single most likely way for a new
`indexarr` producer to silently black-hole the entire firehose.

The producer this documents **does not exist yet**: nothing in the repo publishes on
`clustarr.rel.>`. The only `bus.Serve(events.RPCIndexSearch, …)` call sites outside
`catalogarr` are in `pkg/events/contracttest/contracttest.go` (a test double).

The package doc (`doc.go:18-26`) states the same expectation from the other side:

> It consumes the CLUSTARR_RELEASES firehose that indexarr publishes on
> `clustarr.rel.>`: one message per new indexer row. Most of them match nothing, so
> the matching has to be cheap -- which is what the field indexes in index.go are
> for.

#### What `Match` actually reads off the payload

`match.go:44-55` dispatches on `rel.Kind`:

- `MediaKindMovie` → `matchMovie`
- `MediaKindSeries` **or** `MediaKindEpisode` → `matchSeries`
- anything else → `(nil, nil)`, matched nothing. *"§16 scopes catalogarr's non-video
  kinds to M6, and a release the classifier could not type at all is not something
  to guess at."*

So **`Release.Kind` must be set**, or the release is silently dropped.

`matchMovie` (`match.go:57-83`): ids first — `rel.Info.IDs[commonv1.IDKeyTMDB]` —
then, only when that yields nothing, `TitleYearKey(rel.ParsedTitle, rel.Year)`.
`matchSeries` (`match.go:85-117`): `rel.Info.IDs[commonv1.IDKeyTVDB]`, then the
same title+year fallback.

```go
// index.go:103-109
func TitleYearKey(title string, year int32) string {
	clean := release.CleanTitle(title)
	if clean == "" { return "" }
	return clean + "|" + strconv.Itoa(int(year))
}
```

**`rel.ParsedTitle` and `rel.Year` are load-bearing.** A release with no usable id
and an empty `ParsedTitle` matches nothing at all. And the title side of the index
is built from `Movie.status.metadata.Title` / `.OriginalTitle` (`index.go:115-124`),
so the value `indexarr` puts in `ParsedTitle` must be the **`release.ParsedRelease.Title`**
(the parsed show/movie title), not the raw scene title.

`episodesFor` (`match.go:130-134`):

```go
if rel.MultiSeason || len(rel.Seasons) != 1 { return nil, nil }
season := rel.Seasons[0]
```

**A multi-season pack is deliberately not matched**, and a release must carry
**exactly one** entry in `Seasons` to resolve episodes at all. `rel.Episodes` and
`rel.Absolute` feed the rest of the episode resolution.

`decisionOptions` (`resolve.go:207-225`) reads `rel.Info.IndexerRef` — the
**name of the `Indexer` resource**, not a display name — to look up
`idx.priority`; an unresolvable ref falls back to `defaultIndexerPriority`.

Error handling on the consumer side, for completeness: `Match` failure →
`events.Retry(5s, …)`; a deleted item → ack; an unresolvable quality profile →
**warn and ack** (*"retrying it six times and dead-lettering would bury one release
per indexer row"*, `handler.go:229-233`); `grab.ErrDuplicateGrab` → ack;
`grab.ErrUnsupportedKind` → ack; anything else from `grab.Decide` →
`events.Retry(10s, …)`.

### 7.3 Outbound-HTTP conventions in this repo

There are **two** established shapes, and they disagree. The plan should follow the
first.

#### Shape A — `pkg/torznab` and `pkg/cardigann` (the indexer shape, follow this)

| Concern | Convention |
| --- | --- |
| **Timeout** | `torznab`: `http.Client{Timeout: 30 * time.Second}` built in `NewClient`, overridable by `WithTimeout`; matches `Indexer.spec.timeout`'s CRD default. `cardigann`: none — `Engine.HTTP` is caller-supplied, zero value falls back to `http.DefaultClient` (**no timeout at all**). |
| **Response cap** | `const maxResponseBodyBytes = 8 << 20` in **both**; `io.ReadAll(io.LimitReader(body, max+1))`, compare `len(body) > max`, return a wrapped `ErrResponseTooLarge` package sentinel. The `+1` is deliberate: *"a body that is exactly at the limit is accepted while anything larger is detected without ever buffering more than max+1 bytes."* |
| **Error sentinels** | Package-level `var Err… = errors.New("<pkg>: …")`, plus struct errors (`*torznab.Error`, `*cardigann.CaptchaRequiredError`, `*CloudflareChallengeError`, `*LoginError`) reached with `errors.As`. `torznab.Error` carries `HTTPStatus` and `RetryAfter`. |
| **Retry** | **None in the library.** No retry, no backoff, no circuit breaker inside either package. `pkg/ratelimit` provides `Backoff` and `CircuitBreaker` for the caller to compose. |
| **User agent** | **Neither package sets one.** `torznab.do` and `cardigann.do` send Go's default. |
| **Tracing** | `tracing.Start` on every outbound call, named `"<pkg>.<op>"` — `torznab.request`, `torznab.caps`, `torznab.search`; `cardigann.get`/`cardigann.post` (from `"cardigann."+strings.ToLower(req.Method)`). `tracing.RecordError(span, err)` on **every** error path, then `defer span.End()`. |
| **Logging** | `logging.FromContext(ctx)` only; no package logger, no logger field. `torznab` logs at Debug with the base URL and the `t` parameter — **never the query string**. |
| **Secret redaction** | `cardigann` goes further: `redactURL`/`redactRawURL`/`redactErr` strip query, fragment and userinfo from every error and log line, and unwrap `*url.Error` so `errors.Is` still works through it. **Copy this.** |
| **Rate limiting** | Caller-owned. `torznab` accepts `WithRateLimit(rate.Limit, int)` and defaults nothing; `cardigann.Engine` accepts nothing at all. |
| **Context** | Both guarantee an already-cancelled context never issues a request — `torznab.do` checks `ctx.Err()` explicitly when no limiter is set, because `rate.Limiter.Wait` has that property and the limiter path relies on it. |

The same shape appears in `pkg/subtitles/providers/opensubtitlescom` (`maxSubtitleBytes`,
`maxErrorBodyBytes`), `pkg/subtitles/providers/gestdown` and `pkg/importlist/trakt`.

#### Shape B — `pkg/metadata/clients/*` (the provider shape)

Constructor signature is uniform:

```go
func New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client            // openlibrary
func New(apiKey, pin string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client          // tvdb
func New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client               // comicvine
func New(httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client                              // audnexus
func New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*Client, error)      // tmdb
func New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*Client, error)   // musicbrainz
```

`httpClient == nil` → `http.DefaultClient`; `baseURL == ""` → the provider's
`defaultBaseURL` const, with `httptest.Server` URLs passed in tests.

The canonical `doGet` (`pkg/metadata/clients/openlibrary/openlibrary.go:325-355`):

```go
func (c *Client) doGet(ctx context.Context, path string, out any) error {
	if err := c.limiter.Wait(ctx); err != nil { return err }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil { return fmt.Errorf("openlibrary: build request: %w", err) }
	if c.userAgent != "" { req.Header.Set("User-Agent", c.userAgent) }
	resp, err := c.http.Do(req)
	if err != nil { return fmt.Errorf("openlibrary: %s: %w", path, err) }
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("openlibrary: decode %s: %w: %w", path, metadata.ErrDecode, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "openlibrary", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("openlibrary: unexpected status %d for %s", resp.StatusCode, path)
	}
}
```

Shared sentinels (`pkg/metadata/errors.go:29-53`): `ErrNotFound`, `ErrRateLimited`,
`ErrAuth`, `ErrUnsupported`, `ErrDecode`, plus
`RateLimitedError{Provider, RetryAfter}` which `Unwrap()`s to `ErrRateLimited`.
Spans are `"metadata.<provider>.<Method>"` (18 of them, all consistent). Retries:
**none**, except TVDB's single re-auth retry after a 401 (`tvdb.go:44-45`,
`waitOnLimiter` draws a fresh token per attempt including the retry). User agent:
set per-provider where the provider demands it — `comicvine.go:52` has
`const userAgent = "Clustarr/0.1 (+https://github.com/mediactl/clustarr)"` because
*"ComicVine is reported to block Go's default one"*; `musicbrainz.New` **errors**
when `userAgent == ""`; `openlibrary` documents that an unidentified client is
throttled 1 rps instead of 3.

#### ⚠️ Divergence to flag

CLAUDE.md states under "Conventions across `pkg/`":

> **Every HTTP response body is read through a cap** — a package-level max, an
> `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel.

**`pkg/metadata/clients/*` does not honour this.** All five hand-rolled clients
decode straight off the socket:

```
$ grep -rn 'json.NewDecoder\|io.ReadAll\|LimitReader' pkg/metadata/clients/*/*.go | grep -v _test
pkg/metadata/clients/comicvine/comicvine.go:256:   json.NewDecoder(resp.Body).Decode(out)
pkg/metadata/clients/tvdb/auth.go:76:              json.NewDecoder(resp.Body).Decode(&out)
pkg/metadata/clients/audnexus/audnexus.go:251:     json.NewDecoder(resp.Body).Decode(out)
pkg/metadata/clients/tvdb/tvdb.go:316:             json.NewDecoder(resp.Body).Decode(out)
pkg/metadata/clients/openlibrary/openlibrary.go:344: json.NewDecoder(resp.Body).Decode(out)
```

No `LimitReader`, no cap const, no `ErrResponseTooLarge`. `indexarr` must follow
`pkg/torznab`/`pkg/cardigann`, **not** `pkg/metadata/clients`, on this point. (Worth
filing as a separate finding against `pkg/metadata`; it is out of scope here.)

---

## 8. Supporting facts a plan will want

### 8.1 `indexarr` today

`app/indexer/run.go` is the only file in the package. It is a complete service
skeleton with empty registration points:

```go
const (
	ServiceName              = "indexarr"
	LeaderElectionID         = ServiceName + ".clustarr.io"
	DefaultIndexPath         = "/index/releases.db"
	DefaultFacadeBindAddress = ":9696"
)
type Role string
const RoleAll Role = "all"   // the only role
```

`Options.Validate()` (`run.go:120-137`) enforces three things worth knowing:
`--index-path` is required; **`--nats-url` is required** (*"the search RPC and the
release firehose both use the bus"*); and **`--leader-elect` is rejected** — *"§3
pins indexarr to exactly one replica"* with a Recreate strategy and an RWO PVC,
because the release index is SQLite and two writers would corrupt it.

`Run` already wires `obs.Bootstrap`, the manager, `k8s.ConnectBus` with
`obs.BusHooks()`, `k8s.EnsureTopology` and a `jetstream` readiness probe, then calls
`setupControllers(mgr, o)` and `setupWorkers(mgr, bus, o)`. There is a standing
`TODO(M2)` at `run.go:185` to add a `"releaseindex"` readiness check.

The other two RPC subjects `indexarr` owns, already pinned in schema and unserved:
`RPCIndexDownload = "clustarr.rpc.indexarr.download"` (`DownloadRequest` →
`DownloadResponse`, which carries *exactly one* of `Bytes`, `MagnetURL` or
`RedirectURL`) and `RPCIndexQuery = "clustarr.rpc.indexarr.query"` (`QueryRequest`
→ `QueryResponse`). Plus two work subjects: `index.RssTask.v1` on
`clustarr.work.indexarr.rss.normal.<indexer-uid>` and `index.DefinitionsSync.v1` on
`clustarr.work.indexarr.definitions.normal.sync`, and an event payload
`index.IndexerEvent.v1` on
`clustarr.evt.index.indexer.<disabled|recovered|limited>.<uid>`
(`events.IndexerEventSubject(action, uid)`).

### 8.2 `Indexer` CRD spec fields relevant to the library wiring

`api/index/v1alpha1/indexer_types.go`, spec (abridged to what the libraries consume):

```go
Protocol   commonv1alpha1.Protocol      `json:"protocol"`           // required
BaseURL    string                       `json:"baseURL"`            // required
APIPath    string                       `json:"apiPath,omitempty"`
Definition *string                      `json:"definition,omitempty"`
DefinitionRef *string                   `json:"definitionRef,omitempty"`
Generic    *GenericNewznab              `json:"generic,omitempty"`
Enabled    *bool                        `json:"enabled,omitempty"`
Priority   int32                        `json:"priority,omitempty"`   // lower wins
Settings   map[string]string            `json:"settings,omitempty"`   // -> cardigann ResolveSettings raw
SecretRef  *corev1.LocalObjectReference `json:"secretRef,omitempty"`  // merged into the same map
EnableRss  *bool                        `json:"enableRss,omitempty"`
EnableAutomaticSearch   *bool           `json:"enableAutomaticSearch,omitempty"`
EnableInteractiveSearch *bool           `json:"enableInteractiveSearch,omitempty"`
RssInterval   metav1.Duration           `json:"rssInterval,omitempty"`
Limits        *Limits                   `json:"limits,omitempty"`     // {QueryLimit, GrabLimit *int32; Unit LimitUnit}
RequestDelay  metav1.Duration           `json:"requestDelay,omitempty"`
Timeout       metav1.Duration           `json:"timeout,omitempty"`    // -> torznab.WithTimeout
ProxyRef      *string                   `json:"proxyRef,omitempty"`   // -> cardigann Engine.Proxy (DEFERRED)
Categories      []int32                 `json:"categories,omitempty"`
AnimeCategories []int32                 `json:"animeCategories,omitempty"`
AnimeStandardFormatSearch bool          `json:"animeStandardFormatSearch,omitempty"`
MinimumSeeders  int32                   `json:"minimumSeeders,omitempty"`
SeedCriteria    *commonv1alpha1.SeedCriteria `json:"seedCriteria,omitempty"`
DownloadClientRef *string               `json:"downloadClientRef,omitempty"`
Tags            []string                `json:"tags,omitempty"`
```

Conditions include `IndexerConditionRateLimited = "RateLimited"` (*"True while the
indexer's query or grab limit is exhausted"*), which is the natural home for
`ratelimit.CircuitBreaker` / `Limits` bookkeeping.

**There is no `spec.rateLimit`.** Map `spec.requestDelay` → `rate.Every(delay)` and
`spec.limits` → the query/grab budgets separately.

---

## 9. Reproduction commands

```bash
cd /home/appkins/src/mediactl/clustarr
go doc -all ./pkg/torznab
go doc -all ./pkg/newznab
go doc -all ./pkg/cardigann
go doc -all ./pkg/ratelimit
go doc -all ./pkg/release
go doc -all ./api/common/v1alpha1 | sed -n '/type ReleaseInfo/,/^type /p'

# stub / panic audit
grep -rn --include='*.go' -E 'ErrUnsupported|ErrNotImplemented|not implemented|TODO|FIXME|KNOWN GAP' \
  pkg/torznab pkg/newznab pkg/cardigann pkg/ratelimit pkg/release | grep -v '_test.go'
grep -rn --include='*.go' 'panic(' pkg/torznab pkg/newznab pkg/cardigann pkg/ratelimit pkg/release

# the cardigann dead-field audit (see §6.3)
for f in RequestDelay FollowRedirect Encoding Certificates TestLinkTorrent LegacyLinks \
         Replaces PreprocessingFilters Multiple NoResultsMessage; do
  printf "%-24s " "$f"
  grep -rn --include='*.go' "\.$f\b" pkg/cardigann/ | grep -v '_test.go' | grep -v 'yaml:' | wc -l
done
```
