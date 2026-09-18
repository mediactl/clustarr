# config/samples

**Empty on purpose.** Samples land here per Kind, together with the
controller that gives that Kind meaning -- not before.

The 28 CRDs in `config/crd/bases` are generated and install cleanly, so it
would be easy to write plausible-looking CR YAML for all of them today. It
would also be wrong: a sample is a claim about what the controller does with
those fields (which values are valid together, what defaults apply, what the
reconciler will immediately overwrite). Until a reconciler exists, that claim
cannot be checked, and a sample that quietly does not work is worse than no
sample at all.

So: when a controller lands, its author adds
`<group>_v1alpha1_<kind>.yaml` here in the same change, having actually
applied it against a running manager.

## Convention

One file per Kind, named `<group>_<version>_<kind>.yaml`
(e.g. `catalog_v1alpha1_movie.yaml`), and listed in a `kustomization.yaml`
alongside so that

```sh
kustomize build config/samples | kubectl apply --server-side -f -
```

creates a working demo library. A sample should be the smallest CR that
actually reconciles: required fields, one or two interesting optional fields,
and a comment on anything non-obvious. No `status:` block -- status is the
controller's to write, and every Clustarr Kind has exactly one controller
that owns it (§3, single-writer rule).

Ordering matters for a bulk apply, because some Kinds only reconcile once
their references resolve -- RootFolder and QualityProfile before Movie or
Series, DownloadClient before Download, Indexer before Search,
TranscodeProfile and SubtitleProfile after the MediaFiles they select. List
them in that order in the kustomization rather than relying on retry.

## Until then

`make install` (the CRDs) and `kustomize build config/default` (the full
install) are the things to try. `hack/kind.sh` brings up a local cluster with
NATS for both.
