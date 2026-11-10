# Plex Collections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Plex collections (smart and normal, with their images, settings and visibility) declared as `PlexCollection` resources, which clustarr's UI edits and cluster-plex reconciles into Plex.

**Architecture:**

- clustarr owns the API (`catalog.clustarr.io/v1alpha1 PlexCollection`), a static filter catalogue (`pkg/plexfilter`) and the UI's Collections page.
- cluster-plex reconciles each resource into Plex through PMS's HTTP API:
  - it compiles a smart filter into Plex Web's URI;
  - it resolves a normal collection's members to Plex items by GUID;
  - it creates, adopts and syncs the collection, and applies its presentation;
  - it alone writes the resource's status and finalizer (option A).

**Tech Stack:** Go; controller-gen CRDs with CEL; templ, htmx and shadcn-templ for the UI; client-go dynamic informers and server-side apply in cluster-plex; PMS 1.43.5's HTTP API.

**Spec:** `docs/superpowers/specs/2026-11-10-plex-collections-design.md` (clustarr). Read it first; this plan argues from it.

## Global Constraints

- Repos: clustarr `/home/appkins/src/mediactl/clustarr` (tasks 1-5), cluster-plex `/home/appkins/src/mediactl/cluster-plex` (tasks 6-11), both (task 12).
- No envtests, written or run, and no `make test` (owner, 2026-10-08). Gates: `go build ./...`, `go vet` of the touched packages, `make brand-gate` (clustarr), and plain `go test` of the touched packages with `KUBEBUILDER_ASSETS` unset.
- Never `go get` or `go mod tidy`; no new module dependencies are needed. cluster-plex does not import clustarr.
- Commits through a pathspec (`git add <new files>` first, then `git commit -m ... -- <paths>`). Never `git stash`, never a worktree, never push.
- Every clustarr Go file carries the GPL header from `hack/boilerplate.go.txt`; cluster-plex files follow that repo's own header convention.
- Format templates with `go run github.com/a-h/templ/cmd/templ@v0.3.1020 fmt <file>`, then `make templ`; `make css` after new Tailwind classes.
- UI controls are shadcn-templ components only (no raw `<input>`, `<select>`, `<button>`, `<label>`, `<textarea>`). Load the `shadcn-templ` project skill before choosing one. Check every UI change in a browser.
- Plex URIs sent to PMS are always `server://<machine id>/com.plexapp.plugins.library/...`, never `library://`.
- cluster-plex's field manager is `cluster-plex`; its finalizer is `clusterplex.io/plex-collection`.
- Every optional spec field left out means "keep Plex's": the reconciler never writes a field the spec doesn't state.
- Never print the Plex token. Read it from Secret `clustarr-system/plex-token` (key `token`) into an environment variable, and port-forward on a private local port (never 32499).
- Don't touch the kind rig until clustarr-48 reports "done". Deploy to talos only on the owner's word, announced to the peer sessions first.

## Review Focus

1. **Plex unreachable or still starting** (connection refused, 503, 400 "still starting up"). The status reads `PlexUnavailable`, nothing is created or deleted, and the sync retries. A failed listing must never read as "no collection" and create a duplicate. Test: task 8, `TestSyncNeverCreatesWhenListingFails`.
2. **A member Plex doesn't hold** (not scanned, unmatched, or deleted from clustarr). It is counted in `missingMembers` and listed in `missing`, never added as anything else, and the condition reads `MembersPending`. A normal collection with no member in Plex yet isn't created empty (`Waiting`), and removal never empties one. Tests: task 9, `TestMembersPlexLacksAreReportedAndAddedLater` and `TestMembersNeverEmptyACollection`.
3. **An image URL that fails** (404, timeout, redirect loop, over 10 MiB, not an image). The status reads `ImageUnavailable`, the current image stays, and the filter, members and settings still sync. Test: task 10, `TestImageFailureLeavesTheImageAndSyncsTheRest`.
4. **A metadata-made collection with the same title** (a TMDB collection clustarr's provider gives Plex, GUID not `collection://`). It is never adopted, edited or deleted, and the resource reads `NotAdoptable`. Test: task 8, `TestSyncNeverAdoptsAMetadataCollection`.
5. **Special characters in a string value** (`Tom & Jerry`, `Pokémon`, `50% Off`, `a=b`). The value is percent-encoded so it can't break the `push`/`pop` query, and it decodes back unchanged. Test: task 7, `TestCompileEncodesSpecialCharacters`.

---

### Task 1: The PlexCollection API

**Files:**

- Create: `api/catalog/v1alpha1/plexcollection_types.go`
- Modify: `config/crd/kustomization.yaml` (add `bases/catalog.clustarr.io_plexcollections.yaml`), `CLAUDE.md`
- Generated (by `make generate manifests`): `zz_generated.deepcopy.go`, `api/applyconfiguration/catalog/...`, `config/crd/bases/catalog.clustarr.io_plexcollections.yaml`

**Interfaces:**

- Produces (Go, package `v1alpha1`):
  - Enums:
    - `PlexItemType`: `movie`, `show`, `season`, `episode`.
    - `PlexMatch`: `all`, `any`.
    - `PlexOperator`: `is`, `isNot`, `contains`, `doesNotContain`, `beginsWith`, `endsWith`, `greaterThan`, `lessThan`, `before`, `after`, `isTrue`, `isFalse`.
    - `PlexCollectionMode`: `default`, `hideCollection`, `hideItems`, `showCollectionAndItems`.
    - `PlexCollectionOrder`: `releaseDate`, `alphabetical`.
  - Filter types:
    - `PlexCondition { Field string; Operator PlexOperator; Value string }`
    - `PlexFilterRule { Field string; Operator PlexOperator; Value string; Match PlexMatch; Rules []PlexCondition }` (all `omitempty`)
    - `PlexFilter { Match PlexMatch; Rules []PlexFilterRule }`
    - `PlexSort { Field string; Descending bool }`
  - Normal and presentation types:
    - `PlexMember { Kind string /* Movie | Series */; Name string }`
    - `PlexMembers { Items []PlexMember }` (an object so a later `Selector` can sit beside `Items`)
    - `PlexImage { URL string }`
    - `PlexCollectionSettings { Mode PlexCollectionMode; Order PlexCollectionOrder }` (both `omitempty`)
    - `PlexVisibility { LibraryRecommended, Home, FriendsHome bool }` (no `omitempty`; all three are sent together)
  - `PlexCollectionSpec { Library, Title string; ItemType PlexItemType; Filter *PlexFilter; Sort *PlexSort; Limit *int32; Members *PlexMembers; Summary, SortTitle *string; Poster, Background *PlexImage; Settings *PlexCollectionSettings; Visibility *PlexVisibility }`
  - Status types:
    - `PlexImageStatus { URL, SHA1 string; FetchedAt metav1.Time }`
    - `PlexCollectionStatus { ObservedGeneration int64; RatingKey string; Adopted bool; Content string; ItemCount, MissingMembers int32; Missing []PlexMember; Poster, Background *PlexImageStatus; LastSyncedAt *metav1.Time; Conditions []metav1.Condition }`
  - `PlexCollection`, `PlexCollectionList`.
  - Condition type `PlexCollectionConditionSynced = "Synced"`. Reasons: `Synced`, `Created`, `Adopted`, `MembersPending`, `Waiting`, `InvalidRule`, `UnknownValue`, `LibraryNotFound`, `NotSmart`, `NotAdoptable`, `Conflict`, `ImageUnavailable`, `PlexUnavailable`. `Synced` is `True` for the first four, `False` for the rest.
  - Constants: `PlexCollectionFinalizer = "clusterplex.io/plex-collection"`, `PlexCollectionStatusManager = "cluster-plex"`.
  - The resource `plexcollections`, short name `pcol`, categories `clustarr;catalog`, namespaced, with a status subresource.

- [ ] **Step 1: Write the types with their markers.**
  - Caps: filter rules `MinItems=1`, `MaxItems=32` at both levels; `Members.Items` `MaxItems=1000`; `Missing` `MaxItems=20`; `Conditions` `MaxItems=8`.
  - `Field`: pattern `^[a-zA-Z]+(\.[a-zA-Z]+)?$`.
  - `Limit`: minimum 1. `Library` and `Title`: `MinLength=1`.
  - `PlexImage.URL`: pattern `^https?://`.
  - `PlexMember.Kind`: enum `Movie`, `Series`.
  - `Library` and `ItemType`: immutable (`self == oldSelf`).

  CEL on the spec:
  - `has(self.filter) != has(self.members)`;
  - `has(self.filter) == has(oldSelf.filter)` (transition rule);
  - `!has(self.members) || self.itemType in ['movie','show']`;
  - `!has(self.sort) && !has(self.limit) || has(self.filter)`;
  - `!has(self.settings) || !has(self.settings.order) || has(self.members)`;
  - members' kinds agree with `itemType` (`movie` ⇒ every `members.items` kind is `Movie`, `show` ⇒ every one is `Series`).

  CEL on `PlexFilterRule`: `has(self.field) != has(self.match)`; `!has(self.field) || has(self.operator)`; `!has(self.match) || has(self.rules)`.

  Printer columns:

  | Column | Type | JSONPath |
  | --- | --- | --- |
  | Library | string | `.spec.library` |
  | Title | string | `.spec.title` |
  | Items | integer | `.status.itemCount` |
  | Synced | string | `.status.conditions[?(@.type=="Synced")].reason` |
  | Age | date | `.metadata.creationTimestamp` |

  Mirror `backlogsearch_types.go`'s marker style.
- [ ] **Step 2: Generate.** Run `make generate manifests`. Expected: a new `config/crd/bases/catalog.clustarr.io_plexcollections.yaml`; only generated files change besides the types.
- [ ] **Step 3: Run the non-envtest schema guards.** `go test ./pkg/crdcheck/ -run 'TestEveryStatusListIsCapped|TestNoCRDDefaultIsUnreachableFromGo' -v`. Expected: PASS, running rather than skipping.
- [ ] **Step 4: Document in `CLAUDE.md`.**
  - A "Plex collections" paragraph under the Plex provider paragraphs: the kind, smart or normal, presentation, the split (spec by the UI, kubectl or Terraform; status and finalizer by cluster-plex), and "absent keeps Plex's".
  - Under Invariants, the exception to "The manager is the only writer of every CR": `PlexCollection` status and its finalizer are cluster-plex's (owner, 2026-11-10, option A).
  - Raise the CRD count (27 → 28).
- [ ] **Step 5: Build and commit.** `go build ./... && make brand-gate`, then commit the types, the generated files, the kustomization, CLAUDE.md, the spec and this plan through a pathspec. Message: `catalog: PlexCollection -- Plex collections as resources (owner, 2026-11-10; docs/superpowers/specs/2026-11-10-plex-collections-design.md)`.

### Task 2: `pkg/plexfilter`, the filter catalogue and validation

**Files:**

- Create: `pkg/plexfilter/catalogue.go`, `validate.go`, `describe.go`, `plexfilter_test.go`
- Create: `test/data/plexfilter/meta-movie.json` and `meta-tv.json`. These are copies of `/tmp/claude-1000/-home-appkins-src-mediactl-clustarr/cb7691c4-97c3-4771-97e4-6d0f10939dd8/scratchpad/plexlogs/meta-1.json` and `meta-2.json`, talos's `GET /library/sections/{1,2}/all?includeMeta=1&type={1,2}` with `X-Plex-Container-Size: 0`. If the scratchpad is gone, record them again the same way.

**Interfaces:**

- Consumes: Task 1's types.
- Produces:
  - `type FieldType string`: `tag`, `string`, `integer`, `date`, `boolean`, `audioLanguage`, `subtitleLanguage`, `resolution`.
  - `type Field struct { Key, Title string; Type FieldType }`
  - `func Fields(t catalogv1.PlexItemType) []Field`. `movie` gives the 39 movie fields. `show`, `season` and `episode` give the 20 `show.*` fields plus the 26 `episode.*` fields, in Meta order.
  - `func Lookup(t catalogv1.PlexItemType, key string) (Field, bool)`
  - `func Operators(ft FieldType) []catalogv1.PlexOperator`: the design's operator table, row order.
  - `func OperatorLabel(op catalogv1.PlexOperator) string`: "is", "is not", "contains", "does not contain", "begins with", "ends with", "is greater than", "is less than", "is before", "is after", "is true", "is false".
  - `func Validate(spec catalogv1.PlexCollectionSpec) error`: `errors.Join` of every problem, each naming its path (`filter.rules[1].rules[0]`, `members.items[3]`). It checks everything Task 1's CEL checks, plus:
    - a known field, an operator the field's type allows, and a value unless `isTrue`/`isFalse`;
    - no duplicate member.
  - `func ImageURLAllowed(raw, uiArtBase string) error`: the design's URL rule as far as it can be checked without resolving DNS. It allows:
    - `https` to a host that is no IP literal in a private, loopback or link-local range and doesn't end in `.svc` or `.cluster.local`;
    - a URL under `uiArtBase` (clustarr UI's own external URL plus `/art/`).

    cluster-plex repeats the full check after resolving.
  - `func Describe(spec catalogv1.PlexCollectionSpec) string`: the list's one-line reading. A normal collection reads "N items".

- [ ] **Step 1: Write the failing tests** in `plexfilter_test.go`:
  - `TestCatalogueMatchesRecordedMeta`: every `Meta.Type[].Field[]` key and type in both fixtures equals `Fields(...)`, and vice versa.
  - `TestValidateAcceptsTheReferenceCollections`: the design's three smart specs and its normal example return nil.
  - `TestValidateRejects`: table cases, each asserting the error text contains the path:
    - `{field: genre, operator: contains}`;
    - `show.network` on a movie collection;
    - `title is` with no value;
    - a rule with both `field` and `match`;
    - 33 rules;
    - both `filter` and `members`;
    - a `Series` member in a movie collection;
    - a duplicate member;
    - `settings.order` on a smart collection.
  - `TestImageURLAllowed`: these pass:
    - `https://image.tmdb.org/t/p/original/a.jpg`;
    - `https://clustarr.k8s.appkins.io/art/x` with the base `https://clustarr.k8s.appkins.io/art/`.

    These fail:
    - `http://example.com/a.jpg`;
    - `https://10.0.0.5/a.jpg` and `https://[fe80::1]/a.jpg`;
    - `https://grafana.observability.svc.cluster.local/x`.
  - `TestDescribe`:
    - Anime Dubbed reads `Audio Language is en and Genre is Anime`;
    - UP reads `Title is "Seven Up!" or (Genre is Documentary and Title contains "Up")`;
    - the normal example reads `2 items`.
- [ ] **Step 2: Run.** `go test ./pkg/plexfilter/ -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** The field table is a checked-in Go literal transcribed from the fixtures. Field titles come from Meta's `title`.
- [ ] **Step 4: Run.** Same command. Expected: PASS.
- [ ] **Step 5: Commit.** Message: `plexfilter: Plex's smart-filter fields and operators, validation and a one-line reading`.

### Task 3: UI actions and RBAC for PlexCollection

**Files:**

- Modify: `ui/actions/config.go` (append a `configKinds` entry: Slug `plexcollections`, Group catalog, Kind `PlexCollection`, Resource `plexcollections`, Namespaced)
- Modify: `ui/settings_forms.go` (`configConstructors["plexcollections"]`)
- Modify: `config/rbac/ui_role.yaml` and its copy in `charts/clustarr/templates/rbac.yaml`. Add `plexcollections` to the catalog read rule (get, list, watch), and add `create`, `patch` and `delete` on `plexcollections`, never `plexcollections/status`.

**Interfaces:**

- Produces: `actions.ConfigKindBySlug("plexcollections")`, which the existing `CreateConfig`, `UpdateConfig` and `DeleteConfig` serve. `actions.Grants()` then includes the three write verbs.

- [ ] **Step 1: Baseline the guards.** `go test ./ui/... ./test/guards/... -run 'Role|Grant|NeverWrites|ConfigKind|Settings' 2>&1 | tail -20`.
- [ ] **Step 2: Make the three edits.** Keep the chart's role byte-identical to `config/rbac/ui_role.yaml`'s rules (`TestUIRoleChartMatchesConfig`).
- [ ] **Step 3: Run the same command.** Expected: the same passes as Step 1, with the role and settings-form tests covering the new kind.
- [ ] **Step 4: Commit.** Message: `ui: PlexCollection is a config kind -- create, patch and delete through ui/actions`.

### Task 4: The Collections page, general section and smart editor

**Files:**

- Create: `ui/collections.go`, `ui/collections_form.go`, `ui/views/collections.templ`, `ui/collections_test.go`
- Modify: `ui/routes.go` (routes), `ui/views/layout.templ` (`librarySubMenu`: a "Collections" item after the media tabs, linking `/library/collections`)

**Interfaces:**

- Consumes: Task 2's `plexfilter` functions; Task 3's config kind; `Options.Reader`; `searchArt.URL` for poster thumbnails (`ui/searchart.go`).
- Produces:
  - `func parseCollectionForm(v url.Values) (catalogv1.PlexCollectionSpec, error)`. Form keys:
    - General: `library`, `title`, `itemType`, `kind` (`smart` | `normal`), `summary`, `sortTitle`. An empty `summary` or `sortTitle` leaves the field out.
    - Filter: `match`, `sort.field`, `sort.descending`, `limit`; top-level rules `rule.<i>.field|operator|value|match`; group conditions `rule.<i>.<j>.field|operator|value`.
    - Task 5's keys: `member.<i>.kind|name`, `poster.url`, `background.url`, `settings.mode`, `settings.order`, `visibility.managed`, `visibility.libraryRecommended|home|friendsHome`.

    Indexes are dense from 0.
  - `func editorOp(spec *catalogv1.PlexCollectionSpec, op, at string) error`: `add-rule` (`at` `""` or `<i>`), `add-group`, `remove` (`<i>` or `<i>.<j>`). Task 5 adds its own ops.

  Routes, each answering in place through `finishActionWith` or a toast, never a redirect for htmx:

  | Method | Path | Answer |
  | --- | --- | --- |
  | GET | `/library/collections` | the list, by library |
  | GET | `/library/collections/new` | the editor dialog |
  | GET | `/library/collections/{namespace}/{name}/edit` | the editor dialog |
  | POST | `/library/collections/editor?op=&at=` | the editor re-rendered from the posted form after `editorOp` (no write) |
  | POST | `/library/collections` | create |
  | POST | `/library/collections/{namespace}/{name}` | merge patch of spec |
  | POST | `/library/collections/{namespace}/{name}/delete` | delete, asked through `alertdialog` |

- [ ] **Step 1: Load the `shadcn-templ` skill** and choose the components:
  - `select` for library, item type, smart/normal, match, field, operator and sort;
  - input groups for the title, string values and the limit; `textarea` for the summary;
  - a tag field's value: a combobox over the suggestions that accepts free text, or else a text field plus a suggestion list.
- [ ] **Step 2: Write the failing tests** in `ui/collections_test.go`:
  - `TestParseCollectionFormRoundTripsTheReferences`: rendering each reference spec's editor and parsing its form back gives the same spec, `Summary` and `SortTitle` nil when blank.
  - `TestEditorOps`: `add-rule ""` appends; `add-group` appends a group of one condition; `remove 1.0` removes UP's group's first condition; `remove 9` errors.
  - `TestCollectionsPageListsByLibrary`: with a fake reader holding the three references and a status with `itemCount: 259` and `Synced/Adopted`, the page shows each title, `Describe`'s line, `259` and the reason.
  - `TestCreateRejectsAnInvalidSpecWithoutWriting`: `{field: genre, operator: contains}` answers the validation error and makes no `CreateConfig` call.
  - `TestValueSuggestions`: `show.genre` suggests the cached Series' `status.metadata.genres`; `show.network` suggests `networks` plus `network`; `studio` suggests the Movies' `studios`. Each list is deduplicated and sorted. Never list Episodes.
- [ ] **Step 3: Run.** `go test ./ui/ -run 'Collection|EditorOps|ValueSuggestions' -v`. Expected: FAIL.
- [ ] **Step 4: Implement** the handlers, the form codec, the editor ops and the templ view, with Task 5's sections stubbed as empty. Writes go through `CreateConfig`, `UpdateConfig` (merge patch of `spec`) and `DeleteConfig`, all after `plexfilter.Validate`.
- [ ] **Step 5: Generate and run.** `go run github.com/a-h/templ/cmd/templ@v0.3.1020 fmt ui/views/collections.templ ui/views/layout.templ && make templ css && go test ./ui/ ./ui/actions/ 2>&1 | tail -20`. Expected: PASS.
- [ ] **Step 6: Commit.** Message: `ui: Collections -- PlexCollections listed by library and edited as Plex Web edits a smart filter`.

### Task 5: The editor's members, images, settings and visibility

**Files:**

- Modify: `ui/collections.go`, `ui/collections_form.go`, `ui/views/collections.templ`, `ui/collections_test.go`, `ui/routes.go`

**Interfaces:**

- Consumes: Task 4's codec and ops; `projection.Find` for the member search; `searchArt.URL` for previews.
- Produces:
  - Editor ops `add-member` (`at` = `<Kind>/<name>`) and `remove-member` (`at` = `<i>`).
  - Route `GET /library/collections/members?itemType=&q=`: up to 10 library items of the collection's type from `projection.Find`, each a row whose button posts `add-member`.
  - Settings selects whose first option, "Keep Plex's", leaves the field out. The "Manage visibility" switch drives `visibility.managed`.

- [ ] **Step 1: Write the failing tests:**
  - `TestParseCollectionFormNormalAndPresentation`: the design's normal example plus its "Presentation" block round-trip through render and parse.
    - "Keep Plex's" for mode gives a nil `Settings.Mode`.
    - `visibility.managed` off gives a nil `Visibility`.
    - On, with only `home` ticked, gives `{false, true, false}`.
  - `TestMemberSearchListsOnlyTheCollectionsType`: `itemType=movie&q=tot` lists the projection's Movie "My Neighbor Totoro" and no Series.
  - `TestEditorMemberOps`: `add-member Movie/spirited-away-2001` appends once (a second add is a no-op); `remove-member 0` removes it.
  - `TestImagePreviewOnlyForAllowedHosts`: a TMDB poster URL renders a `/art/search` preview. A public https URL on a host the proxy doesn't allow renders none but is kept.
  - `TestCreateRefusesAnImageURLTheRuleRefuses`: `https://10.0.0.5/x.jpg` answers `ImageURLAllowed`'s error and makes no `CreateConfig` call. The UI passes its own `/art/` base, its external URL (`--external-url`) plus `/art/`.
- [ ] **Step 2: Run.** `go test ./ui/ -run 'Normal|MemberSearch|EditorMemberOps|ImagePreview' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the four sections in the design's UI order (`switch` for visibility; chips with a remove button for members, showing poster and year).
- [ ] **Step 4: Generate and run.** `go run github.com/a-h/templ/cmd/templ@v0.3.1020 fmt ui/views/collections.templ && make templ css && go test ./ui/ 2>&1 | tail -20`. Expected: PASS.
- [ ] **Step 5: Check in a browser** (the portal issue in CLAUDE.md makes rendered-markup tests insufficient). Run `bin/ui` against talos's kubeconfig as the `ui-preview-against-talos` memory describes. With selenium:
  - open Collections;
  - open UP's editor: add a condition to its group and remove it, set a poster URL (preview shows), turn visibility on and off, then cancel;
  - start a new normal collection, find and add two movies, then cancel;
  - confirm no write happened (resourceVersions unchanged);
  - take screenshots.
- [ ] **Step 6: Commit.** Message: `ui: Collections -- normal collections' members, images, settings and visibility`.

### Task 6: cluster-plex's PMS client for collections

**Files:**

- Create: `pkg/plex/api/collections.go`, `pkg/plex/api/collections_test.go`
- Modify: `pkg/plex/api/client.go` (a `sendBody(ctx, method, path, q, h, body io.Reader, limit)` beside `send`, which passes nil)
- Create: fixtures in `pkg/plex/api/testdata/`: `collections-section1.json`, `collections-section2.json`, `meta-section1.json`, `meta-section2.json`, `genre-section2.json`, `collection-prefs.json`, `posters.json`, `managed-hubs-section2.json`. Record them from talos (read-only GETs) and from a stock PMS container (prefs, posters), the token in an environment variable and never printed.

**Interfaces:**

- Produces (package `api`):
  - Types:
    - `Collection { RatingKey, Title, Subtype, Content, GUID string; Smart bool; ChildCount int }`. `Smart` decodes `"1"`, `1` and `true`; a `GUID` starting `collection://` marks one a person made.
    - `SectionMeta { Fields map[string]string; Sorts []string; TagPaths map[string]string }`
    - `TagValue { ID, Title string }`
    - `Item { RatingKey, GUID string }`
    - `CollectionPrefs { Mode, Order int }` (Plex's numbers; -1 is the library default)
    - `Poster { Key string; Selected bool; SHA1 string }` (`SHA1` is parsed from `upload://<posters|arts>/<sha1>`, empty otherwise)
    - `ManagedHub { Identifier string; Recommended, OwnHome, SharedHome bool }`

  | Method | Request |
  | --- | --- |
  | `Collections(ctx, sectionKey string) ([]Collection, error)` | `GET /library/sections/{key}/collections` |
  | `Collection(ctx, ratingKey string) (Collection, error)` | `GET /library/metadata/{rk}`; a 404 is `ErrNotFound` |
  | `CreateCollection(ctx, sectionKey string, itemType int, title, uri string, smart bool) (Collection, error)` | `POST /library/collections?type=&title=&smart=0|1&sectionId=&uri=` |
  | `SetCollectionFilter(ctx, ratingKey, uri string) error` / `AddCollectionItems(ctx, ratingKey, uri string) error` | `PUT /library/collections/{rk}/items?uri=` (same call; two names for the two meanings) |
  | `RemoveCollectionItem(ctx, ratingKey, itemRatingKey string) error` | `DELETE /library/collections/{rk}/items/{item}` |
  | `CollectionChildren(ctx, ratingKey string) ([]Item, error)` | `GET /library/collections/{rk}/children` |
  | `SectionItems(ctx, sectionKey string, itemType int) ([]Item, error)` | `GET /library/sections/{key}/all?type=` paged by `X-Plex-Container-Start`/`-Size` 500 |
  | `EditCollection(ctx, sectionKey, ratingKey, field, value string) error` | `PUT /library/sections/{key}/all?type=18&id={rk}&{field}.value=&{field}.locked=1` (`title`, `summary`, `titleSort`) |
  | `CollectionPrefs(ctx, ratingKey string) (CollectionPrefs, error)` | `GET /library/metadata/{rk}?includePreferences=1` |
  | `SetCollectionPrefs(ctx, ratingKey string, mode, order *int) error` | `PUT /library/metadata/{rk}/prefs?collectionMode=&collectionSort=` (only the non-nil ones) |
  | `Posters(ctx, ratingKey, kind string) ([]Poster, error)` | `GET /library/metadata/{rk}/{posters|arts}` |
  | `UploadImage(ctx, ratingKey, kind string, body []byte, contentType string) error` | `POST /library/metadata/{rk}/{posters|arts}` with the bytes as the body |
  | `ManagedHubs(ctx, sectionKey string) ([]ManagedHub, error)` | `GET /hubs/sections/{key}/manage` |
  | `CreateManagedHub(ctx, sectionKey, ratingKey string, v ManagedHub) error` | `POST /hubs/sections/{key}/manage?metadataItemId=&promotedToRecommended=&promotedToOwnHome=&promotedToSharedHome=` |
  | `UpdateManagedHub(ctx, sectionKey string, v ManagedHub) error` | `PUT /hubs/sections/{key}/manage/{identifier}?...` |
  | `DeleteCollection(ctx, ratingKey string) error` | `DELETE /library/collections/{rk}`; a 404 is nil |
  | `SectionMeta(ctx, sectionKey string) (SectionMeta, error)` | `GET /library/sections/{key}/all?includeMeta=1` with `X-Plex-Container-Start: 0`, `X-Plex-Container-Size: 0` |
  | `TagValues(ctx, path string) ([]TagValue, error)` | `GET {path}`; reads `Directory[].key` and `.title` |

- [ ] **Step 1: Write the failing tests** against an `httptest.Server` serving the fixtures:
  - `TestCollectionsDecodesSmartContentAndGUID`: section 2 lists `Anime Dubbed` with `Smart` true, `ChildCount` 259, `Content` starting `server://` and `GUID` starting `collection://`.
  - `TestSectionMetaFieldsAndTagPaths`: section 2 has `show.genre` → `tag` and `episode.audioLanguage` → `audioLanguage`; `TagPaths["show.genre"]` is non-empty.
  - `TestCreateCollectionSendsTheURIOnce`: the `uri` value equals the given URI, and `smart` is `1` or `0`.
  - `TestCollectionPrefsReadsModeAndOrder`: the stock fixture gives `{Mode: 0, Order: 1}`.
  - `TestPostersParsesUploadSHA1`: the stock fixture's selected entry gives `SHA1` `e9dfa0063251cdcf337791147bdfac4e4ea62df8`.
  - `TestUploadImageSendsTheBytes`: the server receives the body unchanged, with its content type.
  - `TestManagedHubsDecodes`: talos's fixture gives `tv.recentlyadded` with all three promoted.
  - `TestDeleteCollectionTreats404AsDeleted`; `TestCollectionMapsNotFound`.
- [ ] **Step 2: Run.** `go test ./pkg/plex/api/ -run 'Collection|SectionMeta|TagValues|Posters|UploadImage|ManagedHub' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** on `send`/`sendBody`. Pass every `uri` as one query value, so `url.Values` key sorting cannot reorder the filter inside it. Uploads read at most 10 MiB.
- [ ] **Step 4: Run.** Same command. Expected: PASS.
- [ ] **Step 5: Commit.** Message: `plex/api: collections, their items, prefs, images and managed hubs`.

### Task 7: Compiling a smart filter to Plex Web's URI

**Files:**

- Create: `pkg/plex/collections/spec.go`, `compile.go`, `compile_test.go`

**Interfaces:**

- Consumes: Task 6's `api.SectionMeta`.
- Produces (package `collections`):
  - `type Spec` and its parts (`Filter`, `Rule`, `Condition`, `Sort`, `Member`, `Image`, `Settings`, `Visibility`): the JSON mirror of Task 1's spec.
  - `func SpecOf(u *unstructured.Unstructured) (Spec, error)`, via `runtime.DefaultUnstructuredConverter`.
  - `var ErrInvalidRule, ErrUnknownValue error`
  - `type Resolver func(field, name string) (id string, err error)`
  - `type Carry struct { Sort, Limit string }`: what the collection's current filter holds.
  - `func Compile(f Filter, sort *Sort, limit *int32, itemType string, m api.SectionMeta, resolve Resolver, carry Carry) (query string, err error)`: the query in decoded form.
  - `func ParseContent(content string) (query string, carry Carry)`
  - `func SmartURI(machineID, sectionKey, query string) string` and `func ItemsURI(machineID string, ratingKeys []string) string`: `server://` URIs, every value percent-encoded as Plex Web encodes it.
  - `func ItemTypeNumber(t string) int`: movie 1, show 2, season 3, episode 4.

The encoding algorithm (the design's Compile step; the signature does not determine it):

```text
query = "type=" + n
sort  = spec.sort ? key + (desc ? ":desc" : "") : carry.Sort      -> "&sort=" + sort if non-empty
cond  = field + symbol(operator, fieldType) + value'              (value' = tag id | value; boolean -> "1")
join  = "&and=1&" for all, "&or=1&" for any
if top.match == all and every top rule is a condition:
    body = join(conds)
else:
    entry(cond)  = "push=1&" + cond + "&pop=1"
    entry(group) = "push=1&" + join_group(conds) + "&pop=1"
    body = "push=1&" + join_top(entries) + "&pop=1"
query += "&" + body; limit = spec.limit ?: carry.Limit -> "&limit=" + limit if non-empty
```

- [ ] **Step 1: Write the failing tests:**
  - `TestCompileReferences`: with a resolver mapping `Anime` → `6437`, `Netflix` → `6787` and `Documentary` → `5836`, the three design resources compile to exactly:
    - `type=2&sort=titleSort&episode.audioLanguage=en&and=1&show.genre=6437`
    - `type=2&show.network=6787`
    - `type=1&push=1&push=1&title==Seven Up!&pop=1&or=1&push=1&genre=5836&and=1&title=Up&pop=1&pop=1`

    The first resource states its sort; the second and third carry an empty `Carry`.
  - `TestCompileCarriesPlexSortWhenSpecHasNone`: UP with `Carry{Sort: "addedAt:desc"}` compiles with `&sort=addedAt:desc` after `type=1`.
  - `TestCompileEncodesSpecialCharacters`: `title contains "Tom & Jerry"` and `"Pokémon"`, through `SmartURI` and back through `ParseContent`, give the same decoded values. The encoded form contains no bare `&` inside a value. (Review Focus 5.)
  - `TestCompileRejects`: an unknown field and an operator its type lacks give `ErrInvalidRule`; the resolver's miss gives `ErrUnknownValue`.
  - `TestItemsURI`: `ItemsURI("abc", ["2","3"])` is `server://abc/com.plexapp.plugins.library/library/metadata/2,3`.
- [ ] **Step 2: Run.** `go test ./pkg/plex/collections/ -run 'Compile|ItemsURI' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Same command. Expected: PASS.
- [ ] **Step 5: Commit.** Message: `collections: compile a PlexCollection filter to Plex Web's smart-filter URI`.

### Task 8: Syncing: find, adopt, create, smart content, titles, delete

**Files:**

- Create: `pkg/plex/collections/sync.go`, `sync_test.go`, `fakepms_test.go`

**Interfaces:**

- Consumes: Task 6's client; Task 7's compile functions.
- Produces:
  - `type PMS interface`: Task 6's methods plus `Sections(ctx) ([]api.Section, error)`; `*api.Client` satisfies it.
  - `type MemberResolver interface { GUIDs(ctx context.Context, namespace string, members []Member) (map[Member]string, error) }`: Task 11 implements it. A member it can't find (no such clustarr object) is absent from the map.
  - `type ImageFetcher func(ctx context.Context, url string) (body []byte, contentType string, err error)`: Task 10 implements it.
  - `type Syncer struct { PMS PMS; MachineID string; Members MemberResolver; Fetch ImageFetcher; Now func() time.Time }`. Section Meta, tag lists and GUID indexes are cached per section, and dropped by `Forget()`.
  - `type Outcome struct { RatingKey string; Adopted bool; Content string; ItemCount, MissingMembers int; Missing []Member; Poster, Background *ImageState; Reason, Message string }`
  - `type ImageState struct { URL, SHA1 string; FetchedAt time.Time }`
  - `type Previous struct { RatingKey string; Poster, Background *ImageState }`: what the resource's status recorded.
  - `func (s *Syncer) Sync(ctx context.Context, namespace string, spec Spec, prev Previous) Outcome`
  - `func (s *Syncer) Delete(ctx context.Context, ratingKey string) error`

  `Sync`'s order is the design's steps 1-7. This task builds steps 1, 2 (smart), 3, 4 (smart), 5 and 7. Tasks 9 and 10 fill normal content and presentation behind the same `Sync`.

- [ ] **Step 1: Write the fake PMS** (`fakepms_test.go`): an `httptest.Server` holding sections, collections (with GUIDs, children, prefs, posters and arts), section items and managed hubs, recording every write, and able to fail a path once with a given status.
- [ ] **Step 2: Write the failing tests:**
  - `TestSyncAdoptsByTitleAndKeepsRatingKey`: an existing smart "UP" (GUID `collection://...`) with the right filter makes no write, and gives `Adopted` true, `Reason` `Adopted` and the same ratingKey.
  - `TestSyncCreatesWhenAbsent`: one `POST /library/collections` with `smart=1` and the compiled URI; `Reason` `Created`.
  - `TestSyncPutsBackADriftedFilter`: one `PUT .../items?uri=`; `Reason` `Synced`, with a message naming the drift.
  - `TestSyncKeepsPlexSort`: an adopted collection holding `sort=rating:desc`, with no sort in the spec, sends no PUT; a changed filter sends one keeping `sort=rating:desc`.
  - `TestSyncNeverAdoptsTheOtherKind`: a normal same-title collection isn't adopted by a smart resource; `NotSmart`, no write.
  - `TestSyncNeverAdoptsAMetadataCollection`: a same-title collection whose GUID isn't `collection://` gives `NotAdoptable`, no write. (Review Focus 4.)
  - `TestSyncKeepsTheLastGoodFilterOnUnknownValue`: genre "Nope" gives `UnknownValue` and no write.
  - `TestSyncNeverCreatesWhenListingFails`: `/library/sections/2/collections` answering 503 gives `PlexUnavailable`, with no POST. (Review Focus 1.)
  - `TestSyncRecreatesACollectionDeletedInPlex`: a stale ratingKey answering 404 adopts by title if one exists, else creates.
  - `TestSyncEditsOnlyStatedTitles`: a differing title gives one edit with `title.locked=1`; a nil `Summary` sends no summary edit; a stated one differing sends one.
  - `TestDeleteOnlyWithARatingKey`: an empty ratingKey makes no DELETE.
- [ ] **Step 3: Run.** `go test ./pkg/plex/collections/ -run Sync -v`. Expected: FAIL.
- [ ] **Step 4: Implement** `Sync` (the steps named above) and `Delete`.
- [ ] **Step 5: Run.** Same command. Expected: PASS.
- [ ] **Step 6: Commit.** Message: `collections: sync a PlexCollection into Plex -- adopt, create, put back drift, keep what the spec leaves out`.

### Task 9: Syncing a normal collection's members

**Files:**

- Modify: `pkg/plex/collections/sync.go`; create `pkg/plex/collections/members.go`, `members_test.go`

**Interfaces:**

- Consumes: `MemberResolver`; `api.SectionItems`, `CollectionChildren`, `AddCollectionItems`, `RemoveCollectionItem`, `CreateCollection(..., smart false)`; `ItemsURI`.
- Produces: `func baseGUID(guid string) string`, matching `plexproject.BaseGUID`: everything before `/edition/`. A member's Plex items are every section item whose base GUID equals its GUID.

- [ ] **Step 1: Confirm the item removal** on a stock PMS container (the design leaves it to the builder): `DELETE /library/collections/{rk}/items/{item rk}` removes one item. Record the answer in a comment beside `RemoveCollectionItem`. If PMS refuses, use the call PMS does take (python-plexapi's `removeItems`) and change Task 6's method to match.
- [ ] **Step 2: Write the failing tests:**
  - `TestMembersCreateAndAdd`: two members resolving to Plex items 2 and 3 create a normal collection with `ItemsURI(.., ["2","3"])`; a third added later gives one `AddCollectionItems` with only its key.
  - `TestMembersRemoveExtras`: a child not among the members gives one `RemoveCollectionItem`.
  - `TestMembersIncludeEditions`: a member whose GUID matches two section items (`plex://movie/x` and `plex://movie/x/edition/Director%27s%20Cut`) adds both.
  - `TestMembersPlexLacksAreReportedAndAddedLater`: a member the resolver knows but no section item carries gives `MissingMembers` 1, lists it in `Missing`, and reads `MembersPending`. With no member in Plex at all, nothing is created and the outcome is `Waiting`. Once the fake gains the item, the next sync adds it. (Review Focus 2.)
  - `TestMembersNeverEmptyACollection`: an adopted collection whose members resolve to no Plex item keeps all its children (no `RemoveCollectionItem`).
  - `TestMembersResolverFailureChangesNothing`: a resolver error gives no add and no remove, and reads `PlexUnavailable` with the error in the message.
  - `TestMembersRemovePlexWebAdditions`: a child added in Plex that no member resolves to is removed when at least one member resolves.
- [ ] **Step 3: Run.** `go test ./pkg/plex/collections/ -run Members -v`. Expected: FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run.** Same command. Expected: PASS.
- [ ] **Step 6: Commit.** Message: `collections: normal collections -- members resolved by GUID, added and removed`.

### Task 10: Syncing presentation: images, settings, visibility

**Files:**

- Create: `pkg/plex/collections/present.go`, `present_test.go`, `fetch.go`, `fetch_test.go`

**Interfaces:**

- Consumes: `api.Posters`, `UploadImage`, `CollectionPrefs`, `SetCollectionPrefs`, `ManagedHubs`, `CreateManagedHub`, `UpdateManagedHub`.
- Produces:
  - `func NewImageFetcher(uiArtBase string) ImageFetcher`: GET with a 30 s timeout, at most 5 redirects, a body cap of 10 MiB, and a content type starting `image/`. Allowed targets, the design's rule:
    - `https` where every address the dialer connects to is public (checked in the dialer's `Control` hook, so a redirect or a DNS answer can't reach a private address);
    - anything under `uiArtBase`, which may be in-cluster.

    `uiArtBase` is clustarr UI's external URL plus `/art/` (on talos `https://clustarr.k8s.appkins.io/art/`, which resolves to a LAN address), from a new cluster-plex setting `plex.clustarr.uiURL`. Empty means such URLs are refused.
  - The mode and order numbers:

    | Spec value | Plex `collectionMode` |
    | --- | --- |
    | `default` | -1 |
    | `hideCollection` | 0 |
    | `hideItems` | 1 |
    | `showCollectionAndItems` | 2 |

    | Spec value | Plex `collectionSort` |
    | --- | --- |
    | `releaseDate` | 0 |
    | `alphabetical` | 1 |
  - A collection's managed hub is the one whose identifier is `custom.collection.<section key>.<rating key>`.

  The image rule: fetch when the spec's URL differs from `prev`'s, or `prev.FetchedAt` is over 24 h old, else reuse `prev.SHA1`. Upload when the selected entry of `Posters` doesn't carry that SHA-1.

- [ ] **Step 1: Write the failing tests:**
  - `TestImageUploadedOnceThenLeft`: a first sync fetches and uploads; the next, with the fake's selected poster carrying the SHA-1, fetches nothing and uploads nothing.
  - `TestImagePutBackWhenAnotherIsSelected`: the fake selecting another poster gives one upload, reusing the stored SHA-1's bytes by fetching again.
  - `TestImageFailureLeavesTheImageAndSyncsTheRest`: a fetch error, a 12 MiB body and a `text/html` body each give `ImageUnavailable` with no upload, while a drifted filter in the same sync is still put back. (Review Focus 3.)
  - `TestSettingsOnlyWhenStated`: a nil `Settings` makes no prefs call; `{Mode: hideCollection}` sends `collectionMode=0` and no `collectionSort`; a matching value sends nothing.
  - `TestVisibilityCreatesThenUpdatesTheHub`: with no `custom.collection.2.<rk>` hub, one create; with it present and differing, one update; a nil `Visibility` touches nothing; all three false with no hub writes nothing.
  - `TestFetcherCaps` (`fetch_test.go`): over 10 MiB, a non-image type and a redirect loop each error.
  - `TestFetcherRefusesPrivateTargets`: an `httptest` server on 127.0.0.1 is refused, both directly and through a redirect from an allowed URL, unless the URL is under `uiArtBase`. In tests the "public" check is injectable, so the allowed path can still be exercised against `httptest`.
- [ ] **Step 2: Run.** `go test ./pkg/plex/collections/ -run 'Image|Settings|Visibility|Fetcher' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Same command. Expected: PASS.
- [ ] **Step 5: Commit.** Message: `collections: images, collection mode and order, and hub visibility`.

### Task 11: The controller, wiring and RBAC

**Files:**

- Create: `pkg/plex/collections/controller.go`, `controller_test.go`, `resolver.go`; `cmd/manager/collections.go`
- Modify: `cmd/manager/leaderwork.go` (`startLeaderWork`: `go m.collectionsLoop(ctx)` when `m.Config.Clustarr.Enabled && m.Dynamic != nil`)
- Modify: `charts/cluster-plex/templates/rbac.yaml` (on `catalog.clustarr.io`: `plexcollections` get, list, watch, patch; `plexcollections/status` patch)
- Modify: `cmd/manager/config_provision.go` and `charts/cluster-plex/values.yaml` (`plex.clustarr.uiURL`, Task 10's `uiArtBase` source)

**Interfaces:**

- Consumes: Tasks 8-10's `Syncer`; `dynamic.Interface` and discovery as in `pkg/plex/livetv/watch.go`; the Events recorder as in `cmd/manager/livetv.go`; `plexproject.GUID`, `MoviesProvider` and `TVProvider`.
- Produces:
  - `var GVR = schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "plexcollections"}`
  - `type DynamicMembers struct { Dynamic dynamic.Interface }`, implementing `MemberResolver`. For a `Movie` it is `plexproject.GUID(MoviesProvider, TypeMovie, string(uid), status.metadata.externalIDs.plex)`, and for a `Series` the TV provider and `TypeShow`. Each is read by name from the namespace through the dynamic client.
  - `type Controller struct { Dynamic dynamic.Interface; Discovery discovery.DiscoveryInterface; Namespace string; Syncer *Syncer; Record func(obj *unstructured.Unstructured, warning bool, reason, msg string); Every time.Duration; Logger *slog.Logger }`
  - `func (c *Controller) Run(ctx context.Context) error`. It waits, polling every minute, while the CRD isn't installed. It syncs on every add or update, every `Every` (10m), and after `PMSRestarted()`.
  - `func (c *Controller) PMSRestarted()`, which also calls `Syncer.Forget()`.

  Per resource:
  1. A deleted resource carrying the finalizer: `Syncer.Delete(status.ratingKey)`. On success, remove the finalizer by applying `metadata.finalizers: []` under manager `cluster-plex`. On failure, keep it and retry.
  2. Otherwise, add the finalizer by applying it (server-side apply on the main object, manager `cluster-plex`, Force).
  3. Skip a duplicate: the same `library` + `title` as an older resource (by `creationTimestamp`, then name) gives `Conflict`.
  4. Otherwise `Sync` with `Previous` from the status.
  5. Apply status on the `status` subresource (manager `cluster-plex`, Force): every Task 1 status field and one `Synced` condition. `True` for `Synced`, `Created`, `Adopted` and `MembersPending`; `False` otherwise.
  6. Record a Normal Event on create, adopt or put-back, and a Warning Event on a `False` reason change.

- [ ] **Step 1: Write the failing tests** with `k8s.io/client-go/dynamic/fake` and Task 8's fake PMS:
  - `TestControllerAddsFinalizerAndWritesStatus`: a new UP resource ends with the finalizer, `status.ratingKey` the fake's, `Synced=True`, reason `Adopted` and `observedGeneration` 1.
  - `TestDuplicateTitlesConflict`: two resources for "UP": the older adopts, the newer reads `Conflict`, and the fake records no write for it.
  - `TestFinalizerHoldsUntilPlexDeletes`: a deletion while the fake fails DELETE with 503 keeps the finalizer. After the fake recovers, the next pass deletes and releases it; a 404 also releases it.
  - `TestWaitsForTheCRD`: with discovery lacking `plexcollections`, `Run` neither lists nor errors until it appears.
  - `TestDynamicMembersGUIDs`: a fake Movie with UID `u1` and `externalIDs.plex` `5d77` gives `plex://movie/5d77`; one without a Plex id gives `tv.plex.agents.custom.clustarr.movies://movie/u1`; a missing Movie is absent from the map.
- [ ] **Step 2: Run.** `go test ./pkg/plex/collections/ -run 'Controller|Duplicate|Finalizer|WaitsFor|DynamicMembers' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the controller, the resolver and `cmd/manager/collections.go`.
  - `collectionsLoop` builds the `Syncer` with `m.pms()`, the identity from `PMS.Identity` (retrying with backoff until PMS answers), `DynamicMembers` and `NewImageFetcher`.
  - It records Events through an `events.k8s.io` broadcaster, reporter `cluster-plex`.
  - It calls `PMSRestarted()` when `m.pmsStarted` changes.
- [ ] **Step 4: Run the package and manager tests.** `go test ./pkg/plex/collections/ ./cmd/manager/ ./pkg/plex/api/ 2>&1 | tail -15`, then `make build` and `helm lint charts/cluster-plex`. Expected: PASS and clean.
- [ ] **Step 5: Commit** (cluster-plex). Message: `collections: reconcile clustarr's PlexCollections into Plex on the Lease holder; cluster-plex writes their status and finalizer`.

### Task 12: Seeds, proof and docs

**Files:**

- Create (clustarr): `config/samples/catalog_v1alpha1_plexcollection_anime_dubbed.yaml`, `..._netflix.yaml`, `..._up.yaml`. These are the design's three smart specs, namespace `clustarr-system`, with no presentation fields.
- Modify (cluster-plex): `CLAUDE.md`, a "Plex collections" paragraph: the controller, the field manager and finalizer, the 10-minute resync, "the resource wins", "absent keeps Plex's", the image rule and the hub identifier.

- [ ] **Step 1: Prove on the kind rig, after clustarr-48's "done" only.**
  - Apply the CRD by hand and run cluster-plex's manager from Task 11's build.
  - **Smart collection:** apply a test smart resource matching the rig's library. It is created, `Created`; an edit in Plex is put back within 10 minutes.
  - **Normal collection:** apply a normal resource with two members. Removing one in Plex Web puts it back; removing it from the spec removes it.
  - **Presentation:**
    - a poster URL is uploaded once;
    - `settings.mode: hideCollection` hides the collection in the library;
    - `visibility: {libraryRecommended: true, home: true, friendsHome: false}` creates the hub, which `GET /hubs/sections/<n>/manage` shows. This confirms the hub writes on a claimed server.
  - **Deletion:** delete both resources; their collections and hub are gone.
  - **Two Plex pods:** check whether the second pod's PMS shows a change without a restart. Record the answer in cluster-plex's CLAUDE.md, and add the DVR path's restart if it does not.
- [ ] **Step 2: Encodings to confirm on the rig.** Confirm LIMIT TO's parameter and a relative date's form, for example from Plex Web's own bundle served at `/web`. Fix Task 7's `limit` if it differs.
- [ ] **Step 3: Commit** the samples (clustarr) and CLAUDE.md (cluster-plex) through pathspecs.
- [ ] **Step 4: Talos rollout (only on the owner's go, announced to the peer sessions).**
  - Apply the CRD by hand.
  - Hand-package the clustarr chart (CRD and UI role) and the cluster-plex chart (RBAC) as `chart-publishing-hand-packaged` describes.
  - Build both images, then run a targeted Terraform plan, guarded and applied per the recipe.
  - Before plex-0 restarts, check the `clusterplex_plex_streams` gauge.
  - Apply the three samples. Each must read `Adopted` with an unchanged item count (259, 23, 8), and Plex's log must show no new error.

---

## Self-review notes

- **Spec coverage:**

  | Spec section | Task |
  | --- | --- |
  | Resource and its fields | 1 |
  | Fields, operators, values, nesting | 1, 2, 7 |
  | Sort and limit | 7, 8 |
  | Normal collections | 1, 5, 9, 11 |
  | Presentation | 1, 5, 10 |
  | Status | 1, 11 |
  | Reconciling steps 1-7 | 8, 9, 10 |
  | Resource wins / absent keeps Plex's | 8, 10 |
  | Deletion and where it runs | 11 |
  | Who writes status (option A) | 1, 11 |
  | UI | 3, 4, 5 |
  | Seeding | 12 |
  | Testing and proof | every task, plus 12 |

  Later and Non-goals: not built.
- **Names checked across tasks:** `Syncer`, `Outcome`, `Previous`, `ImageState`, `MemberResolver`, `DynamicMembers`, `ImageFetcher`, `Carry`, `ParseContent`, `SmartURI`, `ItemsURI`, `ItemTypeNumber`, `PlexCollectionFinalizer` and the `cluster-plex` manager are used consistently.
