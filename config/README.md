# config

The plain-kustomize install, dependency-free: `kustomize build config/default
| kubectl apply --server-side -f -` renders the 29 CRDs, RBAC, the ten
service Deployments and a single-node dev NATS StatefulSet into the
`clustarr-system` namespace. `hack/e2e.sh` and local kind clusters use it
directly (`config/e2e` layers fixture services and smaller resource requests
on top). Prefer `charts/clustarr` for anything real -- it swaps the dev NATS
here for the clustered upstream chart; see that chart's own README.

## Layout

| Dir | What it is |
| --- | --- |
| `crd/` | Every generated CustomResourceDefinition (`make manifests`). |
| `rbac/` | Generated ClusterRoles/RoleBindings plus the hand-written `ui` role. |
| `manager/` | One Deployment + ServiceAccount + Service per service Kind (§3's process-topology table). The reference: `charts/clustarr/templates/deployments.yaml` is the parameterised form of the same shapes, and `cmd/clustarr`'s `TestChartAndKustomizeAgreePerComponent` holds the two to each other. |
| `nats/` | Single-node dev NATS with JetStream on a PVC. Included by `default`; swap for the `nats` chart dependency in production. |
| `default/` | The deployable install: `namespace.yaml`, `pvc.yaml` (the two PVCs, §11) plus `crd`, `rbac`, `manager` and `nats`. |
| `e2e/` | `hack/e2e.sh`'s overlay: `default` plus every fixture stub service, sample CRs and smaller resource requests. |
| `keda/`, `prometheus/`, `postgres/` | Optional overlays/components, each documented below and in its own README where one exists. Never referenced by `default` -- add the ones your cluster's operators support. |
| `samples/` | Empty by design; see its own README. |

## Optional overlays

None of these are included by `config/default`. Each names the CRDs or
operator it needs and how to apply it.

- **`keda/`** -- the subtitle-fetch `ScaledObject` (needs the KEDA CRDs).
  Read `config/keda/README.md` first.
- **`prometheus/`** -- `ServiceMonitor`s (needs the Prometheus Operator
  CRDs). Read `config/prometheus/README.md` first.
- **`postgres/`** -- indexarr's release index on a CloudNativePG `Cluster`
  instead of local SQLite (design spec 2026-09-24 §A; ADR-0010). Read on.

### `postgres/`: the Postgres release index component

`config/postgres` is a kustomize [`Component`](https://kubernetes.io/docs/tasks/manage-kubernetes-objects/kustomization/#components)
(`kind: Component`, not `Kustomization`), so it composes INTO an overlay that
already carries `config/default`'s `indexarr` Deployment for it to patch,
rather than building on its own. A minimal overlay:

```yaml
# config/my-postgres-overlay/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ../default
components:
- ../postgres
```

```sh
kustomize build config/my-postgres-overlay | kubectl apply --server-side -f -
```

**Install the CloudNativePG operator first, and wait for it to be Ready,
before applying anything that includes this component.** Its admission
webhook fails closed until its own Deployment is up, so the `Cluster` this
component adds (`config/postgres/cluster.yaml`, named `clustarr-postgres`)
is rejected if it lands in the same pass as the operator -- the same
webhook-ordering constraint `charts/clustarr/templates/postgres-cluster.yaml`
and its `clustarr.validate` guard exist for, on the Helm side. See
[cloudnative-pg.io's installation docs](https://cloudnative-pg.io/documentation/current/installation_upgrade/),
then:

```sh
kubectl wait --for=condition=Available deployment/cnpg-controller-manager \
  -n cnpg-system --timeout=120s
```

What the component does: adds the `Cluster`, and a strategic-merge patch
(`config/postgres/indexarr-patch.yaml`) on the `indexarr` Deployment that
adds `CLUSTARR_INDEX_DSN` (from the Secret CNPG's `bootstrap.initdb` creates,
`clustarr-postgres-app`, key `uri`), removes the local SQLite volume and
volume mount, and drops the `Recreate` update strategy back to the `apps/v1`
default. `indexarr.replicas` may then be raised above `1` (indexarr's own
leader election is keyed on `CLUSTARR_INDEX_DSN` being set, not a flag) by
editing `config/manager/indexarr.yaml`'s `spec.replicas` or layering your own
patch over this component's.
