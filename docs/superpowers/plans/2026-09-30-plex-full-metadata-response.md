# Plex Full Metadata Response Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** clustarr's Plex provider answers with every field and element of
Plex's Metadata Response that clustarr has a source for. It also fixes the
certification bug that leaves every Movie without a content rating.

**Architecture:** The work falls into five parts.

- **Clients.** The TMDB and TVDB clients learn a locale and fetch the
  missing fields.
- **Gateway.** The metadata gateway writes scalars and small capped lists
  into `status.metadata`. It writes people and similar titles into a new
  NATS KV bucket, one document per item.
- **Episodes.** The Series controller writes episode stills.
- **Provider.** `ui/plex` reads both sources and renders every schema field.
  It honours `X-Plex-Country`, `X-Plex-Language`, the response-customization
  parameters and `episodeOrder`.
- **Wiring.** The ui gets the KV read as a closure, so it never holds a KV
  handle.

**Tech Stack:** Go, controller-gen v0.22.0 (`make generate manifests`),
`github.com/cyruzin/golang-tmdb` v1.9.4, TheTVDB v4 REST, NATS JetStream KV
(`pkg/events`), testify, envtest.

**Spec:** `docs/superpowers/specs/2026-09-30-plex-full-metadata-response-design.md`

## Global Constraints

- **Status lists are capped.** Every new status list carries
  `+kubebuilder:validation:MaxItems`, with the same cap enforced by
  truncation where the gateway writes it. `TestEveryStatusListIsCapped`
  must pass.
- **No floats under `api/`.**
- **The metadata gateway (`k8s.ManagerCatalogarrMetadata`) is the sole
  writer of `status.metadata`.**
  - Every `buildXMetadataAC` stays that manager's complete declaration, per
    the SSA gotchas in CLAUDE.md.
  - Envtests act on objects that already have status.
- **The Series controller (`k8s.ManagerCatalogarrSeries`) is the sole
  writer of `EpisodeStatus`.**
- **People and similar titles never go into a CRD.** They live only in
  bucket `clustarr-metadata-extended`, keyed by
  `events.KVKeyToken(kind + "/" + uid)`.
- **Document caps:** `role` 50; `director`, `writer`, `producer` and
  `similar` 20 each; 64 KiB per document.
- **`ui/` never writes.** It receives only a read closure. No `events.KV`
  value is ever passed into `ui/`, and `TestUINeverWrites` must pass.
- **Person photos are proxied.** They go through the ui's signed
  `/art/search` proxy and are never hot-linked.
- **Fixtures are recorded from the live TMDB and TVDB APIs**
  (`test/data/metadata/tmdb/…`, `test/data/metadata/tvdb/…`), never
  hand-written.
  - Keys come from `TMDB_API_KEY` and `TVDB_API_KEY` in the executor's
    environment.
  - If they are absent, **stop and ask the owner**. Never read a cluster
    Secret without the owner's explicit OK.
- **Paths:** `contentRating` outside the US is written `cc/rating`, lower
  case.
- **Gates:** `make test` and `make lint` (with `helm dependency build
  charts/clustarr` first in a fresh worktree).
- **Commits:** on `main`, path-scoped (`git commit -m … -- <paths>`), with
  a message style matching the repo (`feat(catalog): …`). Never push, never
  `go mod tidy`.

## Review Focus

1. **The `X-Plex-Country` header and query parameter both present:** the
   header wins. Task 9 pins it.
2. **`includeFields` that omits `ratingKey`, `key`, `guid` or `type`:**
   those four stay. Task 10 pins it.
3. **A KV document over 64 KiB** (a 300-person cast): the gateway truncates
   to the caps, and the stored value decodes. Task 6 pins it.
4. **A movie whose `originalLanguage` equals the configured language:** no
   second TMDB call and no `Original*` fields. Tasks 3 and 9 pin it.
5. **An Episode whose KV document is absent:** the provider emits no people
   and no error. Task 8 pins it.

---

### Task 1: API fields (CRDs)

**Files:**
- Modify: `api/catalog/v1alpha1/shared_types.go`, `movie_types.go`,
  `series_types.go`, `episode_types.go`
- Generated: `make generate manifests` (deepcopy, apply configurations,
  `config/crd`, chart CRDs)
- Test: `pkg/crdcheck` (existing guards), plus
  `api/catalog/v1alpha1/metadata_fields_test.go` (new)

**Interfaces (Produces):**

```go
// shared_types.go
type Certification struct {
	// Country is the ISO 3166-1 alpha-2 code the rating applies in.
	// +required
	Country string `json:"country"`
	// Rating is the certification as that country writes it ("R", "15", "FSK 12").
	// +required
	Rating string `json:"rating"`
}
type SeasonImage struct {
	// +required
	Season int32 `json:"season"`
	// +required
	Type ImageType `json:"type"`
	// +required
	URL string `json:"url"`
}
type SeasonTypeRef struct {
	// +required
	ID string `json:"id"`   // "official", "dvd", "absolute", …
	// +required
	Name string `json:"name"` // "Aired Order"
}
// Image gains:
	// Language is the ISO 639-1 language of any text in the image, empty for none.
	// +optional
	Language string `json:"language,omitempty"`

// ReleaseDate (movie_types.go) gains:
	// +optional
	Certification string `json:"certification,omitempty"`

// MovieMetadata gains (all +optional):
	Tagline string `json:"tagline,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	Studios []string `json:"studios,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	Countries []string `json:"countries,omitempty"`
	Adult bool `json:"adult,omitempty"`
	// +listType=map
	// +listMapKey=country
	// +kubebuilder:validation:MaxItems=60
	Certifications []Certification `json:"certifications,omitempty"`
	// +kubebuilder:validation:MaxItems=30
	OriginalGenres []string `json:"originalGenres,omitempty"`

// SeriesMetadata gains (all +optional):
	Tagline string `json:"tagline,omitempty"`
	// +kubebuilder:validation:MaxItems=5
	Networks []string `json:"networks,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	Studios []string `json:"studios,omitempty"`
	// +kubebuilder:validation:MaxItems=5
	Countries []string `json:"countries,omitempty"`
	// +listType=map
	// +listMapKey=country
	// +kubebuilder:validation:MaxItems=60
	Certifications []Certification `json:"certifications,omitempty"`
	// +kubebuilder:validation:MaxItems=30
	OriginalGenres []string `json:"originalGenres,omitempty"`
	// +kubebuilder:validation:MaxItems=400
	SeasonImages []SeasonImage `json:"seasonImages,omitempty"`
	// +kubebuilder:validation:MaxItems=10
	SeasonTypes []SeasonTypeRef `json:"seasonTypes,omitempty"`

// EpisodeStatus gains:
	// Images are the episode's stills (type screenshot).
	// +optional
	// +kubebuilder:validation:MaxItems=4
	Images []Image `json:"images,omitempty"`
```

Every new field gets a doc comment in the file's own style. Name the
source (TMDB field or TVDB field) and the consumer (`ui/plex`, spec §5).

- [ ] **Step 1: Write the failing test.** Create
  `api/catalog/v1alpha1/metadata_fields_test.go`:

```go
package v1alpha1_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestNewMetadataFieldsRoundTrip pins the JSON names ui/plex and the gateway rely on.
func TestNewMetadataFieldsRoundTrip(t *testing.T) {
	m := catalogv1.MovieMetadata{
		Tagline: "t", Studios: []string{"Film4"}, Countries: []string{"United Kingdom"}, Adult: true,
		Certifications: []catalogv1.Certification{{Country: "GB", Rating: "18"}},
		OriginalGenres: []string{"Drame"},
		ReleaseDates:   []catalogv1.ReleaseDate{{Country: "GB", Type: 3, Certification: "18"}},
		Images:         []catalogv1.Image{{Type: catalogv1.ImageTypePoster, URL: "u", Language: "en"}},
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	for _, k := range []string{`"tagline"`, `"studios"`, `"countries"`, `"adult"`, `"certifications"`,
		`"originalGenres"`, `"certification":"18"`, `"language":"en"`} {
		require.Contains(t, string(b), k)
	}
	s := catalogv1.SeriesMetadata{Networks: []string{"BBC One"},
		SeasonImages: []catalogv1.SeasonImage{{Season: 1, Type: catalogv1.ImageTypePoster, URL: "u"}},
		SeasonTypes:  []catalogv1.SeasonTypeRef{{ID: "official", Name: "Aired Order"}}}
	b, err = json.Marshal(s)
	require.NoError(t, err)
	for _, k := range []string{`"networks"`, `"seasonImages"`, `"seasonTypes"`} {
		require.Contains(t, string(b), k)
	}
	b, err = json.Marshal(catalogv1.EpisodeStatus{Images: []catalogv1.Image{{Type: catalogv1.ImageTypeScreenshot, URL: "u"}}})
	require.NoError(t, err)
	require.Contains(t, string(b), `"images"`)
}
```

  Check the `ImageTypeScreenshot` and `ImageTypePoster` constant names with
  `grep -n 'ImageType[A-Z][a-z]* *ImageType' api/catalog/v1alpha1/shared_types.go`
  and use whatever exists.

- [ ] **Step 2: Run it and watch it fail.**
  Run `go test ./api/catalog/v1alpha1/ -run TestNewMetadataFieldsRoundTrip`.
  Expected: a compile error (unknown fields).

- [ ] **Step 3: Add the fields exactly as listed**, then run
  `make generate manifests`.

- [ ] **Step 4: Run the tests and watch them pass.**
  Run
  `go test ./api/... && KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path) go test ./pkg/crdcheck/`.
  Expected: PASS, including `TestEveryStatusListIsCapped` and
  `TestNoCRDDefaultIsUnreachableFromGo`.

- [ ] **Step 5: Commit.**

```bash
git add api config charts/clustarr && git commit -m 'feat(api): metadata fields for the full Plex response -- tagline, studios, countries, adult, per-country certifications, original-language genres, season images and types, episode stills, and image language' -- api config charts/clustarr
```

  Find the chart's CRD path with `git status --short | grep crd` first; the
  command above assumes `charts/clustarr/crds`.

---

### Task 2: The model, and certification choice

**Files:**
- Modify: `pkg/metadata/model.go`
- Create: `pkg/metadata/certification.go`, `pkg/metadata/certification_test.go`

**Interfaces (Produces):**

```go
// model.go
type Certification struct{ Country, Rating string }
type SeasonTypeRef struct{ ID, Name string }
type SimilarRef struct {
	Title string
	Year  int32
	IDs   ExternalIDs
}
// Person gains:
	Kind  PersonKind // cast | director | writer | producer
	Order int32
	Job   string
type PersonKind string
const (
	PersonCast     PersonKind = "cast"
	PersonDirector PersonKind = "director"
	PersonWriter   PersonKind = "writer"
	PersonProducer PersonKind = "producer"
)
// Movie gains:
	Tagline        string
	Studios        []string
	Countries      []string
	Adult          bool
	Certifications []Certification
	OriginalGenres []string
	Similar        []SimilarRef
// Series gains:
	Tagline        string
	Networks       []string
	Studios        []string
	Countries      []string
	Certifications []Certification
	OriginalGenres []string
	SeasonTypes    []SeasonTypeRef

// certification.go
// PickCertification chooses the rating to show: region's, else origin's, else US, else "".
func PickCertification(certs []Certification, region, origin string) string
// CertificationsFromReleases keeps one rating per country: the theatrical
// release's (type 3) certification, else the first non-empty one.
func CertificationsFromReleases(rds []ReleaseDate) []Certification
```

Give each new field a `json:"…,omitempty"` tag in the file's style.

- [ ] **Step 1: Write the failing tests.** `pkg/metadata/certification_test.go`:

```go
package metadata_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestCertificationsFromReleasesPrefersTheatrical(t *testing.T) {
	rds := []metadata.ReleaseDate{
		{Country: "GB", Type: metadata.ReleaseType(1), Certification: "", Date: time.Now()},
		{Country: "GB", Type: metadata.ReleaseType(4), Certification: "15", Date: time.Now()},
		{Country: "GB", Type: metadata.ReleaseType(3), Certification: "18", Date: time.Now()},
		{Country: "DE", Type: metadata.ReleaseType(5), Certification: "16", Date: time.Now()},
		{Country: "FR", Type: metadata.ReleaseType(3), Certification: "", Date: time.Now()},
	}
	assert.ElementsMatch(t, []metadata.Certification{{Country: "GB", Rating: "18"}, {Country: "DE", Rating: "16"}},
		metadata.CertificationsFromReleases(rds))
}

func TestPickCertificationFallsBackRegionOriginUS(t *testing.T) {
	certs := []metadata.Certification{{Country: "GB", Rating: "18"}, {Country: "US", Rating: "R"}}
	assert.Equal(t, "18", metadata.PickCertification(certs, "GB", "US"))
	assert.Equal(t, "R", metadata.PickCertification(certs, "FR", "US"))
	assert.Equal(t, "18", metadata.PickCertification(certs[:1], "FR", "GB"), "a UK-only film (Weekend)")
	assert.Equal(t, "R", metadata.PickCertification(certs[1:], "FR", "GB"), "US last")
	assert.Equal(t, "", metadata.PickCertification(nil, "US", "US"))
}
```

- [ ] **Step 2: Run them and watch them fail.**
  Run `go test ./pkg/metadata/ -run Certification`. Expected: a compile
  error.

- [ ] **Step 3: Implement** the model fields and `certification.go`:

```go
package metadata

// theatrical is TMDB's release type 3.
const theatrical ReleaseType = 3

// CertificationsFromReleases keeps one rating per country: the theatrical
// release's certification, else the first non-empty one in the order given.
func CertificationsFromReleases(rds []ReleaseDate) []Certification {
	first := map[string]string{}
	theatricalCert := map[string]string{}
	var order []string
	for _, rd := range rds {
		if rd.Certification == "" {
			continue
		}
		if _, ok := first[rd.Country]; !ok {
			first[rd.Country] = rd.Certification
			order = append(order, rd.Country)
		}
		if rd.Type == theatrical {
			if _, ok := theatricalCert[rd.Country]; !ok {
				theatricalCert[rd.Country] = rd.Certification
			}
		}
	}
	out := make([]Certification, 0, len(order))
	for _, c := range order {
		rating := first[c]
		if t, ok := theatricalCert[c]; ok {
			rating = t
		}
		out = append(out, Certification{Country: c, Rating: rating})
	}
	return out
}

// PickCertification chooses the rating to show: the region's, else the
// origin country's, else the US one, else none.
func PickCertification(certs []Certification, region, origin string) string {
	for _, c := range []string{region, origin, "US"} {
		if c == "" {
			continue
		}
		for _, cert := range certs {
			if cert.Country == c {
				return cert.Rating
			}
		}
	}
	return ""
}
```

  Check that `ReleaseType` is the type of `ReleaseDate.Type` in `model.go`,
  and use its real name.

- [ ] **Step 4: Run the tests and watch them pass.**
  Run `go test ./pkg/metadata/...`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add pkg/metadata/model.go pkg/metadata/certification.go pkg/metadata/certification_test.go && git commit -m 'feat(metadata): the model carries studios, countries, per-country certifications, people kinds, similar titles and season types, and chooses a certification region-first' -- pkg/metadata/model.go pkg/metadata/certification.go pkg/metadata/certification_test.go
```

---

### Task 3: TMDB — locale, the new fields and credits

**Files:**
- Modify: `pkg/metadata/clients/tmdb/tmdb.go`
- Create: `hack/record-metadata-fixtures/main.go`,
  `test/data/metadata/tmdb/movie-79120.json` (Weekend),
  `test/data/metadata/tmdb/movie-105.json` (Back to the Future),
  `test/data/metadata/tmdb/movie-79120-fr.json` if needed (see Step 1)
- Test: `pkg/metadata/clients/tmdb/fields_test.go` (new)

**Interfaces:**
- Consumes (Task 2): the model fields, `CertificationsFromReleases`,
  `PickCertification`, `PersonKind`.
- Produces:
  - `func (c *Client) WithLocale(language, region string) *Client`, which
    sets the defaults used when `Movie` is called with region `""`;
  - `Movie` fills every Task 2 field.

- [ ] **Step 1: Record fixtures.**
  - Write `hack/record-metadata-fixtures/main.go`, a `package main` that
    GETs a URL list and writes each body to a path, reading
    `TMDB_API_KEY` and `TVDB_API_KEY` from the environment. Never log the
    keys.
  - The TMDB URLs:
    - `https://api.themoviedb.org/3/movie/{id}?language=en-US&append_to_response=release_dates,external_ids,alternative_titles,credits,recommendations,images&include_image_language=en,null,{orig}`
      for 79120 and 105;
    - for 79120, whose `original_language` is `en` (the same as the
      configured language), no second call. Instead record
      `movie/372058?language=en-US&…` (Your Name, `ja`) and
      `movie/372058?language=ja` for the original-language path.
  - Run it with the owner's keys. If they are absent, stop and ask.

- [ ] **Step 2: Write the failing tests.** `fields_test.go` serves the
  recorded bodies from an `httptest` server keyed by path and
  `language`. It asserts:
  - **79120 (Weekend):**
    - `Certification == "18"` under region GB, `== "18"` under region `""`
      (origin GB), and `Certifications` includes `{GB 18}`;
    - `Studios` is non-empty and `Countries` includes "United Kingdom";
    - `Tagline` equals the fixture's `tagline`;
    - `People` has at least one `PersonCast` with `Order == 0` and a
      `Character`, and one `PersonDirector` named "Andrew Haigh";
    - `Similar` is non-empty, each with `IDs["tmdb"]`.
  - **105:**
    - `Certification == "PG"` under US;
    - `Adult == false`;
    - `Images` includes a `logo` from `images.logos`, with `Language` set.
  - **372058:**
    - two requests made, `language=en-US` then `language=ja`;
    - `OriginalGenres` equals the `ja` fixture's genre names;
    - `Images` includes at least one with `Language == "ja"`.
  - **79120 again:** exactly one request, because the original language
    equals the configured one.

  Use the recorded values; open each fixture and copy the exact strings
  into the assertions.

- [ ] **Step 3: Run them and watch them fail.**
  Run `go test ./pkg/metadata/clients/tmdb/ -run Fields`. Expected: FAIL
  (empty fields; `WithLocale` undefined).

- [ ] **Step 4: Implement** in `tmdb.go`:
  1. Add the `language` and `region` fields to `Client`, and `WithLocale`.
     `Movie(ctx, id, region)` uses `region` when non-empty, else
     `c.region`. `languageFor` becomes
     `func (c *Client) languageFor(region string) string`, returning
     `c.language + "-" + region`, or the old default for an empty
     language.
  2. `append_to_response` becomes
     `release_dates,external_ids,alternative_titles,credits,recommendations,images`,
     plus the `include_image_language` option
     `"<lang>,null,<original>"`.
     - The original language is unknown before the first call. Pass
       `"<lang>,null"` first, then fetch images with the original language
       in the second call (step 5).
  3. `mapMovie(d, region)` additionally sets:
     - `Tagline: d.Tagline` and `Adult: d.Adult`;
     - `Studios`: `d.ProductionCompanies[].Name`;
     - `Countries`: `d.ProductionCountries[].Name`;
     - `Certifications: metadata.CertificationsFromReleases(releaseDates)`;
     - `Certification: metadata.PickCertification(m.Certifications, region, originCountry(d))`,
       where `originCountry` is `d.OriginCountry[0]`, else
       `d.ProductionCountries[0].Iso3166_1`;
     - `People`:
       - `credits.cast` → `Person{Kind: PersonCast, Name, Character, Order: int32(Order), ImageURL: profileBaseURL + ProfilePath}`
         (profile path empty means no URL);
       - `credits.crew`: `Job == "Director"` → `PersonDirector`;
         `Department == "Writing"` → `PersonWriter`;
         `Job` in {"Producer", "Executive Producer"} → `PersonProducer`,
         each with `Job`;
     - `Similar`: `recommendations.results[]` →
       `SimilarRef{Title, Year: parseYear(ReleaseDate), IDs: {tmdb: id}}`;
     - images: `images.logos` → `ImageTypeLogo`, `images.posters` →
       poster and `images.backdrops` → fanart, each with
       `Language: Iso639_1` (all capped later by the gateway). Keep the
       existing single poster and backdrop first, so the default choice is
       unchanged.
     - `const profileBaseURL = "https://image.tmdb.org/t/p/w185"`.
  4. Nil-guard every embedded append pointer (`MovieCreditsAppend`,
     `MovieRecommendationsAppend`, `MovieImagesAppend`), as `mapMovie`
     already does, because a promoted field through a nil pointer panics.
  5. If `d.OriginalLanguage != ""` and it differs from `c.language`
     (default `en`), make a second `GetMovieDetails(id, {"language": d.OriginalLanguage, "append_to_response": "images", "include_image_language": d.OriginalLanguage})`.
     - Set `OriginalGenres` from its genres.
     - Append its images with `Language` set.
     - On error, log at debug and keep the first result. The second call is
       an enrichment, never a failure.
  6. `app/catalog/metadata/registry.go`: after `tmdb.New(...)`, call
     `.WithLocale(p.Spec.Language, p.Spec.Region)`.

- [ ] **Step 5: Run the tests and watch them pass.**
  Run `go test ./pkg/metadata/... ./app/catalog/metadata/`. Expected:
  PASS, the existing tmdb tests included.

- [ ] **Step 6: Falsify.** Remove the `Certification:` assignment, and the
  Weekend assertion must fail by name. Restore it.

- [ ] **Step 7: Commit.**

```bash
git add pkg/metadata/clients/tmdb hack/record-metadata-fixtures test/data/metadata/tmdb app/catalog/metadata/registry.go && git commit -m 'fix(metadata): movies get a certification, region first then origin then US -- none ever had one -- and TMDB brings tagline, studios, countries, adult, credits, recommendations, logos and original-language genres and images, in the MetadataProvider'"'"'s language' -- pkg/metadata/clients/tmdb hack/record-metadata-fixtures test/data/metadata/tmdb app/catalog/metadata/registry.go
```

---

### Task 4: TVDB — series extended fields, episode stills and people

**Files:**
- Modify: `pkg/metadata/clients/tvdb/tvdb.go`
- Create: `test/data/metadata/tvdb/series-<id>-extended.json`,
  `test/data/metadata/tvdb/episodes-<id>.json`,
  `test/data/metadata/tvdb/episode-<epid>-extended.json`
- Test: `pkg/metadata/clients/tvdb/fields_test.go`

**Interfaces:**
- Produces:
  - `func (c *Client) WithLocale(language, region string) *Client`;
  - `func (c *Client) EpisodePeople(ctx context.Context, episodeID string) ([]metadata.Person, error)`;
  - `Series` fills `Networks`, `Studios`, `Countries`, `Certifications`,
    `Certification`, `People` (cast and crew from `characters`), season
    images (in `Images` with `Season` set), `SeasonTypes`, and
    `OriginalGenres` when the original language differs;
  - `Episodes` fills `Episode.Image` (type screenshot) from `image`.

- [ ] **Step 1: Record fixtures** with the Task 3 tool, using TVDB's
  login, then:
  - `/series/{id}/extended?meta=translations`;
  - `/series/{id}/episodes/default/eng?page=0`;
  - `/episodes/{epid}/extended`.

  Record for a series with several companies and content ratings: Doctor
  Who (2005), TVDB 78804. Open the extended fixture and note the exact
  JSON names of:
  - companies and their type (`companyType.companyTypeName` expected);
  - `contentRatings[].{name,country}`;
  - `characters[].{name,personName,personImgURL,peopleType,sort}`;
  - `originalNetwork.name` and `latestNetwork.name`;
  - `seasons[].{number,image,type.type}`;
  - `seasonTypes[].{type,name}`;
  - the artwork type ids for season poster and season background.

  Use those names in Step 4. **Where the fixture disagrees with this plan,
  the fixture wins.** Ledger it as a ruling.

- [ ] **Step 2: Write the failing tests.** Assert, from the fixture values:
  - `Networks[0]` is the original network;
  - `Studios` holds the production companies;
  - `Certifications` holds each `contentRatings` country and name;
  - `Certification` is the GB rating under region GB;
  - `People` has a `PersonCast` with `Character` and `ImageURL`, plus any
    `PersonDirector` or `PersonWriter` present;
  - at least one `Image` with `Season != nil` and type poster;
  - `SeasonTypes` includes `{official, …}`;
  - `Episodes()[i].Image.URL` equals the fixture's episode `image`;
  - `EpisodePeople(epid)` returns the episode's guest cast and crew.

- [ ] **Step 3: Run them and watch them fail.**
  Run `go test ./pkg/metadata/clients/tvdb/ -run Fields`. Expected: FAIL.

- [ ] **Step 4: Implement.**
  - Extend `rawSeries.Data` with the fields noted in Step 1.
  - Add `image string` to `rawEpisode`.
  - Map as above. `peopleType` of "Actor" or "Guest Star" is
    `PersonCast`; "Director" is `PersonDirector`; "Writer" is
    `PersonWriter`; "Producer" or "Executive Producer" is
    `PersonProducer`. `sort` becomes `Order`.
  - `Countries`: `[countryName(originalCountry)]`, using a 3-letter-to-name
    table limited to the codes the fixture and the owner's library use,
    falling back to the upper-cased code.
  - `WithLocale`: the TVDB title language is the ISO 639-2 form of
    `spec.language` (`en` → `eng`) via `pkg/lang`; region feeds
    `PickCertification`.
  - `EpisodePeople` GETs `/episodes/{id}/extended` and maps `characters`
    the same way.
  - `app/catalog/metadata/registry.go`: `tvdb.New(...).WithLocale(p.Spec.Language, p.Spec.Region)`.

- [ ] **Step 5: Run the tests and watch them pass.**
  Run `go test ./pkg/metadata/...`. Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add pkg/metadata/clients/tvdb test/data/metadata/tvdb app/catalog/metadata/registry.go && git commit -m 'feat(metadata): TVDB brings networks, companies, per-country content ratings, characters, season artwork and season types for a series, stills for its episodes and people for an episode, in the configured language' -- pkg/metadata/clients/tvdb test/data/metadata/tvdb app/catalog/metadata/registry.go
```

---

### Task 5: The gateway writes the new status fields

**Files:**
- Modify: `app/catalog/metadata/patch.go` (`buildMovieMetadataAC`,
  `buildSeriesMetadataAC`)
- Test: `app/catalog/metadata/patch_test.go` and an envtest in
  `app/catalog/metadata/gateway_envtest_test.go`

- [ ] **Step 1: Write the failing tests.**
  - **`patch_test.go`** (unit): `buildMovieMetadataAC` of a model with 12
    studios, 70 certifications and 40 original genres sets:
    - `Tagline` and `Adult`;
    - `Studios` truncated to 10, `Countries` capped at 10,
      `Certifications` at 60 (in order), `OriginalGenres` at 30;
    - `ReleaseDates[].Certification`;
    - each `Image.Language`.

    `buildSeriesMetadataAC` sets `Networks` (5), `Studios`, `Countries`
    (5), `Certifications`, `SeasonImages` (from `Images` with `Season`
    set, 400; season images leave `Images`), `SeasonTypes` (10) and
    `Certification`.
  - **Envtest:** a Movie that already has `status.metadata` (title and
    genres from an earlier apply) gets the new fields through the Handler,
    and keeps its title, genres and ratings. This is the "act on an object
    that already has status" rule.

- [ ] **Step 2: Run them and watch them fail.**
  Run `go test ./app/catalog/metadata/ -run 'Build|Metadata'`.
  Expected: FAIL.

- [ ] **Step 3: Implement.**
  - In `buildMovieMetadataAC`: `WithTagline`, `WithAdult`, the capped
    `WithStudios`/`WithCountries`/`WithOriginalGenres` via `capStrings`,
    `WithCertifications` (loop, cap 60), `ReleaseDate.WithCertification(rd.Certification)`,
    and `Image.WithLanguage(img.Language)`.
  - Mirror them in `buildSeriesMetadataAC`. Split `s.Images`: those with
    `Season != nil` go to `WithSeasonImages(catalogac.SeasonImage().WithSeason(*img.Season).WithType(t).WithURL(img.URL))`,
    the rest to `WithImages`.
  - `WithCertification` for series is `s.Certification`, which the client
    now sets.

- [ ] **Step 4: Run the tests and watch them pass.**
  Run `make test` for the package set
  (`go test ./app/catalog/... ./pkg/metadata/...` with KUBEBUILDER_ASSETS).
  Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add app/catalog/metadata/patch.go app/catalog/metadata/patch_test.go app/catalog/metadata/gateway_envtest_test.go && git commit -m 'feat(catalog): the metadata gateway records tagline, studios, countries, adult, certifications per country, original-language genres, season images and types' -- app/catalog/metadata/patch.go app/catalog/metadata/patch_test.go app/catalog/metadata/gateway_envtest_test.go
```

---

### Task 6: The extended-metadata KV document

**Files:**
- Modify: `pkg/events/subjects.go` (the bucket constant),
  `pkg/events/topology.go` (the bucket spec)
- Create: `pkg/metadata/extended/extended.go`, `extended_test.go`;
  `pkg/events/natsbus/extended_contract_test.go`
- Modify: `app/catalog/metadata/worker.go` (write after the status apply)

**Interfaces (Produces):**

```go
package extended // pkg/metadata/extended

const (
	MaxRole        = 50
	MaxCrew        = 20 // director, writer, producer each
	MaxSimilar     = 20
	MaxDocBytes    = 64 << 10
)
type Person struct {
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
	Job       string `json:"job,omitempty"`
	Order     int32  `json:"order,omitempty"`
	Photo     string `json:"photo,omitempty"`
}
type Similar struct {
	Title  string `json:"title"`
	Year   int32  `json:"year,omitempty"`
	TmdbID int64  `json:"tmdbID,omitempty"`
	TvdbID int64  `json:"tvdbID,omitempty"`
}
type Doc struct {
	Role     []Person  `json:"role,omitempty"`
	Director []Person  `json:"director,omitempty"`
	Writer   []Person  `json:"writer,omitempty"`
	Producer []Person  `json:"producer,omitempty"`
	Similar  []Similar `json:"similar,omitempty"`
}
func Key(kind commonv1.MediaKind, uid types.UID) string // events.KVKeyToken(string(kind)+"/"+string(uid))
func FromPeople(people []metadata.Person, similar []metadata.SimilarRef) Doc // sorts cast by Order, applies caps
func Encode(d Doc) ([]byte, error) // drops trailing role/similar entries until len <= MaxDocBytes
func Decode(b []byte) (Doc, error)
```

The bucket is `BucketMetadataExtended = "clustarr-metadata-extended"`,
declared with `b(BucketMetadataExtended, 0, "People and similar titles per catalog item, for the Plex provider.")`.

- [ ] **Step 1: Write the failing tests.**
  - **`extended_test.go`:**
    - `FromPeople` of 300 cast (orders shuffled) and 30 directors gives
      `Role` of 50 in `Order` order and `Director` of 20;
    - `Encode` of a doc whose people have 4 KiB names stays at or under
      `MaxDocBytes` and still decodes;
    - `Key` of a uid is a valid KV key (`events.ValidKVKey`).
  - **`extended_contract_test.go`:** against an embedded real NATS server,
    as the other `natsbus` contract tests do:
    - `EnsureTopology` creates the bucket;
    - a `Put` of an encoded doc under `Key(movie, uid)` round-trips through
      `Get`;
    - `Get` of a missing key returns `events.ErrKeyNotFound`.
  - **`TestEnsureSingleNodeTopologyFitsTheKindServersLimits`** still passes
    with the new bucket, since the bucket is file storage.
  - **Worker test** (`worker_envtest_test.go`, extend the existing movie
    case): after `Handle`, the membus KV `BucketMetadataExtended` holds the
    movie's doc with its cast.

- [ ] **Step 2: Run them and watch them fail.**
  Run `go test ./pkg/metadata/extended/ ./pkg/events/... ./app/catalog/metadata/`.
  Expected: FAIL.

- [ ] **Step 3: Implement.**
  - The package as specified.
  - The bucket constant and spec.
  - `Handler` gains `Extended events.KV` (nil writes nothing). After a
    successful status apply for a `*pkgmetadata.Movie` or
    `*pkgmetadata.Series`, it `Put`s
    `extended.Encode(extended.FromPeople(v.People, v.Similar))` under
    `extended.Key(kind, target.GetUID())`.
    - A KV error is logged and does not fail the task, as the cache write
      already does.
  - Wire `Extended: bus.KV(events.BucketMetadataExtended)` where the
    Handler is built. Find it with
    `grep -rn 'metadata.Handler{' app/catalog cmd`.
  - Documents of deleted items are left in place, never read and harmless.
    Ledger that as a ruling against spec §4's "deletes it when the item is
    deleted". Nothing today deletes metadata-cache entries on item
    deletion either, so that part of the spec rested on a false premise.

- [ ] **Step 4: Run the tests and watch them pass.** Run the same command.
  Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add pkg/metadata/extended pkg/events app/catalog/metadata cmd/clustarr && git commit -m 'feat(catalog): people and similar titles live in a clustarr-metadata-extended KV document per item, capped, written by the metadata gateway' -- pkg/metadata/extended pkg/events app/catalog/metadata cmd/clustarr
```

---

### Task 7: Episode stills and episode people

**Files:**
- Modify: `app/catalog/controller/series/reconciler.go` (`DesiredEpisode`
  and the `EpisodeStatus` apply at about line 679)
- Modify: `app/catalog/metadata/worker.go` (the series case: episode
  documents)
- Tests: `app/catalog/controller/series/*_envtest_test.go`,
  `app/catalog/metadata/worker_envtest_test.go`

- [ ] **Step 1: Write the failing tests.**
  - **Series envtest:** an Episode that already has status gets
    `status.images` with the still after a Series reconcile, and keeps
    `title`, `overview` and `airDate`.
  - **Worker envtest:** a Series task, with the TVDB fake answering
    `EpisodePeople` and two Episodes (one `status.hasFile: true`, one
    false), writes a doc under `extended.Key(episode, uid)` for the first
    only.

- [ ] **Step 2: Run them and watch them fail.**

- [ ] **Step 3: Implement.**
  - `DesiredEpisode` carries `Image *metadata.Image`. The apply adds
    `WithImages(catalogac.Image().WithType(catalogv1.ImageTypeScreenshot).WithURL(url))`
    when present. Always send it when present, so a still that later
    disappears is released, the documented convention.
  - Worker: after the series doc, list the series' Episodes through
    `h.Reader`, filtered by the controller owner reference. Use the field
    index the ui and captionarr use if one exists
    (`grep -rn 'IndexField' app/catalog`); else list the namespace and
    filter by `metav1.IsControlledBy`.
    - For each with `Status.HasFile` and `Status.TvdbID != 0`, call the
      first registry series provider that implements
      `interface{ EpisodePeople(context.Context, string) ([]pkgmetadata.Person, error) }`,
      then `Put` its doc.
    - Rate-limit through the provider's limiter; the client already
      waits.
    - Skip, without error, when no provider implements it.

- [ ] **Step 4: Run the tests and watch them pass.**
  Run `go test ./app/catalog/...` with KUBEBUILDER_ASSETS. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add app/catalog/controller/series app/catalog/metadata && git commit -m 'feat(catalog): episodes carry their stills, and an episode with a file gets its guest cast and crew in the extended metadata document' -- app/catalog/controller/series app/catalog/metadata
```

---

### Task 8: The provider emits every field

**Files:**
- Modify: `ui/plex/provider.go` (Options), `ui/plex/metadata.go` (the
  types and four builders), `ui/plex/images.go`, `ui/plex/children.go`
- Create: `ui/plex/extended.go`, `ui/plex/fields_test.go`, goldens under
  `ui/plex/testdata/full_{movie,show,season,episode}.json`

**Interfaces:**
- Consumes: the Task 1 fields; `extended.Doc` and `extended.Decode`.
- Produces:

```go
// provider.go Options gains:
	// Extended reads an item's people and similar titles; nil reads none.
	Extended func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error)
	// PhotoURL turns a provider image URL into an absolute URL served by
	// the ui's signed /art/search proxy; nil drops person photos.
	PhotoURL func(src string) string
```

`Metadata` gains:

```go
	ParentArt      string       `json:"parentArt,omitempty"`
	GrandparentArt string       `json:"grandparentArt,omitempty"`
	OriginalImage  []Image      `json:"OriginalImage,omitempty"`
	Studio         []Tag        `json:"Studio,omitempty"`
	Role           []PersonTag  `json:"Role,omitempty"`
	Director       []PersonTag  `json:"Director,omitempty"`
	Producer       []PersonTag  `json:"Producer,omitempty"`
	Writer         []PersonTag  `json:"Writer,omitempty"`
	Similar        []SimilarTag `json:"Similar,omitempty"`
	SeasonType     []SeasonType `json:"SeasonType,omitempty"`
```

and the new types:

```go
type PersonTag struct {
	Tag   string `json:"tag"`
	Thumb string `json:"thumb,omitempty"`
	Role  string `json:"role,omitempty"`
	Order int32  `json:"order,omitempty"`
}
type SimilarTag struct {
	Guid string `json:"guid"`
	Tag  string `json:"tag,omitempty"`
}
type SeasonType struct{ ID, Source, Tag, Title string } // json: id, source, tag, title
// Tag gains OriginalTag string `json:"originalTag,omitempty"`; Image gains Alt string `json:"alt,omitempty"`.
```

- [ ] **Step 1: Write the failing tests.** `fields_test.go` builds full
  fixtures:
  - a Movie with every new status field;
  - a Series with `SeasonImages`, `SeasonTypes` and `Networks`;
  - Episodes with `Images`;
  - an `Extended` func returning docs.

  Compare each route's response against `testdata/full_*.json`. Write the
  goldens by hand from the spec's §2 list, so every field appears. Also:
  - `TestNoExtendedDocMeansNoPeople`: `Extended` returns
    `(Doc{}, false, nil)` for an episode, so no `Role` and 200.
  - `TestExtendedErrorIsNotAFailure`: `Extended` returns an error, so 200
    without people.
  - `TestSeasonTitleIsSpecialsForSeasonZero`.
  - `TestSimilarInTheCatalogUsesOurGuid`: a similar tmdb id that is a
    Movie in the index gets `tv.plex.agents.custom.clustarr.movies://movie/<uid>`;
    one that isn't gets `tmdb://N`.

- [ ] **Step 2: Run them and watch them fail.**
  Run `go test ./ui/plex/ -run 'Full|Extended|Specials|Similar'`.
  Expected: FAIL.

- [ ] **Step 3: Implement.** In the builders:
  - **Movie:**
    - `Tagline`, `IsAdult: md.Adult`;
    - `Studio` is the first studio, and `Studio[]` holds all of them;
    - `Country[]`;
    - `ContentRating` from `md.Certification` for now (Task 9 makes it
      country-aware);
    - `Image[].Alt` is the title;
    - `Role`, `Director`, `Writer` and `Producer` from the doc (photos
      through `PhotoURL`);
    - `Similar` from the doc, resolved through `idx.ByTMDB` or `idx.ByTVDB`
      to our guid, else `tmdb://`/`tvdb://`.
  - **Show:** the same, plus `Network[]` from `Networks` (falling back to
    `Network`), and `SeasonType` holding one entry for the series'
    `EffectiveEpisodeOrder` with its name from `SeasonTypes`,
    `Source: "tvdb"` and `Title: "TheTVDB (<name>)"`.
  - **Season:**
    - its poster from `SeasonImages` for that season (falling back to the
      series');
    - `ParentArt` is the series fanart URL;
    - the title is "Specials" when index is 0.
  - **Episode:**
    - `Image` holds a `snapshot` from `Status.Images`, and `thumb` is that
      snapshot;
    - `ParentArt` and `GrandparentArt`;
    - `ContentRating` is the series';
    - people from the episode's doc.
  - Artwork URLs follow the existing `/art` URL pattern in `images.go`.
    - The season poster and episode still are provider URLs with no artwork
      object, so they go through `PhotoURL` as well.
    - Ledger this as a ruling: those images are not fetched into the
      artwork store in this plan.
  - `theme`: fetch `http://tvthemes.plexapp.com/<tvdbID>.mp3` once by hand.
    - If it answers 200 with `audio/*`, emit it for shows.
    - Otherwise omit it, and record which in `docs/research/plex-metadata-provider.md`.

- [ ] **Step 4: Run the tests and watch them pass.**
  Run `go test ./ui/...`. Expected: PASS, the existing goldens included.
  Regenerate only if a new `omitempty` field legitimately appears in them,
  and never by weakening an assertion.

- [ ] **Step 5: Commit.**

```bash
git add ui/plex && git commit -m 'feat(ui): the Plex provider emits every Metadata field clustarr has a source for -- people, studios, countries, similar titles, season types, season and episode art, parent and grandparent art' -- ui/plex
```

---

### Task 9: `X-Plex-Country` and `X-Plex-Language`

**Files:**
- Create: `ui/plex/locale.go`, `ui/plex/locale_test.go`
- Modify: the builders, to take a `locale` value

**Interfaces (Produces):**

```go
type locale struct{ Country, Language string } // Language: ISO 639-1 ("en"), Country upper-case
func localeOf(r *http.Request) locale // header wins over query; "en-US" -> {US, en}; X-Plex-Country overrides the region part
func contentRating(certs []catalogv1.Certification, fallback string, loc locale, region, origin string) string
```

- [ ] **Step 1: Write the failing tests.**

```go
func TestContentRatingPrefixesOutsideTheUS(t *testing.T) {
	certs := []catalogv1.Certification{{Country: "GB", Rating: "18"}, {Country: "US", Rating: "R"}}
	assert.Equal(t, "R", contentRating(certs, "", locale{Country: "US"}, "", "GB"))
	assert.Equal(t, "gb/18", contentRating(certs, "", locale{Country: "GB"}, "", "GB"))
	assert.Equal(t, "gb/18", contentRating(certs[:1], "", locale{Country: "FR"}, "", "GB"), "origin fallback")
	assert.Equal(t, "R", contentRating(certs[1:], "", locale{Country: "FR"}, "", "GB"), "US last")
	assert.Equal(t, "PG", contentRating(nil, "PG", locale{}, "", ""), "the stored certification when no list")
}

func TestLocaleHeaderBeatsQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?X-Plex-Country=DE&X-Plex-Language=de-DE", nil)
	r.Header.Set("X-Plex-Country", "GB")
	assert.Equal(t, locale{Country: "GB", Language: "de"}, localeOf(r))
}
```

  Also, through the handler:
  - a movie whose `originalLanguage` is `ja`, requested with
    `X-Plex-Language: en-US`, has `originalTitle`, `OriginalImage[]` (the
    `ja` images) and `Genre[].originalTag`;
  - requested with `ja-JP`, it has none of them;
  - a movie whose original language is `en`, requested in `en`, has none.

- [ ] **Step 2: Run them and watch them fail.**

- [ ] **Step 3: Implement.**
  - `contentRating` uses `metadata.PickCertification`'s order:
    `loc.Country`, else `region`, else `origin`, else US. It converts to
    `pkgmetadata.Certification` or mirrors the loop; mirror it, since ui
    may not import `pkg/metadata`'s gateway-only code. Check with
    `go list -deps ./ui/... | grep pkg/metadata` whether ui already
    imports it.
  - Its result is prefixed `strings.ToLower(country) + "/"` unless the
    country is US. With no list, it returns `fallback` unprefixed.
  - Original fields: emitted when
    `loc.Language != "" && md.OriginalLanguage != "" && loc.Language != md.OriginalLanguage`.
    - `OriginalImage` holds the images whose `Language == md.OriginalLanguage`.
    - `Genre[i].OriginalTag` is `md.OriginalGenres[i]` when the lengths
      match.
  - The handlers call `localeOf(r)` and pass it to the builders.

- [ ] **Step 4: Run the tests and watch them pass.** Run `go test ./ui/plex/`.

- [ ] **Step 5: Commit.**

```bash
git add ui/plex && git commit -m 'feat(ui): the Plex provider chooses the content rating by X-Plex-Country, prefixed outside the US, and adds original-language title, images and genres when Plex asks in another language' -- ui/plex
```

---

### Task 10: Response customization and `episodeOrder`

**Files:**
- Create: `ui/plex/customize.go`, `ui/plex/customize_test.go`
- Modify: the metadata, children, grandchildren and match handlers; the
  show and season builders for `episodeOrder`

**Interfaces (Produces):**

```go
type customization struct{ includeFields, excludeFields, includeElements, excludeElements map[string]bool }
func customizationOf(r *http.Request, body map[string]any) customization // query params; for match also the JSON body keys of the same names
func (c customization) apply(m map[string]any) map[string]any // recursive into "Children"."Metadata"[]
```

`apply` operates on the object marshalled to `map[string]any`:

- a key whose value is a slice or map is an element; anything else is a
  field;
- `ratingKey`, `key`, `guid` and `type` are never removed;
- include lists win over exclude lists when both are given for the same
  kind;
- names are matched case-sensitively as Plex sends them.

- [ ] **Step 1: Write the failing tests.** A table over one full movie
  object:
  - `includeFields=title,year` keeps title, year and the four protected
    fields, plus every element;
  - `excludeFields=summary,tagline` drops both;
  - `includeElements=Genre` keeps only the `Genre` element, plus all
    fields;
  - `excludeElements=Role,Similar` drops both;
  - `includeFields=title` never drops `guid`;
  - a match body `{"includeElements":"Metadata,Children","includeFields":"guid,title,index"}`
    on a season match keeps `Children` and trims its children's fields.

  For `episodeOrder`:
  - a show whose stored order is `official`, asked with
    `episodeOrder=official`, has `Children`;
  - asked with `episodeOrder=dvd`, it has an empty `Children` and an empty
    `/children`.

- [ ] **Step 2: Run them and watch them fail.**

- [ ] **Step 3: Implement.**
  - Handlers render `[]Metadata` as today, marshal each through
    `json.Marshal` into `map[string]any`, apply the customization, and
    write the container.
  - For match, decode the body once into a `map[string]any` beside
    `matchRequest`, so the customization keys are visible.
  - `episodeOrder`: in the show and season paths, when
    `req.EpisodeOrder != ""` (or the query parameter is set) and differs
    from the series' `EffectiveEpisodeOrder`, `Children` is empty and
    `/children` answers an empty container with `totalSize` 0.

- [ ] **Step 4: Run the tests and watch them pass.** Run `go test ./ui/...`.

- [ ] **Step 5: Commit.**

```bash
git add ui/plex && git commit -m 'feat(ui): the Plex provider honours includeFields, excludeFields, includeElements and excludeElements, never dropping the four identity fields, and answers no seasons for an episode order it does not store' -- ui/plex
```

---

### Task 11: Wiring, the write guard and documentation

**Files:**
- Modify:
  - `cmd/clustarr/services.go` and `cmd/clustarr/all.go`, where the ui's
    bus is connected (about line 551): return an extended-read closure
    beside the artwork store;
  - `ui/server.go` and `ui/routes.go`: `Options.PlexExtended` and the
    `PhotoURL` from `searchArt`, both passed to `plex.Options`;
  - `ui/guard_test.go`: confirm `neverWriteSelectors` covers KV `Create`
    and `Delete` outside `ui/actions`, adding them if not;
  - `cmd/clustarr/ui_options_wiring_test.go`;
  - `docs/research/plex-metadata-provider.md` and `CLAUDE.md`.

- [ ] **Step 1: Write the failing test.** Extend
  `TestBothUICommandsWireEveryUIOption` so it fails while the new
  `Options.PlexExtended` is unset in either ui command.

- [ ] **Step 2: Run it and watch it fail.**

- [ ] **Step 3: Implement.** In the connect function, add:

```go
kv := bus.KV(events.BucketMetadataExtended)
extendedRead := func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error) {
	e, err := kv.Get(ctx, extended.Key(kind, uid))
	if errors.Is(err, events.ErrKeyNotFound) {
		return extended.Doc{}, false, nil
	}
	if err != nil {
		return extended.Doc{}, false, err
	}
	d, err := extended.Decode(e.Value)
	return d, err == nil, err
}
```

  - Thread it to `ui.Options.PlexExtended` and on to `plex.Options.Extended`.
    `PhotoURL` is `func(src string) string { return externalURL + s.searchArt.URL(src) }`.
    Check whether `searchArt.URL` returns a path or an absolute URL, and
    join accordingly.
  - Check the `Entry` field name (`e.Value`) against `pkg/events/bus.go`.

- [ ] **Step 4: Update the docs.**
  - **Research note:** mark §11's "Optional fields with no clustarr
    source" list as resolved, with the sources, and record the `theme` and
    `backgroundSquare` outcomes.
  - **CLAUDE.md**, in the UI paragraph on the Plex provider:
    - add that it now emits the full Metadata Response, with people and
      similar titles from `clustarr-metadata-extended`;
    - note the certification fix (2026-09-30) in the Gotchas section: no
      Movie ever had a certification, because `tmdb.mapReleaseDates`
      parsed them and nothing promoted one.

- [ ] **Step 5: Run the full gate.**
  Run `helm dependency build charts/clustarr; make test && make lint`.
  Expected: green, except the failures recorded in the previous plan's
  ledger as already failing at base. Re-check that set by name and report
  any new one.

- [ ] **Step 6: Commit.**

```bash
git add cmd/clustarr ui docs/research/plex-metadata-provider.md CLAUDE.md && git commit -m 'feat(ui): the ui reads the extended metadata document through a closure over its read-only bus, and person photos go through the signed /art/search proxy' -- cmd/clustarr ui docs/research/plex-metadata-provider.md CLAUDE.md
```

---

### Task 12: Proof on kind-cluster-plex

This touches the owner's cluster, so confirm with the owner before Step 1.
Downloads are paused there, and the clustarr release is theirs.

- [ ] **Step 1: Build and load the images.**
  Build and `kind load` the catalogarr, ui and CRD-bearing images at HEAD,
  as the kind-cluster-plex memory records (image tags per commit).
  `helm upgrade` the `clustarr` release with the new tags. Check
  `kubectl get --raw /readyz` after the load.

- [ ] **Step 2: Refresh two items.**
  Annotate the Movie "Weekend" and one series with
  `clustarr.io/refresh-metadata`, then wait for `status.metadata.refreshedAt`
  to move.

- [ ] **Step 3: Check the stored data.**
  - `kubectl get movie <weekend> -o jsonpath='{.status.metadata.certification}'`
    is `18`, and `certifications`, `studios` and `tagline` are populated.
  - The KV doc exists. Read it through the ui's provider route: the
    Metadata has a `Role`.

- [ ] **Step 4: Refresh in Plex and verify.**
  - Refresh the item in Plex: `PUT /library/metadata/{id}/refresh` via the
    cluster-plex manager's token path, or let the clustarr watch do it on
    the metadata change.
  - Read-only query on the Plex library database: `content_rating`
    (`gb/18`), `studio`, `tagline`, and `tags` of type director and cast
    for the item.
  - Screenshot-level check by the owner.

- [ ] **Step 5: Record the rollout** in the kind-cluster-plex memory.
