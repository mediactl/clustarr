# config/prometheus -- optional monitoring overlay

Seven ServiceMonitors, one per service Deployment, plus the ClusterRole a
Prometheus needs to read auth-filtered metrics. Requires the Prometheus
Operator CRDs; it is not part of `config/default`.

```sh
kustomize build config/prometheus | kubectl apply --server-side -f -
```

## Bind the metrics reader

Metrics are served on `:8443` over https behind controller-runtime's
authn/authz filter (design spec §13), so scrapes must carry a bearer token
whose subject may `get /metrics`. Bind `clustarr-metrics-reader` to whatever
ServiceAccount your Prometheus runs as:

```sh
kubectl create clusterrolebinding clustarr-metrics-reader \
  --clusterrole=clustarr-metrics-reader \
  --serviceaccount=monitoring:prometheus-k8s
```

Skipping this is the usual cause of "the ServiceMonitor exists but every
target is down (401)".

## Label selection

Each ServiceMonitor selects on `app.kubernetes.io/{name,part-of,component}`,
the labels `config/manager/kustomization.yaml` stamps onto every Service. If
you rename the components there, rename them here too.

Your Prometheus may also restrict which ServiceMonitors it picks up
(`serviceMonitorSelector` / `serviceMonitorNamespaceSelector` on the
Prometheus CR). These carry `app.kubernetes.io/part-of: clustarr`; add a
`release: <your-prometheus>` label if that is what your install selects on.

## What you get

controller-runtime's own request/reconcile/workqueue series and the REST
client metrics, plus the Clustarr collectors from §13:
`clustarr_queue_pending`, `clustarr_search_indexer_duration_seconds`,
`clustarr_indexer_escalation_level`, `clustarr_download_rate_bytes`,
`clustarr_transcode_fps`, `clustarr_transcode_slots`,
`clustarr_subtitle_provider_throttled`, `clustarr_metadata_cache_hits_total`.

NATS itself is not scraped here. The plain `config/nats` StatefulSet has no
exporter sidecar; use the upstream nats chart (which `charts/clustarr` pulls
in) for `prometheus-nats-exporter`, which is also what the optional KEDA
triggers in `config/keda` query.
