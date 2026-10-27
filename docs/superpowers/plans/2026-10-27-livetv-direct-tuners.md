# Live TV Phase A: path-addressed tuners — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Plex adds each block of channels by one URL on the livetv Service, `http://<release>-livetv.<ns>.svc.cluster.local/hdhr/<deviceID>`, and cluster-plex runs no proxy, listener or link-local address for Live TV.

**Architecture:** clustarr's tuner (a manager runnable behind the selector-less livetv Service) serves each block's HDHomeRun device under `/hdhr/<deviceID>/…`, with every URL it hands out built from the request's Host and that path; the IPTVProvider controller publishes each block's absolute `uri` in status and allocates no ports. cluster-plex's lease holder registers that `uri` with PMS as the device, keys "what is mine" on DeviceID (a device at a stale URI is replaced), and its per-pod ProxySet and link-local address are deleted.

**Tech Stack:** Go 1.25, controller-runtime, kubebuilder markers (`make generate manifests`), Helm 4 and kustomize (`test/guards`), cluster-plex's Go manager and its `fakepms_test.go` harness, a real PMS for task 0's recording.

**Spec:** `docs/superpowers/specs/2026-10-27-livetv-direct-tuners-and-ui-design.md` §3, §4, §7, §8, §9 (amending `2026-10-07-iptv-live-tv-design.md` §5.0/§6.1 and `2026-10-20-iptv-xtream-ranges-design.md`).

## Global Constraints

- Cold turkey (CLAUDE.md): no code names the link-local address, the old `/livetv/<ns>/<name>/devices/…` route or a port once removed; no compatibility branch.
- The manager is the only writer of CRs; `status.blocks` is written under `k8s.ManagerLiveTV` through `pkg/k8s.PatchStatus` as today (`app/livetv/controller/iptvprovider/status.go:41`).
- Every status list stays capped; `BlockStatus.URI` carries `MaxLength=512`.
- Envtests are not run until the final batch (owner, 2026-10-08): edit them to the new shape, name them in the commit, and run only the unit packages named in each task. `go build ./...`, `go vet` on the touched packages, `make generate manifests` (clean) and `make brand-gate` are the gates.
- Commits are pathspec commits (`git commit -F - -- <paths>`), new files `git add`ed first; never `git stash`; never push from a task.
- Each repo gets its own commits: clustarr (`/home/appkins/src/mediactl/clustarr`, main) and cluster-plex (`/home/appkins/src/mediactl/cluster-plex`, main).
- The GPL header from `hack/boilerplate.go.txt` on every new Go file.

## Review Focus

1. A device URI PMS stores with a trailing slash, or with `:80` spelled out, must compare equal to `status.blocks[].uri` (Task 4's `sameURL` test covers both forms).
2. A `status.blocks[].uri` that is empty (a provider written by the old manager, or Ready=False before the first render) must register nothing and delete nothing: Task 4's converger test "a block without a URI is skipped and recorded".
3. A request to the tuner whose Host carries the Service FQDN with `:80` must build URLs without doubling the port: Task 1's test asserts `http://clustarr-livetv.media.svc.cluster.local:80/hdhr/<id>/stream/1001` is handed out verbatim as Host gives it (PMS dials what it was given).
4. Two providers' blocks under `/hdhr/<id>` must never answer each other's lineup: Task 1's test serves two providers and asserts each id answers its own provider's numbers and an unknown id is 404.
5. The old PMS devices at `169.254.47.1:<port>` must be replaced in one pass, DVRs first, with the Lease kept and the saved hashes cleared, so the new DVR gets its channel map and guide: Task 4's "a device at a stale URI is replaced" test.

---

### Task 0: Record PMS's handling of a path in a device URI (cluster-plex)

Runs against the owner's PMS on talos and creates and deletes a DVR there; **run it only with the owner's OK**, announced to the other sessions first. Its outcome gates Tasks 1-5: if PMS strips the path or ignores `BaseURL`, stop and bring the spec's §8 fallback (an HTTPRoute per block) back to the owner before building anything else.

**Files:**
- Modify: `hack/hdhrprobe/main.go` (the `deviceMux` routes gain a prefix)
- Modify: `hack/record-livetv.sh`
- Create: `pkg/plex/api/testdata/livetv/20-path-add-device.json`, `21-path-devices.json`, `22-path-create-dvr.json`, `23-path-dvr.json`, `24-same-id-second-uri.json`, `25-same-id-devices.json`
- Modify: `docs/livetv-pms-calls.md` (a new section)

**Interfaces:**
- Produces: the recorded answers, and one paragraph in `docs/livetv-pms-calls.md` stating (a) the `uri` PMS lists for a device registered as `http://<host>/hdhr/CAFE0002`, (b) which URL PMS fetched `discover.json`, `lineup.json` and `xmltv.xml` from (the probe logs each request path), (c) PMS's answer to `POST /media/grabbers/devices?uri=http://<host>/hdhr/CAFE0002-other` carrying the same `DeviceID` `CAFE0002` in its `discover.json` (a conflict status, or a second device).

- [ ] **Step 1: Give the probe a path prefix.** In `hack/hdhrprobe/main.go` add `flag.String("prefix", "", "serve every device file below this path, and build BaseURL and LineupURL with it")`; register each `deviceMux` route at `prefix+"/discover.json"` and so on, and make `base(r)` return `scheme://host+prefix`. Log every request's method and path to stderr (`log.Printf("%s %s", r.Method, r.URL.Path)`) so the recording shows what PMS fetched.
- [ ] **Step 2: Add the two recordings to the script.** After the existing `14-dvrs-after` call in `hack/record-livetv.sh`, restart the probe Deployment with `-prefix /hdhr/CAFE0002 -device-id CAFE0002` (a second `probe.yaml` variant or `kubectl set args`), then: `call POST "/media/grabbers/devices?uri=$(q "$PROBE/hdhr/CAFE0002")" 20-path-add-device`; `call GET /media/grabbers/devices 21-path-devices`; `call POST "/livetv/dvrs?language=eng&device=$(q "device://tv.plex.grabbers.hdhomerun/CAFE0002")&lineup=$(q "lineup://tv.plex.providers.epg.xmltv/$PROBE/hdhr/CAFE0002/xmltv.xml#probe-path")" 22-path-create-dvr`; `call GET /livetv/dvrs/$DVR 23-path-dvr`; then a third probe at `-prefix /hdhr/other -device-id CAFE0002` and `call POST "/media/grabbers/devices?uri=$(q "$PROBE/hdhr/other")" 24-same-id-second-uri`; `call GET /media/grabbers/devices 25-same-id-devices`; then delete the DVR and devices as the script's cleanup does. Capture the probe pod's log (`k logs deploy/hdhrprobe`) into `$OUT/20-path-probe.log`.
- [ ] **Step 3: Run it** with `CTX=admin@talos NS=<plex namespace> hack/record-livetv.sh` once the owner says go. Expected: `20-path-add-device` answers 200, `21-path-devices` lists `uri` equal to `http://<ip>/hdhr/CAFE0002`, the probe log shows `GET /hdhr/CAFE0002/discover.json` and `GET /hdhr/CAFE0002/lineup.json`, `23-path-dvr` lists the guide URL with the path.
- [ ] **Step 4: Write the section** "A device URI with a path (Task 0, 2026-10-27)" in `docs/livetv-pms-calls.md`, stating (a), (b), (c) from the recordings, in the style of "Link-local devices, two on one address".
- [ ] **Step 5: Commit** `git add pkg/plex/api/testdata/livetv/2*.json pkg/plex/api/testdata/livetv/20-path-probe.log && git commit -F - -- hack/hdhrprobe/main.go hack/record-livetv.sh pkg/plex/api/testdata/livetv docs/livetv-pms-calls.md` with the message `hack(livetv): record PMS's handling of a device URI with a path, and of a known DeviceID at a second URI (the direct-tuners design's task 0)`.

---

### Task 1: The tuner serves each block under `/hdhr/<deviceID>` (clustarr)

**Files:**
- Modify: `app/livetv/tuner/http.go:51-66` (doc), `98-135` (`baseURL`, `serveHTTP`), `137-139` (`serveDevice` signature)
- Modify: `app/livetv/tuner/config.go:77-89` (`BlockConfig` doc, `DeviceEndpoint`)
- Modify: `app/livetv/tuner/tuner.go:333-337` (add `lookupDevice`)
- Test: `app/livetv/tuner/tuner_test.go:135-150` (the `proxied` helper goes), `325-363`

**Interfaces:**
- Produces: `func DeviceEndpoint(deviceID string) string` returning `"/hdhr/" + deviceID` (the `key` parameter is gone; Task 2 calls the new form).
- Produces: the routes `/hdhr/<deviceID>/<file>` for every `<file>` `serveDevice` and `serveCommon` serve today, and `/livetv/<ns>/<name>/<file>?from=&to=` unchanged.
- Produces: `func (t *Tuner) lookupDevice(deviceID string) (*provider, types.NamespacedName)` (unexported; nil, zero when no provider's `blocks` holds the id).

- [ ] **Step 1: Write the failing tests** in `tuner_test.go`. Replace `proxied` and the first block of `TestEachDeviceServesItsOwnBlock…` (the test at 325-363) with:

```go
func TestABlockIsServedUnderHDHRByItsID(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	_, body := h.get(t, "clustarr-livetv.media.svc.cluster.local:80", "/hdhr/"+deviceA+"/lineup.json")
	assert.Equal(t, []hdhr.LineupEntry{
		{GuideNumber: "1001", GuideName: "BBC One", URL: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/" + deviceA + "/stream/1001"},
		{GuideNumber: "1002", GuideName: "News, Live", URL: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/" + deviceA + "/stream/1002"},
	}, lineupOf(t, body), "Host as PMS dialled it, the device's path, the block's numbers only")
	_, body = h.get(t, "anything", "/hdhr/"+deviceA+"/discover.json")
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	assert.Equal(t, deviceA, d["DeviceID"])
	assert.Equal(t, "http://anything/hdhr/"+deviceA, d["BaseURL"])
	assert.Equal(t, "http://anything/hdhr/"+deviceA+"/lineup.json", d["LineupURL"])
	code, _ := h.get(t, "x", "/hdhr/FFFFFFFF/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "a device no provider has")
	code, _ = h.get(t, "x", "/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "no root route")
	code, _ = h.get(t, "x", tuner.DeviceEndpoint(deviceA)+"/../../livetv/media/news/devices/"+deviceA+"/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "the old device route is gone")
}

func TestForwardedHostAndProtoAreHonouredAndPrefixIsNot(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	resp := h.do(t, "internal", "/hdhr/"+deviceA+"/discover.json",
		http.Header{"X-Forwarded-Host": {"tv.example.com"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Prefix": {"/elsewhere"}})
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var d map[string]any
	require.NoError(t, json.Unmarshal(b, &d))
	assert.Equal(t, "https://tv.example.com/hdhr/"+deviceA, d["BaseURL"], "the path is the request's; a prefix header changes nothing")
}

func TestTwoProvidersBlocksNeverAnswerEachOther(t *testing.T) {
	// a second provider "media/sport" with one block whose DeviceID is
	// hdhr.BlockDeviceID("media", "sport", "2001") and number 2001;
	// /hdhr/<sport's id>/lineup.json lists 2001 only, /hdhr/<deviceA>/lineup.json 1001-1002 only.
}
```

Keep the `device()` helper that builds a device path, now `"/hdhr/" + id + "/" + file`. Every other test that calls `device(...)` or `proxied(...)` is updated to the new path and to no proxy headers; the assertions on hashes, ETags, ranges, logos and streams stay.

- [ ] **Step 2: Run** `go test ./app/livetv/tuner -run 'TestABlockIsServedUnderHDHR|TestForwardedHost|TestTwoProviders' -v`. Expected: FAIL (404 on `/hdhr/…`).
- [ ] **Step 3: Implement.** In `config.go`: `func DeviceEndpoint(deviceID string) string { return "/hdhr/" + deviceID }`, and the `BlockConfig` doc says "served at DeviceEndpoint on the livetv Service, which Plex is given directly". In `tuner.go` add `lookupDevice` scanning `t.providers` under `t.mu` for one whose `blocks[deviceID] != nil`. In `http.go`: `baseURL(r *http.Request, prefix string)` keeps Host and `X-Forwarded-Host`/`-Proto` and drops the `X-Forwarded-Prefix` branch; `serveHTTP` first tries `strings.CutPrefix(r.URL.Path, "/hdhr/")`, splits `<deviceID>/<file>` on the first `/`, calls `lookupDevice`, 404 when nil, else `t.serveDevice(w, r, p, key, deviceID, file, DeviceEndpoint(deviceID))`; the `/livetv/<ns>/<name>/` branch keeps `serveRange` only (the `devices/` sub-branch is deleted). Rewrite the `Handler` doc comment for the two route families.
- [ ] **Step 4: Run** `go test ./app/livetv/tuner/... ./pkg/iptv/...` Expected: PASS.
- [ ] **Step 5: Commit** `git commit -F - -- app/livetv/tuner` with `feat(livetv): the tuner serves each block under /hdhr/<deviceID>, the path Plex is given -- …`.

---

### Task 2: Blocks publish a URI, not a port (clustarr)

**Files:**
- Modify: `api/clustarr/v1alpha1/iptvprovider_types.go:374-385` (`Endpoint`→`Path`, `Port` removed, `URI` added), `423-426` (kind doc), `439` (the `Port` printer column removed)
- Modify: `app/livetv/controller/iptvprovider/blocks.go:49-50, 64-94, 131-132, 187-240, 265-277` (ports removed), `232` (`Path`, `URI`)
- Modify: `app/livetv/controller/iptvprovider/reconciler.go:55-59` (`Ports` removed; `Address` doc)
- Modify: `app/livetv/manager/register.go:55-80, 132` (`Ports`, `DefaultPortRange`, `ParsePortRange` removed; the FQDN address)
- Modify: `internal/cli/manager/command.go:169-174`, `internal/cli/manager/options.go:126-130, 159, 262-264` (`--livetv-port-range` removed)
- Generated: `api/applyconfiguration/clustarr/v1alpha1/blockstatus.go`, `config/crd/bases/clustarr.io_iptvproviders.yaml`, `api/clustarr/v1alpha1/zz_generated.deepcopy.go` (by `make generate manifests`)
- Test: `app/livetv/controller/iptvprovider/status_internal_test.go:85`, `reconciler_envtest_test.go:133, 224, 265-345, 407, 531` (envtest, edited not run), `test/e2e/livetv_test.go:116-119`

**Interfaces:**
- Consumes: `tuner.DeviceEndpoint(deviceID string) string` (Task 1).
- Produces: `BlockStatus.Path string` (`json:"path,omitempty"`, MaxLength 512: `/hdhr/<deviceID>`), `BlockStatus.URI string` (`json:"uri,omitempty"`, MaxLength 512: `http://<status.address><path>`); `Reconciler.Address` is `<service>.<namespace>.svc.cluster.local:80`.
- Produces: the manager's Live TV flags are exactly `--livetv-bind-address` and `--livetv-service` (Task 3's guards count two).

- [ ] **Step 1: Write the failing unit test** in `status_internal_test.go`, beside the test at line 78:

```go
func TestABlockCarriesItsPathAndURIAndNoPort(t *testing.T) {
	pl := plan{blocks: []clustarrv1.BlockStatus{{Start: "1001", DeviceID: "CAFE0001", Path: "/hdhr/CAFE0001", URI: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001"}}}
	ac := render(provider("media", "news"), "clustarr-livetv.media.svc.cluster.local:80", pl, fakeTuner{}, validCondition(), false)
	b := ac.Status.Blocks[0]
	assert.Equal(t, "/hdhr/CAFE0001", *b.Path)
	assert.Equal(t, "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001", *b.URI)
}
```
(use the file's existing helpers for the provider, tuner and condition; the point is the two leaves are rendered). And in `blocks.go`'s own unit test file (create `blocks_internal_test.go` if none): `TestPlanBlocksGivesEachBlockItsURI` calling the pure parts, asserting `URI == "http://" + address + "/hdhr/" + DeviceID` for two blocks and that `BlockStatus` has no port.
- [ ] **Step 2: Run** `go test ./app/livetv/controller/iptvprovider -run 'TestABlockCarries|TestPlanBlocksGives' -v`. Expected: FAIL to compile (`Path`, `URI` undefined).
- [ ] **Step 3: Change the API type.** Replace the `Endpoint` and `Port` fields with:

```go
	// Path is the block's device on Address: /hdhr/<deviceID>, which Plex
	// is given (with Address) as the device's network address.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path,omitempty"`

	// URI is the block's device address as Plex registers it:
	// http://<address><path>.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	URI string `json:"uri,omitempty"`
```
Update the `IPTVProvider` kind doc (no ports, no proxies) and `GuidePath`'s doc ("under its path"). Remove the `Port` printer column. Run `make generate manifests`; expected: a clean generation with `blockstatus.go` carrying `WithPath` and `WithURI`.
- [ ] **Step 4: Implement the controller.** In `blocks.go` delete `PortRange`, `ports`, `errNoFreePort`, `usedByOthers`, `taken`, `kept`, `allocate`, `heldByAny` and the port lines in the loop; set `bs.Path = tuner.DeviceEndpoint(bs.DeviceID)` and `bs.URI = "http://" + r.Address + bs.Path` (`planBlocks` reads `r.Address`; an empty `Address` leaves `URI` empty). In `reconciler.go` delete `Ports`; in `status.go`'s render emit `WithPath` and `WithURI` where `WithEndpoint`/`WithPort` were. In `register.go` delete `Ports`, `DefaultPortRange`, `ParsePortRange`; set `Address: o.Service + "." + o.Namespace + ".svc.cluster.local:80"`. In `internal/cli/manager` delete the `--livetv-port-range` flag, `LiveTVPortRange` and its validation.
- [ ] **Step 5: Edit the envtests** to the new shape without running them: at `reconciler_envtest_test.go:133` drop `Ports`; delete `TestPortsAreUniqueAcrossProvidersDespiteACacheLag` (265) and `TestTheOlderProviderKeepsAPortClaimedTwice` (328); in `TestAnAddedChannelMovesNoOtherChannelAndPortsAndIDsHold` (287) assert `Path` and `URI` hold where `Port` did, renamed `…AndURIsAndIDsHold`; at 224, 407, 531 replace `Port` with `URI`; in `TestAProviderPublishesItsBlocksAddressAndTunerConfig` (240) assert `Address == "livetv.clustarr-system.svc.cluster.local:80"` and each block's `URI == "http://" + Address + "/hdhr/" + DeviceID`. In `test/e2e/livetv_test.go:116-119` replace the port assertions with `assert.Equal(t, "/hdhr/"+blocks["1001"].DeviceID, blocks["1001"].Path)` and `assert.Equal(t, "http://"+got.Status.Address+blocks["1001"].Path, blocks["1001"].URI)`.
- [ ] **Step 6: Run** `go build ./... && go vet ./app/livetv/... ./internal/cli/manager/... && go test ./app/livetv/controller/... ./app/livetv/manager/... ./internal/cli/manager/...` Expected: PASS (the envtest package skips without `KUBEBUILDER_ASSETS`; say so in the commit).
- [ ] **Step 7: Commit** `git add app/livetv/controller/iptvprovider/blocks_internal_test.go && git commit -F - -- api app/livetv internal/cli/manager config/crd test/e2e/livetv_test.go` with `feat(livetv): a block publishes its path and URI on the livetv Service and takes no port; --livetv-port-range is gone -- …`.

---

### Task 3: Installers, guards and docs (clustarr)

**Files:**
- Modify: `charts/clustarr/values.yaml:525-535` (`livetv.portRange` removed), `charts/clustarr/values.schema.json:368-373`, `charts/clustarr/templates/deployments.yaml:44-52`, `charts/clustarr/README.md:459-462, 519-524`
- Modify: `config/manager/manager.yaml:91-95`
- Modify: `test/guards/livetv/livetv_test.go:181-215, 283-300`
- Modify: `CLAUDE.md` (the Live TV section's "Proxies" and "Ranges" bullets), `docs/superpowers/specs/2026-10-07-iptv-live-tv-design.md` (a dated note under §5.0 and §6.1 pointing at the new design), `docs/superpowers/specs/2026-10-20-iptv-xtream-ranges-design.md` (the same note)

**Interfaces:**
- Consumes: the manager's two Live TV flags (Task 2).

- [ ] **Step 1: Make the guards fail first.** In `TestLiveTVServiceIsSelectorLessInBothInstallers` delete the `portRange` assertion and add `_, has := arg(ctr.Args, "livetv-port-range"); assert.False(t, has, "no port range: Plex is given each block's path")`. In `TestBothInstallersLiveTVArgsParseAsTheManager` change `require.Len(t, args, 3)` to `2` and drop the `portRange` line. Run `go test ./test/guards/livetv/ -run 'TestLiveTVService|TestBothInstallersLiveTV' -v`. Expected: FAIL (the chart still renders the flag).
- [ ] **Step 2: Change the installers.** Remove `livetv.portRange` from `values.yaml` (and its comment), from the schema's `required` and `properties`, and the `--livetv-port-range` line from `deployments.yaml` and `config/manager/manager.yaml`'s args; update the comments beside them ("the tuner on livetv.port and the one livetv Service; Plex is given each block's path"). README: drop the `livetv.portRange` row; in the cluster-plex paragraph replace "serve each block's port on a link-local address to the `<release>-livetv` Service" with "register each block's `status.blocks[].uri` on the `<release>-livetv` Service with Plex".
- [ ] **Step 3: Run** `go test ./test/guards/livetv/ && helm lint --strict charts/clustarr && make brand-gate`. Expected: PASS.
- [ ] **Step 4: Docs.** CLAUDE.md's Live TV section: replace the "Proxies" bullet with "**Direct.** Plex is given each block's `status.blocks[].uri`, `http://<release>-livetv.<ns>.svc.cluster.local/hdhr/<deviceID>`; the tuner builds every URL it hands out from the request's Host and that path (`X-Forwarded-Host`/`-Proto` honoured; no prefix header), so nothing in a Plex pod proxies or listens for Live TV (2026-10-27, `docs/superpowers/specs/2026-10-27-livetv-direct-tuners-and-ui-design.md`)"; delete the port-range sentence under "Each block gets". Add the dated "superseded" note to the two older specs' affected sections.
- [ ] **Step 5: Commit** `git commit -F - -- charts/clustarr config/manager test/guards/livetv CLAUDE.md docs/superpowers/specs/2026-10-07-iptv-live-tv-design.md docs/superpowers/specs/2026-10-20-iptv-xtream-ranges-design.md` with `chore(livetv): the installers render no port range and the guards hold it; the docs say Plex is given each block's path -- …`.

---

### Task 4: cluster-plex registers the URI and runs no proxy

**Files:**
- Modify: `pkg/plex/livetv/converge.go:40, 56, 88-97, 147-149, 239-248, 363-384, 464-493, 695-724, 748-813`
- Modify: `pkg/plex/livetv/watch.go:139-151`
- Delete: `pkg/plex/livetv/proxies.go`, `proxies_test.go`, `linklocal_linux.go`, `linklocal_other.go`, `linklocal_netns_test.go`
- Modify: `cmd/manager/livetv.go:40-55, 140-203` (no `LocalAddress`, no `EnsureLinkLocal`, no `ProxySet`; `startLiveTV` keeps the watcher and the first-list wait), `cmd/manager/config_provision.go:93-144` (`LocalAddress` and its validation removed; `metadataAddress` goes with it)
- Test: `pkg/plex/livetv/converge_test.go:104-130, 233-270, 415-460`, `pkg/plex/livetv/watch_test.go`, `cmd/manager/config_provision_test.go` (if it names `localAddress`)

**Interfaces:**
- Consumes: `status.blocks[].uri` and `.path` (Task 2).
- Produces: `Block.Path string`, `Block.URI string` (the `Endpoint` and `Port` fields removed); `func sameURL(a, b string) bool` (scheme, hostname, port with 80/443 implied by scheme, path with a trailing `/` trimmed); `func (ps *pass) ghosts(address string) []plexapi.DVR` keyed on the lineup URL's prefix `http://<address>/hdhr/`; the Event reason `ReasonDeviceIDTaken` deleted and `ReasonReplaced = "DVRDeviceReplaced"` added.

- [ ] **Step 1: Write the failing converger tests** in `converge_test.go` using the file's harness (`r.pms.devices`, `r.pms.dvrs`, the provider builders):

```go
func TestThreeBlocksGetALeaseADeviceAtItsURIAndADVREach(t *testing.T) {
	// as the test at line 104, with each device's URI
	// "http://clustarr-livetv.clustarr-system.svc.cluster.local:80/hdhr/"+b.DeviceID
	// and each DVR's lineup "lineup://tv.plex.providers.epg.xmltv/<that uri>/xmltv.xml#polutv <start>+"
}

func TestADeviceAtAStaleURIIsReplaced(t *testing.T) {
	// PMS lists device CAFE0001 at http://169.254.47.1:47000 with DVR 30 on it;
	// the block's URI is the Service's. After one pass: DVR 30 deleted, device 29
	// deleted, a device at the block's URI added, a DVR created, the channel map
	// saved (saved hashes cleared), ReasonReplaced recorded once, and the Lease held.
}

func TestABlockWithoutAURIIsSkippedAndRecorded(t *testing.T) {
	// Block.URI == "": no AddDevice, no delete, one ReasonFailed Event naming the block.
}

func TestGhostsAreDVRsUnderTheServicePrefixWithNoDevice(t *testing.T) {
	// a DVR whose lineup is lineup://…/http://clustarr-livetv.clustarr-system.svc.cluster.local:80/hdhr/DEADBEEF/xmltv.xml with no device: deleted;
	// a DVR whose lineup host is elsewhere: kept; a real HDHomeRun device at http://10.0.0.5: kept.
}

func TestSameURLImpliesPortAndTrailingSlash(t *testing.T) {
	assert.True(t, sameURL("http://a.svc/hdhr/X", "http://a.svc:80/hdhr/X/"))
	assert.False(t, sameURL("http://a.svc/hdhr/X", "http://a.svc/hdhr/Y"))
	assert.False(t, sameURL("http://a.svc/hdhr/X", "https://a.svc/hdhr/X"))
}
```
Delete `TestADeviceIDAtAnotherURIIsReportedAndNotTouched` (233) and rewrite `TestGhostsGoAndARealHDHomeRunStays` (245) and the fixtures at 419, 433, 455 to Service URIs. In `watch_test.go` assert `ProviderOf` reads `path` and `uri` and nothing named `port`.
- [ ] **Step 2: Run** `go test ./pkg/plex/livetv/ -run 'TestThreeBlocks|TestADeviceAtAStale|TestABlockWithoutAURI|TestGhostsAre|TestSameURL' -v`. Expected: FAIL to compile (`URI`, `sameURL` undefined).
- [ ] **Step 3: Implement.** `Block{Start, DeviceID, FriendlyName, GuidePath, LineupHash, GuideHash, Path, URI string}`; `ProviderOf` reads `path` and `uri`. In `converge.go`: delete `DefaultLocalAddress`, `Converger.LocalAddress`, `localAddress()`, `blockURI`, `sameEndpoint`, `hostOf`, `lineupHost`, `ReasonDeviceIDTaken`; add `ReasonReplaced`; `converge` uses `b.URI` (empty: `return fmt.Errorf("block %s has no URI yet", b.Start)` so the pass records it as failed and retries) and, when `ps.devices[id]` exists and `!sameURL(dev.URI, b.URI)`, calls `c.remove(ctx, p, dev, ps)` (which deletes the device's DVRs then the device, under `idle`), deletes `c.saved[id]`, records `ReasonReplaced` with both URIs, then falls into the add path. Rule M (363-372): a device is clustarr's when `deviceID(dev)` is a current block's id (`named`) — those are converged — or when `strings.HasPrefix(dev.URI, "http://"+p.Address+"/hdhr/")` for any live provider's `Address` and it is not named: removed. `ghosts(addresses []string)` takes every live `Address` and tests the lineup's guide URL prefix the same way. Keep `remove`, `addDevices`, `acquire`, the restart round and stale grabs as they are.
- [ ] **Step 4: Remove the proxies.** Delete the five files; in `cmd/manager/livetv.go` delete `EnsureLinkLocal`, `m.proxies`, `serveLiveTVProxies`, `liveTVRetry`, and the `LocalAddress` line of the `Converger` literal; `startLiveTV`'s doc says it runs the IPTVProvider watch for the lease holder's loop and waits for the first list. In `config_provision.go` delete `LocalAddress`, its default, `metadataAddress` and the validation. `grep -rn "169.254.47\|LocalAddress\|localAddress\|ProxySet\|EnsureLinkLocal" --include=*.go .` must find nothing.
- [ ] **Step 5: Run** `go build ./... && go vet ./pkg/plex/livetv/... ./cmd/manager/... && go test ./pkg/plex/livetv/... ./cmd/manager/...` Expected: PASS.
- [ ] **Step 6: Commit** `git commit -F - -- pkg/plex/livetv cmd/manager` with `livetv: Plex is given each block's URI on clustarr's livetv Service; the per-pod proxies and the link-local address are gone, a device at a stale URI is replaced -- …`.

---

### Task 5: cluster-plex chart and docs, and the cutover runbook

**Files:**
- Modify (cluster-plex): `charts/cluster-plex/values.yaml:75-94`, `charts/cluster-plex/templates/storage.yaml:64-69`, `charts/cluster-plex/Chart.yaml` (version 0.5.1 → 0.6.0: a removed value), `docs/configuration.md:558-600` and the paragraph at 584-589, `docs/livetv-pms-calls.md` (a pointer to task 0's section at the top of "Link-local devices")
- Test (cluster-plex): `test/manifests/*` if any names `localAddress` (`grep -rn localAddress test/`)
- Create (clustarr): `docs/runbooks/livetv-direct-tuners-cutover.md`
- Modify (clustarr): `docs/superpowers/specs/2026-10-27-livetv-direct-tuners-and-ui-design.md` (an "As built, Phase A" section)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: cluster-plex chart.** Delete `plex.liveTV.localAddress` and its comment from `values.yaml` and the `localAddress:` line from `storage.yaml`; add to `values.yaml`'s `liveTV` comment: "Each block is registered at clustarr's `status.blocks[].uri` on its livetv Service; nothing in the Plex pod proxies it." Bump `Chart.yaml` to 0.6.0. Run `make helm-lint && go test ./test/manifests/`. Expected: PASS.
- [ ] **Step 2: cluster-plex docs.** In `docs/configuration.md` replace the `localAddress` line of the YAML and rewrite "**The address.**" to: Plex registers each block at its `status.blocks[].uri`, `http://<release>-livetv.<ns>.svc.cluster.local/hdhr/<deviceID>`, which its network namespace reaches as it reaches every cluster Service (ADR-0003); the tuner tells blocks apart by the path and builds its URLs from the Host PMS dialled; a device PMS lists at another address for a block's DeviceID is replaced (`DVRDeviceReplaced`). Update the numbered steps where they name the port.
- [ ] **Step 3: The runbook** `docs/runbooks/livetv-direct-tuners-cutover.md` in clustarr, in the style of `docs/runbooks/unify-cold-cutover.md`: (1) deploy clustarr with Task 2's CRD (`kubectl apply` the CRD first on talos, as every CRD change there is) and the manager; confirm `kubectl get iptvproviders -o jsonpath='{.items[*].status.blocks[*].uri}'` lists a URI per block; (2) deploy cluster-plex 0.6.0 (values without `localAddress`); (3) watch the provider's Events for `DVRDeviceReplaced` then `DVRProvisioned` and `DVRChannelMapSaved` per block; (4) in Plex, re-create the recording rules; (5) check a tune from a client and `clustarr_livetv_tuners_in_use`; rollback: redeploy the previous images; the old devices are then replaced back the same way. State plainly that the recording rules are lost once (owner, 2026-10-27).
- [ ] **Step 4: "As built, Phase A"** in the design: what landed, by commit subject, and anything that differed.
- [ ] **Step 5: Commit** cluster-plex: `git commit -F - -- charts/cluster-plex docs` with `chart: 0.6.0 -- no plex.liveTV.localAddress: Plex is given each block's URI on clustarr's livetv Service -- …`; clustarr: `git add docs/runbooks/livetv-direct-tuners-cutover.md && git commit -F - -- docs/runbooks/livetv-direct-tuners-cutover.md docs/superpowers/specs/2026-10-27-livetv-direct-tuners-and-ui-design.md` with `docs(livetv): the direct-tuners cutover runbook and the design's as-built note`.

---

## Self-review

- Spec coverage: §3.1 routes (Task 1), §3.2 `uri`/`path`/address (Task 2), §3.3 removals (Tasks 2-4), §3.4 relay unchanged (nothing to do), §4.1 removals (Task 4), §4.2 converger (Task 4), §4.3 cutover (Task 4's replace rule, Task 5's runbook), §7 guards (Tasks 3-4), §9 task 0 (Task 0). §5 and §6 are Phases B and C, not this plan.
- Types: `DeviceEndpoint(deviceID string)` is used by Task 2 as defined in Task 1; `Block.Path`/`Block.URI` in Task 4 match the status leaves of Task 2; `sameURL` is defined and tested in Task 4 only.
- Review Focus 1-5 each name the task whose test pins them.
