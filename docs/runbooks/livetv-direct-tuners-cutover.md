# Live TV cutover: link-local proxies to direct tuner URIs

**Status:** written 2026-10-27; nothing here has run. Every step that writes to the cluster or to
PMS needs the owner's explicit OK and an announcement to the other sessions first (no deploy
while another is in flight; never write to PMS while `/activities` lists `provider.epg.load`).
The owner approved the cutover once the other changes are confirmed working (the direct-tuners
design §10): task 0's recording proves PMS keeps a device URI's path, Phase A is built and its
guards pass, and the owner picks the deploy.

**What changes.** Until this cutover every Plex pod proxied a link-local port per block
(`http://169.254.47.1:<port>`) to clustarr's livetv Service. After it, Plex is given each block's
`status.blocks[].uri`, `http://<release>-livetv.<ns>.svc.cluster.local:80/hdhr/<deviceID>`, and
nothing in a Plex pod proxies or listens for Live TV
(`docs/superpowers/specs/2026-10-27-livetv-direct-tuners-and-ui-design.md`). There is no
migration code: the PMS devices at the old address carry the same DeviceIDs as the blocks, and
the first converger pass that runs the new cluster-plex replaces each one (rule M's stale-URI
case): it deletes the DVR and the device, registers the block at its URI and creates its DVR.

**Accepted loss** (owner, 2026-10-27): **every Live TV recording rule in Plex**, which belongs to
the DVR it is deleted with. Re-create them in Plex after step 5. Recordings already on disk stay.

**Names used below.** On talos: kube context `admin@talos`, clustarr in `clustarr-system`, the
release `clustarr` (so the Service is `clustarr-livetv`), Plex from the cluster-plex subchart.
Deploys go through Terraform (`~/src/tfc/cluster-bootstrap`, `clustarr/talos-values.yaml`,
`hack/clustarr-deploy.sh`, `--apply` after reading the plan), never `helm upgrade`. Image tags
in the values are double-quoted.

---

## 0. Before the deploy

1. **Task 0's recording exists** (`cluster-plex/docs/livetv-pms-calls.md`, "A device URI with a
   path"): PMS kept the path in the device's `uri`, fetched `<uri>/discover.json`, and followed
   `BaseURL`/`LineupURL`. If it did not, stop: the fallback is the design's §8 (a hostname per
   block through an HTTPRoute), not this runbook.
2. **Publish.** cluster-plex: push main and let CI publish its images. clustarr: push main and
   tag a release so CI publishes its images and chart; in cluster-bootstrap raise
   `chart_version` to it (the clustarr chart's schema refuses `livetv.portRange`, and its manager
   args drop `--livetv-port-range`). The cluster-plex chart need not change: chart 0.4.1, which
   the umbrella pins, still renders `plex.liveTV.localAddress` into the manager's config, and the
   new manager ignores the key. Raising the pin to 0.6.0 (`make chart-push` in cluster-plex, the
   dependency in `charts/clustarr/Chart.yaml`, `make chart-deps`) also brings 0.5.x's chart
   changes (the maintenance Jobs' TTL, the opt-in HTTPRoute): the owner's choice, not this
   cutover's.
3. **The values.** In `clustarr/talos-values.yaml` set `image.clustarr`, `image.native` and
   `cluster-plex.image.tag` to the new images, quoted. Remove `livetv.portRange` if it is set
   (the schema refuses it); `cluster-plex.plex.liveTV.localAddress` is ignored if set, and is
   best removed.
4. **Write down the Plex state** to compare against: `GET /livetv/dvrs` and
   `GET /media/grabbers/devices` through the PMS port-forward (read-only), and each DVR's
   recording rules (Plex Web, Live TV, DVR schedule).

## 1 and 2. The CRD, then the deploy, back to back

**Live TV is down from the CRD apply until step 4's replacements finish.** Helm does not upgrade
CRDs, so the IPTVProvider CRD is applied by hand, and with its structural schema the apiserver
prunes `status.blocks[].endpoint` and `.port` from every read at once: the old cluster-plex's
watch re-lists, sees no port, and closes its proxies. So read the Terraform plan first
(`hack/clustarr-deploy.sh`), then apply the CRD and run `--apply` immediately after it:

```bash
kubectl --context admin@talos apply --server-side -f config/crd/bases/clustarr.io_iptvproviders.yaml
hack/clustarr-deploy.sh --apply   # in ~/src/tfc/cluster-bootstrap, the plan already read
```

The manager and cluster-plex roll together; the order between them does not matter for
correctness:

- a new manager publishes `path` and `uri` and serves only `/hdhr/<id>`; an old cluster-plex
  still running proxies to the removed `/livetv/<ns>/<name>/devices/<id>` routes, so tunes fail
  until it rolls;
- a new cluster-plex against an old manager finds no `uri` and registers and deletes nothing
  (`DVRProvisionFailed`, "has no URI yet"), so it waits for the manager.

## 3. Check clustarr

```bash
kubectl --context admin@talos -n clustarr-system get iptvproviders \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.address}{"\n"}{range .status.blocks[*]}{"  "}{.start}{" "}{.uri}{"\n"}{end}{end}'
```

Expect `status.address` `clustarr-livetv.clustarr-system.svc.cluster.local:80` (it was
`clustarr-livetv.clustarr-system.svc:80`) and one `uri` per block.
From a Plex pod, `curl -s <uri>/discover.json` answers the block's `DeviceID` with
`BaseURL` equal to `<uri>`.

## 4. Watch the converger

On the provider's Events, per block: `DVRDeviceReplaced` (the old device at
`169.254.47.1:<port>` deleted with its DVR), `DVRProvisioned` (registered at the URI, DVR
created), then `DVRChannelMapSaved`. A pass that meets a guide load waits
(`DVRWaitingForGuide`); each pass starts at most one load, so a provider of several blocks takes
several passes. The lease holder then restarts PMS on the other pods once (`DVRPodsRestarted`).

```bash
kubectl --context admin@talos -n clustarr-system get events --field-selector involvedObject.kind=IPTVProvider \
  --sort-by=.lastTimestamp | tail -20
```

## 5. Re-create the recording rules

In Plex, re-create each rule written down in step 0.4 on the new DVRs.

## 6. Check a tune

From a client, tune a channel of each block; `clustarr_livetv_tuners_in_use` rises and falls
with it. `GET /livetv/dvrs` lists one DVR per block whose lineup is
`lineup://tv.plex.providers.epg.xmltv/<uri>/xmltv.xml#<FriendlyName>`, and no device at
`169.254.47.1` remains. The converger replaces only a device carrying a current block's
DeviceID: one at `169.254.47.1` whose id no block names (a block removed before the cutover), and
a DVR whose guide is still at `169.254.47.1`, are no longer clustarr's to it. Delete such rows by
hand in Plex (Settings, Live TV & DVR), after `GET /activities` shows no `provider.epg.load`.

## Rollback

Re-apply the previous CRD, then redeploy the previous images and chart (Terraform, with the
previous `chart_version` and tags); the old manager publishes ports again. The old cluster-plex
finds each block's DeviceID at its Service URI, not its link-local port, records
`DVRDeviceIDTaken` and changes nothing: Live TV stays down until the new DVRs and devices are
deleted by hand in Plex (`GET /activities` first, no `provider.epg.load`), after which it
registers the link-local devices and creates their DVRs. Or roll forward again. Recording rules
are lost again either way.
