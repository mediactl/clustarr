# Settings: configure every kind from the UI

**Date:** 2026-09-24 · **Status:** approved for implementation (autonomous
session; the owner asked for it in one line, the design choices below are
recorded as assumptions to correct) · **Amends:** amendment §A3.2, §A3.4
(the "Settings" row), CLAUDE.md's "The UI never writes status" invariant.

## The ask

> Allow configuring download clients, indexers, root folders, quality
> profiles, subtitle providers, transcode profiles and metadata providers
> all from the UI.

Today the Settings page renders each of those kinds and lets a person patch
one or two leaves per object (enabled and priority, a scan schedule, an
upgrade flag). Everything else -- creating an indexer, pointing a download
client at a usenet server, adding a root folder -- is `kubectl apply` of
hand-written YAML. This design makes the Settings page a complete editor:
add, edit and delete for every kind above (and SubtitleProfile, the eighth
kind already on the page, so no kind is left half-editable), with the
credentials those kinds need written into Secrets the UI never reads.

## What stays true

- **The UI never writes status and owns no CRD.** Unchanged. Every write is
  a create, a JSON merge patch of `spec`, or a delete of an object the
  person configured; `kubectl` can do each one.
- **Field manager `clustarr-ui` on spec only.** Creates and patches carry
  it, so `managedFields` still tells a person's edit from a controller's.
- **Secrets are write-only.** The role gains `create` and `patch` on
  `secrets` and nothing else: no `get`, `list` or `watch`, so no page can
  ever read one. A form shows that credentials are set (the Secret's name)
  and blank password inputs; only a non-blank input is written.
- **The apiserver validates.** CEL rules (immutable `path` and `kind` on a
  RootFolder, `builtIn` profiles, torrent-or-usenet, definition-or-generic)
  and required fields reject a bad form at the apiserver; the form is
  re-rendered with the person's values and the apiserver's message, never
  a second copy of the rule in Go.

## Design

### The schema is the CRD

The form for a kind is derived from that kind's CRD at render time: the
generated CRDs under `config/crd/bases/` are embedded by a small package in
that directory (`crdbases`, `//go:embed *.yaml`), so the UI binary carries
exactly the schema the apiserver enforces and a new API field appears in
the forms without anyone hand-writing it. `ui/schema` walks the
`v1alpha1` OpenAPI schema of `spec` into a tree of fields (path, type,
enum, default, description, required, min/max/pattern, nested objects,
arrays, string maps) and offers two more operations on that tree:

- `Decode(values url.Values) (map[string]any, error)`: a posted form into
  a spec object, typed per field (integers parsed, booleans from the
  input's last value, empty inputs omitted, arrays from indexed names
  `path.0.sub`, maps from paired `path.__k.0` / `path.__v.0` inputs,
  durations checked as Go durations, enums checked against the schema).
- `Patch(old, new map[string]any) map[string]any`: a JSON merge patch of
  `spec` that turns a key present in `old` and absent in `new` into
  `null`, recursively for objects, whole-value for arrays, so an edit
  can clear a field, not only set one.

### A curated overlay per kind

A raw schema walk renders forty fields in alphabetical order. `ui/settings`
holds one overlay per kind -- Go data, not code -- that gives the form its
shape: ordered groups with titles ("Connection", "Torrent", "Usenet",
"Limits", "Advanced"), labels and help where the CRD description is not a
label, fields hidden from the form (workload plumbing such as `resources`,
`nodeSelector`, `tolerations`, which stay editable through YAML), fields
shown only when another has a value (`spec.torrent` under
`protocol=torrent`, the generic Newznab block when no definition is
chosen), reference fields whose choices are live objects (a quality
profile ref lists QualityProfiles, a download client ref lists
DownloadClients, an indexer definition lists IndexerDefinitions, a proxy
ref lists IndexerProxies), and the credential fields behind each
`secretRef` with the data keys that kind's consumer reads. A field the
overlay does not mention still renders, in an "Other" group at the end,
so nothing the CRD accepts is unreachable.

### Pages and routes

- `GET /settings` keeps its sections; every card gains Edit and Delete,
  every section gains Add.
- `GET /settings/{kind}/new` and `GET /settings/{kind}/{namespace}/{name}`
  (or `/settings/{kind}/{name}` for the cluster-scoped kinds) render the
  form page: name and namespace (the process's own `--namespace` by
  default; a cluster-scoped kind has none), then the overlay's groups.
- `POST /settings/{kind}` creates; `POST /settings/{kind}/…/{name}`
  updates (the merge patch above); `POST /settings/{kind}/…/{name}/delete`
  deletes after a confirmation the browser asks for. The existing quick
  forms on the cards (enabled and priority, scan schedule, upgrade
  allowed, default, priority) stay as they are.
- On an apiserver rejection the form page is re-rendered with the posted
  values and the message in an alert; the `data-action-error` element
  every other action renders is kept for the machine-readable code.
- Repeated structures (usenet providers, quality tiers, size limits,
  string maps) render as rows with Add and Remove; a small script
  (`ui/static/settings.js`) clones a row template and renumbers the
  inputs, and shows or hides the conditional groups. Without it, every
  row already on the object still renders and edits.

### Writes (`ui/actions/config.go`)

- `CreateConfig(ctx, kind, namespace, name, spec)`: an unstructured
  object of the kind's group, version and kind, labelled
  `clustarr.io/origin=ui` like every object this package creates, sent
  with `client.FieldOwner(clustarr-ui)`.
- `UpdateConfig(ctx, kind, namespace, name, patch)`: the merge patch of
  `spec` from `schema.Patch`, under the same manager.
- `DeleteConfig(ctx, kind, namespace, name)`: a plain delete; the
  controllers' finalizers do their work.
- `WriteSecret(ctx, namespace, name, data)`: create, and on AlreadyExists
  a merge patch of `data` with only the given keys, so an unchanged
  password is never overwritten by a blank. No read, ever.
- `Grants()` grows by create/patch/delete on the eight kinds and
  create/patch on secrets; `config/rbac/ui_role.yaml` and the chart's
  copy follow, and the guards (`TestUIRoleGrantsOnlyReadsAndActionWrites`,
  `TestUINeverWrites`) admit Delete in `ui/actions` and nowhere else.
- Reads gain `indexerdefinitions` and `indexerproxies` (the reference
  pickers), still get/list/watch.

### Testing

- `ui/schema`: table tests over a real CRD (walk, decode, patch), with
  falsification (a removed field shows as `null`; an enum outside the
  schema is refused; an unknown form key is ignored, not written).
- `ui/actions`: the fake-writer unit tests every other action has (the
  exact object, patch type, body and manager), plus an envtest that
  creates, updates and deletes each of the eight kinds on a real
  apiserver and writes a Secret under the same role, proving the grants
  suffice and that the manager owns spec leaves only.
- `ui`: handler tests for the new pages (every field of the overlay
  renders with the object's value; a posted form reaches the action with
  the decoded spec; a rejection re-renders with values and message; the
  Secret's contents never appear in any page).
- The kind cluster: each kind created, edited and deleted from the
  browser against the deployed release.

## Assumptions to correct

1. Full-page forms rather than modals: reliable without JavaScript and
   simpler to test; a dialog can wrap the same form later.
2. Deleting is in scope ("configure" includes removing), behind a
   browser confirmation.
3. SubtitleProfile is included, so the page has one editing model.
4. Custom quality profiles are made by copying a built-in (the CRD refuses
   edits to a built-in), with the TRaSH custom-format model left as is
   per CLAUDE.md; the tiers, cutoff, language and protocol are editable.
5. Workload plumbing on a DownloadClient (`resources`, `nodeSelector`,
   `tolerations`) is not on the form; it stays a YAML concern.
