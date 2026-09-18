# charts/clustarr/crds

**Empty in git. Populated at release time.**

The 28 CustomResourceDefinitions live in one place --
[`config/crd/bases`](../../../config/crd/bases) -- generated there by
`make manifests` (controller-gen) from the `+kubebuilder` markers on the Go
types in `api/`. The release pipeline copies them into this directory when it
packages the chart, so a published `clustarr-<version>.tgz` is
self-contained:

```sh
cp config/crd/bases/*.yaml charts/clustarr/crds/
helm package charts/clustarr
```

Checking a second copy into git would guarantee it drifts from the generated
one. Don't.

## What Helm does with this directory

Helm installs everything in `crds/` **before** the templates, once, and
**never updates or deletes it**. That is deliberate on Helm's part and has
two consequences:

* `helm upgrade` does **not** apply CRD schema changes. Upgrades that add or
  change fields need the CRDs applied first:

  ```sh
  kubectl apply --server-side -f config/crd/bases/
  helm upgrade clustarr charts/clustarr
  ```

  (or `kustomize build config/crd | kubectl apply --server-side -f -`, which
  is the same content plus labels.)

* `helm uninstall` leaves the CRDs, and therefore every Movie, Series,
  Indexer, Download, TranscodeJob and SubtitleRequest, in place. Deleting
  them is an explicit, destructive `kubectl delete crd` -- it garbage-collects
  every CR of those kinds.

Files here are plain YAML; Helm does not template them, so no
`{{ }}` and no `helm.sh/hook` annotations.
