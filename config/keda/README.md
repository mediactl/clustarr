# config/keda -- optional autoscaling overlay

**KEDA is opt-in and this overlay is not part of a default install.** The
supported operating mode (design spec §12) is fixed replicas:
`captionarr-worker` runs 2 pods, and transcodes are gated by squasharr's slot
scheduler (`--slots cpu=2,nvidia=1,intel=1`) rather than by queue depth.
Clustarr works completely without KEDA; the Helm chart ships
`keda.enabled=false`.

Apply it only when KEDA's CRDs are installed:

```sh
kustomize build config/keda | kubectl apply --server-side -f -
```

## What is here

| File | What it does |
|---|---|
| `captionarr-worker-scaledobject.yaml` | Scales the subtitle fetch workers 1-8 on JetStream consumer lag. |
| `transcode-scaledjob.yaml` | **Example only.** An alternative to squasharr's slot scheduler; the two are mutually exclusive. |

## Why the `prometheus` trigger and not `nats-jetstream`

KEDA's native `nats-jetstream` scaler cannot combine
`jetstream_consumer_num_pending` (not yet delivered) with
`jetstream_consumer_num_ack_pending` (delivered, still being worked) until
kedacore/keda#8166 ships. Pending alone reads as zero while every worker is
mid-fetch, which makes the replica count flap. So the trigger scrapes
`prometheus-nats-exporter` through the Prometheus scaler and adds the two
counters. Swap it for the native scaler once that issue lands.

Point `serverAddress` at your own Prometheus; the manifests assume
`prometheus-operated.monitoring.svc:9090`.

## The two invariants you must not break

1. `cooldownPeriod` >= the consumer's `AckWait`, and the Deployment's
   `terminationGracePeriodSeconds` = `AckWait` (both 120s here). Scaling a
   worker away inside one AckWait window redelivers its message to a pod that
   is already shutting down. Workers drain on SIGTERM.
2. Never put a ScaledObject on `catalogarr-metadata` or `indexarr`. Both are
   pinned to exactly one replica for correctness -- in-process rate-limiter
   windows and a ReadWriteOnce SQLite database respectively -- and scaling
   them is silently wrong rather than merely slow.

catalogarr's search workers are the other legitimate ScaledObject target
(§12); that one is not written yet because the consumer name is fixed by the
catalogarr implementation, which does not exist.
