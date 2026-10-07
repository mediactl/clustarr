# Live TV Phase 1 (clustarr) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Plex sees an IPTV provider's channels as one HDHomeRun DVR. This
plan builds:

- **the kinds:** `IPTVProvider` and `IPTVChannel` (`clustarr.io/v1alpha1`);
- **the shared rules:** the pure-Go `pkg/iptv` packages;
- **in the manager:** a tuner runnable with the stream relay, and two
  controllers;
- **storage:** the `clustarr-livetv` object store;
- **installers and proof:** the installer changes, the NetworkPolicy, and
  e2e scenario 19.

**Architecture:**

- **What stays pure:** every rule (M3U, filters, XMLTV, the HDHomeRun JSON)
  is a pure package under `pkg/iptv`, which the tuner, and later the UI,
  share.
- **The tuner** (`app/livetv/tuner`) holds no Kubernetes client. The
  IPTVProvider controller hands it a resolved `tuner.Config`, with the
  Secrets already read. The tuner then:
  - fetches the playlist and the guides;
  - renders the lineup and the guide;
  - serves the HDHomeRun surface and relays streams;
  - reports a `Snapshot`, from which both controllers write status.
- **Where it runs:** everything is registered in `cmd/manager` as the
  `livetv` step, and the tuner runs only on the leader.

**Tech Stack:**

- Go 1.27, controller-runtime, client-go;
- `encoding/xml` streaming, `net/http`, `compress/gzip`;
- NATS JetStream object store (`pkg/events`);
- envtest (`KUBEBUILDER_ASSETS`), testify, kind for e2e.

**Spec:** `/home/appkins/src/mediactl/clustarr/docs/superpowers/specs/2026-10-07-iptv-live-tv-design.md`.

- The spec lives on main. Read it from that path: this plan's code lands
  on another branch.
- The owner approved it on 2026-10-07, including both §10 consequences.
- cluster-plex's half is a separate plan:
  `/home/appkins/src/mediactl/cluster-plex/docs/superpowers/plans/2026-10-07-livetv-dvr-provisioner.md`.

## Global Constraints

- **Where you work:**
  - in a worktree of your own, `../clustarr-livetv`, on branch `livetv` cut
    from `unify-manager-agent` (`git worktree add ../clustarr-livetv -b livetv unify-manager-agent`);
  - never commit in `../clustarr-unify`, which is clustarr-c3's;
  - never push.
  - Merging `livetv` into `unify-manager-agent` is clustarr-c3's and the
    owner's call (Task 13).
- **Part B waits for Wave 6.** Tasks 11-13 need the unify plan's Wave 6
  (`docs/superpowers/plans/2026-10-06-manager-agent-split.md`, W6.1-W6.17) to
  have landed on `unify-manager-agent`. Before Task 11, rebase `livetv` onto
  it and check that `config/manager/manager.yaml`, `$managerArgs` in
  `charts/clustarr/templates/deployments.yaml`, `RBAC_PATHS_manager` and
  `config/rbac/manager_role.yaml` exist. If they don't, stop and say so.
- **Until then, `make test` may be red on the branch** for reasons outside
  this plan (moved test packages). Each Part A task runs its own packages'
  tests. Task 13 runs `make test` and `make lint`.
- **Every Go file:** the GPL header from `hack/boilerplate.go.txt`.
- **Logging:** `slog` from context (`pkg/obs/logging.FromContext`). No
  package-level logger and no logger struct fields.
- **Dependencies:** no `go get` and no `go mod tidy`. Everything used is
  the standard library or already in `go.mod`. If a step seems to need a
  new module, stop.
- **Git:**
  - pathspec commits only: `git add` every new file first, then
    `git commit -m '…' -- <paths>`;
  - never `git stash`;
  - check `git status --short | grep '^??'` before each commit.
- **Status writes:**
  - only through `k8s.PatchStatus`;
  - every apply declares everything its manager owns, including on early
    returns;
  - test each against an object that already has status.
- **CRD fields:**
  - an optional field with a default is a pointer with an `…OrDefault`
    accessor, never a `+kubebuilder:default` that a Go zero overrides;
  - every status list has `MaxItems`;
  - no floats in `api/`.
- **Metrics:** prefix `clustarr_livetv_`, labelled by `provider` (and
  `reason` or `source`). Never by channel, title or URL.
- **HTTP bodies:** each one is read through a cap with an
  `ErrResponseTooLarge` sentinel.
- **Credentials in URLs:**
  - every URL that is logged, put in status or an error, or stored passes
    `iptv.Redact` or `iptv.RedactError`;
  - the object store never holds a stream URL.
- **`cmd/manager` links no ffgo and no purego.** `pkg/iptv` and
  `app/livetv` import neither.
- **No deploy to kind-cluster-plex.** Only e2e on `kind-clustarr`
  (`hack/e2e.sh`).

## Review Focus

1. **A playlist refresh that fails after a good fetch** keeps serving the
   last good lineup. It sets `status.playlist.error`, redacted, and never
   blanks Plex's channels. Before the first good fetch, `lineup.json` is
   503. Covered by Task 7, `TestAFailedRefreshKeepsServingTheLastLineup`.
2. **Plex sends `Host: 10.96.0.5:80`** (with a port) or any host
   casing/port form. The root routes still find the provider; an unknown
   host is 404; path routes work under any host. Covered by Task 7,
   `TestRootRoutesMatchTheHostWithOrWithoutAPort`.
3. **A channel whose key leaves the playlist and later returns** reads
   `Missing`, then `Active`. Its mapping is intact and nothing is deleted.
   Covered by Task 9, `TestAChannelGoneFromThePlaylistIsMissingThenActiveAgain`.
4. **A viewer that vanishes without a clean close** (client context
   cancelled, connection reset) frees the tuner after `linger`. The tuner
   count never leaks. Covered by Task 6,
   `TestAVanishedViewerFreesTheTunerAfterLinger`.
5. **An Xtream URL with credentials in its path and query** shows up
   redacted in status errors, logs and objects: `/live/user/pass/1001.ts`
   and `get.php?username=…`. Covered by Task 2, `TestRedact`, and by Task 7,
   `TestNoStoredOrReportedStringCarriesACredential`.

---

## Part A: code (lands on the branch now)

### Task 1: The `clustarr.io/v1alpha1` group, both kinds, CEL, scheme and field managers

**Files:**

- Create:
  - `api/clustarr/v1alpha1/groupversion_info.go`
  - `api/clustarr/v1alpha1/iptvprovider_types.go`
  - `api/clustarr/v1alpha1/iptvchannel_types.go`
  - `api/clustarr/v1alpha1/defaults.go`
  - `api/clustarr/v1alpha1/defaults_test.go`
  - `pkg/crdcheck/livetv_cel_test.go`
- Generated:
  - `api/clustarr/v1alpha1/zz_generated.deepcopy.go`
  - `api/applyconfiguration/clustarr/...` (wherever controller-gen writes;
    `ls` it)
  - `config/crd/bases/clustarr.io_iptvproviders.yaml`
  - `config/crd/bases/clustarr.io_iptvchannels.yaml`
- Modify:
  - `pkg/k8s/scheme.go` (`AddToSchemeFuncs`)
  - `pkg/k8s/scheme_test.go` (both maps)
  - `pkg/k8s/fieldmanager.go` (two managers, `FieldManagers()`)
  - `pkg/k8s/fieldmanager_test.go` (`want`)
  - `config/crd/kustomization.yaml` (a `# clustarr.io` block)
  - `test/guards/rbac_markers_test.go` (`apiGroupAliases`: dir `clustarr` maps to `clustarr.io`)

**Interfaces:**

- Produces:
  - `clustarrv1alpha1.{IPTVProvider, IPTVProviderSpec, IPTVProviderStatus, PlaylistSource, XMLTVSource, ChannelFilter, FilterType, EPGSource, DeviceSpec, StreamSpec, PlaylistStatus, GuideStatus, LineupStatus, TunerStatus, IPTVChannel, IPTVChannelSpec, ChannelEPG, IPTVChannelStatus}`;
  - constants `EPGSourceXEPG`, `EPGSourcePMS`, `FilterGroupTitle`, `FilterCustom`;
  - condition types `ConditionReady`, `ConditionValid`, `ConditionPlaylistFetched`, `ConditionGuidesFetched`;
  - channel reasons `ReasonActive`, `ReasonInactive`, `ReasonMissing`, `ReasonDuplicateNumber`, `ReasonGuideNotFound`;
  - accessors:
    - `(IPTVProviderSpec) EnabledOrDefault() bool` (true)
    - `EPGSourceOrDefault() EPGSource` (XEPG)
    - `ChannelNumberStartOrDefault() int32` (1000)
    - `(PlaylistSource) RefreshOrDefault() time.Duration` (24h)
    - `UserAgentOrDefault() string` ("clustarr")
    - `(XMLTVSource) RefreshOrDefault() time.Duration` (12h)
    - `(ChannelFilter) EnabledOrDefault() bool` (true)
    - `(DeviceSpec) FriendlyNameOrDefault(name string) string` ("Clustarr " + name)
    - `(StreamSpec) BufferOrDefault() int64` (8 MiB)
    - `LingerOrDefault() time.Duration` (10s)
    - `(ChannelEPG) DummyMinutesOrDefault() int32` (60)
  - `k8s.ManagerLiveTV = "clustarr-livetv"` and
    `k8s.ManagerLiveTVChannel = "clustarr-livetv-channel"`.

- [ ] **Step 1: Write the failing accessor test.** `api/clustarr/v1alpha1/defaults_test.go`:

```go
package v1alpha1_test

import (
 "testing"
 "time"

 "github.com/stretchr/testify/assert"
 "k8s.io/apimachinery/pkg/api/resource"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/utils/ptr"

 clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

func TestAnEmptySpecReadsEveryDefault(t *testing.T) {
 var s clustarrv1.IPTVProviderSpec
 assert.True(t, s.EnabledOrDefault())
 assert.Equal(t, clustarrv1.EPGSourceXEPG, s.EPGSourceOrDefault())
 assert.Equal(t, int32(1000), s.ChannelNumberStartOrDefault())
 assert.Equal(t, 24*time.Hour, s.Playlist.RefreshOrDefault())
 assert.Equal(t, "clustarr", s.Playlist.UserAgentOrDefault())
 assert.Equal(t, 12*time.Hour, clustarrv1.XMLTVSource{}.RefreshOrDefault())
 assert.True(t, clustarrv1.ChannelFilter{}.EnabledOrDefault())
 assert.Equal(t, "Clustarr news", s.Device.FriendlyNameOrDefault("news"))
 assert.Equal(t, int64(8<<20), s.Stream.BufferOrDefault())
 assert.Equal(t, 10*time.Second, s.Stream.LingerOrDefault())
 assert.Equal(t, int32(60), clustarrv1.ChannelEPG{Dummy: true}.DummyMinutesOrDefault())
}

func TestASetValueWinsOverItsDefault(t *testing.T) {
 q := resource.MustParse("4Mi")
 s := clustarrv1.IPTVProviderSpec{
  Enabled:            new(false),
  EPGSource:          clustarrv1.EPGSourcePMS,
  ChannelNumberStart: new(int32(1)),
  Stream:             clustarrv1.StreamSpec{Buffer: &q, Linger: &metav1.Duration{Duration: time.Second}},
 }
 assert.False(t, s.EnabledOrDefault())
 assert.Equal(t, clustarrv1.EPGSourcePMS, s.EPGSourceOrDefault())
 assert.Equal(t, int32(1), s.ChannelNumberStartOrDefault())
 assert.Equal(t, int64(4<<20), s.Stream.BufferOrDefault())
 assert.Equal(t, time.Second, s.Stream.LingerOrDefault())
}
```

- [ ] **Step 2: Run it.** `go test ./api/clustarr/...`. Expected: FAIL, because the package does not exist.

- [ ] **Step 3: Write the types.**
  - **`groupversion_info.go`:** copy `api/download/v1alpha1/groupversion_info.go` and change three things:
    - `+kubebuilder:ac:output:package=../../applyconfiguration/clustarr`;
    - `+groupName=clustarr.io`;
    - `GroupVersion = schema.GroupVersion{Group: "clustarr.io", Version: "v1alpha1"}`.
  - **The provider:** the spec's §3 code block is the IPTVProviderSpec,
    PlaylistSource, XMLTVSource, ChannelFilter, DeviceSpec and StreamSpec,
    verbatim, with these additions:
    - **IPTVProviderSpec:**
      - `Playlist`: `+required`;
      - `Tuners`: `+required`, Minimum 1, Maximum 64;
      - `EPGSource`: `+optional`, `+kubebuilder:validation:Enum=XEPG;PMS`;
      - `Guides` and `Filters`: `+optional` with their `listType=map` markers.
    - **PlaylistSource and XMLTVSource:** on each struct,
      `// +kubebuilder:validation:XValidation:rule="has(self.url) != has(self.urlFrom)",message="set exactly one of url and urlFrom"`.
      `URL` takes `+kubebuilder:validation:MaxLength=4096`.
    - **XMLTVSource.Name:** `+kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$``.
    - **ChannelFilter:**
      - `Name`: MaxLength 63;
      - `Match`: MaxLength 256;
      - `Include` and `Exclude` items: `+kubebuilder:validation:items:MaxLength=128`.
  - **Provider status:**

    ```go
    type IPTVProviderStatus struct {
     ObservedGeneration int64 `json:"observedGeneration,omitempty"`
     // +listType=map
     // +listMapKey=type
     // +kubebuilder:validation:MaxItems=8
     Conditions []metav1.Condition `json:"conditions,omitempty"`
     DeviceID string `json:"deviceID,omitempty"`
     Address  string `json:"address,omitempty"`
     GuideURL string `json:"guideURL,omitempty"`
     Playlist *PlaylistStatus `json:"playlist,omitempty"`
     // +listType=map
     // +listMapKey=name
     // +kubebuilder:validation:MaxItems=16
     Guides    []GuideStatus `json:"guides,omitempty"`
     Lineup    *LineupStatus `json:"lineup,omitempty"`
     GuideHash string        `json:"guideHash,omitempty"`
     Tuners    *TunerStatus  `json:"tuners,omitempty"`
    }
    type PlaylistStatus struct {
     Entries    int32        `json:"entries"`
     Groups     int32        `json:"groups"`
     Candidates int32        `json:"candidates"`
     FetchedAt  *metav1.Time `json:"fetchedAt,omitempty"`
     Hash       string       `json:"hash,omitempty"`
     // +kubebuilder:validation:MaxLength=1024
     Error string `json:"error,omitempty"`
    }
    type GuideStatus struct {
     Name       string       `json:"name"`
     Channels   int32        `json:"channels"`
     Programmes int32        `json:"programmes"`
     FetchedAt  *metav1.Time `json:"fetchedAt,omitempty"`
     // +kubebuilder:validation:MaxLength=1024
     Error string `json:"error,omitempty"`
    }
    type LineupStatus struct {
     Active   int32  `json:"active"`
     Unmapped int32  `json:"unmapped"`
     Missing  int32  `json:"missing"`
     Hash     string `json:"hash,omitempty"`
    }
    type TunerStatus struct {
     Total int32 `json:"total"`
     InUse int32 `json:"inUse"`
    }
    ```

    The counters are non-`omitempty` on purpose: zero is meaningful.
  - **The provider's root markers:** copy DownloadClient's:
    - `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, `+kubebuilder:ac:generate=true`;
    - `+kubebuilder:resource:scope=Namespaced,shortName=iptv,categories=clustarr`;
    - printcolumns:
      - Tuners `.spec.tuners`;
      - Active `.status.lineup.active`;
      - Candidates `.status.playlist.candidates`;
      - EPG `.spec.epgSource`;
      - Ready `.status.conditions[?(@.type=='Ready')].status`;
      - Address `.status.address`;
      - Age.
    - The List type, and `init()` registering both.
  - **`iptvchannel_types.go`:** `IPTVChannelSpec` is the spec's §3 `IPTVChannelSpec` and `ChannelEPG`, verbatim, plus:
    - **`ProviderRef`:**
      `+required`, MinLength 1, MaxLength 253,
      `// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"`.
    - **`Key`:** `+required`, MinLength 1, MaxLength 64, and the same
      immutability rule with message "key is immutable".
    - **`Name`, `Group`:** MaxLength 256.
    - **`Logo`:** MaxLength 4096.
    - **`ChannelEPG`, on the struct:**

      ```go
      // +kubebuilder:validation:XValidation:rule="(has(self.guide) && self.guide != '') != (has(self.dummy) && self.dummy)",message="set exactly one of guide and dummy"
      // +kubebuilder:validation:XValidation:rule="!has(self.guide) || self.guide == '' || (has(self.channelID) && self.channelID != '')",message="channelID is required with guide"
      ```

      plus `DummyMinutes`: `+kubebuilder:validation:Enum=30;60;120;180`.
    - **The status:**

      ```go
      type IPTVChannelStatus struct {
       ObservedGeneration int64 `json:"observedGeneration,omitempty"`
       // +listType=map
       // +listMapKey=type
       // +kubebuilder:validation:MaxItems=4
       Conditions []metav1.Condition `json:"conditions,omitempty"`
       // Guide is the mapping actually used: "<guide>/<channelID>", "dummy/<minutes>", or "" under PMS.
       // +kubebuilder:validation:MaxLength=512
       Guide string `json:"guide,omitempty"`
      }
      ```

    - **Root markers:** `shortName=iptvch`, `categories=clustarr`. Printcolumns:
      - Provider `.spec.providerRef`;
      - Number `.spec.number`;
      - Name `.spec.name`;
      - Active `.spec.active`;
      - Guide `.status.guide`;
      - Ready `.status.conditions[?(@.type=='Ready')].reason`.
  - **`defaults.go`:** the accessors above, each a nil or zero check
    returning the default. Put the condition types and reasons there as
    `const` blocks.

- [ ] **Step 4: Generate.** `make generate manifests`. Expected:
  - the deepcopy, the apply configurations and two CRD files are created
    (`git status --short`);
  - no change to any other group's output.

  Then:
  - add the two CRD file names under a `# clustarr.io` comment in
    `config/crd/kustomization.yaml`;
  - `git add` every new file.

- [ ] **Step 5: Run the accessor test.** `go test ./api/clustarr/...`. Expected: PASS.

- [ ] **Step 6: Write the failing CEL test.** `pkg/crdcheck/livetv_cel_test.go`, modelled on `download_cel_test.go`: envtest with `CRDDirectoryPaths: {"../../config/crd/bases"}` and a dynamic client, so absent keys stay absent.

```go
func TestLiveTVCEL(t *testing.T) {
 if os.Getenv("KUBEBUILDER_ASSETS") == "" {
  t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
 }
 env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
 cfg, err := env.Start()
 require.NoError(t, err)
 t.Cleanup(func() { require.NoError(t, env.Stop()) })
 dyn, err := dynamic.NewForConfig(cfg)
 require.NoError(t, err)
 ctx := context.Background()
 providers := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvproviders"}).Namespace("default")
 channels := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvchannels"}).Namespace("default")

 provider := func(name string, playlist map[string]any) *unstructured.Unstructured {
  return &unstructured.Unstructured{Object: map[string]any{
   "apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVProvider",
   "metadata": map[string]any{"name": name},
   "spec":     map[string]any{"tuners": int64(2), "playlist": playlist},
  }}
 }
 _, err = providers.Create(ctx, provider("by-url", map[string]any{"url": "http://p.example/get.php"}), metav1.CreateOptions{})
 require.NoError(t, err, "url alone is accepted")
 _, err = providers.Create(ctx, provider("by-secret", map[string]any{"urlFrom": map[string]any{"name": "s", "key": "playlist-url"}}), metav1.CreateOptions{})
 require.NoError(t, err, "urlFrom alone is accepted")
 _, err = providers.Create(ctx, provider("both", map[string]any{"url": "http://p.example", "urlFrom": map[string]any{"name": "s", "key": "k"}}), metav1.CreateOptions{})
 require.ErrorContains(t, err, "set exactly one of url and urlFrom")
 _, err = providers.Create(ctx, provider("neither", map[string]any{}), metav1.CreateOptions{})
 require.ErrorContains(t, err, "set exactly one of url and urlFrom")

 channel := func(name string, epg map[string]any) *unstructured.Unstructured {
  spec := map[string]any{"providerRef": "by-url", "key": "t:bbc1.uk", "number": "1001", "active": true}
  if epg != nil {
   spec["epg"] = epg
  }
  return &unstructured.Unstructured{Object: map[string]any{
   "apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVChannel",
   "metadata": map[string]any{"name": name}, "spec": spec,
  }}
 }
 _, err = channels.Create(ctx, channel("guide", map[string]any{"guide": "main", "channelID": "bbc1.uk"}), metav1.CreateOptions{})
 require.NoError(t, err)
 _, err = channels.Create(ctx, channel("dummy", map[string]any{"dummy": true}), metav1.CreateOptions{})
 require.NoError(t, err)
 _, err = channels.Create(ctx, channel("both-epg", map[string]any{"guide": "main", "channelID": "x", "dummy": true}), metav1.CreateOptions{})
 require.ErrorContains(t, err, "set exactly one of guide and dummy")
 _, err = channels.Create(ctx, channel("no-id", map[string]any{"guide": "main"}), metav1.CreateOptions{})
 require.ErrorContains(t, err, "channelID is required with guide")
 _, err = channels.Create(ctx, channel("bad-number", nil), metav1.CreateOptions{})
 require.NoError(t, err)

 got, err := channels.Get(ctx, "guide", metav1.GetOptions{})
 require.NoError(t, err)
 require.NoError(t, unstructured.SetNestedField(got.Object, "t:other", "spec", "key"))
 _, err = channels.Update(ctx, got, metav1.UpdateOptions{})
 require.ErrorContains(t, err, "key is immutable")
 got, err = channels.Get(ctx, "guide", metav1.GetOptions{})
 require.NoError(t, err)
 require.NoError(t, unstructured.SetNestedField(got.Object, "other", "spec", "providerRef"))
 _, err = channels.Update(ctx, got, metav1.UpdateOptions{})
 require.ErrorContains(t, err, "providerRef is immutable")

 bad := channel("bad-number-2", nil)
 require.NoError(t, unstructured.SetNestedField(bad.Object, "10-01", "spec", "number"))
 _, err = channels.Create(ctx, bad, metav1.CreateOptions{})
 require.Error(t, err, "a number is digits with an optional .minor")
}
```

- [ ] **Step 7: Run it.** `KUBEBUILDER_ASSETS=$(setup-envtest use -p path 1.37.0) go test ./pkg/crdcheck/ -run 'TestLiveTVCEL|TestEveryStatusListIsCapped|TestNoCRDDefaultIsUnreachableFromGo' -v`.
  - Expected: PASS, since the markers were written in Step 3.
  - Then falsify it: delete the `has(self.url) != has(self.urlFrom)` marker, run `make manifests`, and watch `TestLiveTVCEL` fail on "both". Restore the marker and run `make manifests` again.

- [ ] **Step 8: Scheme and field managers.**
  - Add `clustarrv1alpha1.AddToScheme` to `AddToSchemeFuncs` in
    `pkg/k8s/scheme.go`.
  - Add the group to both maps in `pkg/k8s/scheme_test.go`: an
    `&clustarrv1alpha1.IPTVProvider{}` entry in
    `TestSchemeKnowsEveryGroupAServiceTouches`, and `"clustarr.io"` in
    `TestSchemeCoversEveryClustarrGroup`'s `want`.
  - Add both constants to `pkg/k8s/fieldmanager.go`, with doc comments:
    > "The IPTVProvider controller, sole writer of IPTVProvider.status
    > (Live TV spec §3.2)"
    >
    > "The IPTVChannel controller, sole writer of IPTVChannel.status (§3.0)"
  - Add both to `FieldManagers()`, and to `fieldmanager_test.go`'s `want`.
    - **Ruling:** that list already lacks `ManagerAutoscale`. Add it too if
      the test is otherwise red on that, and ledger it.
  - In `test/guards/rbac_markers_test.go`, make `apiGroupAliases` map dir
    `clustarr` to `clustarr.io`, not `clustarr.clustarr.io`.
  - Run: `go test ./pkg/k8s/ -run 'TestScheme|TestFieldManagers' -v`.
    Expected: PASS.

- [ ] **Step 9: Commit.**

```bash
git add api/clustarr api/applyconfiguration/clustarr config/crd/bases/clustarr.io_iptvproviders.yaml config/crd/bases/clustarr.io_iptvchannels.yaml pkg/crdcheck/livetv_cel_test.go
git commit -m "feat(api): clustarr.io/v1alpha1 IPTVProvider and IPTVChannel -- Live TV spec §3; CEL for url/urlFrom, guide/dummy, immutable providerRef and key; two field managers" -- api/clustarr api/applyconfiguration/clustarr config/crd pkg/crdcheck/livetv_cel_test.go pkg/k8s/scheme.go pkg/k8s/scheme_test.go pkg/k8s/fieldmanager.go pkg/k8s/fieldmanager_test.go test/guards/rbac_markers_test.go
```

### Task 2: `pkg/iptv`: Redact and the M3U parser with `EntryKey`

**Files:**

- Create:
  - `pkg/iptv/doc.go`
  - `pkg/iptv/redact.go`
  - `pkg/iptv/redact_test.go`
  - `pkg/iptv/m3u/parse.go`
  - `pkg/iptv/m3u/parse_test.go`
  - `test/data/iptv/m3u_plus.m3u`

**Interfaces:**

- Produces:
  - `iptv.Redact(raw string) string`
  - `iptv.RedactError(err error) string`
  - `m3u.Entry{Key, Name, Group, TvgID, TvgName, Logo, URL, UserAgent string}`
  - `m3u.Playlist{GuideURLs []string; Entries []Entry}`
  - `m3u.Parse(r io.Reader) (Playlist, error)`
  - `m3u.MaxBytes int64 = 64 << 20`, `m3u.MaxEntries = 200000`
  - `m3u.ErrResponseTooLarge`, `m3u.ErrTooManyEntries`
  - `m3u.NameKey(group, name string) string`

- [ ] **Step 1: Write the fixture** `test/data/iptv/m3u_plus.m3u`, exactly this, with LF line ends:

```
#EXTM3U url-tvg="http://epg.example/guide.xml.gz" x-tvg-url="http://epg.example/alt.xml"
#EXTINF:-1 tvg-id="bbc1.uk" tvg-name="BBC One HD" tvg-logo="http://logo.example/bbc1.png" group-title="UK | Entertainment",BBC One HD
http://provider.example/live/user/pass/1001.ts
#EXTINF:-1 tvg-id="" tvg-name="News, Live" group-title="UK | News",News, Live
#EXTVLCOPT:http-user-agent=VLC/3.0.20
http://provider.example/live/user/pass/1002.ts
#EXTINF:-1 tvg-id=dup.id group-title=Sports,Sports 1
http://provider.example/live/user/pass/1003.ts
#EXTINF:-1 tvg-id="dup.id" group-title="Sports",Sports 2
http://provider.example/live/user/pass/1004.ts
#EXTINF:-1,No Attributes
#EXTGRP:Misc
http://provider.example/live/user/pass/1005.ts

#EXTINF:-1 tvg-id="nogroup",Orphan
http://provider.example/live/user/pass/1006.ts
```

- [ ] **Step 2: Write the failing tests.**

```go
// pkg/iptv/redact_test.go
package iptv_test

func TestRedact(t *testing.T) {
 for in, want := range map[string]string{
  "http://user:pw@provider.example/get.php?username=u&password=p&type=m3u_plus": "http://provider.example/get.php",
  "http://provider.example/live/user/pass/1001.ts":                             "http://provider.example/…/1001.ts",
  "http://provider.example:8080/":                                              "http://provider.example:8080/",
  "https://epg.example/guide.xml.gz?token=abc#frag":                            "https://epg.example/guide.xml.gz",
  "not a url\x7f":                                                              "<redacted url>",
 } {
  assert.Equal(t, want, iptv.Redact(in), in)
 }
}

func TestRedactErrorRewritesTheURLInsideAURLError(t *testing.T) {
 err := &url.Error{Op: "Get", URL: "http://provider.example/live/user/pass/1001.ts", Err: errors.New("connection refused")}
 got := iptv.RedactError(fmt.Errorf("fetch playlist: %w", err))
 assert.Equal(t, `fetch playlist: Get "http://provider.example/…/1001.ts": connection refused`, got)
 assert.NotContains(t, got, "pass")
}
```

```go
// pkg/iptv/m3u/parse_test.go
package m3u_test

func fixture(t *testing.T) []byte {
 b, err := os.ReadFile("../../../test/data/iptv/m3u_plus.m3u")
 require.NoError(t, err)
 return b
}

func TestParseReadsEveryAttributeForm(t *testing.T) {
 pl, err := m3u.Parse(bytes.NewReader(fixture(t)))
 require.NoError(t, err)
 assert.Equal(t, []string{"http://epg.example/guide.xml.gz", "http://epg.example/alt.xml"}, pl.GuideURLs)
 require.Len(t, pl.Entries, 6)
 assert.Equal(t, m3u.Entry{Key: "t:bbc1.uk", Name: "BBC One HD", Group: "UK | Entertainment", TvgID: "bbc1.uk",
  TvgName: "BBC One HD", Logo: "http://logo.example/bbc1.png", URL: "http://provider.example/live/user/pass/1001.ts"}, pl.Entries[0])
 assert.Equal(t, "News, Live", pl.Entries[1].Name, "a comma inside the name survives")
 assert.Equal(t, "News, Live", pl.Entries[1].TvgName, "a comma inside quotes survives")
 assert.Equal(t, "VLC/3.0.20", pl.Entries[1].UserAgent)
 assert.Equal(t, "Sports", pl.Entries[2].Group, "an unquoted attribute is read")
 assert.Equal(t, "Misc", pl.Entries[4].Group, "#EXTGRP gives the group when group-title is absent")
 assert.Equal(t, "t:nogroup", pl.Entries[5].Key, "a blank line between entries is skipped")
}

func TestKeysAreTvgIDWhenUniqueElseNameBased(t *testing.T) {
 pl, err := m3u.Parse(bytes.NewReader(fixture(t)))
 require.NoError(t, err)
 assert.Equal(t, m3u.NameKey("UK | News", "News, Live"), pl.Entries[1].Key, "an empty tvg-id is name-based")
 assert.Equal(t, m3u.NameKey("Sports", "Sports 1"), pl.Entries[2].Key, "a duplicated tvg-id is name-based")
 assert.Equal(t, m3u.NameKey("Sports", "Sports 2"), pl.Entries[3].Key)
 assert.NotEqual(t, pl.Entries[2].Key, pl.Entries[3].Key)
 assert.Regexp(t, `^n:[a-z2-7]{26}$`, pl.Entries[2].Key)
 for _, e := range pl.Entries {
  assert.LessOrEqual(t, len(e.Key), 64)
 }
}

func TestAKeyIsStableAcrossStreamURLTokenChanges(t *testing.T) {
 a, err := m3u.Parse(bytes.NewReader(fixture(t)))
 require.NoError(t, err)
 b, err := m3u.Parse(bytes.NewReader(bytes.ReplaceAll(fixture(t), []byte("/user/pass/"), []byte("/user/rotated/"))))
 require.NoError(t, err)
 for i := range a.Entries {
  assert.Equal(t, a.Entries[i].Key, b.Entries[i].Key)
 }
}

func TestABOMAndCRLFParseTheSame(t *testing.T) {
 want, err := m3u.Parse(bytes.NewReader(fixture(t)))
 require.NoError(t, err)
 crlf := append([]byte("﻿"), bytes.ReplaceAll(fixture(t), []byte("\n"), []byte("\r\n"))...)
 got, err := m3u.Parse(bytes.NewReader(crlf))
 require.NoError(t, err)
 assert.Equal(t, want, got)
}

func TestALongTvgIDFallsBackToANameKey(t *testing.T) {
 long := strings.Repeat("x", 63)
 pl, err := m3u.Parse(strings.NewReader("#EXTM3U\n#EXTINF:-1 tvg-id=\"" + long + "\",Long\nhttp://p.example/1.ts\n"))
 require.NoError(t, err)
 assert.Equal(t, m3u.NameKey("", "Long"), pl.Entries[0].Key)
}

func TestTheCapsAreErrors(t *testing.T) {
 _, err := m3u.Parse(io.LimitReader(neverEnding{}, m3u.MaxBytes+2))
 require.ErrorIs(t, err, m3u.ErrResponseTooLarge)
 var b strings.Builder
 b.WriteString("#EXTM3U\n")
 for i := 0; i <= m3u.MaxEntries; i++ {
  fmt.Fprintf(&b, "#EXTINF:-1,C%d\nhttp://p.example/%d.ts\n", i, i)
 }
 _, err = m3u.Parse(strings.NewReader(b.String()))
 require.ErrorIs(t, err, m3u.ErrTooManyEntries)
}

func TestAMissingHeaderIsAnError(t *testing.T) {
 _, err := m3u.Parse(strings.NewReader("<html>login</html>"))
 require.ErrorContains(t, err, "not an M3U playlist")
}

func BenchmarkParseFiftyThousand(b *testing.B) { /* builds 50,000 entries like TestTheCapsAreErrors; b.ReportAllocs(); parse once per b.N */ }

type neverEnding struct{}

func (neverEnding) Read(p []byte) (int, error) {
 for i := range p {
  p[i] = '#'
 }
 return len(p), nil
}
```

Write the benchmark's body out in full: the loop of `TestTheCapsAreErrors` with 50,000 entries, then `for b.Loop() { m3u.Parse(...) }`.

- [ ] **Step 3: Run them.** `go test ./pkg/iptv/...`. Expected: FAIL to compile, because the packages do not exist.

- [ ] **Step 4: Implement.**
  - **`redact.go`:**
    - `Redact`: `url.Parse`. On error, or with an empty `Host`, return
      `"<redacted url>"`. Drop the userinfo, query and fragment. Split
      `Path` on `/`, dropping empties. With two or more segments, the path
      is `"/…/" + last`; with one, `"/" + it`; with none, `"/"`.
    - `RedactError`: walk the error chain for every `*url.Error` with
      `errors.As`. Replace each occurrence of its `URL` in `err.Error()`
      with `Redact(URL)`, using `strings.ReplaceAll`.
  - **`parse.go`:**
    - **Reading:** wrap the reader in `io.LimitReader(r, MaxBytes+1)`.
      Read it with a `bufio.Scanner` with a 1 MiB buffer, counting bytes;
      past `MaxBytes`, return `ErrResponseTooLarge`.
    - **The header:** strip a leading `﻿` and trim `\r`. The first
      non-empty line must start with `#EXTM3U`, else
      `errors.New("not an M3U playlist")`.
    - **Header attributes:** parse the `#EXTM3U` line's attributes with the
      same attribute scanner as `#EXTINF`. `url-tvg` and `x-tvg-url`, in
      that order and when non-empty, become `GuideURLs`.
    - **The attribute scanner:**
      `func attrs(s string) (map[string]string, string)` reads
      `key="value"` (a value may contain commas) or `key=value` (ending at
      a space or comma), from after `#EXTINF:<duration>`. The remainder
      after the first comma that is outside quotes is the name.
    - **A pending entry:** `#EXTINF` starts one. `#EXTGRP:` sets its group
      when there is no `group-title`. `#EXTVLCOPT:http-user-agent=` sets
      its user agent.
    - **Closing an entry:** the next non-`#`, non-empty line is its URL,
      which closes it. Past `MaxEntries`, return `ErrTooManyEntries`.
    - **Keys:** count tvg-ids, then give each entry
      `"t:"+TvgID` when the id is non-empty, unique and at most 62 bytes;
      otherwise `NameKey(Group, Name)`.
    - **`NameKey`:** `"n:" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sha256.Sum256(group + "\x00" + name)[:]))[:26]`.

- [ ] **Step 5: Run.** `go test ./pkg/iptv/... && go test ./pkg/iptv/m3u -run '^$' -bench . -benchtime 1x`. Expected: PASS, and the benchmark under 1 s per op.

- [ ] **Step 6: Commit.**

```bash
git add pkg/iptv test/data/iptv/m3u_plus.m3u
git commit -m "feat(iptv): M3U parser with stable entry keys and URL redaction -- Live TV spec §4.1" -- pkg/iptv test/data/iptv/m3u_plus.m3u
```

### Task 3: `pkg/iptv/filter`

**Files:**

- Create: `pkg/iptv/filter/filter.go`, `pkg/iptv/filter/filter_test.go`

**Interfaces:**

- Consumes: `m3u.Entry`
- Produces:
  - `filter.Rule{Name, Type, Match string; Include, Exclude []string; CaseSensitive, Enabled bool}`, where `Type` is `"groupTitle"` or `"custom"`;
  - `filter.Matches(r Rule, e m3u.Entry) bool`;
  - `filter.Candidates(entries []m3u.Entry, rules []Rule) []m3u.Entry`, which keeps playlist order and holds no duplicates;
  - `filter.Preview(entries []m3u.Entry, r Rule) int`.

- [ ] **Step 1: Write the failing test.**

```go
package filter_test

func entries() []m3u.Entry {
 return []m3u.Entry{
  {Key: "a", Name: "BBC One HD", Group: "UK | Entertainment"},
  {Key: "b", Name: "BBC Two SD", Group: "UK | Entertainment"},
  {Key: "c", Name: "Sky Sports Main Event", Group: "UK | Sports"},
  {Key: "d", Name: "sky news", Group: "UK | News"},
 }
}

func TestMatches(t *testing.T) {
 for _, tc := range []struct {
  name string
  rule filter.Rule
  want []string
 }{
  {"a group title matches its group exactly", filter.Rule{Type: "groupTitle", Match: "UK | Entertainment", Enabled: true}, []string{"a", "b"}},
  {"a group title is case-insensitive by default", filter.Rule{Type: "groupTitle", Match: "uk | entertainment", Enabled: true}, []string{"a", "b"}},
  {"a case-sensitive group title must match case", filter.Rule{Type: "groupTitle", Match: "uk | entertainment", CaseSensitive: true, Enabled: true}, nil},
  {"a group title is not a substring", filter.Rule{Type: "groupTitle", Match: "UK", Enabled: true}, nil},
  {"custom matches a substring of the name", filter.Rule{Type: "custom", Match: "sky", Enabled: true}, []string{"c", "d"}},
  {"include needs one of its words", filter.Rule{Type: "groupTitle", Match: "UK | Entertainment", Include: []string{"HD", "FHD"}, Enabled: true}, []string{"a"}},
  {"exclude drops any of its words", filter.Rule{Type: "custom", Match: "sky", Exclude: []string{"news"}, Enabled: true}, []string{"c"}},
  {"a disabled rule matches nothing", filter.Rule{Type: "custom", Match: "sky"}, nil},
 } {
  t.Run(tc.name, func(t *testing.T) {
   var got []string
   for _, e := range entries() {
    if filter.Matches(tc.rule, e) {
     got = append(got, e.Key)
    }
   }
   assert.Equal(t, tc.want, got)
   assert.Equal(t, len(tc.want), filter.Preview(entries(), tc.rule))
  })
 }
}

func TestCandidatesAreTheUnionInPlaylistOrder(t *testing.T) {
 got := filter.Candidates(entries(), []filter.Rule{
  {Type: "custom", Match: "sky", Enabled: true},
  {Type: "groupTitle", Match: "UK | Sports", Enabled: true},
  {Type: "custom", Match: "BBC One", Enabled: true},
 })
 var keys []string
 for _, e := range got {
  keys = append(keys, e.Key)
 }
 assert.Equal(t, []string{"a", "c", "d"}, keys, "each entry once, in playlist order")
 assert.Empty(t, filter.Candidates(entries(), nil), "with no filter nothing is a candidate (xTeVe's rule)")
}
```

- [ ] **Step 2: Run.** `go test ./pkg/iptv/filter`. Expected: FAIL to compile.
- [ ] **Step 3: Implement.**
  - **`fold`:** returns `strings.ToLower(s)` unless `CaseSensitive`.
  - **`Matches`:**
    - false when not `Enabled`;
    - `groupTitle`: `fold(e.Group) == fold(r.Match)`;
    - `custom`: `strings.Contains(fold(e.Name), fold(r.Match))`;
    - a non-empty `Include` needs some word `w` with
      `Contains(fold(name), fold(w))`;
    - any `Exclude` word found in the name means false.
  - **`Candidates`:** one pass over the entries, keeping an entry when any
    rule matches.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit.** `git add pkg/iptv/filter && git commit -m "feat(iptv): xTeVe filters -- Live TV spec §4.2" -- pkg/iptv/filter`

### Task 4: `pkg/iptv/xmltv`: the parser, `BuildGuide`, `AutoMap`

**Files:**

- Create:
  - `pkg/iptv/xmltv/parse.go`
  - `pkg/iptv/xmltv/build.go`
  - `pkg/iptv/xmltv/automap.go`
  - `pkg/iptv/xmltv/xmltv_test.go`
  - `test/data/iptv/guide.xml`
  - `test/data/iptv/guide_built.golden.xml` (written by `-update`, then reviewed)

**Interfaces:**

- Produces:
  - `xmltv.Channel{ID string; DisplayNames []string; Icon string}`
  - `xmltv.Programme{Channel string; Start, Stop time.Time; Inner []byte}`.
    `Inner` is the element's inner XML, kept verbatim.
  - `xmltv.Guide{Channels []Channel; Programmes map[string][]Programme}`
  - `xmltv.MaxBytes int64 = 1 << 30`, `xmltv.ErrResponseTooLarge`
  - `xmltv.Parse(r io.Reader, want map[string]bool, from, to time.Time) (Guide, error)`.
    A nil `want` parses channels only. Gzip is detected by its magic
    bytes.
  - `xmltv.Mapping{Guide, ChannelID string}`
  - `xmltv.LineupChannel{Number, Name, Logo string; Map *Mapping; DummyMinutes int; TimeshiftMinutes int}`.
    `Map` nil with `DummyMinutes > 0` is a dummy.
  - `xmltv.BuildGuide(w io.Writer, lineup []LineupChannel, guides map[string]Guide, from time.Time, days int) error`
  - `xmltv.AutoMap(tvgID, name string, guides map[string]Guide) (Mapping, bool)`

- [ ] **Step 1: Write the fixture** `test/data/iptv/guide.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE tv SYSTEM "xmltv.dtd">
<tv generator-info-name="fixture">
  <channel id="bbc1.uk"><display-name lang="en">BBC One HD</display-name><icon src="http://logo.example/bbc1.png"/></channel>
  <channel id="news.uk"><display-name>News Live</display-name><display-name>News, Live</display-name></channel>
  <programme start="20261007180000 +0000" stop="20261007190000 +0000" channel="bbc1.uk"><title lang="en">Six O'Clock News</title><desc lang="en">Headlines &amp; weather.</desc><category>News</category></programme>
  <programme start="20261007190000 +0000" stop="20261007193000 +0000" channel="bbc1.uk"><title>The One Show</title></programme>
  <programme start="20261007180000 +0000" stop="20261007183000 +0000" channel="news.uk"><title>Bulletin</title></programme>
  <programme start="20261020180000 +0000" stop="20261020190000 +0000" channel="bbc1.uk"><title>Too Far Ahead</title></programme>
  <programme start="20261006100000 +0000" stop="20261006110000 +0000" channel="bbc1.uk"><title>Already Over</title></programme>
</tv>
```

- [ ] **Step 2: Write the failing tests.**

```go
package xmltv_test

var update = flag.Bool("update", false, "rewrite golden files")

var from = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

func open(t *testing.T) []byte {
 b, err := os.ReadFile("../../../test/data/iptv/guide.xml")
 require.NoError(t, err)
 return b
}

func TestParseReadsChannelsAndTheWantedProgrammesInTheWindow(t *testing.T) {
 g, err := xmltv.Parse(bytes.NewReader(open(t)), map[string]bool{"bbc1.uk": true}, from, from.AddDate(0, 0, 7))
 require.NoError(t, err)
 assert.Equal(t, []xmltv.Channel{
  {ID: "bbc1.uk", DisplayNames: []string{"BBC One HD"}, Icon: "http://logo.example/bbc1.png"},
  {ID: "news.uk", DisplayNames: []string{"News Live", "News, Live"}},
 }, g.Channels)
 require.Len(t, g.Programmes["bbc1.uk"], 2, "only the window, only wanted channels")
 assert.Empty(t, g.Programmes["news.uk"])
 p := g.Programmes["bbc1.uk"][0]
 assert.Equal(t, time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC), p.Start.UTC())
 assert.Contains(t, string(p.Inner), `<desc lang="en">Headlines &amp; weather.</desc>`, "inner XML is kept verbatim, escapes included")
}

func TestParseReadsGzip(t *testing.T) {
 var buf bytes.Buffer
 zw := gzip.NewWriter(&buf)
 _, _ = zw.Write(open(t))
 require.NoError(t, zw.Close())
 g, err := xmltv.Parse(&buf, nil, from, from.AddDate(0, 0, 7))
 require.NoError(t, err)
 assert.Len(t, g.Channels, 2)
}

func TestParseCapsTheUncompressedSize(t *testing.T) {
 r := io.MultiReader(strings.NewReader("<tv>"), &repeat{b: []byte("<!-- padding -->"), n: xmltv.MaxBytes / 16 + 1})
 _, err := xmltv.Parse(r, nil, from, from.AddDate(0, 0, 7))
 require.ErrorIs(t, err, xmltv.ErrResponseTooLarge)
}

func TestBuildGuide(t *testing.T) {
 g, err := xmltv.Parse(bytes.NewReader(open(t)), map[string]bool{"bbc1.uk": true}, from, from.AddDate(0, 0, 7))
 require.NoError(t, err)
 lineup := []xmltv.LineupChannel{
  {Number: "1002", Name: "Dummy Chan", DummyMinutes: 60},
  {Number: "1001", Name: "BBC One", Logo: "http://logo.example/bbc1.png", Map: &xmltv.Mapping{Guide: "main", ChannelID: "bbc1.uk"}, TimeshiftMinutes: 60},
 }
 var out bytes.Buffer
 require.NoError(t, xmltv.BuildGuide(&out, lineup, map[string]xmltv.Guide{"main": g}, from, 7))
 s := out.String()
 assert.Contains(t, s, `<channel id="1001"><display-name>BBC One</display-name><icon src="http://logo.example/bbc1.png"></icon></channel>`)
 assert.Contains(t, s, `<programme start="20261007190000 +0000" stop="20261007200000 +0000" channel="1001">`, "timeshifted by an hour")
 assert.Less(t, strings.Index(s, `channel id="1001"`), strings.Index(s, `channel id="1002"`), "channels sorted by number")
 assert.Equal(t, 7*24, strings.Count(s, `channel="1002"`), "one dummy block per hour for seven days")
 var again bytes.Buffer
 require.NoError(t, xmltv.BuildGuide(&again, lineup, map[string]xmltv.Guide{"main": g}, from, 7))
 assert.Equal(t, s, again.String(), "the same input gives the same bytes")
 golden := "../../../test/data/iptv/guide_built.golden.xml"
 if *update {
  require.NoError(t, os.WriteFile(golden, out.Bytes(), 0o644))
 }
 want, err := os.ReadFile(golden)
 require.NoError(t, err)
 assert.Equal(t, string(want), s)
 _, err = xmltv.Parse(bytes.NewReader(out.Bytes()), map[string]bool{"1001": true}, from, from.AddDate(0, 0, 7))
 require.NoError(t, err, "the built guide parses as XMLTV")
}

func TestAutoMap(t *testing.T) {
 g, err := xmltv.Parse(bytes.NewReader(open(t)), nil, from, from.AddDate(0, 0, 7))
 require.NoError(t, err)
 guides := map[string]xmltv.Guide{"main": g}
 m, ok := xmltv.AutoMap("bbc1.uk", "anything", guides)
 assert.True(t, ok)
 assert.Equal(t, xmltv.Mapping{Guide: "main", ChannelID: "bbc1.uk"}, m, "tvg-id first")
 m, ok = xmltv.AutoMap("", "NEWS LIVE", guides)
 assert.True(t, ok)
 assert.Equal(t, xmltv.Mapping{Guide: "main", ChannelID: "news.uk"}, m, "then a normalised display name")
 _, ok = xmltv.AutoMap("", "Unknown Channel", guides)
 assert.False(t, ok)
}

type repeat struct {
 b []byte
 n int64
}

func (r *repeat) Read(p []byte) (int, error) {
 if r.n == 0 {
  return 0, io.EOF
 }
 r.n--
 return copy(p, r.b), nil
}
```

- [ ] **Step 3: Run.** `go test ./pkg/iptv/xmltv`. Expected: FAIL to compile.
- [ ] **Step 4: Implement.**
  - **`Parse`:**
    - **Opening:** peek 2 bytes with `bufio.Reader`. On `0x1f 0x8b`, wrap
      in `gzip.NewReader`. Then
      `io.LimitReader(…, MaxBytes+1)` behind a counting reader that
      returns `ErrResponseTooLarge` past `MaxBytes`.
    - **Decoding:** an `xml.Decoder` with `Strict = false` and
      `CharsetReader` passing UTF-8 and ISO-8859-1 through
      (`golang.org/x/net/html/charset` only if already in go.mod; else
      ISO-8859-1 by a byte-to-rune copy).
    - **Elements:**
      - on `<channel>`: `DecodeElement` into `struct{ID string`xml:"id,attr"`; DisplayNames []string`xml:"display-name"`; Icon struct{Src string`xml:"src,attr"`}`xml:"icon"`}`;
      - on `<programme>`: when `want[channel]`, `DecodeElement` into
        `struct{Start, Stop, Channel string attrs; Inner []byte`xml:",innerxml"`}`.
        Otherwise skip it with `d.Skip()`.
    - **Times:** parse with layout `"20060102150405 -0700"`, falling back
      to `"20060102150405"` as UTC.
    - **The window:** keep a programme when `Stop.After(from) && Start.Before(to)`.
  - **`BuildGuide`:**
    - Sort the lineup by number, numerically: by the major part, then the
      minor.
    - Write `<?xml version="1.0" encoding="UTF-8"?>\n<tv generator-info-name="clustarr">\n`.
    - **Channels:** one per lineup channel,
      `<channel id="N"><display-name>Name</display-name>[<icon src="Logo"></icon>]</channel>`,
      escaped through `xml.EscapeText`.
    - **Programmes,** in number order, each written as
      `<programme start=… stop=… channel="N">` + `Inner` + `</programme>`:
      - **Mapped:** the source programmes, with `TimeshiftMinutes` added
        to start and stop.
      - **Dummy:** blocks of `DummyMinutes` from `from.Truncate(minutes)`
        to `from+days`, each `<title>Name</title>`.
    - Time format `"20060102150405 -0700"` in UTC.
  - **`AutoMap`:**
    - iterate the guide names sorted;
    - an exact `tvgID == Channel.ID` wins first;
    - else the first channel with a display name where
      `release.TitleNorm(dn) == release.TitleNorm(name)` (`pkg/release/normalize.go:88`).
- [ ] **Step 5: Golden.**
  - Run `go test ./pkg/iptv/xmltv -run TestBuildGuide -update`.
  - Read `test/data/iptv/guide_built.golden.xml` by eye: two channels, the
    shifted programmes, 168 dummy blocks, well-formed.
  - Run `go test ./pkg/iptv/xmltv`. Expected: PASS.
- [ ] **Step 6: Commit.** `git add pkg/iptv/xmltv test/data/iptv/guide.xml test/data/iptv/guide_built.golden.xml && git commit -m "feat(iptv): streaming XMLTV parser, XEPG guide builder and auto-mapping -- Live TV spec §4.3" -- pkg/iptv/xmltv test/data/iptv`

### Task 5: `pkg/iptv/hdhr`: the HDHomeRun surface

**Files:**

- Create: `pkg/iptv/hdhr/hdhr.go`, `pkg/iptv/hdhr/hdhr_test.go`

**Interfaces:**

- Produces:
  - `hdhr.Device{FriendlyName, DeviceID, BaseURL string; TunerCount int}`
  - `hdhr.LineupEntry{GuideNumber, GuideName, URL string}`
  - `hdhr.DeviceID(namespace, name string) string`: 8 uppercase hex.
  - `hdhr.Discover(d Device) []byte`
  - `hdhr.LineupStatus() []byte`
  - `hdhr.Lineup(entries []LineupEntry) []byte`
  - `hdhr.DeviceXML(d Device) []byte`

- [ ] **Step 1: Write the failing test.**

```go
package hdhr_test

func device() hdhr.Device {
 return hdhr.Device{FriendlyName: "Clustarr news", DeviceID: "12AB34CD", BaseURL: "http://10.96.0.5/livetv/media/news", TunerCount: 2}
}

func TestDiscover(t *testing.T) {
 assert.JSONEq(t, `{"FriendlyName":"Clustarr news","Manufacturer":"Silicondust","ModelNumber":"HDTC-2US",
  "FirmwareName":"hdhomeruntc_atsc","FirmwareVersion":"20150826","DeviceID":"12AB34CD","DeviceAuth":"clustarr",
  "TunerCount":2,"BaseURL":"http://10.96.0.5/livetv/media/news","LineupURL":"http://10.96.0.5/livetv/media/news/lineup.json"}`,
  string(hdhr.Discover(device())))
}

func TestLineupStatus(t *testing.T) {
 assert.JSONEq(t, `{"ScanInProgress":0,"ScanPossible":1,"Source":"Cable","SourceList":["Cable"]}`, string(hdhr.LineupStatus()))
}

func TestLineup(t *testing.T) {
 assert.JSONEq(t, `[{"GuideNumber":"1001","GuideName":"BBC One","URL":"http://10.96.0.5/livetv/media/news/stream/1001"}]`,
  string(hdhr.Lineup([]hdhr.LineupEntry{{GuideNumber: "1001", GuideName: "BBC One", URL: "http://10.96.0.5/livetv/media/news/stream/1001"}})))
 assert.JSONEq(t, `[]`, string(hdhr.Lineup(nil)), "an empty lineup is an empty array, never null")
}

func TestDeviceXMLNamesTheDevice(t *testing.T) {
 x := string(hdhr.DeviceXML(device()))
 assert.Contains(t, x, `<URLBase>http://10.96.0.5/livetv/media/news</URLBase>`)
 assert.Contains(t, x, `<friendlyName>Clustarr news</friendlyName>`)
 assert.Contains(t, x, `<UDN>uuid:12AB34CD</UDN>`)
 require.NoError(t, xml.Unmarshal([]byte(x), new(struct{})), "well-formed")
}

func TestDeviceIDIsStableAndDistinct(t *testing.T) {
 a := hdhr.DeviceID("media", "news")
 assert.Regexp(t, `^[0-9A-F]{8}$`, a)
 assert.Equal(t, a, hdhr.DeviceID("media", "news"))
 assert.NotEqual(t, a, hdhr.DeviceID("media", "sports"))
 assert.NotEqual(t, a, hdhr.DeviceID("other", "news"))
}
```

- [ ] **Step 2: Run.** Expected: FAIL to compile.
- [ ] **Step 3: Implement.**
  - **The JSON renderers:** `json.Marshal` on unexported structs whose
    field order matches the test. `Lineup` marshals `[]LineupEntry{}` when
    it is nil.
  - **`DeviceID`:** `strings.ToUpper(hex.EncodeToString(sha256.Sum256(ns+"/"+name)[:4]))`.
  - **`DeviceXML`:** a `fmt.Sprintf` UPnP document with `xml.EscapeText`
    for the friendly name. Its elements: `root xmlns="urn:schemas-upnp-org:device-1-0"`,
    `specVersion 1.0`, `URLBase`, then `device` with
    `deviceType urn:schemas-upnp-org:device:MediaServer:1`,
    `friendlyName`, `manufacturer Silicondust`, `modelName HDTC-2US`,
    `modelNumber HDTC-2US`, `serialNumber` and `UDN uuid:<DeviceID>`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit.** `git add pkg/iptv/hdhr && git commit -m "feat(iptv): HDHomeRun discover, lineup and device renderers -- Live TV spec §4.4" -- pkg/iptv/hdhr`

### Task 6: `app/livetv/relay`: the stream relay

**Files:**

- Create:
  - `app/livetv/relay/ring.go`
  - `app/livetv/relay/relay.go`
  - `app/livetv/relay/relay_test.go`
  - `app/livetv/relay/ring_test.go`

**Interfaces:**

- Produces:
  - `relay.Options{HTTP *http.Client; Tuners int; Buffer int; Linger, Backoff, ReadyTimeout time.Duration; Retries int; UserAgent string; OnChange func(inUse int); OnError func(reason string)}`
  - `relay.New(o Options) *Relay`
  - `(*Relay).Serve(w http.ResponseWriter, req *http.Request, channel, upstream string)`
  - `(*Relay).InUse() int`, `(*Relay).Viewers() int`
  - `(*Relay).SetTuners(n int)`
  - `(*Relay).Close()`
  - The reasons: `relay.ReasonRefused`, `ReasonTimeout`, `ReasonEOF`, `ReasonUnsupportedStream`.

- [ ] **Step 1: Write the failing tests.** An `httptest` upstream:
  - writes 188-byte packets (`0x47`, then 187 bytes of the packet's index
    mod 256) in a loop;
  - counts connections per path atomically;
  - can be told to close every live connection.

```go
package relay_test

type upstream struct {
 srv   *httptest.Server
 conns sync.Map // path -> *atomic.Int32
 kill  chan struct{}
 hls   bool
}

func newUpstream(t *testing.T) *upstream {
 u := &upstream{kill: make(chan struct{})}
 u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  c, _ := u.conns.LoadOrStore(r.URL.Path, new(atomic.Int32))
  c.(*atomic.Int32).Add(1)
  if u.hls {
   w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
   _, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
   return
  }
  w.Header().Set("Content-Type", "video/mp2t")
  pkt := make([]byte, 188)
  for i := 0; ; i++ {
   pkt[0] = 0x47
   for j := 1; j < 188; j++ {
    pkt[j] = byte(i)
   }
   select {
   case <-r.Context().Done():
    return
   case <-u.kill:
    return
   default:
   }
   if _, err := w.Write(pkt); err != nil {
    return
   }
   if i%64 == 0 {
    w.(http.Flusher).Flush()
    time.Sleep(time.Millisecond)
   }
  }
 }))
 t.Cleanup(u.srv.Close)
 return u
}

func (u *upstream) connections(path string) int32 {
 c, ok := u.conns.Load(path)
 if !ok {
  return 0
 }
 return c.(*atomic.Int32).Load()
}

func newRelay(tuners int, linger time.Duration) *relay.Relay {
 return relay.New(relay.Options{HTTP: http.DefaultClient, Tuners: tuners, Buffer: 1 << 20, Linger: linger,
  Retries: 3, Backoff: 10 * time.Millisecond, ReadyTimeout: 5 * time.Second, UserAgent: "clustarr-test"})
}

// watch serves one viewer through the relay and returns the response and a cancel.
func watch(t *testing.T, r *relay.Relay, channel, up string) (*http.Response, context.CancelFunc) {
 front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.Serve(w, req, channel, up) }))
 t.Cleanup(front.Close)
 ctx, cancel := context.WithCancel(context.Background())
 req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
 resp, err := http.DefaultClient.Do(req)
 require.NoError(t, err)
 t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
 return resp, cancel
}

func TestTwoViewersShareOneUpstreamAndOneTuner(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, time.Second)
 a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 b, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 for _, resp := range []*http.Response{a, b} {
  require.Equal(t, http.StatusOK, resp.StatusCode)
  assert.Equal(t, "video/mp2t", resp.Header.Get("Content-Type"))
  _, err := io.CopyN(io.Discard, resp.Body, 1<<20)
  require.NoError(t, err)
 }
 assert.Equal(t, int32(1), u.connections("/1001.ts"))
 assert.Equal(t, 1, r.InUse())
 assert.Equal(t, 2, r.Viewers())
}

func TestAChannelPastTheTunersIsRefused(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, time.Second)
 a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 require.Equal(t, http.StatusOK, a.StatusCode)
 b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
 assert.Equal(t, http.StatusServiceUnavailable, b.StatusCode)
 assert.Equal(t, int32(0), u.connections("/1002.ts"))
}

func TestANewViewerStartsOnAPacketBoundary(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, time.Second)
 a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 _, _ = io.CopyN(io.Discard, a.Body, 100_003)
 b, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 head := make([]byte, 188*4)
 _, err := io.ReadFull(b.Body, head)
 require.NoError(t, err)
 for i := 0; i < 4; i++ {
  assert.Equal(t, byte(0x47), head[i*188], "sync byte at every 188")
 }
}

func TestASlowViewerIsCutOffAndTheOtherIsNot(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, time.Second)
 slow, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 fast, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 _, err := io.CopyN(io.Discard, fast.Body, 8<<20) // 8 buffers' worth while slow reads nothing
 require.NoError(t, err)
 _, err = io.Copy(io.Discard, slow.Body)
 assert.NoError(t, err, "the slow viewer's response ends")
 _, err = io.CopyN(io.Discard, fast.Body, 1<<20)
 assert.NoError(t, err, "the fast viewer keeps streaming")
}

func TestAnUpstreamResetIsReopened(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, time.Second)
 a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 _, _ = io.CopyN(io.Discard, a.Body, 1<<20)
 close(u.kill)
 u.kill = make(chan struct{}) // the next connection streams
 _, err := io.CopyN(io.Discard, a.Body, 2<<20)
 require.NoError(t, err, "the viewer stays attached across the reopen")
 assert.Equal(t, int32(2), u.connections("/1001.ts"))
}

func TestAVanishedViewerFreesTheTunerAfterLinger(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, 50*time.Millisecond)
 a, cancel := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 _, _ = io.CopyN(io.Discard, a.Body, 1<<16)
 cancel() // no clean close: the client's context is gone
 require.Eventually(t, func() bool { return r.InUse() == 0 && r.Viewers() == 0 }, 2*time.Second, 10*time.Millisecond)
 b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
 assert.Equal(t, http.StatusOK, b.StatusCode, "the freed tuner takes another channel")
}

func TestARejoinInsideLingerReusesTheUpstream(t *testing.T) {
 u := newUpstream(t)
 r := newRelay(1, time.Second)
 a, cancel := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 _, _ = io.CopyN(io.Discard, a.Body, 1<<16)
 cancel()
 b, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
 _, err := io.CopyN(io.Discard, b.Body, 1<<16)
 require.NoError(t, err)
 assert.Equal(t, int32(1), u.connections("/1001.ts"))
}

func TestAnHLSAnswerIsRefused(t *testing.T) {
 u := newUpstream(t)
 u.hls = true
 var reasons []string
 r := relay.New(relay.Options{HTTP: http.DefaultClient, Tuners: 1, Buffer: 1 << 20, Linger: time.Second, ReadyTimeout: time.Second,
  OnError: func(reason string) { reasons = append(reasons, reason) }})
 a, _ := watch(t, r, "1001", u.srv.URL+"/1001.m3u8")
 assert.Equal(t, http.StatusBadGateway, a.StatusCode)
 assert.Equal(t, []string{relay.ReasonUnsupportedStream}, reasons)
 require.Eventually(t, func() bool { return r.InUse() == 0 }, time.Second, 10*time.Millisecond)
}
```

`ring_test.go`:

- `TestRingReaderWaitsThenReads`;
- `TestRingReaderTooFarBehindGetsErrSlow`;
- `TestRingCloseEndsReaders`.

Each writes known bytes and reads them back through a cursor.

- [ ] **Step 2: Run.** `go test -race ./app/livetv/relay`. Expected: FAIL to compile.
- [ ] **Step 3: Implement.**
  - **`ring.go`:**

    ```go
    type ring struct {
     mu      sync.Mutex
     buf     []byte
     written int64         // total bytes ever written
     notify  chan struct{} // closed and replaced on every write
     err     error         // set by close
    }
    var errSlow = errors.New("relay: viewer fell a buffer behind")
    func newRing(size int) *ring
    func (r *ring) Write(p []byte)                  // copies p at written%len, advances, closes notify
    func (r *ring) Join() int64                     // written - written%188
    func (r *ring) Read(ctx context.Context, cur *int64, p []byte) (int, error)
    func (r *ring) close(err error)
    ```

    How `Read` behaves:
    - `written-*cur > len(buf)` returns `errSlow`;
    - `*cur == written` waits on `notify` or `ctx.Done()`, returning
      `r.err` once closed;
    - otherwise it copies up to `min(len(p), written-*cur)` bytes, which
      may wrap, and advances `*cur`.
  - **`relay.go`:**
    - **The Relay:** `mu`, `streams map[string]*stream`, `tuners`,
      `inUse`, `viewers`.
    - **A stream:** `ring`, `viewers int`, `ready chan struct{}`
      (closed on the first data or a failure), `failed string` (a
      reason), `cancel context.CancelFunc`, `linger *time.Timer`.
  - **`Serve`, step by step:**
    1. Lock. When no stream exists for the channel:
       - at `inUse >= tuners`, unlock, write 503 with body
         "all tuners in use", and return;
       - otherwise create one, `inUse++`, call `OnChange(inUse)`, and
         start `go pump(ctx, s, upstream)` with a background context.
    2. `s.viewers++`, `r.viewers++`, stop `s.linger`. Unlock.
    3. Wait for `s.ready`, the request's context, or `ReadyTimeout`. On a
       failure or timeout, write 502 and `leave`.
    4. Write the headers `Content-Type: video/mp2t` and 200. Then loop
       `ring.Read` into a 64 KiB buffer, `w.Write` and `Flush`, until an
       error, the request's context ending, or a write error.
    5. `leave(s)`: lock, decrement the viewer counts. At zero viewers,
       start `s.linger = time.AfterFunc(Linger, …)`. When it fires, under
       the lock and still at zero viewers, it calls `s.cancel()`, deletes
       the stream, `inUse--` and `OnChange`.
  - **`pump`:**
    - up to `1+Retries` attempts, sleeping `Backoff` between them;
    - each attempt is a GET with `User-Agent`;
    - a status ≥ 400 is `ReasonRefused`;
    - a Content-Type containing `mpegurl`, or a body starting with
      `#EXTM3U`, is `ReasonUnsupportedStream`, and is final, with no retry;
    - otherwise it copies the body into the ring in 32 KiB reads and closes
      `ready` on the first byte;
    - an EOF or read error after data counts as `ReasonEOF` and retries.
  - **Giving up:** `OnError(reason)`, `s.failed = reason`, close `ready`
    if it is still open, `ring.close(io.EOF)`. Then, under the lock, end
    the stream right away: delete it, `inUse--` and `OnChange`.
  - **Panics:** `pump` and `Serve` each recover their own panic. They log
    it from the request's or the pump's context, then end only that stream
    (spec §5.0.1). `TestAPanickingUpstreamEndsOnlyItsStream` drives a
    custom `RoundTripper` that panics for one channel's URL. That
    channel's viewer gets 502, and another channel keeps streaming.
  - **`SetTuners(n)`:** sets the limit. It only affects later admissions
    and never ends a stream.
  - **`Close()`:** cancels every stream.
- [ ] **Step 4: Run.** `go test -race -count=3 ./app/livetv/relay`. Expected: PASS three times.
- [ ] **Step 5: Commit.** `git add app/livetv/relay && git commit -m "feat(livetv): the stream relay -- one upstream per channel shared by every viewer, tuner admission, slow viewers cut off, reopen, linger -- Live TV spec §5.2" -- app/livetv/relay`

### Task 7: `app/livetv/tuner`: fetch, render, route, snapshot, store

**Files:**

- Create:
  - `app/livetv/tuner/config.go`
  - `app/livetv/tuner/tuner.go`
  - `app/livetv/tuner/fetch.go`
  - `app/livetv/tuner/render.go`
  - `app/livetv/tuner/http.go`
  - `app/livetv/tuner/metrics.go`
  - `app/livetv/tuner/tuner_test.go`

**Interfaces:**

- Consumes: Task 2's `m3u`, Task 3's `filter`, Task 4's `xmltv`, Task 5's `hdhr`, Task 6's `relay`, and `events.ObjectStore` (`Put`, `Get`).
- Produces:
  - **The configuration types:**

    ```go
    type Config struct {
     Key              types.NamespacedName
     Address          string // the Service ClusterIP, without a port
     FriendlyName     string
     DeviceID         string
     Tuners           int
     UserAgent        string
     PlaylistURL      string
     PlaylistRefresh  time.Duration
     EPGSource        string // "XEPG" | "PMS"
     Guides           []GuideConfig
     Filters          []filter.Rule
     Channels         []ChannelConfig
     Buffer           int
     Linger           time.Duration
     // Refresh is the clustarr.io/livetv-refresh annotation's value. A new
     // value makes every source of the provider due now (spec §5.1).
     Refresh          string
    }
    type GuideConfig struct { Name, URL string; Refresh time.Duration }
    type ChannelConfig struct {
     Object, Key, Number, Name, Group, Logo string
     Active                                 bool
     Map                                    *xmltv.Mapping
     DummyMinutes, TimeshiftMinutes         int
    }
    ```

  - **The snapshot types:**

    ```go
    type Snapshot struct {
     PlaylistFetched bool
     Playlist        PlaylistState // Entries, Groups, Candidates int; FetchedAt time.Time; Hash, Error string
     Guides          []GuideState  // Name; Channels, Programmes int; FetchedAt time.Time; Error string
     Lineup          LineupState   // Active, Unmapped, Missing int; Hash string
     GuideHash       string
     TunersInUse     int
     Channels        map[string]ChannelState // by IPTVChannel object name
    }
    type ChannelState struct{ Missing, Duplicate, GuideNotFound bool; Guide string }
    ```

  - **The tuner itself:**
    - `tuner.Options{BindAddress string; Store events.ObjectStore; HTTP *http.Client; Now func() time.Time; Debounce time.Duration; Notify func(types.NamespacedName)}`
    - `tuner.New(o Options) *Tuner`
    - `(*Tuner).Apply(c Config)`
    - `(*Tuner).Remove(key types.NamespacedName)`
    - `(*Tuner).Snapshot(key types.NamespacedName) (Snapshot, bool)`
    - `(*Tuner).Handler() http.Handler`
    - `(*Tuner).Start(ctx context.Context) error`
    - `(*Tuner).NeedLeaderElection() bool`
  - **The stored objects:** `tuner.PlaylistObject(key)`,
    `tuner.GuideChannelsObject(key, guide)` and `tuner.GuideObject(key)`,
    giving `"playlist/<ns>/<name>"`, `"guide-channels/<ns>/<name>/<guide>"`
    and `"guide/<ns>/<name>"`.

- [ ] **Step 1: Write the failing tests.**
  - **The fixture server:** an `httptest` server serving the M3U fixture at
    `/get.php`, the guide fixture at `/guide.xml`, and a TS loop at every
    `/live/...` path, as in Task 6. It counts requests per path and can be
    switched to answer 500 for `/get.php`.
  - **The bus:** `membus.New(nil)` with
    `Ensure(ctx, events.Topology{ObjectStores: []events.ObjectStoreSpec{{Name: events.BucketLiveTV, MaxBytes: 1 << 30}}})`.
    Task 8 adds `BucketLiveTV`; until then, use the literal
    `"clustarr-livetv"`. Task 8 replaces it.
  - **The tuner:** `Debounce: 10 * time.Millisecond`, `BindAddress: "127.0.0.1:0"`,
    and a `Now` fixed at `2026-10-07T00:00:00Z`.
  - **Serving:** `httptest.NewServer(tn.Handler())`, sending requests with
    an explicit `req.Host`.

```go
func config(srv string) tuner.Config {
 return tuner.Config{
  Key: types.NamespacedName{Namespace: "media", Name: "news"}, Address: "10.96.0.5",
  FriendlyName: "Clustarr news", DeviceID: hdhr.DeviceID("media", "news"), Tuners: 1, UserAgent: "clustarr",
  PlaylistURL: srv + "/get.php?username=u&password=p", PlaylistRefresh: time.Hour, EPGSource: "XEPG",
  Guides:  []tuner.GuideConfig{{Name: "main", URL: srv + "/guide.xml?token=secret", Refresh: time.Hour}},
  Filters: []filter.Rule{{Type: "groupTitle", Match: "UK | Entertainment", Enabled: true}, {Type: "groupTitle", Match: "UK | News", Enabled: true},
   {Type: "custom", Match: "Sports 1", Enabled: true}, {Type: "groupTitle", Match: "Misc", Enabled: true}},
  Channels: []tuner.ChannelConfig{
   {Object: "news-a", Key: "t:bbc1.uk", Number: "1001", Name: "BBC One", Active: true},             // auto-maps by tvg-id
   {Object: "news-b", Key: m3u.NameKey("UK | News", "News, Live"), Number: "1002", Active: true},   // auto-maps by display name
   {Object: "news-c", Key: "t:gone", Number: "1003", Active: true},                                 // missing
   {Object: "news-d", Key: m3u.NameKey("Sports", "Sports 1"), Number: "1001", Active: true},        // duplicate number
   {Object: "news-e", Key: m3u.NameKey("Misc", "No Attributes"), Number: "1004", Active: true,
    Map: &xmltv.Mapping{Guide: "nope", ChannelID: "x"}},                                         // guide not found
  },
  Buffer: 1 << 20, Linger: time.Second,
 }
}
```

The tests:

- **`TestALineupWaits503UntilThePlaylistIsFetched`**: `Apply` before
  `Start`; `GET /lineup.json` with Host `10.96.0.5` gives 503.
- **`TestATunerServesItsProvidersLineupByHostAndByPath`**: after `Start`,
  `Eventually` `Snapshot().PlaylistFetched` holds. Then:
  - `GET /lineup.json` with Host `10.96.0.5` and
    `GET /livetv/media/news/lineup.json` with Host `anything` give
    identical bytes;
  - the JSON lists `1001` (BBC One, URL
    `http://10.96.0.5/livetv/media/news/stream/1001`), `1002` and `1004`,
    in that order;
  - `news-d` (the duplicate 1001, which sorts after `news-a` by object
    name) and `news-c` (missing) are absent;
  - `discover.json`'s `DeviceID` and `BaseURL` are right.
- **`TestRootRoutesMatchTheHostWithOrWithoutAPort`**: Host `10.96.0.5:80`
  routes; Host `10.96.0.9` is 404 at `/discover.json`; and
  `/livetv/media/news/discover.json` under Host `10.96.0.9` is 200.
- **`TestSnapshotMarksDuplicateMissingAndGuideNotFound`**:
  - `Channels["news-c"].Missing`;
  - `Channels["news-d"].Duplicate`;
  - `Channels["news-e"].GuideNotFound`;
  - `Channels["news-a"].Guide == "main/bbc1.uk"` and
    `Channels["news-b"].Guide == "main/news.uk"`;
  - `Lineup.Active == 3`, `Lineup.Missing == 1`, and `Lineup.Unmapped == 1`:
    `news-e` falls back to the dummy guide.
- **`TestTheXEPGGuideServesTheLineupsChannels`**:
  `GET /livetv/media/news/xmltv.xml` contains `<channel id="1001">` and
  `<channel id="1004">`. Under `EPGSource: "PMS"` it gives 404, and
  `Snapshot().GuideHash == ""`.
- **`TestAFailedRefreshKeepsServingTheLastLineup`**:
  - after the first fetch, switch `/get.php` to 500 and `Apply` with a
    changed `PlaylistURL` query, which forces a refetch;
  - `Eventually`, `Snapshot().Playlist.Error` is non-empty, contains no
    `password` and no `username=u`, and contains `/get.php`;
  - `/lineup.json` still lists the same three channels.
- **`TestABurstOfChannelEditsRendersOnce`**: count `Notify` calls; apply
  20 configs in a tight loop, each changing one name. `Eventually` the
  snapshot carries the final name. Assert that `Lineup.Hash` changed and
  that `Notify` ran at most 3 times after the burst, with the 10 ms
  debounce.
- **`TestTheGuideIsStoredAndServedAfterARestart`**:
  - after the first tuner has rendered, `GuideObject` exists in the store;
  - a second tuner on the same store, with `/guide.xml` switched to 500,
    serves `xmltv.xml` from the stored copy as soon as `Apply` and `Start`
    run, with the stored bytes.
- **`TestNoStoredOrReportedStringCarriesACredential`**:
  - `List` the store's objects, read each (gunzip where gzip), and assert
    that none contains `/live/user/pass/`, `password`, `token=secret` or
    `username=u`;
  - assert the same of `fmt.Sprintf("%+v", snapshot)`;
  - the stored playlist object holds exactly the keys
    `{key,name,group,tvgID,tvgName,logo}` per entry.
- **`TestAStreamRequestReachesTheRelay`**: `GET /livetv/media/news/stream/1001`
  with Host `10.96.0.5` gives 200 `video/mp2t`, 188-aligned, and the
  fixture server counts one `/live/user/pass/1001.ts`.
  `Snapshot().TunersInUse == 1`. An unknown number gives 404.
- **`TestANewRefreshValueRefetchesEverySource`**: `Apply` the same config
  with `Refresh: "1"`, then `"2"`. The fixture server counts one more
  `/get.php` and one more `/guide.xml` per change, and none for an
  unchanged value.
- **`TestRemoveEndsTheProvider`**: after `Remove`, `/discover.json` with
  Host `10.96.0.5` gives 404 and `Snapshot` returns `false`.

- [ ] **Step 2: Run.** `go test -race ./app/livetv/tuner`. Expected: FAIL to compile.
- [ ] **Step 3: Implement.**
  - **`tuner.go`, the state:**
    - one `provider` per key, holding `cfg Config`, the `playlist`
      (entries with URLs, in memory only), `guides map[string]xmltv.Guide`,
      the rendered `lineup []byte`, `guideXML []byte`, `snap Snapshot`, a
      `relay *relay.Relay` and `dirty chan struct{}`;
    - all of it under one mutex.
  - **`Apply`:**
    - stores the config;
    - creates the relay on first sight, or calls `SetTuners`;
    - marks the playlist due when the URL changed or it was never fetched;
    - marks a guide due when its URL changed;
    - triggers a debounced render.
  - **`Remove`:** closes the relay, deletes the state, and calls `Notify`.
  - **`Start`:**
    - listens on `BindAddress` and serves `Handler()` through an
      `http.Server` with `ReadHeaderTimeout` 10 s, `IdleTimeout` 90 s and
      no `WriteTimeout`, since streams are long;
    - runs a scheduler loop with a 1 s tick: due fetches run one goroutine
      per provider and source, never two for the same source;
    - on `ctx.Done()`, shuts the server down and closes every relay.
  - **`NeedLeaderElection()`** returns true.
  - **`fetch.go`:**
    - **Playlist:**
      - a GET with the `User-Agent`;
      - status ≥ 300 is an error naming the status;
      - `m3u.Parse` on the body;
      - on success: replace the playlist, `Hash` = sha256 hex of the body,
        `FetchedAt`, and an empty `Error`;
      - on failure: keep the old playlist and set `Error` to
        `iptv.RedactError(err)`, truncated to 1024 bytes on a rune
        boundary;
      - schedule the next fetch at `now + PlaylistRefresh`.
    - **Guide:**
      - read `GuideObject(key)`'s headers for its fetch time; when that
        copy is fresh and the guide is not yet in memory, serve the stored
        bytes without fetching;
      - otherwise GET it and `xmltv.Parse`, with `want` set to the active
        mapped channel ids for that guide, from `Now()` to `Now()+7d`;
      - put
        `GuideChannelsObject(key, name)`: gzip JSON of `[]struct{ID, DisplayName, Icon string}`,
        taking the first display name;
      - after every fetch, playlist or guide, trigger a render.
  - **`render.go`, rendering under the lock after the debounce:**
    1. **Candidates and lookup:** `filter.Candidates(entries, cfg.Filters)`
       gives `Snapshot.Playlist.Candidates`, and `Groups` counts the
       distinct groups. An entry index by key.
    2. **The active channels:** sorted by number numerically, then by
       object name.
       - A key that is not a candidate is Missing and is skipped. That
         covers a key gone from the playlist and one a filter edit dropped
         (spec §7.2: kept as Missing, never deactivated).
       - A number already taken is Duplicate and is skipped.
       - The mapping is `ch.Map`, or else
         `xmltv.AutoMap(entry.TvgID, entry.Name, guides)`.
       - A `Map` naming no configured guide is GuideNotFound and falls
         back to the dummy.
       - No mapping means the dummy, with `DummyMinutes` or else 60; it
         counts in `Unmapped`.
       - `Guide` reads `"<guide>/<id>"` or `"dummy/<minutes>"`, or `""`
         under PMS.
    3. **The lineup:** `hdhr.Lineup` entries with
       `URL = "http://"+Address+"/livetv/<ns>/<name>/stream/<number>"`;
       `Lineup.Hash` is sha256 hex of those bytes.
    4. **The guide (XEPG only):** `xmltv.BuildGuide` from `Now()`, 7 days;
       `GuideHash` is sha256 hex. When the hash changed, put
       `GuideObject(key)` gzip with header `X-Clustarr-Fetched-At`.
    5. **The playlist object:** when `Playlist.Hash` changed, put
       `PlaylistObject(key)` gzip JSON of entries without URL or user
       agent.
    6. **Notify:** call `Notify(key)` outside the lock when the snapshot
       differs from the last one notified.
  - **`http.go`:**
    - `hostOnly(r.Host)` is `net.SplitHostPort`, falling back to the raw
      host, lowercased.
    - The root routes match `/discover.json`, `/lineup_status.json`,
      `/lineup.json`, `/lineup.post` and `/device.xml` to the provider
      whose `Address == host`.
    - `/livetv/{ns}/{name}/{rest...}` routes by path (Go 1.22 mux
      patterns): `discover.json` and the rest of the device surface, plus
      `xmltv.xml` and `stream/{number}`.
    - `lineup.json` answers 503 until the first successful fetch.
    - `stream/{number}` finds the rendered lineup entry and calls
      `p.relay.Serve(w, r, number, entry.URL)`.
  - **`metrics.go`:**
    - the gauges `clustarr_livetv_tuners_in_use{provider}` and
      `clustarr_livetv_viewers{provider}`;
    - the counters `clustarr_livetv_stream_bytes_total{provider}` and
      `clustarr_livetv_upstream_errors_total{provider,reason}`;
    - the histogram `clustarr_livetv_fetch_duration_seconds{provider,source}`.
    - The `provider` label is `ns/name`.
    - They are registered once on `ctrlmetrics.Registry` through a
      `sync.Once`, as `app/import/agent/prober.go:49` registers on it.
    - The relay's `OnChange` and `OnError` feed them, and `OnChange` also
      updates `TunersInUse` and triggers `Notify`.
- [ ] **Step 4: Run.** `go test -race -count=2 ./app/livetv/...`. Expected: PASS.
- [ ] **Step 5: Commit.** `git add app/livetv/tuner && git commit -m "feat(livetv): the tuner -- playlist and guide fetches, debounced lineup and XEPG render, Host and path routing, the snapshot and the clustarr-livetv objects -- Live TV spec §5" -- app/livetv/tuner`

### Task 8: The `clustarr-livetv` object store

**Files:**

- Modify:
  - `pkg/events/subjects.go` (`BucketLiveTV`, `LiveTVMaxBytes`)
  - `pkg/events/topology.go` (`defaultObjectStores`)
  - `pkg/events/events_test.go` (`TestDefaultObjectStoreIsArtwork`)
  - `app/livetv/tuner/tuner_test.go` (use the constant)

- [ ] **Step 1: Update the failing test first.** In
  `pkg/events/events_test.go:617`, change `TestDefaultObjectStoreIsArtwork`'s
  expected count from 2 to 3. Assert the new entry:

```go
 lt := byName[events.BucketLiveTV]
 require.NotNil(t, lt, "the Live TV bucket is part of the default topology")
 assert.Equal(t, events.StorageFile, lt.Storage)
 assert.Equal(t, events.LiveTVMaxBytes, lt.MaxBytes)
```

  Build `byName` from the slice; read the test's existing shape first.

- [ ] **Step 2: Run.** `go test ./pkg/events/ -run TestDefaultObjectStore`. Expected: FAIL to compile on `BucketLiveTV`.
- [ ] **Step 3: Implement.**
  - **`subjects.go`:**
    - `BucketLiveTV = "clustarr-livetv"`, with the comment: "the tuner's
      playlist, guide-channel and guide objects (Live TV spec §5.4);
      written by the tuner alone, read by the ui";
    - `LiveTVMaxBytes int64 = 1 * GiB`.
  - **`defaultObjectStores()`:** add
    `{Name: BucketLiveTV, Description: "Live TV playlists and guides", Storage: StorageFile, MaxBytes: LiveTVMaxBytes, Replicas: 3}`.
- [ ] **Step 4: Run.** `go test ./pkg/events/... ./app/livetv/...`. Expected: PASS, including:
  - `TestDefaultTopologyIsValid`;
  - `TestDefaultTopologyObjectNamesAreDistinct`;
  - `natsbus`'s `TestEnsureDefaultTopology` and
    `TestEnsureSingleNodeTopologyFitsTheKindServersLimits`: 7 GiB of the
    10 GiB file store.
- [ ] **Step 5: Commit.** `git commit -m "feat(events): the clustarr-livetv object store -- Live TV spec §5.4" -- pkg/events app/livetv/tuner/tuner_test.go`

### Task 9: The controllers: IPTVProvider and IPTVChannel

**Files:**

- Create:
  - `app/livetv/index/index.go`
  - `app/livetv/controller/iptvprovider/{doc.go,reconciler.go,service.go,config.go,status.go,reconciler_envtest_test.go}`
  - `app/livetv/controller/iptvchannel/{doc.go,reconciler.go,reconciler_envtest_test.go}`

**Interfaces:**

- Consumes:
  - Task 1's types, accessors, managers and reasons;
  - Task 7's `tuner.Config`, `tuner.Snapshot`, `tuner.ChannelState`;
  - `hdhr.DeviceID`, `names.HashSuffix`, `k8s.PatchStatus`.
- Produces:
  - **The index:**
    - `index.ChannelByProvider = "livetv.spec.providerRef"`
    - `index.Register(ctx context.Context, mgr ctrl.Manager) error`
    - `index.ChannelsOf(ctx, c client.Reader, ns, provider string) ([]clustarrv1.IPTVChannel, error)`
  - **The interfaces the reconcilers take:**
    - `iptvprovider.Tuner` interface `{ Apply(tuner.Config); Remove(types.NamespacedName); Snapshot(types.NamespacedName) (tuner.Snapshot, bool) }`
    - `iptvchannel.Snapshots` interface `{ Snapshot(types.NamespacedName) (tuner.Snapshot, bool) }`
  - **The provider reconciler:**
    - `iptvprovider.Reconciler{Client client.Client; SecretReader client.Reader; Tuner Tuner; Namespace string; Selector map[string]string; TargetPort int32; Events <-chan event.GenericEvent}`
    - `(*Reconciler).SetupWithManager(mgr) error`
    - `iptvprovider.Finalizer = "clustarr.io/livetv-service"`
    - `iptvprovider.ServiceName(ns, name string) string` (`"livetv-" + names.HashSuffix(ns, name)`)
  - **The channel reconciler:**
    - `iptvchannel.Reconciler{Client client.Client; Snapshots Snapshots; Events <-chan event.GenericEvent}`
    - `(*Reconciler).SetupWithManager(mgr) error`
  - **The controller names:** `"iptvprovider"` and `"iptvchannel"`.

- [ ] **Step 1: Write the failing envtests.**
  - **The environment:** a manager on envtest with
    `CRDDirectoryPaths: {"../../../../config/crd/bases"}`, skipped without
    `KUBEBUILDER_ASSETS`. Model it on
    `app/grab/controller/downloadclient/controller_envtest_test.go:47`'s
    `newTestClient`, plus a started `ctrl.Manager` for the reconcilers.
  - **A fake tuner:** records `Apply` and `Remove`, and returns a settable
    `Snapshot`.
  - **The namespaces:** `clustarr-system` for the manager, `media` for
    providers and channels.

  The tests:
  - **`TestAProviderGetsAServiceInTheManagersNamespaceAndAnAddress`**:
    - create a Secret `news-livetv` with `playlist-url`, and an
      IPTVProvider `media/news` with `urlFrom` naming it;
    - `Eventually` the Service `clustarr-system/livetv-<hash>` exists,
      with `Spec.Selector == Selector`, port 80 → `TargetPort` and the
      labels `clustarr.io/livetv-provider-namespace` and
      `clustarr.io/livetv-provider-name`;
    - `status.address == svc.Spec.ClusterIP`,
      `status.deviceID == hdhr.DeviceID("media","news")`,
      `status.guideURL == "http://<ip>/livetv/media/news/xmltv.xml"`;
    - the finalizer is present;
    - the fake's last `Apply` has `PlaylistURL` equal to the Secret's
      value and `Address` equal to the ClusterIP.
  - **`TestAProviderSpecEditKeepsTheServicesClusterIP`**: patch
    `spec.tuners`; the Service's ClusterIP and UID are unchanged, and the
    fake receives `Tuners: 3`.
  - **`TestDeletingAProviderDeletesItsServiceAndReleasesTheTuner`**: delete
    the provider. `Eventually` the Service is gone, the fake recorded
    `Remove`, and the object is gone.
  - **`TestAProviderWithAMissingSecretIsInvalid`**: `urlFrom` names an
    absent Secret, so `Valid=False`, reason `SecretNotFound`, and the
    message names the Secret but no value. `Ready=False`. No `Apply`.
  - **`TestProviderStatusComesFromTheSnapshot`**:
    - the fake's snapshot carries playlist counts, one guide, a lineup
      hash, `GuideHash` and `TunersInUse: 1`;
    - after a send on the `Events` channel for the provider, the status
      carries each leaf: assert every one, not the parent;
    - `PlaylistFetched=True`, `GuidesFetched=True`, `Ready=True`.
  - **`TestAnEarlyReturnKeepsTheStatus`**:
    - from the steady state above, delete the Secret and trigger a
      reconcile;
    - `Valid=False`, and `address`, `deviceID`, `playlist.*`, `lineup.*`
      and `tuners.*` keep their values: the full declaration on every
      path;
    - `managedFields` shows `status` owned by `clustarr-livetv` only.
  - **`TestChannelsReachTheTunerThroughTheIndex`**: create two
    IPTVChannels for `news` and one for `other`. The fake's last `Apply`
    for `news` carries exactly two `Channels`, with `Object`, `Key`,
    `Number`, `Active`, `Map` and `DummyMinutes` mapped from spec.
    Creating a third `news` channel triggers another `Apply`.
  - **`TestADisabledProviderIsRemovedFromTheTunerButKeepsItsService`**:
    `enabled: false` makes the fake record `Remove`; the Service stays;
    `Ready=False`, reason `Disabled`.
  - **Channel controller, `TestChannelStatusReadsTheSnapshot`**: the
    snapshot's `Channels` decide the reason, with the Missing,
    DuplicateNumber and GuideNotFound cases plus
    `Active`/`Inactive` (`spec.active`) and `status.guide`.
  - **`TestAChannelGoneFromThePlaylistIsMissingThenActiveAgain`**: the
    snapshot marks it Missing, then a snapshot without the mark arrives;
    the reason goes `Missing`, then `Active`, and the spec is untouched.
  - **`TestAnUnchangedSnapshotWritesNoChannelStatus`**: reconcile twice
    through `Events` with the same snapshot; `resourceVersion` is
    unchanged after the second.
  - **`TestAChannelOfAnUnknownProviderIsNotReady`**: reason
    `ProviderNotFound`.
- [ ] **Step 2: Run.** `KUBEBUILDER_ASSETS=$(setup-envtest use -p path 1.37.0) go test ./app/livetv/controller/... -v`. Expected: FAIL to compile.
- [ ] **Step 3: Implement.**
  - **`index.go`:** follow `app/caption/itemindex/itemindex.go:46-95`
    exactly: the per-indexer mutex and the `registered` map. `Extract`
    returns `[]string{ch.Spec.ProviderRef}`, and `ChannelsOf` lists with
    `client.InNamespace(ns), client.MatchingFields{ChannelByProvider: provider}`.
  - **The provider `Reconcile`:**
    1. **Get the provider.** On NotFound, `Tuner.Remove` and return.
    2. **Deletion:** `Remove`, delete the Service (ignore NotFound), drop
       the finalizer with an `Update` of metadata only, return.
    3. **The finalizer:** ensure it.
    4. **The Service:** `service.go`'s `ensureService`.
       - Get it. When it is absent, create:
         `ServiceSpec{Type: ClusterIP, Selector: r.Selector, Ports: [{Name: "hdhomerun", Port: 80, TargetPort: intstr.FromInt32(r.TargetPort)}]}`
         plus the two labels.
       - When the selector or port has drifted, update the existing object
         in place. That keeps its ClusterIP: the Service is never deleted
         and recreated.
       - Return the ClusterIP.
    5. **The Secrets:** for `urlFrom` and each guide's `urlFrom`,
       `SecretReader.Get`. A missing Secret or key means `Valid=False`,
       reasons `SecretNotFound` or `SecretKeyNotFound`, then
       `writeStatus` and return, without `Apply`.
    6. **Disabled:** when `!EnabledOrDefault()`, `Tuner.Remove`, write
       `Ready=False` `Disabled`, return.
    7. **The channels:** `index.ChannelsOf`. Build the config with
       `config.go`'s
       `buildConfig(p *IPTVProvider, address, playlistURL string, guideURLs map[string]string, channels []IPTVChannel) tuner.Config`,
       which maps each spec field through its `…OrDefault`. `Refresh` is
       `p.Annotations["clustarr.io/livetv-refresh"]`. Then
       `Tuner.Apply`.
    8. **Status:** `writeStatus` from `Snapshot`.
  - **`status.go`:** `render(p, address, snap, ok, valid metav1.Condition) *ac.IPTVProviderStatusApplyConfiguration`
    is the single renderer, used on every path; `writeStatus` calls
    `k8s.PatchStatus(ctx, c, k8s.ManagerLiveTV, ac)`.
    - **When there is no snapshot yet**, it re-asserts the last known
      `playlist`, `guides`, `lineup`, `guideHash` and `tuners` from the
      current object (`reassertKnownStatus`, as Movie does). Conditions are
      set in exactly one place.
    - **The derived conditions:**
      - `Ready = Valid && PlaylistFetched && (EPG=PMS || GuidesFetched)`;
      - `PlaylistFetched` is true when `snap.PlaylistFetched`, and its
        message is `snap.Playlist.Error` when that is set.
  - **The provider's watches:**
    - `For(&IPTVProvider{})`;
    - `Watches(&IPTVChannel{}, EnqueueRequestsFromMapFunc(spec.providerRef → provider))`;
    - `Watches(&corev1.Service{}, map by the two labels)`;
    - `WatchesRawSource(source.Channel(r.Events, &handler.EnqueueRequestForObject{}))`;
    - `index.Register` in `SetupWithManager`;
    - `Named("iptvprovider")`.
  - **The channel `Reconcile`:**
    1. Get the channel.
    2. Snapshot the provider at `{ns, spec.providerRef}`. With no snapshot
       and the provider absent, the reason is `ProviderNotFound`.
    3. `st := snap.Channels[ch.Name]`. The reason is, in priority order:
       Missing, then DuplicateNumber, then GuideNotFound, then
       Active/Inactive.
    4. Build the desired status: `Ready` true only for Active,
       `guide: st.Guide`, `observedGeneration`.
    5. Compare it with the current status, ignoring
       `LastTransitionTime` when status and reason are unchanged. Write
       through `k8s.PatchStatus(..., k8s.ManagerLiveTVChannel, ...)` only
       when it differs.
  - **The channel's watches:**
    - `For(&IPTVChannel{})`;
    - `WatchesRawSource(source.Channel(r.Events, handler.EnqueueRequestsFromMapFunc(provider event → index.ChannelsOf → requests)))`;
    - `Named("iptvchannel")`.
  - **The RBAC markers,** in each `doc.go`, at package level:
    - **iptvprovider:**
      - `clustarr.io`: `iptvproviders` get;list;watch;update;patch,
        `iptvproviders/status` get;update;patch,
        `iptvproviders/finalizers` update, `iptvchannels` get;list;watch;
      - core: `services` get;list;watch;create;update;patch;delete,
        `secrets` get;
      - `events.k8s.io`: `events` create;patch.
    - **iptvchannel:**
      - `clustarr.io`: `iptvchannels` get;list;watch,
        `iptvchannels/status` get;update;patch, `iptvproviders` get;list;watch.
- [ ] **Step 4: Run.** `KUBEBUILDER_ASSETS=… go test -race ./app/livetv/...`. Expected: PASS.
- [ ] **Step 5: Falsify the early-return test.** In `writeStatus`'s
  Secret-missing path, render only the conditions. Watch
  `TestAnEarlyReturnKeepsTheStatus` fail on `playlist.entries`, then
  restore.
- [ ] **Step 6: Commit.** `git add app/livetv/index app/livetv/controller && git commit -m "feat(livetv): IPTVProvider and IPTVChannel controllers -- the provider's Service in the manager's namespace, Secrets resolved for the tuner, status from its snapshot; channel status by the providerRef index -- Live TV spec §6" -- app/livetv`

### Task 10: `app/livetv/manager.Register` and the `livetv` step in `cmd/manager`

**Files:**

- Create:
  - `app/livetv/manager/register.go`
  - `app/livetv/manager/register_test.go`
  - `internal/cli/manager/livetv_options_test.go`
- Modify:
  - `internal/cli/manager/options.go` (`LiveTVBindAddress`,
    `LiveTVServiceSelector`, the defaults, `Validate`)
  - `internal/cli/manager/command.go` (two flags)
  - `internal/cli/manager/run.go` (a `livetv` step after `caption`, plus
    the `register` doc comment)

**Interfaces:**

- Consumes: Tasks 7-9.
- Produces:
  - `livetvmanager.Options{k8s.Options; BindAddress string; Selector map[string]string}`
  - `livetvmanager.Register(mgr ctrl.Manager, bus events.Bus, o Options) error`
  - **Flags:**
    - `--livetv-bind-address`, default `":5004"`; `"0"` disables Live TV
      entirely, so neither controller nor tuner is registered;
    - `--livetv-service-selector`, default
      `"app.kubernetes.io/component=manager"`, a comma-separated `k=v`
      list.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/cli/manager/livetv_options_test.go
func TestLiveTVDefaults(t *testing.T) {
 o := DefaultOptions()
 assert.Equal(t, ":5004", o.LiveTVBindAddress)
 assert.Equal(t, "app.kubernetes.io/component=manager", o.LiveTVServiceSelector)
}

func TestLiveTVSelectorMustParse(t *testing.T) {
 o := DefaultOptions()
 o.LiveTVServiceSelector = "no-equals"
 assert.ErrorContains(t, o.Validate(), "--livetv-service-selector")
 o.LiveTVServiceSelector = ""
 assert.ErrorContains(t, o.Validate(), "--livetv-service-selector")
 o.LiveTVBindAddress = k8s.DisabledBindAddress
 assert.NoError(t, o.Validate(), "a disabled Live TV needs no selector")
}

func TestLiveTVBindAddressMustNameAPort(t *testing.T) {
 o := DefaultOptions()
 o.LiveTVBindAddress = "localhost"
 assert.ErrorContains(t, o.Validate(), "--livetv-bind-address")
}
```

`app/livetv/manager/register_test.go` covers two behaviours:

- **`Register` with `BindAddress: "0"`** adds nothing. Check it with a
  fake `ctrl.Manager`, or an envtest manager whose runnable count is read
  through `mgr.Add` interception: wrap a manager in a struct that counts
  `Add` calls.
- **`TargetPort`** is parsed from `":5004"` as 5004 and from
  `"0.0.0.0:6000"` as 6000, through the exported helper
  `livetvmanager.TargetPort(bind string) (int32, error)`.

- [ ] **Step 2: Run.** `go test ./internal/cli/manager/ ./app/livetv/manager/`. Expected: FAIL to compile.
- [ ] **Step 3: Implement.**
  - **`Validate`:** unless the bind address is `"0"`, require
    `net.SplitHostPort` with a numeric port of 1-65535, and a selector of
    one or more `k=v` pairs (`labels.ConvertSelectorToLabelsMap`).
  - **`Register`:**
    1. Return nil when `BindAddress == "0"`.
    2. Take `store := bus.ObjectStore(events.BucketLiveTV)`.
    3. Make two channels, `provEvents := make(chan event.GenericEvent, 64)`
       and `chEvents := make(chan event.GenericEvent, 64)`.
    4. `notify` converts a key into a `GenericEvent` with an `IPTVProvider`
       stub object `{Name, Namespace}`. It sends to both channels without
       blocking: on a full channel it drops the event, since the next
       snapshot change re-notifies and the reconcilers also resync.
    5. `tn := tuner.New(tuner.Options{BindAddress, Store: store, HTTP: &http.Client{Transport: http.DefaultTransport}, Now: time.Now, Debounce: 2 * time.Second, Notify: notify})`,
       then `mgr.Add(tn)`. That makes it leader-only, through
       `NeedLeaderElection`.
    6. Set up the provider reconciler with `SecretReader: mgr.GetAPIReader()`,
       since Secrets are not cached in the manager (`options.go:150`).
       `Namespace` is `o.Options.Namespace`, `Selector` comes from the
       flag, and `TargetPort` from `TargetPort(bind)`.
    7. Set up the channel reconciler with `Snapshots: tn`.
  - **`run.go`:**
    `{"livetv", func() error { return livetvmanager.Register(mgr, bus, livetvmanager.Options{Options: o.Options, BindAddress: o.LiveTVBindAddress, Selector: sel}) }}`.
    Place it between `caption` and `autoscale`, and update the comment
    listing the order.
- [ ] **Step 4: Run.** `go test ./internal/cli/manager/ ./app/livetv/...` and `go build ./cmd/manager`. Expected: PASS and a clean build.
  - Check that `go list -deps ./cmd/manager | grep -E 'ffgo|purego'`
    prints nothing.
  - Run `go test ./test/guards/ -run 'TestControllerNamesAreUniqueAcrossTheBinary|TestEveryServiceComponentIsRegistered'`.
    If the package does not compile on the branch, that is the
    pre-existing Wave 6 breakage: ledger it as "deferred to Task 13", and
    do not fix test/guards here.
- [ ] **Step 5: Commit.** `git add app/livetv/manager internal/cli/manager/livetv_options_test.go && git commit -m "feat(manager): the livetv step -- tuner runnable and both controllers, --livetv-bind-address and --livetv-service-selector -- Live TV spec §5, §6" -- app/livetv/manager internal/cli/manager`

---

## Part B: installers, e2e and docs (after Wave 6 lands)

Before Task 11, rebase `livetv` onto the current `unify-manager-agent` and
check the Wave 6 files the Global Constraints name. If they are absent,
stop.

### Task 11: RBAC, both installers' manager flags and port, and the NetworkPolicy

**Files:**

- Modify:
  - `Makefile`: add `./app/livetv/...` to `RBAC_PATHS_manager`.
  - `config/rbac/manager_role.yaml` and `charts/clustarr/templates/rbac.yaml`:
    the generated rules, copied between the role's BEGIN and END markers.
  - `charts/clustarr/templates/deployments.yaml`:
    - in `$managerArgs`, `--livetv-bind-address=:<livetv.port>` (or `0`
      when `livetv.enabled` is false);
    - `--livetv-service-selector=` the manager's selector labels joined
      `k=v,k=v`;
    - a container port `livetv`.
  - `charts/clustarr/values.yaml` and `values.schema.json`: a top-level
    `livetv` block, closed with `additionalProperties: false`.
  - `config/manager/manager.yaml`: the same args and port, with
    kustomize's selector labels.
- Create:
  - `charts/clustarr/templates/livetv-networkpolicy.yaml`
  - `config/manager/livetv-networkpolicy.yaml`, added to
    `config/manager/kustomization.yaml`
  - `test/guards/livetv_test.go`

**The values:**

```yaml
livetv:
  # Live TV (spec 2026-10-07): the tuner runs in the manager on this port.
  enabled: true
  port: 5004
  networkPolicy:
    enabled: true
    # Peers allowed to reach the tuner port. Default: Plex's pods in this
    # namespace. While Plex runs elsewhere, add a namespaceSelector here.
    allowFrom:
      - podSelector:
          matchLabels:
            app.kubernetes.io/component: plex
```

- **Ruling:** the spec's `livetv.memory` added to the manager's resources
  becomes a raised manager memory default. The default request and limit
  each gain 128Mi, with a comment saying why, rather than Helm arithmetic
  on quantities a user may override.
- **Kustomize:** the same NetworkPolicy, with the same default peer.

- [ ] **Step 1: Write the failing guards** in `test/guards/livetv_test.go`.
  They render both installers through the helpers W6.11 left in
  `test/guards/topology_test.go` (`installers`, `containerEnv`, `argIn`)
  and `test/installtest.Workloads`. Read those first and use their exact
  signatures.
  - **`TestLiveTVServicesSelectTheManager`:** for each installer:
    - `--livetv-service-selector`'s pairs are a subset of the manager pod
      template's labels;
    - and match none of the other Deployments' pod templates in the
      render;
    - `--livetv-bind-address`'s port equals the manager container's
      `livetv` port.
  - **`TestLiveTVPolicyListsEveryManagerPort`:** for each installer, the
    NetworkPolicy:
    - selects the manager pod (its `podSelector` labels are a subset of
      the pod template's);
    - has `policyTypes: [Ingress]`;
    - has exactly one rule whose ports are `{livetv}` with a non-empty
      `from`;
    - has one rule without `from` whose ports are every other port of the
      manager container: `metrics`, `health`, the external-metrics port,
      and whatever else the manager declares.
  - **`TestLiveTVDisabledRendersNoPolicyAndPortZero`:** under Helm with
    `--set livetv.enabled=false`, there is no NetworkPolicy and the
    argument is `--livetv-bind-address=0`.
- [ ] **Step 2: Run.** `go test ./test/guards/ -run LiveTV -v` (helm dependency build first, per CLAUDE.md). Expected: FAIL.
- [ ] **Step 3: Implement** the files above.
  - **One port list for both:** the chart's policy takes its port list
    from a named template, `clustarr.managerPorts`, that the Deployment
    also uses, so the two cannot drift. The guard holds that.
  - **RBAC:** `make manifests`, then copy the regenerated manager rules
    into the chart between the markers. `TestChartRBACMatchesTheGeneratedRoles`
    compares them.
- [ ] **Step 4: Run.** `go test ./test/guards/... ./cmd/manager/...`. Expected: PASS, including:
  - `TestChartRBACMatchesTheGeneratedRoles`;
  - `TestEveryPackageWithRBACMarkersIsInARole`;
  - `TestEveryCRDKindAControllerTouchesHasAnRBACMarker`;
  - `TestEveryBuiltinKindAControllerTouchesHasAnRBACMarker`;
  - W6.11's `TestInstallerArgsParseAsTheManager`, which proves both
    installers' new flags parse.
- [ ] **Step 5: Commit.** `git add charts/clustarr/templates/livetv-networkpolicy.yaml config/manager/livetv-networkpolicy.yaml test/guards/livetv_test.go && git commit -m "feat(installers): Live TV in the manager -- RBAC, --livetv-* flags and port, the NetworkPolicy on the tuner port only -- Live TV spec §5.3" -- Makefile config charts test/guards/livetv_test.go`

### Task 12: e2e scenario 19 and the `iptv-stub` fixture

**Files:**

- Create:
  - `test/fixtures/iptvstub/server.go`
  - `test/fixtures/iptvstub/server_test.go`
  - `test/fixtures/iptvstub_cmd.go`
  - `config/e2e/iptv-stub.yaml`
  - `test/e2e/livetv_test.go`
- Modify:
  - `test/fixtures/root.go` (`AddCommand`)
  - `config/e2e/kustomization.yaml`
  - `test/e2e/main_test.go` (`expectedCRDCount` += 2, `deploymentRoster`
    += `iptv-stub`)

**Interfaces:**

- Produces:
  - `iptvstub.NewHandler(base string) http.Handler`, serving:
    - `/get.php`: an M3U of three channels in group `E2E`, whose stream
      URLs are `<base>/live/e2e/secret/<n>.ts`;
    - `/live/e2e/secret/{n}.ts`: endless null TS packets, PID 0x1FFF;
    - `/guide.xml`: an XMLTV guide for `e2e.1` to `e2e.3` over the
      current day plus 7;
    - `/stats`: JSON `{"connections": {"<n>": int}}`.
  - `const fixtureIPTVStubService = "iptv-stub"`.

- [ ] **Step 1: Write the stub's unit test.** `server_test.go`:
  `m3u.Parse` of `/get.php` gives three entries; `xmltv.Parse` of
  `/guide.xml` gives three channels; a stream read gives 188-aligned
  `0x47` packets; `/stats` counts one connection.
- [ ] **Step 2: Run it.** It fails. Implement the stub and the cobra
  command, both copied from `importliststub`'s shape: an `--addr` flag,
  and `--base` defaulting to `http://iptv-stub.clustarr-system.svc`. Run
  it again: it passes.
- [ ] **Step 3: Write the scenario** `test/e2e/livetv_test.go`, `//go:build e2e`.

```go
func TestLiveTVLineupGuideAndASharedStream(t *testing.T) {
 ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
 defer cancel()
 requireFixtureService(ctx, t, fixtureIPTVStubService)
 name := uniqueName("livetv")
 p := &clustarrv1.IPTVProvider{
  ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace},
  Spec: clustarrv1.IPTVProviderSpec{
   Tuners:   1,
   Playlist: clustarrv1.PlaylistSource{URL: "http://iptv-stub." + Namespace + ".svc/get.php"},
   Guides:   []clustarrv1.XMLTVSource{{Name: "e2e", URL: "http://iptv-stub." + Namespace + ".svc/guide.xml"}},
   Filters:  []clustarrv1.ChannelFilter{{Name: "e2e", Type: clustarrv1.FilterGroupTitle, Match: "E2E"}},
  },
 }
 require.NoError(t, k8sClient.Create(ctx, p))
 cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), p) })
 for i, key := range []string{"t:e2e.1", "t:e2e.2"} {
  ch := &clustarrv1.IPTVChannel{
   ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", name, i), Namespace: Namespace},
   Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: name, Key: key, Number: fmt.Sprintf("%d", 1001+i), Active: true},
  }
  require.NoError(t, k8sClient.Create(ctx, ch))
  cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), ch) })
 }
 waitFor(t, ctx, 5*time.Minute, "the provider is Ready with two active channels", func(ctx context.Context) (bool, error) {
  var got clustarrv1.IPTVProvider
  if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &got); err != nil {
   return false, err
  }
  return meta.IsStatusConditionTrue(got.Status.Conditions, clustarrv1.ConditionReady) &&
   got.Status.Lineup != nil && got.Status.Lineup.Active == 2 && got.Status.Address != "", nil
 })
 base, stop := portForwardService(ctx, t, iptvprovider.ServiceName(Namespace, name), 80)
 defer stop()
 prefix := base + "/livetv/" + Namespace + "/" + name
 // lineup.json: both channels; xmltv.xml: both ids; then two viewers of 1001 over one upstream, and 1002 refused.
 ...
}
```

  Write the elided part in full:

- `GET prefix/lineup.json` decodes to two entries, `1001` and `1002`;
- `GET prefix/xmltv.xml` contains `<channel id="1001">` and
    `<channel id="1002">`;
- two goroutines each `GET prefix/stream/1001` and `io.CopyN` 1 MiB;
- then the stub's `/stats`, port-forwarded, shows `connections["1"] == 1`;
- while both viewers hold, `GET prefix/stream/1002` gives 503.

- [ ] **Step 4: Wire it.**
  - `config/e2e/iptv-stub.yaml`: a Deployment and a Service, as
    `importlist-stub.yaml`, with `args: ["iptv-stub","--addr=:8080"]`;
  - its kustomization entry;
  - the roster and the CRD count.
- [ ] **Step 5: Run.** `hack/e2e.sh` with `E2E_ARGS='-run TestLiveTV -v'`. Expected: PASS on kind.
  - If any other scenario is red for reasons outside this plan, record it
    by name in the ledger; do not fix it here.
  - Then run the whole suite once (`hack/e2e.sh`) and record the result.
- [ ] **Step 6: Commit.** `git add test/fixtures/iptvstub test/fixtures/iptvstub_cmd.go config/e2e/iptv-stub.yaml test/e2e/livetv_test.go && git commit -m "test(e2e): scenario 19 -- Live TV lineup, XEPG guide and one shared upstream on kind" -- test config/e2e`

### Task 13: Docs, gate and hand-off

**Files:**

- Modify:
  - `CLAUDE.md`:
    - a **Live TV** paragraph after **Transcoding**, a dense summary of
      spec §§3-6 as built;
    - the services table: no new row; add "Live TV tuner and controllers
      (`app/livetv`)" to the manager's description;
    - Status: one Phase 1 line.
  - The spec, on main: an "As built (phase 1, clustarr)" section. This is
    a separate commit on main, in `/home/appkins/src/mediactl/clustarr`,
    pathspec-only.

- [ ] **Step 1: Write the docs.** Name every ruling the ledger holds.
- [ ] **Step 2: Gate in a clean worktree.**
  - `git worktree add ../clustarr-livetv-gate livetv`;
  - `helm dependency build charts/clustarr`;
  - `make pg-assets test lint > <workspace>/gate.log 2>&1`.
  - Expected: exit 0. Read the log's tail.
- [ ] **Step 3: Hand off.**
  - Tell clustarr-c3, by SendMessage to `clustarr-c3`, that branch
    `livetv`, rebased on the current `unify-manager-agent`, is ready to
    merge. Name the commits by subject.
  - Stop: the merge and any deploy are the owner's call.
  - Remove the gate worktree.
