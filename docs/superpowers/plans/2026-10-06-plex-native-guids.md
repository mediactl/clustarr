# Plex-native GUIDs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** clustarr's Plex provider answers movies, shows, seasons and episodes with their real `plex://` GUIDs, so Plex Web shows Watchlist, while Plex keeps reading every field from clustarr.

**Architecture:**
- A new `pkg/metadata/clients/plex` client resolves Plex ids from `metadata.provider.plex.tv`. It is enabled by a `plex` MetadataProvider.
- The metadata gateway stores them:
  - Movie and Series: `status.metadata.externalIDs["plex"]`, through the existing IDResolver crosswalk.
  - Seasons: `status.metadata.plexSeasons`.
  - Episodes: on each episode it serves the Series reconciler, which writes Episode `status.plexID`.
- The UI's Plex provider builds `plex://` GUIDs from those fields (switch `--plex-guids`), and resolves Plex ids PMS sends back through a new projection lookup.

**Tech Stack:** Go, controller-runtime/envtest, server-side apply (`pkg/k8s.PatchStatus`), `pkg/metadata/clients/httpjson`, testify, Helm chart + kustomize.

**Spec:** `docs/superpowers/specs/2026-10-06-plex-native-guids-design.md`

## Global Constraints

- GPL-3.0 header (`hack/boilerplate.go.txt`, "Copyright 2026 The Clustarr Authors.") on every new Go file.
- Every status write goes through `pkg/k8s.PatchStatus`, and every apply declares the manager's complete set (CLAUDE.md "Server-side apply replaces a field manager's ownership set").
- The metadata gateway writes `status.metadata` as `k8s.ManagerCatalogarrMetadata`. The Series reconciler writes Episode provider fields as `k8s.ManagerCatalogarrSeries`.
- Provider client rules (CLAUDE.md "Conventions across `pkg/`"):
  - bodies are read through a cap (`httpjson.Client` does this);
  - the limiter is injected, never defaulted;
  - errors are `metadata` sentinels;
  - the token goes only in the `X-Plex-Token` header, never in a URL or an error.
- A Plex id is accepted only when Plex returns exactly one result whose `Guid[]` contains the id asked for. Anything else is `metadata.ErrNotFound`.
- A Plex id is 24 lowercase hex digits (`^[0-9a-f]{24}$`). It can never collide with a clustarr ratingKey (a 36-char UUID, `ratingKeyPattern`).
- `ratingKey` and `key` in Plex answers stay clustarr's own. Only `guid`, `parentGuid`, `grandparentGuid` and in-catalog `Similar[].guid` change.
- With `--plex-guids=false` every Plex answer is byte-identical to today's (the existing goldens in `ui/plex/testdata/` must not change). Plex-id routes still resolve.
- `cmd/clustarr` must never link ffgo or purego (no new imports of either).
- **Shared checkout:** commit with a pathspec (`git commit -m '...' -- <paths>`), never a bare `git commit`, never `git stash`, never `go get`/`go mod tidy`. Other sessions commit on `main` concurrently.
- **`charts/clustarr/values.yaml` carries another session's staged hunk** (as of 2026-10-06). Commit only your own hunk of it through a temporary index (Task 9 shows the commands).
- Envtest commands need `export KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)"` first (CLAUDE.md: a suite that finishes in milliseconds skipped).
- Push only from the controlling session, after `make test` passes at HEAD in a clean worktree (copy `charts/clustarr/charts/*.tgz` in first).

## Review Focus

1. **Two catalog items claiming one Plex id** (a film held as two Movies, or a remake mis-resolved). `ByPlexID` must refuse the id rather than answer with an arbitrary one. Pinned in Task 6.
2. **A malformed Plex id in status** (hand-edited `externalIDs["plex"]`, uppercase, wrong length). The provider must keep clustarr's own GUID for that item, never emit `plex://movie/<garbage>`. Pinned in Task 7.
3. **A `plex://show/` hint or Plex id sent to the movies root** (or a movie's id to the TV root). It must be not-found, as a clustarr ratingKey of the wrong type is. Pinned in Task 8.
4. **Plex numbering an episode pair differently from TVDB** (Firefly: Plex has S00E07-12 that TVDB lacks; the pair fallback could hand one episode's id to another). The (season, episode) fallback must never assign a Plex episode whose TVDB id names a different episode, nor one already claimed. Pinned in Task 4.
5. **Plex's cloud failing or throttling (429) during a refresh or an episode sync.** Stored ids stay put:
   - `externalIDs["plex"]` through `Merge(known)`;
   - `plexSeasons` through the prior value;
   - Episode `status.plexID` through the existing value.
   Pinned in Tasks 3 and 5.

---

## File Structure

| File | Responsibility |
|---|---|
| `pkg/metadata/ids.go` | `KeyPlex` |
| `pkg/metadata/model.go` | `Episode.PlexID` |
| `pkg/metadata/plex.go` (new) | `PlexSeason`, `PlexEpisode`, `PlexChildren`, `PlexProvider` |
| `pkg/metadata/registry.go` | `Registry.Plex` slot |
| `pkg/metadata/clients/plex/plex.go` (new) | the Plex metadata client (match, children, paging, cache, ping) |
| `test/data/metadata/plex/*.json` (new) | recorded, trimmed Plex responses |
| `api/catalog/v1alpha1/metadataprovider_types.go` | `plex` type, `token` secret key |
| `api/catalog/v1alpha1/shared_types.go` | `PlexSeasonRef` |
| `api/catalog/v1alpha1/series_types.go` | `SeriesMetadata.PlexSeasons` |
| `api/catalog/v1alpha1/episode_types.go` | `EpisodeStatus.PlexID` |
| `app/catalog/metadata/registry.go` | builds the plex client into `Resolvers` and `Plex` |
| `app/catalog/controller/metadataprovider/registry.go` | readiness probe and registry for `plex` |
| `app/catalog/metadata/plexseasons.go` (new) | `plexSeasons`, `knownPlexSeasons` |
| `app/catalog/metadata/worker.go` | sends `plexSeasons` with a Series' metadata |
| `pkg/metadata/refresh.go` | `SchemaVersion` 1 → 2 |
| `app/catalog/metadata/plexepisodes.go` (new) | `withPlexIDs`, `joinPlexEpisodes` |
| `app/catalog/metadata/rpc.go` | `lookupEpisodes` attaches Plex ids |
| `app/catalog/controller/series/fanout.go` | `DesiredEpisode.PlexID` |
| `app/catalog/controller/series/reconciler.go` | `ensureEpisode` writes or keeps `status.plexID` |
| `ui/projection/index.go` | `ByPlexID` |
| `ui/plex/plexguid.go` (new) | `urls.guid`, `*PlexID` helpers, `resolveKey` |
| `ui/plex/provider.go`, `extended.go`, `metadata.go`, `children.go`, `images.go`, `match.go` | use them |
| `ui/server.go`, `ui/routes.go` | `PlexOptions.PlexGUIDs` |
| `cmd/clustarr/services.go`, `all.go` | `--plex-guids` |
| `charts/clustarr/{values.yaml,values.schema.json,templates/deployments.yaml}`, `config/manager/ui.yaml` | the switch in both installers |
| `CLAUDE.md`, `docs/research/plex-metadata-provider.md`, the spec | docs |

---

### Task 1: The Plex metadata client

**Files:**
- Create: `pkg/metadata/plex.go`, `pkg/metadata/clients/plex/plex.go`, `pkg/metadata/clients/plex/plex_test.go`, `test/data/metadata/plex/*.json`
- Modify: `pkg/metadata/ids.go` (the `const` block at line ~43), `pkg/metadata/model.go:313` (`Episode`), `pkg/metadata/registry.go:32` (`Registry`)

**Interfaces:**
- Produces:
  - `metadata.KeyPlex = "plex"`
  - `metadata.Episode.PlexID string`
  - `metadata.PlexSeason{Number int32; ID string}`
  - `metadata.PlexEpisode{Season, Episode int32; TVDB, ID string}`
  - `metadata.PlexChildren{ShowID string; Seasons []PlexSeason; Episodes []PlexEpisode}`
  - `metadata.PlexProvider` interface: `Provider` + `ShowChildren(ctx, ids ExternalIDs) (*PlexChildren, error)`
  - `Registry.Plex []PlexProvider`
  - `plex.New(plex.Config) (*plex.Client, error)`, where `Config{HTTPClient *http.Client; BaseURL string; Limiter *rate.Limiter; UserAgent, Token string; PageSize int; Clock clockwork.Clock}`
  - `plex.DefaultBaseURL`, `plex.DefaultRate`, `plex.DefaultBurst`, `plex.ErrNoToken`
  - `(*plex.Client).Resolve(ctx, kind, ids) (metadata.ExternalIDs, error)`
  - `(*plex.Client).ShowChildren(...)`
  - `(*plex.Client).Ping(ctx) error`

- [ ] **Step 1: Record the fixtures from the live service.** The token comes from the cluster's `plex-token` Secret and is never printed or written to a file.

```bash
cd /home/appkins/src/mediactl/clustarr
D=test/data/metadata/plex; mkdir -p "$D"
cat > /tmp/claude-1000/plex-trim.py <<'EOF'
import json, sys
mc = json.load(sys.stdin)["MediaContainer"]
keep = ("guid", "type", "title", "index", "parentIndex", "Guid")
out = {k: mc[k] for k in ("offset", "size", "totalSize", "identifier") if k in mc}
if "Metadata" in mc:
    out["Metadata"] = [{k: m[k] for k in keep if k in m} for m in mc["Metadata"]]
json.dump({"MediaContainer": out}, sys.stdout, indent=1); print()
EOF
T=$(kubectl --context kind-cluster-plex -n clustarr-system get secret plex-token -o jsonpath='{.data.token}' | base64 -d)
B=https://metadata.provider.plex.tv; S=5d9c086c7d06d9001ffd27aa
rec() { curl -sf -m 30 -H 'Accept: application/json' -H "X-Plex-Token: $T" "$B$1" | python3 /tmp/claude-1000/plex-trim.py > "$D/$2"; }
rec "/library/metadata/matches?type=1&guid=tmdb://329865&includeGuids=1" matches_movie_tmdb_329865.json
rec "/library/metadata/matches?type=2&guid=tvdb://78874&includeGuids=1" matches_show_tvdb_78874.json
rec "/library/metadata/matches?type=1&guid=tmdb://999999999&includeGuids=1" matches_empty.json
rec "/library/metadata/$S/children?X-Plex-Container-Start=0&X-Plex-Container-Size=10" "children_$S.json"
for s in 0 10 20; do rec "/library/metadata/$S/grandchildren?includeGuids=1&X-Plex-Container-Start=$s&X-Plex-Container-Size=10" "grandchildren_${S}_$s.json"; done
rm /tmp/claude-1000/plex-trim.py; unset T
grep -c '"guid"' "$D"/*.json; grep -l "X-Plex-Token\|$USER" "$D"/*.json || echo "no token in fixtures"
```

Expected:
- `matches_movie…` has 1 `guid`, `plex://movie/5d776b83fb0d55001f56a04b`.
- `matches_show…` has 1, `plex://show/5d9c086c7d06d9001ffd27aa`.
- `matches_empty.json` has 0, with `"totalSize": 0` and no `Metadata` key.
- `children_…` has 2 (seasons 0 and 1).
- The grandchildren pages have 10, 10 and 3, with `totalSize` 23.
- Prints "no token in fixtures".

- [ ] **Step 2: Add the shared types to `pkg/metadata`.**

In `pkg/metadata/ids.go`, add to the `const` block of recognised keys:

```go
	// KeyPlex is an item's id in Plex's own metadata service
	// (metadata.provider.plex.tv): the 24-hex id of its plex:// GUID.
	KeyPlex = "plex"
```

In `pkg/metadata/model.go`, add to `type Episode struct` after `Ratings`:

```go
	// PlexID is the episode's id in Plex's own metadata service, set by
	// the gateway from a PlexProvider; "" when none answered.
	PlexID string `json:"plexID,omitempty"`
```

In `pkg/metadata/registry.go`, add to `type Registry struct` after `Markers`:

```go
	// Plex supplies Plex's own season and episode ids for a series
	// (PlexProvider), first answer wins. A plex client is also an
	// IDResolver, which is how a Movie or Series gets its KeyPlex id.
	Plex []PlexProvider
```

Create `pkg/metadata/plex.go`:

```go
/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package metadata

import "context"

// PlexSeason is one season's id in Plex's own metadata service.
type PlexSeason struct {
	Number int32  `json:"number"`
	ID     string `json:"id"`
}

// PlexEpisode is one episode's id in Plex's own metadata service, with the
// numbering and TVDB episode id Plex files it under (Plex numbers specials
// its own way, so TVDB is the join key and the pair only a fallback).
type PlexEpisode struct {
	Season  int32  `json:"season"`
	Episode int32  `json:"episode"`
	TVDB    string `json:"tvdb,omitempty"`
	ID      string `json:"id"`
}

// PlexChildren are a show's Plex id and its seasons' and episodes'.
type PlexChildren struct {
	ShowID   string        `json:"showID"`
	Seasons  []PlexSeason  `json:"seasons,omitempty"`
	Episodes []PlexEpisode `json:"episodes,omitempty"`
}

// PlexProvider supplies a show's Plex ids, which the ui's Plex provider
// answers seasons and episodes with as plex:// GUIDs
// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md).
type PlexProvider interface {
	Provider
	ShowChildren(ctx context.Context, ids ExternalIDs) (*PlexChildren, error)
}
```

- [ ] **Step 3: Write the failing client tests** in `pkg/metadata/clients/plex/plex_test.go`.

```go
/*
<GPL-3.0 header as in Step 2>
*/

package plex_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/plex"
)

const (
	token   = "test-token-0123456789"
	fireID  = "5d9c086c7d06d9001ffd27aa"
	arrival = "5d776b83fb0d55001f56a04b"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../test/data/metadata/plex/" + name)
	require.NoError(t, err)
	return b
}

// fakePlex serves the recorded responses by path and query, refusing a
// request without the token header as the live service does (401).
type fakePlex struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*http.Request
	// matches maps a match request's guid to the fixture answering it;
	// any other guid answers matches_empty.json.
	matches map[string]string
}

func (f *fakePlex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	if r.Header.Get("X-Plex-Token") != token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	require.Equal(f.t, "application/json", r.Header.Get("Accept"))
	q := r.URL.Query()
	switch r.URL.Path {
	case "/library/metadata/matches":
		name, ok := f.matches[q.Get("guid")]
		if !ok {
			name = "matches_empty.json"
		}
		_, _ = w.Write(fixture(f.t, name))
	case "/library/metadata/" + fireID + "/children":
		_, _ = w.Write(fixture(f.t, "children_"+fireID+".json"))
	case "/library/metadata/" + fireID + "/grandchildren":
		require.Equal(f.t, "1", q.Get("includeGuids"))
		_, _ = w.Write(fixture(f.t, "grandchildren_"+fireID+"_"+q.Get("X-Plex-Container-Start")+".json"))
	default:
		f.t.Errorf("unexpected request %s", r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func newClient(t *testing.T, matches map[string]string) (*plex.Client, *fakePlex) {
	t.Helper()
	f := &fakePlex{t: t, matches: matches}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token, PageSize: 10})
	require.NoError(t, err)
	return c, f
}

var recorded = map[string]string{
	"tmdb://329865":      "matches_movie_tmdb_329865.json",
	"imdb://tt2543164":   "matches_movie_tmdb_329865.json", // Arrival's record carries its imdb id too
	"tvdb://78874":       "matches_show_tvdb_78874.json",
	"tmdb://1":           "matches_movie_tmdb_329865.json", // a result that is not the item asked for
}

func TestNewRefusesNoToken(t *testing.T) {
	_, err := plex.New(plex.Config{})
	require.ErrorIs(t, err, plex.ErrNoToken)
}

func TestResolveFindsAMovieByItsTMDBID(t *testing.T) {
	c, _ := newClient(t, recorded)
	got, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "329865"})
	require.NoError(t, err)
	require.Equal(t, metadata.ExternalIDs{metadata.KeyPlex: arrival}, got)
}

func TestResolveFallsBackToIMDb(t *testing.T) {
	c, _ := newClient(t, recorded)
	got, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt2543164"})
	require.NoError(t, err)
	require.Equal(t, arrival, got[metadata.KeyPlex])
}

func TestResolveFindsAShowByItsTVDBID(t *testing.T) {
	c, _ := newClient(t, recorded)
	got, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "78874"})
	require.NoError(t, err)
	require.Equal(t, fireID, got[metadata.KeyPlex])
}

func TestResolveIsNotFoundWhenPlexHasNoMatch(t *testing.T) {
	c, _ := newClient(t, recorded)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "999999999"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

// A result whose Guid[] does not carry the id asked for is another item:
// clustarr never guesses a Plex id.
func TestResolveRefusesAResultWithoutTheAskedID(t *testing.T) {
	c, _ := newClient(t, recorded)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "1"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestResolveIsUnsupportedForOtherKinds(t *testing.T) {
	c, _ := newClient(t, recorded)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyTMDB: "329865"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

func TestShowChildrenPagesEveryEpisode(t *testing.T) {
	c, f := newClient(t, recorded)
	got, err := c.ShowChildren(context.Background(), metadata.ExternalIDs{metadata.KeyTVDB: "78874"})
	require.NoError(t, err)
	require.Equal(t, fireID, got.ShowID)
	require.Equal(t, []metadata.PlexSeason{
		{Number: 0, ID: "5d9c09de08fddd001f2afb57"},
		{Number: 1, ID: "5d9c09de08fddd001f2afb4c"},
	}, got.Seasons)
	require.Len(t, got.Episodes, 23, "every page of totalSize 23 at page size 10")
	require.Contains(t, got.Episodes, metadata.PlexEpisode{Season: 1, Episode: 1, TVDB: "297989", ID: "5d9c127e4eefaa001f6449c2"})
	require.Contains(t, got.Episodes, metadata.PlexEpisode{Season: 0, Episode: 7, ID: "5ea14257f3d60a003f39ea44"}, "an episode Plex has no TVDB id for")
	require.Len(t, f.requests, 5, "one match, one seasons page, three episode pages")
}

func TestShowChildrenIsCached(t *testing.T) {
	c, f := newClient(t, recorded)
	ids := metadata.ExternalIDs{metadata.KeyTVDB: "78874"}
	_, err := c.ShowChildren(context.Background(), ids)
	require.NoError(t, err)
	n := len(f.requests)
	_, err = c.ShowChildren(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, f.requests, n, "the second call is served from the client's cache")
}

func TestAWrongTokenIsAnAuthError(t *testing.T) {
	f := &fakePlex{t: t, matches: recorded}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "wrong"})
	require.NoError(t, err)
	require.ErrorIs(t, c.Ping(context.Background()), metadata.ErrAuth)
}

// The token travels in the X-Plex-Token header only: no request URL and no
// error carries it.
func TestTheTokenNeverLeavesTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, r.URL.String(), token)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)
	_, err = c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "329865"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
	require.False(t, strings.Contains(err.Error(), "X-Plex-Token"))
}
```

- [ ] **Step 4: Run the tests and check they fail.**

Run: `go test ./pkg/metadata/clients/plex/`
Expected: FAIL to compile, `undefined: plex.New` (package does not exist yet).

- [ ] **Step 5: Implement `pkg/metadata/clients/plex/plex.go`.**

```go
/*
<GPL-3.0 header as in Step 2>
*/

// Package plex resolves items to their ids in Plex's own metadata service
// (metadata.provider.plex.tv), whose plex:// GUIDs Plex Web keys its
// Watchlist and Discover features on
// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md §2).
//
// The shapes are the ones recorded from the live service on 2026-10-06
// into test/data/metadata/plex/ (trimmed to the fields read here):
// GET /library/metadata/matches?type=1|2&guid=<scheme>://<id>&includeGuids=1
// answers a MediaContainer whose Metadata[] carry guid
// ("plex://movie/<24 hex>") and Guid[] (the external ids); no match is 200
// with no Metadata. /library/metadata/<id>/children lists a show's seasons
// (index = season number) and /grandchildren its episodes (parentIndex,
// index, Guid[] with tvdb://<episode id>), paged by X-Plex-Container-Start
// and -Size against totalSize. Every request needs a Plex account token in
// X-Plex-Token; without one the service answers 401.
package plex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is Plex's metadata service.
const DefaultBaseURL = "https://metadata.provider.plex.tv"

// DefaultRate and DefaultBurst are the limit a caller should give this
// client when its MetadataProvider sets none. Plex publishes no rate; two
// requests a second is chosen politeness.
const (
	DefaultRate  rate.Limit = 2
	DefaultBurst int        = 2
)

// DefaultPageSize is how many seasons or episodes one page asks for. An
// episode is ~7 KB untrimmed, so 100 stays far under
// metadata.MaxResponseBytes (8 MiB).
const DefaultPageSize = 100

// maxPages bounds one listing: 40 pages of 100 is 4,000 episodes, above the
// longest show in the owner's library with room to spare.
const maxPages = 40

// Cache lifetimes: a Plex id does not change, a show's episode list grows
// as episodes air, and a miss is retried daily.
const (
	idTTL       = 7 * 24 * time.Hour
	childrenTTL = 12 * time.Hour
	missTTL     = 24 * time.Hour
	cacheSize   = 8192
)

// ErrNoToken is returned by New without a token: Plex answers every
// request without one 401.
var ErrNoToken = errors.New("plex: a Plex token is required")

var idPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
	// Token is a Plex account token, sent as X-Plex-Token.
	Token string
	// PageSize overrides DefaultPageSize (tests page a short show).
	PageSize int
	// Clock times the client's cache; nil is the real clock.
	Clock clockwork.Clock
}

// Client resolves Plex ids. It is a metadata.IDResolver (a Movie's or
// Series' KeyPlex) and a metadata.PlexProvider (a show's seasons and
// episodes).
type Client struct {
	h        *httpjson.Client
	baseURL  string
	token    string
	pageSize int
	cache    *metadata.LRUCache
}

// New builds a Client, refusing a Config without a token.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrNoToken
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	size := cfg.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	clock := cfg.Clock
	if clock == nil {
		clock = clockwork.NewRealClock()
	}
	cache, err := metadata.NewLRUCache(cacheSize, clock)
	if err != nil {
		return nil, err
	}
	return &Client{
		h:        &httpjson.Client{Provider: "plex", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent, Now: clock.Now},
		baseURL:  base,
		token:    cfg.Token,
		pageSize: size,
		cache:    cache,
	}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "plex" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTMDB, metadata.KeyTVDB, metadata.KeyIMDb}}
}

// item is one entry of a MediaContainer. guid and Guid are distinct JSON
// keys; encoding/json matches each tag exactly before falling back to a
// case-insensitive match, so the two fields do not collide.
type item struct {
	GUID        string `json:"guid"`
	Index       *int32 `json:"index"`
	ParentIndex *int32 `json:"parentIndex"`
	Guid        []struct {
		ID string `json:"id"`
	} `json:"Guid"`
}

type container struct {
	MediaContainer struct {
		Offset    int    `json:"offset"`
		Size      int    `json:"size"`
		TotalSize int    `json:"totalSize"`
		Metadata  []item `json:"Metadata"`
	} `json:"MediaContainer"`
}

func (c *Client) header() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("X-Plex-Token", c.token)
	return h
}

// plexID is it's id when its guid is plex://<typ>/<24 hex>.
func plexID(it item, typ string) (string, bool) {
	id, ok := strings.CutPrefix(it.GUID, "plex://"+typ+"/")
	return id, ok && idPattern.MatchString(id)
}

// externalID is it's Guid[] value for scheme ("tvdb" -> "297989"), "" for
// none.
func externalID(it item, scheme string) string {
	for _, g := range it.Guid {
		if v, ok := strings.CutPrefix(g.ID, scheme+"://"); ok {
			return v
		}
	}
	return ""
}

// match asks Plex for the item of plexType carrying guid and accepts only a
// single result that carries guid among its own ids.
func (c *Client) match(ctx context.Context, plexType int, typ, guid string) (string, error) {
	q := url.Values{"type": {strconv.Itoa(plexType)}, "guid": {guid}, "includeGuids": {"1"}}
	var out container
	if err := c.h.GetJSON(ctx, c.baseURL+"/library/metadata/matches?"+q.Encode(), c.header(), &out); err != nil {
		return "", err
	}
	ms := out.MediaContainer.Metadata
	if len(ms) != 1 {
		return "", fmt.Errorf("plex: %s: %d results: %w", guid, len(ms), metadata.ErrNotFound)
	}
	scheme, id, _ := strings.Cut(guid, "://")
	if externalID(ms[0], scheme) != id {
		return "", fmt.Errorf("plex: %s: the result is another item: %w", guid, metadata.ErrNotFound)
	}
	pid, ok := plexID(ms[0], typ)
	if !ok {
		return "", fmt.Errorf("plex: %s: unexpected guid %q: %w", guid, ms[0].GUID, metadata.ErrNotFound)
	}
	return pid, nil
}

// itemID is a Movie's or Series' Plex id, asked by each of its ids in turn
// (a movie by tmdb then imdb, a show by tvdb, tmdb then imdb), each answer
// and each miss cached.
func (c *Client) itemID(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (string, error) {
	var (
		plexType int
		typ      string
		keys     []string
	)
	switch kind {
	case commonv1.MediaKindMovie:
		plexType, typ, keys = 1, "movie", []string{metadata.KeyTMDB, metadata.KeyIMDb}
	case commonv1.MediaKindSeries:
		plexType, typ, keys = 2, "show", []string{metadata.KeyTVDB, metadata.KeyTMDB, metadata.KeyIMDb}
	default:
		return "", fmt.Errorf("plex: no Plex id for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	for _, k := range keys {
		v := ids[k]
		if v == "" {
			continue
		}
		guid := k + "://" + v
		key := "id/" + typ + "/" + guid
		var cached string
		if hit, _ := c.cache.Get(ctx, key, &cached); hit {
			if cached == "" {
				continue
			}
			return cached, nil
		}
		id, err := c.match(ctx, plexType, typ, guid)
		switch {
		case err == nil:
			_ = c.cache.Set(ctx, key, id, idTTL)
			return id, nil
		case errors.Is(err, metadata.ErrNotFound):
			_ = c.cache.Set(ctx, key, "", missTTL)
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("plex: no Plex %s for %v: %w", typ, ids, metadata.ErrNotFound)
}

// Resolve answers KeyPlex for a Movie or Series (metadata.IDResolver).
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.plex.Resolve")
	defer span.End()
	id, err := c.itemID(ctx, kind, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return metadata.ExternalIDs{metadata.KeyPlex: id}, nil
}

// ShowChildren answers a show's Plex id and its seasons' and episodes'
// (metadata.PlexProvider), cached per show.
func (c *Client) ShowChildren(ctx context.Context, ids metadata.ExternalIDs) (*metadata.PlexChildren, error) {
	ctx, span := tracing.Start(ctx, "metadata.plex.ShowChildren")
	defer span.End()
	show, err := c.itemID(ctx, commonv1.MediaKindSeries, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	key := "children/" + show
	var cached metadata.PlexChildren
	if hit, _ := c.cache.Get(ctx, key, &cached); hit {
		return &cached, nil
	}
	seasons, err := c.pages(ctx, "/library/metadata/"+show+"/children", nil)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	episodes, err := c.pages(ctx, "/library/metadata/"+show+"/grandchildren", url.Values{"includeGuids": {"1"}})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out := metadata.PlexChildren{ShowID: show}
	for _, s := range seasons {
		if id, ok := plexID(s, "season"); ok && s.Index != nil {
			out.Seasons = append(out.Seasons, metadata.PlexSeason{Number: *s.Index, ID: id})
		}
	}
	for _, e := range episodes {
		id, ok := plexID(e, "episode")
		if !ok || e.Index == nil || e.ParentIndex == nil {
			continue
		}
		out.Episodes = append(out.Episodes, metadata.PlexEpisode{
			Season: *e.ParentIndex, Episode: *e.Index, TVDB: externalID(e, metadata.KeyTVDB), ID: id,
		})
	}
	_ = c.cache.Set(ctx, key, out, childrenTTL)
	return &out, nil
}

// pages reads every page of a listing, refusing one longer than maxPages.
func (c *Client) pages(ctx context.Context, path string, q url.Values) ([]item, error) {
	var all []item
	for range maxPages {
		v := url.Values{}
		for k, vs := range q {
			v[k] = vs
		}
		v.Set("X-Plex-Container-Start", strconv.Itoa(len(all)))
		v.Set("X-Plex-Container-Size", strconv.Itoa(c.pageSize))
		var out container
		if err := c.h.GetJSON(ctx, c.baseURL+path+"?"+v.Encode(), c.header(), &out); err != nil {
			return nil, err
		}
		mc := out.MediaContainer
		all = append(all, mc.Metadata...)
		if len(mc.Metadata) == 0 || len(all) >= mc.TotalSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("plex: %s: more than %d pages", path, maxPages)
}

// Ping asks for one well-known film (The Matrix): the cheapest call that
// proves the service reachable and the token accepted.
func (c *Client) Ping(ctx context.Context) error {
	q := url.Values{"type": {"1"}, "guid": {"tmdb://603"}}
	var out container
	return c.h.GetJSON(ctx, c.baseURL+"/library/metadata/matches?"+q.Encode(), c.header(), &out)
}

var (
	_ metadata.IDResolver   = (*Client)(nil)
	_ metadata.PlexProvider = (*Client)(nil)
)
```

- [ ] **Step 6: Run the tests and check they pass.**

Run: `go test ./pkg/metadata/... && go vet ./pkg/metadata/clients/plex/`
Expected: PASS, every `pkg/metadata` package `ok`.

- [ ] **Step 7: Commit.**

```bash
git add pkg/metadata/plex.go pkg/metadata/clients/plex test/data/metadata/plex
git commit -m "feat(metadata): a Plex client resolves movies, shows, seasons and episodes to their ids in Plex's own metadata service (plex:// GUIDs), from recorded responses" -- pkg/metadata/ids.go pkg/metadata/model.go pkg/metadata/registry.go pkg/metadata/plex.go pkg/metadata/clients/plex test/data/metadata/plex
```

---

### Task 2: The `plex` MetadataProvider type

**Files:**
- Modify: `api/catalog/v1alpha1/metadataprovider_types.go:38-83`, `app/catalog/metadata/registry.go` (`isSupplementary`, `addSupplementary`), `app/catalog/controller/metadataprovider/registry.go` (`supplementary`, `addToRegistry`, `isSupplementary`, `buildSupplementary`)
- Test: `app/catalog/metadata/registry_test.go`, `app/catalog/controller/metadataprovider/registry_test.go`

**Interfaces:**
- Consumes: `plex.New`, `plex.Config`, `plex.DefaultBaseURL`, `plex.DefaultRate`, `plex.DefaultBurst`, `(*plex.Client).Ping`, `Registry.Plex` (Task 1).
- Produces:
  - `catalogv1alpha1.MetadataProviderPlex = "plex"` and `catalogv1alpha1.MetadataSecretKeyToken = "token"`;
  - a `plex` MetadataProvider puts one client in both `Registry.Resolvers` and `Registry.Plex`.

- [ ] **Step 1: Write the failing test** in `app/catalog/metadata/registry_test.go` (package `metadata`):

```go
// A plex MetadataProvider reads secretRef key "token" and is both a
// resolver (a Movie's or Series' plex id) and a PlexProvider (a show's
// seasons and episodes).
func TestBuildRegistryWiresPlexAsAResolverAndAPlexProvider(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Plex-Token")
		b, err := os.ReadFile("../../../test/data/metadata/plex/matches_movie_tmdb_329865.json")
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "plex-token", Namespace: "clustarr"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyToken: []byte("tok")},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()
	reg, err := BuildRegistry(context.Background(), c, []catalogv1alpha1.MetadataProvider{{
		ObjectMeta: metav1.ObjectMeta{Name: "plex", Namespace: "clustarr"},
		Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderPlex, Enabled: enabled(), BaseURL: &srv.URL,
			SecretRef: &corev1.LocalObjectReference{Name: "plex-token"},
		},
	}}, srv.Client())
	require.NoError(t, err)
	require.Len(t, reg.Resolvers, 1)
	require.Len(t, reg.Plex, 1)
	got, err := reg.Resolvers[0].Resolve(context.Background(), commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "329865"})
	require.NoError(t, err)
	require.Equal(t, "5d776b83fb0d55001f56a04b", got[pkgmetadata.KeyPlex])
	require.Equal(t, "tok", gotToken)
}
```

(Add `"os"` to the file's imports if it is not there.)

In `app/catalog/controller/metadataprovider/registry_test.go`, add a probe test. First read the top of that file and copy the closest existing supplementary-provider test's setup (it builds a `catalogv1alpha1.MetadataProviderSpec` and calls `newSupplementaryProber`, or `NewProber`, with a secret map):

```go
func TestThePlexProbePingsWithTheToken(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Plex-Token")
		require.Equal(t, "tmdb://603", r.URL.Query().Get("guid"))
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":0}}`))
	}))
	defer srv.Close()
	spec := catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderPlex, BaseURL: &srv.URL}
	p, err := newSupplementaryProber(spec, map[string][]byte{catalogv1alpha1.MetadataSecretKeyToken: []byte("tok")}, srv.Client())
	require.NoError(t, err)
	_, err = p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok", gotToken)

	_, err = newSupplementaryProber(spec, map[string][]byte{}, srv.Client())
	require.Error(t, err, "a plex provider without secretRef key token is refused")
}
```

- [ ] **Step 2: Run the tests and check they fail.**

Run: `go test ./app/catalog/metadata/ -run TestBuildRegistryWiresPlex ; go test ./app/catalog/controller/metadataprovider/ -run TestThePlexProbe`
Expected: FAIL to compile, `undefined: catalogv1alpha1.MetadataProviderPlex`.

- [ ] **Step 3: Add the API type.** In `api/catalog/v1alpha1/metadataprovider_types.go`:
- Append `;plex` to the `+kubebuilder:validation:Enum=` line.
- Add this to the type constants:

```go
	// MetadataProviderPlex resolves items to their ids in Plex's own
	// metadata service, which the ui's Plex provider answers with as
	// plex:// GUIDs (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md).
	// Takes secretRef key token: a Plex account token (the Plex watchlist
	// import list's Secret carries one under the same key).
	MetadataProviderPlex MetadataProviderType = "plex"
```

Add this to the secret-key constants:

```go
	// MetadataSecretKeyToken is a Plex account token, read by plex only.
	MetadataSecretKeyToken = "token"
```

Run: `make generate manifests`
Expected: `config/crd/bases/catalog.clustarr.io_metadataproviders.yaml` gains `- plex` in the type enum. `git status --short api config` shows only the types file and that CRD.

- [ ] **Step 4: Wire the gateway registry.** In `app/catalog/metadata/registry.go`:
- Import `plexclient "github.com/mediactl/clustarr/pkg/metadata/clients/plex"`.
- Add `catalogv1alpha1.MetadataProviderPlex` to `isSupplementary`'s case list.
- Add this case to `addSupplementary` (and add `plex: Resolvers and Plex.` to its doc list):

```go
	case catalogv1alpha1.MetadataProviderPlex:
		tok, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyToken)
		if err != nil {
			return err
		}
		cl, err := plexclient.New(plexclient.Config{
			HTTPClient: httpClient, BaseURL: baseURL(p, plexclient.DefaultBaseURL),
			Limiter: supplementaryLimiter(p, plexclient.DefaultRate, plexclient.DefaultBurst), UserAgent: ua, Token: tok,
		})
		if err != nil {
			return fmt.Errorf("metadata: build plex client for %s/%s: %w", p.Namespace, p.Name, err)
		}
		reg.Resolvers = append(reg.Resolvers, cl)
		reg.Plex = append(reg.Plex, cl)
```

- [ ] **Step 5: Wire the controller's registry and probe.** In `app/catalog/controller/metadataprovider/registry.go`:
- Import `plexclient "github.com/mediactl/clustarr/pkg/metadata/clients/plex"`.
- Add `plex metadata.PlexProvider` to `type supplementary struct`.
- Add `catalogv1alpha1.MetadataProviderPlex` to `isSupplementary`.
- In `addToRegistry`'s default branch, after the markers append, add:

```go
		if a.plex != nil {
			reg.Plex = append(reg.Plex, a.plex)
		}
```

Add this case to `buildSupplementary`, before `MetadataProviderOMDb`:

```go
	case catalogv1alpha1.MetadataProviderPlex:
		c, err := plexclient.New(plexclient.Config{
			HTTPClient: httpClient, BaseURL: baseURL(spec), Limiter: limiterFor(spec, plexclient.DefaultRate, plexclient.DefaultBurst), UserAgent: ua,
			Token: string(secret[catalogv1alpha1.MetadataSecretKeyToken]),
		})
		if err != nil {
			return nil, fmt.Errorf("plex requires secretRef key %s: %w", catalogv1alpha1.MetadataSecretKeyToken, err)
		}
		return &supplementary{resolver: c, plex: c, ping: c.Ping}, nil
```

`baseURL(spec)` in this file returns `""` for an unset BaseURL, and `plexclient.New` then defaults it. Read `baseURL` first to confirm. If it does not return `""`, pass `plexclient.DefaultBaseURL` the way the other cases do.

- [ ] **Step 6: Run the tests and check they pass.**

Run: `go test ./app/catalog/metadata/ ./app/catalog/controller/metadataprovider/ ./pkg/crdcheck/ 2>&1 | tail -5`
Expected: PASS. Then run the envtest-backed ones: `export KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)"; go test ./app/catalog/controller/metadataprovider/ ./pkg/crdcheck/`. Expected: `ok`.

- [ ] **Step 7: Commit.**

```bash
git commit -m "feat(catalog): a plex MetadataProvider (secretRef key token) puts the Plex client in the gateway's resolvers and PlexProviders, probed by a match for The Matrix" -- api/catalog/v1alpha1/metadataprovider_types.go api/catalog/v1alpha1/zz_generated.deepcopy.go api/applyconfiguration config/crd/bases app/catalog/metadata/registry.go app/catalog/metadata/registry_test.go app/catalog/controller/metadataprovider
```

(Only paths `make generate manifests` actually changed will be in the commit. Check `git show --stat HEAD`.)

---

### Task 3: Movie and Series ids, and season ids, in `status.metadata`

**Files:**
- Modify: `api/catalog/v1alpha1/shared_types.go` (after `SeasonImage`), `api/catalog/v1alpha1/series_types.go:315` (after `SeasonImages`), `app/catalog/metadata/worker.go` (the `case *pkgmetadata.Series:` at ~line 236), `pkg/metadata/refresh.go:107`
- Create: `app/catalog/metadata/plexseasons.go`, `app/catalog/metadata/plex_envtest_test.go`

**Interfaces:**
- Consumes:
  - `Registry.Plex`, `metadata.PlexProvider.ShowChildren`, `metadata.KeyPlex` (Task 1);
  - the existing `enrich` crosswalk, which already writes every resolver id into `status.metadata.externalIDs` and keeps a prior one through `Merge(known)`.
- Produces:
  - `catalogv1alpha1.PlexSeasonRef{Number int32; ID string}`;
  - `SeriesMetadata.PlexSeasons []PlexSeasonRef`;
  - `metadata.SchemaVersion == 2`.

- [ ] **Step 1: Add the API field.** In `api/catalog/v1alpha1/shared_types.go`, after `SeasonImage`:

```go
// PlexSeasonRef is one season's id in Plex's own metadata service, which
// the ui's Plex provider answers the season with as plex://season/<id>.
type PlexSeasonRef struct {
	// Number is the season number.
	// +required
	Number int32 `json:"number"`

	// ID is Plex's 24-hex id, without the plex://season/ prefix.
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{24}$`
	ID string `json:"id"`
}
```

In `SeriesMetadata` (`series_types.go`), after `SeasonImages`:

```go
	// PlexSeasons are each season's id in Plex's own metadata service,
	// from a plex MetadataProvider. They live here rather than on
	// status.seasons for the same reason as SeasonImages.
	// +optional
	// +kubebuilder:validation:MaxItems=400
	PlexSeasons []PlexSeasonRef `json:"plexSeasons,omitempty"`
```

Run: `make generate manifests`
Expected: `catalogac.PlexSeasonRef()` exists in `api/applyconfiguration/catalog/catalog/v1alpha1/plexseasonref.go`, `SeriesMetadataApplyConfiguration` has `WithPlexSeasons`, and the series CRD has `plexSeasons` with `maxItems: 400`.

- [ ] **Step 2: Write the failing envtest** in `app/catalog/metadata/plex_envtest_test.go` (package `metadata_test`, the same package and helpers as `ratings_envtest_test.go`: `newTestClient`, `handleTask`, `noopCache`):

```go
/*
<GPL-3.0 header>
*/

package metadata_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// stubPlex stands in for the plex client: a resolver and a PlexProvider
// that can be switched to failing, as Plex's cloud throttling would.
type stubPlex struct{ fail *bool }

func (stubPlex) Name() string                           { return "plex" }
func (stubPlex) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubPlex) Resolve(_ context.Context, kind commonv1.MediaKind, _ pkgmetadata.ExternalIDs) (pkgmetadata.ExternalIDs, error) {
	if *p.fail {
		return nil, &pkgmetadata.RateLimitedError{Provider: "plex"}
	}
	return pkgmetadata.ExternalIDs{pkgmetadata.KeyPlex: "5d9c086c46115600200aa2fe"}, nil
}
func (p stubPlex) ShowChildren(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.PlexChildren, error) {
	if *p.fail {
		return nil, errors.New("plex: unexpected status 502")
	}
	return &pkgmetadata.PlexChildren{ShowID: "5d9c086c46115600200aa2fe", Seasons: []pkgmetadata.PlexSeason{
		{Number: 0, ID: "5d9c09dd3c3f87001f36250e"},
		{Number: 1, ID: "5d9c09dd3c3f87001f36250a"},
	}}, nil
}

// A Series refresh lands its Plex id in externalIDs and its seasons' in
// plexSeasons, both under the gateway's manager; a second refresh with
// Plex failing releases neither (spec §4: a failed lookup keeps the ids
// stored before).
func TestHandlerLandsASeriesPlexIDsAndKeepsThemWhenPlexFails(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hplex", "got"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 121361, QualityProfileRef: "web", RootFolderRef: "tv"},
	}))
	login, err := os.ReadFile("../../../test/data/metadata/tvdb/login.json")
	require.NoError(t, err)
	series, err := os.ReadFile("../../../test/data/metadata/tvdb/series_121361.json")
	require.NoError(t, err)
	tvdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			_, _ = w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			_, _ = w.Write(series)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(tvdbSrv.Close)
	tv := tvdb.New("test-key", "test-pin", tvdbSrv.Client(), tvdbSrv.URL, pkgmetadata.NewLimiter(1000, 1))

	fail := false
	p := stubPlex{fail: &fail}
	h := &metadata.Handler{
		Client: c, Reader: c, Cache: noopCache{},
		Registry: &pkgmetadata.Registry{
			Series:    []pkgmetadata.SeriesProvider{tv},
			Resolvers: []pkgmetadata.IDResolver{p},
			Plex:      []pkgmetadata.PlexProvider{p},
		},
	}
	key := types.NamespacedName{Namespace: ns, Name: name}
	want := []catalogv1alpha1.PlexSeasonRef{{Number: 0, ID: "5d9c09dd3c3f87001f36250e"}, {Number: 1, ID: "5d9c09dd3c3f87001f36250a"}}

	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))
	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, key, &got))
	require.Equal(t, "5d9c086c46115600200aa2fe", got.Status.Metadata.ExternalIDs["plex"])
	require.Equal(t, want, got.Status.Metadata.PlexSeasons)
	requireManagerOwns(t, got.ManagedFields, k8s.ManagerCatalogarrMetadata, `"f:plexSeasons"`, `"f:plex"`)

	fail = true
	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))
	require.NoError(t, c.Get(ctx, key, &got))
	require.Equal(t, "5d9c086c46115600200aa2fe", got.Status.Metadata.ExternalIDs["plex"], "a failed lookup must not release the plex id")
	require.Equal(t, want, got.Status.Metadata.PlexSeasons, "a failed lookup must not release plexSeasons")
	require.Equal(t, "Game of Thrones", got.Status.Metadata.Title)
}

// requireManagerOwns asserts that manager's managedFields entry names
// every field path fragment in paths: an over- or under-claim is visible
// only there (CLAUDE.md, "A double-claim is silent").
func requireManagerOwns(t *testing.T, mfs []metav1.ManagedFieldsEntry, manager string, paths ...string) {
	t.Helper()
	for _, mf := range mfs {
		if mf.Manager != manager || mf.FieldsV1 == nil {
			continue
		}
		for _, p := range paths {
			require.True(t, strings.Contains(string(mf.FieldsV1.Raw), p), "%s owns no %s: %s", manager, p, mf.FieldsV1.Raw)
		}
		return
	}
	t.Fatalf("no managedFields entry for %s", manager)
}
```

- [ ] **Step 3: Run it and check it fails.**

Run: `export KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)"; go test ./app/catalog/metadata/ -run TestHandlerLandsASeriesPlexIDs -v 2>&1 | tail -15`
Expected: FAIL. `externalIDs["plex"]` already lands, because `enrich` runs every resolver. The failure must be at `require.Equal(t, want, got.Status.Metadata.PlexSeasons)` with `actual: []` / `nil`. If it fails earlier, read the output before going on.

- [ ] **Step 4: Implement `app/catalog/metadata/plexseasons.go`.**

```go
/*
<GPL-3.0 header>
*/

package metadata

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// maxPlexSeasons is SeriesMetadata.PlexSeasons' MaxItems.
const maxPlexSeasons = 400

// plexSeasons is a Series' status.metadata.plexSeasons: Plex's season ids
// from the first PlexProvider that answers, deduplicated by number (first
// wins) and capped, else prior -- a failed or absent lookup never releases
// ids an earlier refresh found (spec §4).
func plexSeasons(ctx context.Context, reg *pkgmetadata.Registry, ids pkgmetadata.ExternalIDs, prior []catalogv1alpha1.PlexSeasonRef) []catalogv1alpha1.PlexSeasonRef {
	if reg == nil {
		return prior
	}
	for _, p := range reg.Plex {
		pCtx, span := tracing.Start(ctx, "metadata.PlexProvider.ShowChildren")
		ch, err := p.ShowChildren(pCtx, ids)
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			continue
		}
		span.End()
		seen := make(map[int32]bool, len(ch.Seasons))
		out := make([]catalogv1alpha1.PlexSeasonRef, 0, len(ch.Seasons))
		for _, s := range ch.Seasons {
			if seen[s.Number] || len(out) == maxPlexSeasons {
				continue
			}
			seen[s.Number] = true
			out = append(out, catalogv1alpha1.PlexSeasonRef{Number: s.Number, ID: s.ID})
		}
		return out
	}
	return prior
}

// knownPlexSeasons is obj's current status.metadata.plexSeasons, read
// before this refresh started.
func knownPlexSeasons(obj client.Object) []catalogv1alpha1.PlexSeasonRef {
	if s, ok := obj.(*catalogv1alpha1.Series); ok && s.Status.Metadata != nil {
		return s.Status.Metadata.PlexSeasons
	}
	return nil
}

// withPlexSeasons adds seasons to md's apply configuration.
func withPlexSeasons(md *catalogac.SeriesMetadataApplyConfiguration, seasons []catalogv1alpha1.PlexSeasonRef) {
	for _, s := range seasons {
		md.WithPlexSeasons(catalogac.PlexSeasonRef().WithNumber(s.Number).WithID(s.ID))
	}
}
```

In `app/catalog/metadata/worker.go`, in `case *pkgmetadata.Series:`, right after `md := buildSeriesMetadataAC(v, ratings, now())`, add:

```go
		withPlexSeasons(md, plexSeasons(ctx, h.Registry, v.IDs, knownPlexSeasons(target)))
```

In `pkg/metadata/refresh.go`, change `const SchemaVersion int32 = 1` to `2`, and add one line to its doc comment: `2 (2026-10-06): Movie and Series learn their Plex id (KeyPlex) and a Series its plexSeasons.`

- [ ] **Step 5: Run the tests and check they pass.**

Run: `export KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)"; go test ./app/catalog/metadata/ ./pkg/metadata/ 2>&1 | tail -5`
Expected: `ok` for both. If a test pins `SchemaVersion == 1` or a `"metadata-schema1"` id, update it to 2. That pin is the point of the version.

- [ ] **Step 6: Commit.**

```bash
git commit -m "feat(catalog): the metadata gateway records a Series' Plex season ids in status.metadata.plexSeasons, kept when Plex fails; SchemaVersion 2 refreshes every Movie and Series once for its Plex id" -- api/catalog/v1alpha1/shared_types.go api/catalog/v1alpha1/series_types.go api/catalog/v1alpha1/zz_generated.deepcopy.go api/applyconfiguration config/crd/bases app/catalog/metadata/plexseasons.go app/catalog/metadata/plex_envtest_test.go app/catalog/metadata/worker.go pkg/metadata/refresh.go
```

(Add any SchemaVersion test you updated to the pathspec.)

---

### Task 4: Episode Plex ids in the gateway's episode list

**Files:**
- Create: `app/catalog/metadata/plexepisodes.go`, `app/catalog/metadata/plexepisodes_test.go`
- Modify: `app/catalog/metadata/rpc.go:174-203` (`lookupEpisodes`)
- Test: `app/catalog/metadata/rpc_test.go`

**Interfaces:**
- Consumes: `metadata.Episode.PlexID`, `metadata.PlexEpisode`, `Registry.Plex` (Task 1).
- Produces: the `rpc.catalogarr.metadata.lookup` episode answer carries `plexID` per episode (`metadata.Episode.PlexID`), which Task 5 consumes.

- [ ] **Step 1: Write the failing tests.** `app/catalog/metadata/plexepisodes_test.go` (package `metadata`):

```go
/*
<GPL-3.0 header>
*/

package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func ep(season, number int32, tvdb string) pkgmetadata.Episode {
	e := pkgmetadata.Episode{SeasonNumber: season, EpisodeNumber: number}
	if tvdb != "" {
		e.IDs = pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: tvdb}
	}
	return e
}

func TestJoinPlexEpisodes(t *testing.T) {
	plex := []pkgmetadata.PlexEpisode{
		{Season: 1, Episode: 1, TVDB: "297989", ID: "aaaaaaaaaaaaaaaaaaaaaaa1"},
		{Season: 1, Episode: 2, TVDB: "297990", ID: "aaaaaaaaaaaaaaaaaaaaaaa2"},
		// Plex files a special TVDB numbers S00E03 as S00E07, with no tvdb id.
		{Season: 0, Episode: 7, ID: "aaaaaaaaaaaaaaaaaaaaaaa7"},
		// Two Plex episodes on one pair: ambiguous.
		{Season: 2, Episode: 1, ID: "bbbbbbbbbbbbbbbbbbbbbbb1"},
		{Season: 2, Episode: 1, ID: "bbbbbbbbbbbbbbbbbbbbbbb2"},
		// Plex's S03E01 is a different episode (tvdb 900) from clustarr's S03E01 (tvdb 901).
		{Season: 3, Episode: 1, TVDB: "900", ID: "ccccccccccccccccccccccc1"},
	}
	episodes := []pkgmetadata.Episode{
		ep(1, 1, "297989"),     // by tvdb id
		ep(9, 9, "297990"),     // by tvdb id, whatever its numbering
		ep(1, 2, ""),           // pair (1,2) is Plex's tvdb 297990, already claimed above
		ep(0, 7, "555"),        // pair fallback: Plex's episode carries no tvdb id
		ep(2, 1, ""),           // ambiguous pair
		ep(3, 1, "901"),        // pair fallback refused: Plex's episode names another tvdb id
		ep(4, 1, ""),           // Plex has nothing
	}
	joinPlexEpisodes(plex, episodes)
	got := make([]string, len(episodes))
	for i, e := range episodes {
		got[i] = e.PlexID
	}
	require.Equal(t, []string{
		"aaaaaaaaaaaaaaaaaaaaaaa1",
		"aaaaaaaaaaaaaaaaaaaaaaa2",
		"",
		"aaaaaaaaaaaaaaaaaaaaaaa7",
		"",
		"",
		"",
	}, got)
}
```

In `rpc_test.go`, add a stub and an RPC test:

```go
type stubPlexProvider struct {
	children *pkgmetadata.PlexChildren
	err      error
	gotIDs   *pkgmetadata.ExternalIDs
}

func (stubPlexProvider) Name() string                           { return "plex" }
func (stubPlexProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubPlexProvider) ShowChildren(_ context.Context, ids pkgmetadata.ExternalIDs) (*pkgmetadata.PlexChildren, error) {
	*p.gotIDs = ids
	return p.children, p.err
}

func TestServeRPCLookupEpisodesCarriesPlexIDs(t *testing.T) {
	var gotIDs pkgmetadata.ExternalIDs
	reg := &pkgmetadata.Registry{
		Series: []pkgmetadata.SeriesProvider{stubSeriesProvider{episodes: []pkgmetadata.Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Serenity", IDs: pkgmetadata.ExternalIDs{"tvdb": "297989"}},
		}}},
		Plex: []pkgmetadata.PlexProvider{stubPlexProvider{gotIDs: &gotIDs, children: &pkgmetadata.PlexChildren{
			Episodes: []pkgmetadata.PlexEpisode{{Season: 1, Episode: 1, TVDB: "297989", ID: "5d9c127e4eefaa001f6449c2"}},
		}}},
	}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))
	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"tvdb": "78874", "order": "official"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	var e pkgmetadata.Episode
	require.NoError(t, json.Unmarshal(resp.Results[0], &e))
	require.Equal(t, "5d9c127e4eefaa001f6449c2", e.PlexID)
	require.Equal(t, pkgmetadata.ExternalIDs{"tvdb": "78874"}, gotIDs, "the show is asked of Plex by its tvdb id")
}

// Plex failing leaves the episode list as TVDB answered it.
func TestServeRPCLookupEpisodesSurvivesPlexFailing(t *testing.T) {
	var gotIDs pkgmetadata.ExternalIDs
	reg := &pkgmetadata.Registry{
		Series: []pkgmetadata.SeriesProvider{stubSeriesProvider{episodes: []pkgmetadata.Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Serenity"}}}},
		Plex:   []pkgmetadata.PlexProvider{stubPlexProvider{gotIDs: &gotIDs, err: pkgmetadata.ErrRateLimited}},
	}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))
	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"tvdb": "78874"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	require.Len(t, resp.Results, 1)
}
```

- [ ] **Step 2: Run them and check they fail.**

Run: `go test ./app/catalog/metadata/ -run 'TestJoinPlexEpisodes|TestServeRPCLookupEpisodesCarriesPlexIDs|TestServeRPCLookupEpisodesSurvivesPlexFailing'`
Expected: FAIL to compile, `undefined: joinPlexEpisodes`.

- [ ] **Step 3: Implement `app/catalog/metadata/plexepisodes.go`.**

```go
/*
<GPL-3.0 header>
*/

package metadata

import (
	"context"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// withPlexIDs sets each episode's PlexID from the first PlexProvider that
// answers for the series ids name. Any failure leaves the episodes as they
// are: the Series reconciler keeps the ids it stored before.
func withPlexIDs(ctx context.Context, reg *pkgmetadata.Registry, ids pkgmetadata.ExternalIDs, episodes []pkgmetadata.Episode) {
	for _, p := range reg.Plex {
		pCtx, span := tracing.Start(ctx, "metadata.PlexProvider.ShowChildren")
		ch, err := p.ShowChildren(pCtx, ids)
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			continue
		}
		span.End()
		joinPlexEpisodes(ch.Episodes, episodes)
		return
	}
}

// joinPlexEpisodes gives each episode Plex's id for it: by TVDB episode id,
// else by (season, episode) when exactly one of Plex's episodes carries
// that pair, no other episode claimed it by TVDB id, and it does not name
// a different TVDB episode. Plex numbers specials its own way, so the pair
// is only a fallback, and anything ambiguous gets no id (spec §4).
func joinPlexEpisodes(plex []pkgmetadata.PlexEpisode, episodes []pkgmetadata.Episode) {
	type pair struct{ season, episode int32 }
	byTVDB := make(map[string]string, len(plex))
	byPair := make(map[pair]pkgmetadata.PlexEpisode, len(plex))
	count := make(map[pair]int, len(plex))
	for _, p := range plex {
		if p.TVDB != "" {
			byTVDB[p.TVDB] = p.ID
		}
		k := pair{p.Season, p.Episode}
		count[k]++
		byPair[k] = p
	}
	claimed := make(map[string]bool, len(episodes))
	for i := range episodes {
		if id := byTVDB[episodes[i].IDs[pkgmetadata.KeyTVDB]]; episodes[i].IDs[pkgmetadata.KeyTVDB] != "" && id != "" {
			episodes[i].PlexID = id
			claimed[id] = true
		}
	}
	for i := range episodes {
		e := &episodes[i]
		if e.PlexID != "" {
			continue
		}
		k := pair{e.SeasonNumber, e.EpisodeNumber}
		p, ok := byPair[k]
		if !ok || count[k] != 1 || claimed[p.ID] {
			continue
		}
		if tvdb := e.IDs[pkgmetadata.KeyTVDB]; p.TVDB != "" && tvdb != p.TVDB {
			continue
		}
		e.PlexID = p.ID
		claimed[p.ID] = true
	}
}
```

The test's row 3, `ep(1, 2, "")`, expects `""`: pair (1,2) is Plex's episode with TVDB 297990, which row 2 claimed by TVDB id. The `claimed` check refuses it.

In `rpc.go` `lookupEpisodes`, replace the success branch:

```go
		epSpan.End()
		withPlexIDs(ctx, reg, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: tvdbID}, episodes)
		return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(episodes)}
```

- [ ] **Step 4: Run the tests and check they pass.**

Run: `go test ./app/catalog/metadata/ -run 'TestJoinPlexEpisodes|TestServeRPCLookup' -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)' | tail -12`
Expected: every listed test `--- PASS`, then `ok`.

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(catalog): the gateway's episode list carries each episode's Plex id, joined by TVDB episode id, else by an unambiguous season and episode Plex files under no other TVDB id" -- app/catalog/metadata/plexepisodes.go app/catalog/metadata/plexepisodes_test.go app/catalog/metadata/rpc.go app/catalog/metadata/rpc_test.go
```

---

### Task 5: Episode `status.plexID`

**Files:**
- Modify: `api/catalog/v1alpha1/episode_types.go` (after `TvdbID`, ~line 134), `app/catalog/controller/series/fanout.go` (`DesiredEpisode` struct and `DesiredEpisodes`' append), `app/catalog/controller/series/reconciler.go` (`ensureEpisode`, after the `statusAC := ...WithTvdbID(d.TvdbID)` chain)
- Test: `app/catalog/controller/series/reconciler_test.go`

**Interfaces:**
- Consumes: `metadata.Episode.PlexID` from the episode RPC (Task 4).
- Produces: `EpisodeStatus.PlexID string`, written by `k8s.ManagerCatalogarrSeries`, and kept when a later list omits it.

- [ ] **Step 1: Add the API field** after `TvdbID` in `EpisodeStatus`:

```go
	// PlexID is the episode's id in Plex's own metadata service, without
	// the plex://episode/ prefix; the ui's Plex provider answers the
	// episode with it as plex://episode/<id>. Written beside tvdbID and
	// kept when a later episode list carries none.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{24}$`
	PlexID string `json:"plexID,omitempty"`
```

Run: `make generate manifests`
Expected: `EpisodeStatusApplyConfiguration.WithPlexID` exists, and the episodes CRD has `plexID` with the pattern.

- [ ] **Step 2: Write the failing envtest** in `reconciler_test.go`, beside `TestSeriesEnsureEpisodeProviderFieldRefresh` and using its helpers (`newTestConfig`, `startCacheOnly`, `testNamespace`, `testRootFolder`, `fakeEpisodeRPC`, `combinedBus`, `fakePublisher`):

```go
// The episode list's Plex id lands on the Episode under the Series'
// manager, and a later list without it (Plex failing) keeps it.
func TestSeriesEnsureEpisodeKeepsItsPlexID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("plexid-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("plexid-ns", "tv-root", "/data/media/tv")))

	requester := &fakeEpisodeRPC{episodes: []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "Serenity", Runtime: 42, PlexID: "5d9c127e4eefaa001f6449c2"},
	}}
	r := &series.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: requester}}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "firefly", Namespace: "plexid-ns"},
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 78874, QualityProfileRef: "none", RootFolderRef: "tv-root",
			AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll}},
	}))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "plexid-ns", Name: "firefly"}}
	epKey := types.NamespacedName{Namespace: "plexid-ns", Name: "firefly-s01e01"}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got catalogv1alpha1.Episode
	require.Eventually(t, func() bool {
		return c.Get(ctx, epKey, &got) == nil && got.Status.PlexID == "5d9c127e4eefaa001f6449c2"
	}, 5*time.Second, 10*time.Millisecond, "the Plex id never landed")

	// The next list carries no Plex id, and a changed runtime to wait on.
	requester.episodes = []metadata.Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Serenity", Runtime: 43}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, epKey, &got) == nil && got.Status.RuntimeMinutes == 43
	}, 5*time.Second, 10*time.Millisecond, "the second list never landed")
	require.Equal(t, "5d9c127e4eefaa001f6449c2", got.Status.PlexID, "a list without a Plex id must not release the stored one")

	// managedFields through a direct (uncached) client: caches strip them.
	direct, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	require.NoError(t, direct.Get(ctx, epKey, &got))
	owned := false
	for _, mf := range got.ManagedFields {
		if mf.Manager == k8s.ManagerCatalogarrSeries && mf.FieldsV1 != nil && strings.Contains(string(mf.FieldsV1.Raw), `"f:plexID"`) {
			owned = true
		}
	}
	require.True(t, owned, "status.plexID belongs to %s", k8s.ManagerCatalogarrSeries)
}
```

Check the episode name format against `EpisodeName` in `fanout.go` and the existing test (`refresh-series-s01e01`), and adjust `firefly-s01e01` if it differs. Add `"strings"` and `"sigs.k8s.io/controller-runtime/pkg/client"` to the imports if they are missing.

- [ ] **Step 3: Run it and check it fails.**

Run: `export KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)"; go test ./app/catalog/controller/series/ -run TestSeriesEnsureEpisodeKeepsItsPlexID 2>&1 | tail -8`
Expected: FAIL, "the Plex id never landed".

- [ ] **Step 4: Implement.**
- In `fanout.go`'s `DesiredEpisode` struct, add after `TvdbID int64`:

```go
	// PlexID is the episode's id in Plex's own metadata service, "" when
	// the gateway found none this time.
	PlexID string
```

- In `DesiredEpisodes`' `out = append(out, DesiredEpisode{...})`, add `PlexID: ep.PlexID,` beside `TvdbID: tvdbID,`.
- In `reconciler.go` `ensureEpisode`, directly after the `statusAC := catalogac.EpisodeStatus()...WithTvdbID(d.TvdbID)` statement, add:

```go
	// PlexID is kept when this list carries none: the gateway sets it
	// only when Plex answered, so an empty value means Plex failed or has
	// not been asked, not that the episode lost its id. Sent whenever
	// known, so this manager keeps owning it; never sent empty, which the
	// field's pattern refuses.
	plexID := d.PlexID
	if plexID == "" {
		plexID = ep.Status.PlexID
	}
	if plexID != "" {
		statusAC = statusAC.WithPlexID(plexID)
	}
```

`ep` here is the live Episode (from the `r.Get` above), or the one just created with an empty status.

- [ ] **Step 5: Run the series tests and check they pass.**

Run: `export KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)"; go test ./app/catalog/controller/series/ 2>&1 | tail -3`
Expected: `ok`. That includes `TestSeriesEnsureEpisodeProviderFieldRefresh`, unchanged.

- [ ] **Step 6: Commit.**

```bash
git commit -m "feat(catalog): an Episode records its Plex id in status.plexID beside tvdbID, kept when a later episode list carries none" -- api/catalog/v1alpha1/episode_types.go api/catalog/v1alpha1/zz_generated.deepcopy.go api/applyconfiguration config/crd/bases app/catalog/controller/series/fanout.go app/catalog/controller/series/reconciler.go app/catalog/controller/series/reconciler_test.go
```

---

### Task 6: `projection.Index.ByPlexID`

**Files:**
- Modify: `ui/projection/index.go` (the `Index` struct near line 267, `BuildIndex` maps at ~308, the movie, series and episode loops, a new method after `ByIMDb`)
- Test: `ui/projection/index_test.go`

**Interfaces:**
- Consumes: `MovieMetadata.ExternalIDs["plex"]`, `SeriesMetadata.ExternalIDs["plex"]`, `SeriesMetadata.PlexSeasons` (Task 3), `EpisodeStatus.PlexID` (Task 5).
- Produces: `func (idx *Index) ByPlexID(id string) (uid types.UID, season int32, isSeason, ok bool)`, in `plex.ParseRatingKey`'s shape.

- [ ] **Step 1: Write the failing test** in `index_test.go`, using the file's `testScheme(t)`:

```go
func TestByPlexIDResolvesEveryKindAndRefusesASharedID(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000001"},
		Status: catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{ExternalIDs: map[string]string{"plex": "5d776b83fb0d55001f56a04b"}}},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "firefly", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000002"},
		Status: catalogv1.SeriesStatus{Metadata: &catalogv1.SeriesMetadata{
			ExternalIDs: map[string]string{"plex": "5d9c086c7d06d9001ffd27aa"},
			PlexSeasons: []catalogv1.PlexSeasonRef{{Number: 1, ID: "5d9c09de08fddd001f2afb4c"}},
		}},
	}
	episode := &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "firefly-s01e01", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000003"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "firefly", SeasonNumber: 1, EpisodeNumber: 1},
		Status:     catalogv1.EpisodeStatus{PlexID: "5d9c127e4eefaa001f6449c2"},
	}
	// Two Movies claiming one Plex id: neither is answered.
	dupA := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000004"},
		Status: catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{ExternalIDs: map[string]string{"plex": "5d7768254eefaa001f5d0fa1"}}},
	}
	dupB := dupA.DeepCopy()
	dupB.Name, dupB.UID = "heat-2", "aaaaaaaa-0000-0000-0000-000000000005"

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie, series, episode, dupA, dupB).Build()
	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)

	type want struct {
		uid      types.UID
		season   int32
		isSeason bool
		ok       bool
	}
	for id, w := range map[string]want{
		"5d776b83fb0d55001f56a04b": {uid: movie.UID, ok: true},
		"5d9c086c7d06d9001ffd27aa": {uid: series.UID, ok: true},
		"5d9c09de08fddd001f2afb4c": {uid: series.UID, season: 1, isSeason: true, ok: true},
		"5d9c127e4eefaa001f6449c2": {uid: episode.UID, ok: true},
		"5d7768254eefaa001f5d0fa1": {},
		"000000000000000000000000": {},
	} {
		uid, season, isSeason, ok := idx.ByPlexID(id)
		require.Equal(t, w, want{uid, season, isSeason, ok}, id)
	}
}
```

- [ ] **Step 2: Run it and check it fails.**

Run: `go test ./ui/projection/ -run TestByPlexID`
Expected: FAIL to compile, `idx.ByPlexID undefined`.

- [ ] **Step 3: Implement.** In `index.go`, add the type:

```go
// plexRef is what a Plex id names: an item's UID and, for a season, its
// number.
type plexRef struct {
	uid      types.UID
	season   int32
	isSeason bool
}
```

Add `plexIDs map[string]plexRef` and `plexShared map[string]bool` to the `Index` struct. Initialise both in `BuildIndex` (`plexIDs: map[string]plexRef{}, plexShared: map[string]bool{},`). Add the method:

```go
// addPlexID records what id names; an id two items claim names neither.
func (idx *Index) addPlexID(id string, ref plexRef) {
	if id == "" {
		return
	}
	if prev, ok := idx.plexIDs[id]; ok && prev != ref {
		idx.plexShared[id] = true
	}
	idx.plexIDs[id] = ref
}
```

Call it in the loops of `BuildIndex`:
- **Movies:** inside `if md := m.Status.Metadata; md != nil {`, add `idx.addPlexID(md.ExternalIDs["plex"], plexRef{uid: m.UID})`.
- **Series:** inside `if md := s.Status.Metadata; md != nil {`, add:

```go
			idx.addPlexID(md.ExternalIDs["plex"], plexRef{uid: s.UID})
			for _, ps := range md.PlexSeasons {
				idx.addPlexID(ps.ID, plexRef{uid: s.UID, season: ps.Number, isSeason: true})
			}
```

- **Episodes:** add `idx.addPlexID(ep.Status.PlexID, plexRef{uid: ep.UID})`.

After `ByIMDb`:

```go
// ByPlexID resolves a Plex metadata id -- the 24-hex id of a plex:// GUID,
// which PMS sends in place of the ratingKey once it holds an item under
// that GUID -- to what it names, in plex.ParseRatingKey's shape: a Movie,
// Series or Episode UID, or a Series UID and season number. An id two
// items claim resolves to neither: the provider never guesses.
func (idx *Index) ByPlexID(id string) (uid types.UID, season int32, isSeason, ok bool) {
	ref, found := idx.plexIDs[id]
	if !found || idx.plexShared[id] {
		return "", 0, false, false
	}
	return ref.uid, ref.season, ref.isSeason, true
}
```

- [ ] **Step 4: Run the tests and check they pass.**

Run: `go test ./ui/projection/`
Expected: `ok`.

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(ui): the projection index resolves a Plex id to its movie, show, season or episode, and refuses an id two items claim" -- ui/projection/index.go ui/projection/index_test.go
```

---

### Task 7: The provider answers with `plex://` GUIDs

**Files:**
- Create: `ui/plex/plexguid.go`, `ui/plex/plexguid_test.go`
- Modify:
  - `ui/plex/provider.go:42-64` (`Options`)
  - `ui/plex/extended.go` (`urls` struct, `urlsFor`, `similarGuid` and its call in `enrichExtended`)
  - `ui/plex/metadata.go`: lines 121, 200, 299, 305, 449, 456 and 464, the `GUID(...)` calls in `buildMovieMetadata`, `buildShowMetadata`, `buildSeasonMetadata` and `buildEpisodeMetadata`

**Interfaces:**
- Consumes: the status fields of Tasks 3 and 5.
- Produces:
  - `plex.Options.PlexGUIDs bool`;
  - `func (u urls) guid(identifier, metadataType, ratingKey, plexID string) string`;
  - `moviePlexID`, `seriesPlexID`, `seasonPlexID`, `episodePlexID`.

- [ ] **Step 1: Write the failing tests** in `ui/plex/plexguid_test.go` (package `plex_test`, reusing `fixtureMovie`, `fixtureSeriesAndEpisodes`, `testScheme`, `getJSON`, `postJSON`, `decodeJSON`, `movieUID`, `seriesUID`, `episodeUID`):

```go
/*
<GPL-3.0 header>
*/

package plex_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

const (
	moviePlex   = "5d776b83fb0d55001f56a04b"
	showPlex    = "5d9c086c7d06d9001ffd27aa"
	season1Plex = "5d9c09de08fddd001f2afb4c"
	ep1Plex     = "5d9c127e4eefaa001f6449c2"
)

// plexObjects are the fixtures with Plex ids: the movie, the show, its
// season 1 and its s01e01 -- s01e02 onward and season 2 have none, a
// partly resolved show.
func plexObjects() []client.Object {
	m := fixtureMovie()
	if m.Status.Metadata.ExternalIDs == nil {
		m.Status.Metadata.ExternalIDs = map[string]string{}
	}
	m.Status.Metadata.ExternalIDs["plex"] = moviePlex
	s, eps := fixtureSeriesAndEpisodes()
	s.Status.Metadata.ExternalIDs["plex"] = showPlex
	s.Status.Metadata.PlexSeasons = []catalogv1.PlexSeasonRef{{Number: 1, ID: season1Plex}}
	eps[0].Status.PlexID = ep1Plex
	objs := []client.Object{m, s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	return objs
}

func plexGUIDHandler(t *testing.T, on bool, objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://clustarr.example",
		PlexGUIDs:   on,
		Index:       func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
	})
}

type guids struct {
	RatingKey       string `json:"ratingKey"`
	Guid            string `json:"guid"`
	ParentGuid      string `json:"parentGuid"`
	GrandparentGuid string `json:"grandparentGuid"`
}

func firstGUIDs(t *testing.T, body []byte) guids {
	t.Helper()
	var out struct {
		MediaContainer struct {
			Metadata []guids `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(body, &out))
	require.NotEmpty(t, out.MediaContainer.Metadata)
	return out.MediaContainer.Metadata[0]
}

func TestAMovieIsAnsweredWithItsPlexGUID(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID))
	require.Equal(t, http.StatusOK, rec.Code)
	g := firstGUIDs(t, rec.Body.Bytes())
	require.Equal(t, "plex://movie/"+moviePlex, g.Guid)
	require.Equal(t, string(movieUID), g.RatingKey, "the ratingKey stays clustarr's")

	rec = postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{"type": 1, "title": "Skyfall Protocol", "year": 2015})
	require.Equal(t, "plex://movie/"+moviePlex, firstGUIDs(t, rec.Body.Bytes()).Guid, "a match result too")
}

func TestWithPlexGUIDsOffEveryGUIDIsClustarrs(t *testing.T) {
	h := plexGUIDHandler(t, false, plexObjects()...)
	g := firstGUIDs(t, getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)).Body.Bytes())
	require.Equal(t, plex.GUID(plex.MoviesIdentifier, "movie", string(movieUID)), g.Guid)
}

func TestAnEpisodeAndItsParentsAreAnsweredWithTheirPlexGUIDs(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	g := firstGUIDs(t, getJSON(t, h, "/plex/tv/library/metadata/"+string(episodeUID(1))).Body.Bytes())
	require.Equal(t, "plex://episode/"+ep1Plex, g.Guid)
	require.Equal(t, "plex://season/"+season1Plex, g.ParentGuid)
	require.Equal(t, "plex://show/"+showPlex, g.GrandparentGuid)

	g = firstGUIDs(t, getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"-s01").Body.Bytes())
	require.Equal(t, "plex://season/"+season1Plex, g.Guid)
	require.Equal(t, "plex://show/"+showPlex, g.ParentGuid)
}

// A show only partly known to Plex: an episode and a season without a Plex
// id keep clustarr's GUIDs beside siblings that have theirs.
func TestAPartlyResolvedShowMixesGUIDs(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	g := firstGUIDs(t, getJSON(t, h, "/plex/tv/library/metadata/"+string(episodeUID(4))).Body.Bytes()) // s02e01
	require.Equal(t, plex.GUID(plex.TVIdentifier, "episode", string(episodeUID(4))), g.Guid)
	require.Equal(t, plex.GUID(plex.TVIdentifier, "season", string(seriesUID)+"-s02"), g.ParentGuid)
	require.Equal(t, "plex://show/"+showPlex, g.GrandparentGuid)
}

// A malformed Plex id in status (hand-edited, uppercase, short) is never
// published: the item keeps clustarr's own GUID.
func TestAMalformedPlexIDKeepsClustarrsGUID(t *testing.T) {
	m := fixtureMovie()
	m.Status.Metadata.ExternalIDs = map[string]string{"plex": "5D776B83FB0D55001F56A04B"}
	h := plexGUIDHandler(t, true, m)
	g := firstGUIDs(t, getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)).Body.Bytes())
	require.Equal(t, plex.GUID(plex.MoviesIdentifier, "movie", string(movieUID)), g.Guid)
}
```

Check two assumptions against `fixtures_test.go`: that `episodeUID(4)` is s02e01 (the loop numbers season 1 episodes 1-3 first), and that `fixtureMovie()`'s title and year are "Skyfall Protocol" / 2015.

- [ ] **Step 2: Run them and check they fail.**

Run: `go test ./ui/plex/ -run 'PlexGUID|PartlyResolved|MalformedPlexID'`
Expected: FAIL to compile, `unknown field PlexGUIDs in struct literal of type plex.Options`.

- [ ] **Step 3: Implement.** Add to `plex.Options` (`provider.go`):

```go
	// PlexGUIDs answers a movie, show, season or episode Plex knows with
	// its plex:// GUID instead of clustarr's own (--plex-guids), so Plex
	// Web offers Watchlist and its other Discover features for it
	// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md).
	// ratingKey and key stay clustarr's either way.
	PlexGUIDs bool
```

In `extended.go`:
- add `plexGUIDs bool` to the `urls` struct, commented `// plexGUIDs is Options.PlexGUIDs.`;
- in `urlsFor`, add `plexGUIDs: h.opts.PlexGUIDs,`.

Create `ui/plex/plexguid.go`:

```go
/*
<GPL-3.0 header>
*/

package plex

import (
	"regexp"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// plexScheme is the scheme of Plex's own GUIDs, and the externalIDs key
// the metadata gateway records an item's Plex id under.
const plexScheme = "plex"

// plexIDPattern is a Plex metadata id: 24 lowercase hex digits. It can
// never be a clustarr ratingKey, a 36-character UUID ([ratingKeyPattern]).
var plexIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// guid is the guid an item is answered with: plex://<type>/<plexID> when
// the provider answers with Plex's GUIDs and plexID is a well-formed Plex
// id, else clustarr's own [GUID]. Plex Web offers Watchlist only for an
// item whose own guid is plex://, and PMS 1.43.4 accepts one from a custom
// provider while still reading the item from it (spec §2).
func (u urls) guid(identifier, metadataType, ratingKey, plexID string) string {
	if u.plexGUIDs && plexIDPattern.MatchString(plexID) {
		return plexScheme + "://" + metadataType + "/" + plexID
	}
	return GUID(identifier, metadataType, ratingKey)
}

func moviePlexID(m *catalogv1.Movie) string {
	if md := m.Status.Metadata; md != nil {
		return md.ExternalIDs[plexScheme]
	}
	return ""
}

func seriesPlexID(s *catalogv1.Series) string {
	if md := s.Status.Metadata; md != nil {
		return md.ExternalIDs[plexScheme]
	}
	return ""
}

func seasonPlexID(s *catalogv1.Series, number int32) string {
	if md := s.Status.Metadata; md != nil {
		for _, p := range md.PlexSeasons {
			if p.Number == number {
				return p.ID
			}
		}
	}
	return ""
}

func episodePlexID(e *catalogv1.Episode) string { return e.Status.PlexID }
```

In `metadata.go`, replace each GUID call:
- **`buildMovieMetadata`:** `Guid: u.guid(root.identifier, metadataTypeMovie, key, moviePlexID(m)),`
- **`buildShowMetadata`:** `Guid: u.guid(root.identifier, metadataTypeShow, key, seriesPlexID(s)),`
- **`buildSeasonMetadata`:**
  - `Guid: u.guid(root.identifier, metadataTypeSeason, key, seasonPlexID(s, number)),`
  - `ParentGuid: u.guid(root.identifier, metadataTypeShow, seriesKey, seriesPlexID(s)),`
- **`buildEpisodeMetadata`:**
  - `Guid: u.guid(root.identifier, metadataTypeEpisode, key, episodePlexID(e)),`
  - `ParentGuid: u.guid(root.identifier, metadataTypeSeason, seasonKey, seasonPlexID(s, e.Spec.SeasonNumber)),`
  - `GrandparentGuid: u.guid(root.identifier, metadataTypeShow, seriesKey, seriesPlexID(s)),`

In `extended.go`, change `similarGuid` to take `u urls` first and use the Plex id of an item in the catalog:

```go
func similarGuid(u urls, s extended.Similar, idx *projection.Index) string {
	if s.TmdbID != 0 {
		if obj, ok := idx.ByTMDB(commonv1.MediaKindMovie, s.TmdbID); ok {
			if m, ok := obj.(*catalogv1.Movie); ok {
				return u.guid(moviesRoot.identifier, metadataTypeMovie, RatingKey(m.UID), moviePlexID(m))
			}
		}
		return "tmdb://" + strconv.FormatInt(s.TmdbID, 10)
	}
	if s.TvdbID != 0 {
		if sr, ok := idx.ByTVDB(s.TvdbID); ok {
			return u.guid(tvRoot.identifier, metadataTypeShow, RatingKey(sr.UID), seriesPlexID(sr))
		}
		return "tvdb://" + strconv.FormatInt(s.TvdbID, 10)
	}
	return ""
}
```

Update its caller in `enrichExtended` to `similarGuid(u, s, idx)`. Update the doc comment to say "clustarr's own, or its plex:// GUID, when the title is in the catalog". Add the `catalogv1` import to `extended.go` if it is missing.

Run: `grep -n 'GUID(' ui/plex/*.go | grep -v _test`
Expected: only `ratingkey.go`'s definition and `plexguid.go`'s fallback remain.

- [ ] **Step 4: Run every provider test and check they pass.**

Run: `go test ./ui/plex/ 2>&1 | tail -3`
Expected: `ok`. The existing goldens are unchanged, because `newTestHandler` leaves `PlexGUIDs` false; that is the flag-off byte-identity check.

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(ui): the Plex provider answers a movie, show, season or episode Plex knows with its plex:// GUID (Options.PlexGUIDs), ratingKey and key staying clustarr's, so Plex Web offers Watchlist" -- ui/plex/plexguid.go ui/plex/plexguid_test.go ui/plex/provider.go ui/plex/extended.go ui/plex/metadata.go
```

---

### Task 8: The provider resolves Plex ids PMS sends back

**Files:**
- Modify:
  - `ui/plex/plexguid.go`: add `resolveKey`
  - `ui/plex/metadata.go:526` (`resolveMetadata`), `ui/plex/children.go:62` (`handleGrandchildren`), `ui/plex/children.go:93` (`childrenOf`), `ui/plex/images.go:173` (`resolveArtwork`, which also gets `idx` already)
  - `ui/plex/match.go`: `showByGuid`, `movieByGuid`
- Test: `ui/plex/plexguid_test.go`

**Interfaces:**
- Consumes: `projection.Index.ByPlexID` (Task 6).
- Produces: `func resolveKey(idx *projection.Index, key string) (uid types.UID, season int32, isSeason, ok bool)`.

- [ ] **Step 1: Write the failing tests** (append to `plexguid_test.go`):

```go
// PMS fetches an item it holds under a plex:// GUID by the Plex id (spike
// 3: GET .../library/metadata/5d776b83fb0d55001f56a04b). Every ratingKey
// route resolves one, with the flag on or off.
func TestEveryRouteResolvesAPlexID(t *testing.T) {
	for _, on := range []bool{true, false} {
		h := plexGUIDHandler(t, on, plexObjects()...)
		for _, path := range []string{
			"/plex/movies/library/metadata/" + moviePlex,
			"/plex/movies/library/metadata/" + moviePlex + "/images",
			"/plex/tv/library/metadata/" + showPlex,
			"/plex/tv/library/metadata/" + showPlex + "/children",
			"/plex/tv/library/metadata/" + showPlex + "/grandchildren",
			"/plex/tv/library/metadata/" + season1Plex,
			"/plex/tv/library/metadata/" + season1Plex + "/children",
			"/plex/tv/library/metadata/" + ep1Plex,
		} {
			rec := getJSON(t, h, path)
			require.Equal(t, http.StatusOK, rec.Code, "%s (plexGUIDs=%t): %s", path, on, rec.Body.String())
		}
		g := firstGUIDs(t, getJSON(t, h, "/plex/movies/library/metadata/"+moviePlex).Body.Bytes())
		require.Equal(t, string(movieUID), g.RatingKey)
	}
}

// A Plex id names one type, and only the root declaring it answers, as
// with a clustarr ratingKey.
func TestThePlexIDOfAnotherRootsTypeIsNotFound(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	for _, path := range []string{
		"/plex/movies/library/metadata/" + showPlex,
		"/plex/movies/library/metadata/" + ep1Plex,
		"/plex/tv/library/metadata/" + moviePlex,
		"/plex/tv/library/metadata/" + moviePlex + "/children",
		"/plex/tv/library/metadata/000000000000000000000000",
	} {
		require.Equal(t, http.StatusNotFound, getJSON(t, h, path).Code, path)
	}
}

func TestAMatchByAPlexGUIDHint(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{"type": 1, "guid": "plex://movie/" + moviePlex})
	require.Equal(t, string(movieUID), firstGUIDs(t, rec.Body.Bytes()).RatingKey)
	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{"type": 2, "guid": "plex://show/" + showPlex})
	require.Equal(t, string(seriesUID), firstGUIDs(t, rec.Body.Bytes()).RatingKey)
	// A show's id hinted to the movies root matches nothing by guid.
	rec = postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{"type": 1, "guid": "plex://show/" + showPlex})
	var out struct {
		MediaContainer struct{ Metadata []guids } `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	require.Empty(t, out.MediaContainer.Metadata)
}
```

Before running, check the images route's success status: read `handleImages` (`images.go:142`). It may answer 200 with an empty `Image` list for a fixture without artwork, which is what the test expects. If it answers 404 for no artwork, drop the `/images` paths and add an `artwork` entry to the fixture instead. Do not weaken the 200 assertion.

- [ ] **Step 2: Run them and check they fail.**

Run: `go test ./ui/plex/ -run 'EveryRouteResolvesAPlexID|AnotherRootsType|MatchByAPlexGUIDHint' 2>&1 | tail -10`
Expected: FAIL, `/plex/movies/library/metadata/5d776b83fb0d55001f56a04b (plexGUIDs=true)` answers 404.

- [ ] **Step 3: Implement.** Append to `plexguid.go` (and import `k8s.io/apimachinery/pkg/types` and `github.com/mediactl/clustarr/ui/projection`):

```go
// resolveKey parses a ratingKey route's path value: clustarr's own
// ratingKey first ([ParseRatingKey]), then a Plex id, which PMS sends in
// place of the ratingKey once it holds an item under its plex:// GUID (it
// takes the id from the GUID; spec §2). It resolves with --plex-guids off
// too, so items PMS already holds that way keep refreshing.
func resolveKey(idx *projection.Index, key string) (uid types.UID, season int32, isSeason, ok bool) {
	if uid, season, isSeason, ok = ParseRatingKey(key); ok {
		return uid, season, isSeason, ok
	}
	if !plexIDPattern.MatchString(key) {
		return "", 0, false, false
	}
	return idx.ByPlexID(key)
}

// byPlexGUID resolves a plex://<metadataType>/<id> match hint's id part
// ("movie/<id>", as splitGuid leaves it) to a UID, false for another type.
func byPlexGUID(idx *projection.Index, metadataType, rest string) (types.UID, bool) {
	id, ok := strings.CutPrefix(rest, metadataType+"/")
	if !ok || !plexIDPattern.MatchString(id) {
		return "", false
	}
	uid, _, isSeason, ok := idx.ByPlexID(id)
	return uid, ok && !isSeason
}
```

Also import `strings`. Replace `ParseRatingKey(` with `resolveKey(idx, ` in the four route functions:
- `metadata.go` `resolveMetadata`: `uid, season, isSeason, ok := resolveKey(idx, ratingKey)`;
- `children.go` `handleGrandchildren`: `resolveKey(idx, r.PathValue("ratingKey"))`;
- `children.go` `childrenOf`: `resolveKey(idx, ratingKey)`;
- `images.go` `resolveArtwork`: `resolveKey(idx, ratingKey)`.

The type checks after each (`root.declares`, `SeriesByUID`, `ByUID`) already refuse another root's type.

In `match.go`, add a `case "plex":` to both resolvers:

```go
	// showByGuid:
	case plexScheme:
		uid, ok := byPlexGUID(idx, metadataTypeShow, id)
		if !ok {
			return nil, false
		}
		return idx.SeriesByUID(uid)
```

```go
	// movieByGuid:
	case plexScheme:
		uid, ok := byPlexGUID(idx, metadataTypeMovie, id)
		if !ok {
			return nil, false
		}
		return idx.MovieByUID(uid)
```

Then extend both functions' doc comments to name `plex://`.

- [ ] **Step 4: Run every provider test and check they pass.**

Run: `go test ./ui/plex/ ./ui/... 2>&1 | tail -4`
Expected: `ok` for every package.

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(ui): every Plex provider route resolves a Plex id PMS sends back for an item it holds under its plex:// GUID, as does a plex:// match hint, only on the root declaring its type" -- ui/plex/plexguid.go ui/plex/plexguid_test.go ui/plex/metadata.go ui/plex/children.go ui/plex/images.go ui/plex/match.go
```

---

### Task 9: `--plex-guids` in both commands and both installers

**Files:**
- Modify:
  - `ui/server.go:80` (`PlexOptions`), `ui/routes.go:97`
  - `cmd/clustarr/services.go` (`buildUIPlexOptions`, `newUICommand`), `cmd/clustarr/all.go` (`uiArgs`, `allServices`, the `all` command's flags and its `uiArgs{...}`)
  - `charts/clustarr/values.yaml` (`ui.plex`), `charts/clustarr/values.schema.json` (`ui.plex`), `charts/clustarr/templates/deployments.yaml:282`, `config/manager/ui.yaml:89`
- Test: `cmd/clustarr/ui_options_wiring_test.go`

**Interfaces:**
- Consumes: `plex.Options.PlexGUIDs` (Task 7).
- Produces:
  - `ui.PlexOptions.PlexGUIDs bool`;
  - flag `--plex-guids` (default `true`) on `clustarr ui` and `clustarr all`;
  - chart value `ui.plex.plexGuids` (default `true`).

- [ ] **Step 1: Write the failing test** (append to `ui_options_wiring_test.go`):

```go
// --plex-guids reaches the Plex provider from both commands, on by
// default.
func TestBothUICommandsPassPlexGUIDs(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"}, true},
		{[]string{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous", "--plex-guids=false"}, false},
		{[]string{"all", "--ui-auth-mode", "anonymous"}, true},
		{[]string{"all", "--ui-auth-mode", "anonymous", "--plex-guids=false"}, false},
	} {
		o := captureUIOptions(t, tc.argv...)
		require.NotNil(t, o.Plex, "%v", tc.argv)
		require.Equal(t, tc.want, o.Plex.PlexGUIDs, "%v", tc.argv)
	}
}
```

- [ ] **Step 2: Run it and check it fails.**

Run: `go test ./cmd/clustarr/ -run TestBothUICommandsPassPlexGUIDs`
Expected: FAIL to compile, `o.Plex.PlexGUIDs undefined`.

- [ ] **Step 3: Implement the Go wiring.**

- `ui/server.go` `PlexOptions`: add

```go
	// PlexGUIDs answers items Plex knows with their plex:// GUIDs
	// (`--plex-guids`, plex.Options.PlexGUIDs).
	PlexGUIDs bool
```

- `ui/routes.go`: add `PlexGUIDs: s.opts.Plex.PlexGUIDs,` to the `plex.Options{...}` literal.
- `cmd/clustarr/services.go`: change `buildUIPlexOptions` to `func buildUIPlexOptions(enabled bool, externalURL string, plexGUIDs bool) *ui.PlexOptions`, returning `&ui.PlexOptions{ExternalURL: externalURL, PlexGUIDs: plexGUIDs}`, and mention `--plex-guids` in its doc comment. In `newUICommand`, add `var plexGUIDs bool` and register:

```go
	cmd.Flags().BoolVar(&plexGUIDs, "plex-guids", true,
		"Answer the movies, shows, seasons and episodes Plex knows with their plex:// GUIDs instead of "+
			"clustarr's own, so Plex Web offers Watchlist; ids come from a plex MetadataProvider. False "+
			"goes back to clustarr's GUIDs for new matches.")
```

  Change the call to `buildUIPlexOptions(plexProvider, externalURL, plexGUIDs)`.
- `cmd/clustarr/all.go`:
  - add `plexGUIDs bool` to `uiArgs`;
  - make the default `uiArgs{provider: true, plexGUIDs: true, pipelineHistory: projection.DefaultPipelineHistory}`;
  - add `var uiPlexGUIDs bool` and the same `--plex-guids` flag (default `true`, same help text) beside `--plex-provider`;
  - pass `plexGUIDs: uiPlexGUIDs` in the `uiArgs{...}` the command builds;
  - change the `buildUIPlexOptions(plex.provider, plex.externalURL)` call inside `allServices` to pass `plex.plexGUIDs`. Find it with `grep -n buildUIPlexOptions cmd/clustarr/*.go`.

- [ ] **Step 4: Run the command tests and check they pass.**

Run: `go test ./cmd/clustarr/ -run 'TestBothUICommands' 2>&1 | tail -3`
Expected: `ok`.

- [ ] **Step 5: Add it to both installers.**
- `charts/clustarr/templates/deployments.yaml:282`: add `(printf "--plex-guids=%t" .Values.ui.plex.plexGuids)` to the `$uiArgs` list, right after the `--plex-provider` element.
- `config/manager/ui.yaml:89`: make the args `["ui", "--auth-mode=anonymous", "--plex-provider=true", "--plex-guids=true", "--pipeline-history=100"]`, and add `--plex-guids` to the comment above it ("named explicitly here so the flag is visible").
- `charts/clustarr/values.schema.json` `ui.plex`:
  - `"required": ["enabled", "externalURL", "plexGuids"]`;
  - add `"plexGuids": {"type": "boolean", "description": "--plex-guids: answer items Plex knows with their plex:// GUIDs, so Plex Web offers Watchlist."}`.
- `charts/clustarr/values.yaml` `ui.plex`: add `plexGuids: true`, and to the block comment above it:

```yaml
  # plexGuids maps to --plex-guids: items a plex MetadataProvider found in
  # Plex's own metadata service are answered with their plex:// GUIDs, which
  # Plex Web needs to offer Watchlist (docs/superpowers/specs/
  # 2026-10-06-plex-native-guids-design.md). false answers clustarr's own
  # GUIDs for new matches; items PMS already holds by plex:// keep resolving.
```

Run: `make test 2>&1 | tail -5` (or at least `go test ./cmd/clustarr/` with `helm dependency build charts/clustarr` done)
Expected: `TestChartAndKustomizeAgreePerComponent` and the chart schema tests pass.

- [ ] **Step 6: Commit, leaving the other session's `values.yaml` hunk out.** Commit the files that are wholly yours with a pathspec. Commit your `values.yaml` hunk through a temporary index:

```bash
git commit -m "feat(ui): --plex-guids (chart ui.plex.plexGuids, on by default) switches the Plex provider's plex:// GUIDs in both commands and both installers" -- ui/server.go ui/routes.go cmd/clustarr/services.go cmd/clustarr/all.go cmd/clustarr/ui_options_wiring_test.go charts/clustarr/values.schema.json charts/clustarr/templates/deployments.yaml config/manager/ui.yaml
# values.yaml: HEAD's blob plus only the plexGuids hunk, through a temporary index
git show HEAD:charts/clustarr/values.yaml > /tmp/claude-1000/values.head.yaml
# apply ONLY the plexGuids line and its comment to /tmp/claude-1000/values.head.yaml with the Edit tool, then:
blob=$(git hash-object -w /tmp/claude-1000/values.head.yaml)
export GIT_INDEX_FILE=/tmp/claude-1000/plexguids.index
git read-tree HEAD && git update-index --cacheinfo 100644,"$blob",charts/clustarr/values.yaml
tree=$(git write-tree); unset GIT_INDEX_FILE
old=$(git rev-parse HEAD)
new=$(git commit-tree "$tree" -p "$old" -m "chore(chart): ui.plex.plexGuids defaults true")
git update-ref refs/heads/main "$new" "$old"
git diff HEAD -- charts/clustarr/values.yaml   # only the other session's hunk remains
rm -f /tmp/claude-1000/plexguids.index /tmp/claude-1000/values.head.yaml
```

If `values.yaml` no longer shows another session's change (`git diff --cached -- charts/clustarr/values.yaml` empty, and `git diff -- charts/clustarr/values.yaml` showing only your hunk), commit it with a plain pathspec instead.

---

### Task 10: Docs, then the live proof

**Files:**
- Modify: `CLAUDE.md` (the UI section's Plex provider paragraphs), `docs/research/plex-metadata-provider.md` (§6, §10), `docs/superpowers/specs/2026-10-06-plex-native-guids-design.md` (an "As built" section)

**Interfaces:**
- Consumes: everything above, deployed.

- [ ] **Step 1: Document.**

In `CLAUDE.md`, after the paragraph ending "…so the provider never guesses between two items.", add:

```markdown
The provider answers items Plex knows with Plex's own GUIDs (2026-10-06,
`docs/superpowers/specs/2026-10-06-plex-native-guids-design.md`):
- Plex Web offers Watchlist and Discover only for an item whose own `guid`
  is `plex://`. PMS 1.43.4 accepts one from a custom provider and keeps
  reading the item from it, fetching by the GUID's id
  (`/library/metadata/<24 hex>`).
- A keyed `plex` MetadataProvider (secretRef key `token`) resolves the ids
  from `metadata.provider.plex.tv` (`pkg/metadata/clients/plex`):
  - Movie and Series: `status.metadata.externalIDs["plex"]`, through the
    gateway's resolvers;
  - seasons: `status.metadata.plexSeasons`;
  - episodes: Episode `status.plexID`, joined by TVDB episode id in the
    gateway's episode list and written by the Series reconciler. It is kept
    when a later list carries none.
- `ui/plex` builds `guid`, `parentGuid` and `grandparentGuid` from them
  (`--plex-guids`, chart `ui.plex.plexGuids`, on by default). `ratingKey`
  and `key` stay clustarr's.
- Every ratingKey route also resolves a Plex id through
  `projection.Index.ByPlexID`, which refuses an id two items claim. It does
  so with the flag off too.
- This rests on PMS not enforcing its documented rule that a provider's
  GUID starts with its identifier; `--plex-guids=false` is the way back.
```

In `docs/research/plex-metadata-provider.md` §6, after "External ids in `Guid[].id`…", add:

```markdown
**As found (2026-10-06, PMS 1.43.4):** the identifier rule above is not
enforced. A provider's match answered with `plex://movie/<id>` was applied
by Fix Match, and PMS then fetched the item from the provider as
`GET {metadata key}/<id>`, the id taken from the GUID. A `plex://` entry in
`Guid[]` alone does not make Plex Web offer Watchlist; the item's own
`guid` must be `plex://` (clustarr
`docs/superpowers/specs/2026-10-06-plex-native-guids-design.md` §2).
```

Commit: `git commit -m "docs: Plex-native GUIDs in CLAUDE.md and the Plex provider research note" -- CLAUDE.md docs/research/plex-metadata-provider.md`

Spec §7's test is already in the tree: `TestSyncFindsAHandAddedMovieByItsTmdbIDUnderAnotherTitle` (`app/import/worker/importlist/ownership_test.go`). A watchlisted movie the library already holds is found by its TMDB id, and the list creates nothing. Confirm it passes in the gate run below rather than writing a second copy.

- [ ] **Step 2: Gate and deploy.**
- Gate: `make test` at HEAD in a clean worktree (`git worktree add --detach ../clustarr-gate-plexguids HEAD`, copy `charts/clustarr/charts/*.tgz` in, `make test > /tmp/claude-1000/gate.txt 2>&1; tail -5`). Expected: exit 0. A failure in another session's package is reported by name, not ignored.
- Push.
- Build the controller image: `make docker-build IMG=ghcr.io/mediactl/clustarr:<sha7>` builds all three images. Only the controller image changed, so build it alone with `docker build -f images/Dockerfile.controller -t ghcr.io/mediactl/clustarr:<sha7> .`.
- Load it: `kind load docker-image ghcr.io/mediactl/clustarr:<sha7> --name cluster-plex`, then wait for `kubectl --context kind-cluster-plex get --raw=/readyz` to succeed (the load stalls etcd).
- Upgrade:
  1. Refresh values: `helm --kube-context kind-cluster-plex -n clustarr-system get values clustarr -o yaml > /tmp/claude-1000/values-plexguids.yaml`.
  2. Set `image.tag` to `<sha7>` in that file.
  3. Run `helm upgrade clustarr <worktree>/charts/clustarr -n clustarr-system --kube-context kind-cluster-plex -f /tmp/claude-1000/values-plexguids.yaml`.
  4. Apply the new CRDs first: `kubectl --context kind-cluster-plex apply --server-side -f config/crd/bases/`.

- [ ] **Step 3: Enable the provider and watch the ids land.**

```bash
kubectl --context kind-cluster-plex -n clustarr-system apply -f - <<'EOF'
apiVersion: catalog.clustarr.io/v1alpha1
kind: MetadataProvider
metadata: {name: plex, namespace: clustarr-system}
spec: {type: plex, enabled: true, secretRef: {name: plex-token}}
EOF
kubectl --context kind-cluster-plex -n clustarr-system wait metadataprovider/plex --for=condition=Ready --timeout=120s
```

Expected: `condition met`. Then, after the SchemaVersion-2 refresh reaches them, check these (poll; it can take tens of minutes for the whole library):
- `kubectl … get movie -o json | jq -r '.items[] | select(.status.metadata.title=="Arrival") | .status.metadata.externalIDs.plex'` is `5d776b83fb0d55001f56a04b`;
- a series' `status.metadata.plexSeasons` is non-empty;
- its episodes carry `status.plexID`.

Record one show where some episodes have no `plexID`.

- [ ] **Step 4: Prove it in Plex.**
- Answer the spec §6 question: does Refresh Metadata alone move an existing item onto its `plex://` GUID?
  - Run `PUT /library/metadata/66/refresh` on Arrival (PMS at `http://172.19.0.4:32400`), with the token from the `plex-token` Secret in a header, never printed.
  - Read `metadata_items.guid` from Plex's Postgres (`kubectl … -n media exec plex-postgres-… -- psql -U plex -d plex -Atc "select guid from metadata_items where id=66"`).
- Do the same for one show, one season and one episode of it, and for the partly resolved show.
- If a refresh does not switch an item, Fix Match it (`PUT /library/metadata/<id>/match?guid=plex://…`), as the spike did. Record that the fallback is needed.
- Confirm in Plex Web that **Add to Watchlist** shows on the movie and the show. Use the spike's method: a headless browser through a local proxy that serves the token to the page same-origin, and the token never printed.

Expected: `plex://` GUIDs in Plex's database, metadata still clustarr's (tagline and ratings match `status.metadata`), and Watchlist visible.

- [ ] **Step 5: Prove the switch.**
1. Upgrade with `ui.plex.plexGuids: false`.
2. A match for Arrival (`GET /library/metadata/66/matches?manual=1&title=Arrival&year=2016&agent=tv.plex.agents.custom.clustarr.movies`) now offers clustarr's GUID.
3. `GET http://<ui>/plex/movies/library/metadata/5d776b83fb0d55001f56a04b` still answers 200.
4. Upgrade back to `true`.

- [ ] **Step 6: Record "As built".** Append to the spec a section `## As built (2026-10-06)` stating:
- the commits;
- the §6 answer (refresh moved items or not, and whether the Fix Match fallback was used);
- the partly resolved show's name and counts;
- that Watchlist was seen.

Commit it with a pathspec, then push.
