# Clustarr Remaining Work — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the tree from "M0 scaffold complete" to the full brief: seven services, observability, a web UI, and the six feature milestones.

**Architecture:** One Go module, one cobra binary, seven controller-runtime managers. Kubernetes watches are the default coupling; NATS JetStream carries rate-limited and long-running work. Custom resources are the interface, and the UI is a view over them.

**Tech Stack:** Go 1.27, controller-runtime v0.25.1, controller-gen v0.22.0, k8s.io/\* v0.37.0, NATS JetStream, templ, OpenTelemetry, ffmpeg 9.

**Spec:** `docs/superpowers/specs/2026-09-18-clustarr-design.md` and `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md`. **Where they disagree, the amendment wins.** Both travel with this plan; executors read both.

---

## Global Constraints

Copied verbatim from the spec and `CLAUDE.md`. Every task's requirements implicitly include this section.

- Module `github.com/mediactl/clustarr`. Go 1.27. Licence **GPL-3.0**; every Go file starts with the header in `hack/boilerplate.go.txt`.
- **One controller-writer per resource.** Sole exception: `MediaFile`, split by field manager on disjoint fields (`importarr` owns `status.file` and `status.probe`; `catalogarr` owns `status.quality`, `status.formatScore`, conditions).
- **All status writes go through `pkg/k8s.PatchStatus`.** `.Status().Update()` and `.Status().Patch()` are banned outside `pkg/k8s`; `.golangci.yml` forbidigo enforces it.
- **No `float32`/`float64` under `api/`.** `resource.Quantity` for decimals a human types; scaled integers for telemetry (`Milli` thousandths, `Centis` hundredths, `Percent` whole 0-100).
- **Cap every status list** with `+kubebuilder:validation:MaxItems`.
- Every root type carries `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, `+kubebuilder:ac:generate=true` (on the Kind **and** its List), and an `init()` registering both with `SchemeBuilder`.
- Prometheus metrics use the `clustarr_` prefix and base units. **Never label by title, path or release name.**
- Logging is `slog` through `context`. No package-level logger, no logger struct fields.
- Tests are table-driven with testify; fixtures under `testdata/`. No network access in tests; use `httptest`.

### Rules for parallel agents

These are not style preferences. Violating them corrupts shared state and has already cost this project a full rebuild.

1. **Never run `go get` or `go mod tidy` from a worker agent.** Concurrent module edits corrupt `go.mod`, and a `tidy` in one agent strips dependencies another just added. The batch driver adds every dependency serially **before** launching workers, and workers are told the dependency is already present.
2. **Each agent owns disjoint paths.** Two agents must never write the same directory. Path ownership is listed per task below.
3. **A green `go test ./...` does not mean the CRDs are valid.** `pkg/crdcheck` and every envtest suite skip silently when `KUBEBUILDER_ASSETS` is unset. Gate with:
   ```bash
   export KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path)
   go test -count=1 ./...
   ```
   A suite finishing in milliseconds **skipped**. `pkg/k8s` takes ~23s and `pkg/crdcheck` ~5s when they genuinely run.
4. **Never `go build` without `-o`.** A bare `go build` drops a `clustarr` binary in the repo root. Use `make build`.
5. **Verify, do not trust.** The batch driver re-runs every gate itself after workers report. Agents have reported `build_ok: true` on trees that did not compile.

### Existing API worth reading before writing a controller

```go
// pkg/k8s — the only legal status write.
func PatchStatus[T ApplyConfiguration](ctx context.Context, c client.Client,
    fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)

// Field managers (pkg/k8s/fieldmanager.go): ManagerCatalogarr, ManagerCatalogarrWorker,
// ManagerIndexarr, ManagerGrabarr, ManagerGrabarrEngine, ManagerSquasharr,
// ManagerSquasharrWorker, ManagerCaptionarr, ManagerCaptionarrWorker.

// Conditions
func MarkTrue/MarkFalse/MarkUnknown(obj client.Object, conditions *[]metav1.Condition,
    condType, reason, message string, args ...any) bool
func MarkReady(obj, conditions, ready bool, reason, message string, args ...any) bool
func StatusUpToDate(obj client.Object, conditions []metav1.Condition, condType string) bool
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration

// Finalizers
func FinalizerFor(obj runtime.Object, scheme *runtime.Scheme) (string, error)
func EnsureFinalizer/RemoveFinalizer(ctx, c, obj, name) (bool, error)
func IsDeleting(obj client.Object) bool

// Predicates
func GenerationChanged() predicate.Predicate
func StatusFieldChanged[T comparable](extract func(client.Object) T) predicate.Predicate
func StatusFieldIn[T comparable](extract func(client.Object) T, want ...T) predicate.Predicate
func LabelSelector(sel metav1.LabelSelector) (predicate.Predicate, error)
func And/Or/Not(...) predicate.Predicate

// Scheme + bus
func NewScheme() (*runtime.Scheme, error); func MustNewScheme() *runtime.Scheme
func ConnectBus(url, service string) (*natsbus.Bus, *nats.Conn, error)
func EnsureTopology(ctx context.Context, bus events.Bus, t events.Topology) error
func BusReadyChecker(nc *nats.Conn, bus *natsbus.Bus) healthz.Checker

// pkg/events — Bus is Publisher + Subscriber + Requester + KV(bucket) + Ensure + Close.
// Handler errors settle the message: events.Retry(after, err) naks with delay,
// events.Discard(reason, err) terms. Anything else follows the Subscription backoff.
```

---

## Current state, verified 2026-09-18 04:20

Commit `61449f8`. Build, vet and the full suite pass with envtest assets set.

| Area | State |
| --- | --- |
| `api/` 5 groups, 28 CRDs | Complete; install into a real apiserver |
| `api/applyconfiguration` | Generated for every kind |
| `pkg/events` | Complete: NATS + in-memory behind one contract suite |
| `pkg/k8s` | Complete: SSA status, conditions, finalizers, predicates, scheme, bus |
| `pkg/crdcheck`, `pkg/version` | Complete |
| `cmd/clustarr` + 5 `run.go` | Manager wiring; **zero controllers registered** |
| `config/`, `charts/` | 28 CRDs, 7 Deployments, 2 PVCs render; chart lints |
| `docs/adr/0001-0008`, `README.md` | Complete |
| **`importarr/`, `ui/`, `pkg/obs`, `pkg/pipeline`** | **Do not exist** |
| **`LibraryScan` kind** | **Not generated** |
| **The 11 library packages** | **Do not exist** (lost to a rate limit; never written) |
| **RBAC** | Placeholder only; real rules generate from controller markers |

### Known defects carried forward

- `go.sum` has only the `/go.mod` hash for `github.com/inconshreveable/mousetrap`. Linux and CI are fine because cobra only builds it on Windows; a Windows build fails until someone runs `go mod tidy`.
- `clustarr-data` PVC size is a placeholder and no storage class is pinned, so the cluster default must support `ReadWriteMany`, which most do not.
- `GOMEMLIMIT` is unset on every container. §12 wants 80% of the memory limit for torrent engines; the Downward API only gives 100%.
- KEDA 2.17.2 was picked by an agent, not chosen. Confirm before release.
- `config/rbac/role.yaml` and `charts/clustarr/templates/rbac.yaml` are two copies of the same placeholder and will drift.
- Per-service readiness beyond the JetStream ping is a TODO in each `run.go`. `indexarr` needs a release-index check (Phase C); `grabarr` engine roles must stay unready until torrent re-attach completes (Phase D). **Reporting ready early lets the controller hand an engine work it would double-download.**

---

# Part 1 — Phase A: amendment catch-up

Nothing else should start until Phase A lands. Every later service is expected to emit traces, metrics and logs from its first commit, and retrofitting that across seven services is far more work than doing it once now.

**Dependencies to add serially, before launching any Phase A worker:**

```bash
cd /home/appkins/src/mediactl/clustarr
go get github.com/a-h/templ@v0.3.1020 \
       github.com/axadrn/shadcn-templ@v1.13.2 \
       go.opentelemetry.io/otel@latest \
       go.opentelemetry.io/otel/sdk@latest \
       go.opentelemetry.io/otel/trace@latest \
       go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@latest \
       go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp@latest \
       github.com/prometheus/client_golang@latest \
       github.com/robfig/cron/v3@latest
go build ./...
```

These will not survive a `go mod tidy` until something imports them. Run the command, launch the workers, and do not tidy until Task A7.

## File structure for Phase A

| Path | Responsibility | Owning task |
| --- | --- | --- |
| `pkg/obs/logging/` | slog construction, context carry, logr bridge | A1 |
| `pkg/obs/tracing/` | TracerProvider, span helpers, NATS propagation | A2 |
| `pkg/obs/metrics/` | Prometheus collectors and the metric catalogue | A3 |
| `api/catalog/v1alpha1/libraryscan_types.go` | The `LibraryScan` kind | A4 |
| `api/catalog/v1alpha1/rootfolder_types.go` | Add `spec.scanSchedule` | A4 |
| `pkg/k8s/fieldmanager.go` | Add `importarr`, `importarr-worker` | A4 |
| `importarr/` | Service skeleton and manager wiring | A5 |
| `pkg/pipeline/` | Stage projection, pure | A6 |
| `ui/` | Server, routes, templates, SSE | A6 |
| `cmd/clustarr/`, `config/`, `charts/` | Wire 7 services | A7 |
| `docs/observability.md` | Metric catalogue and trace layout | A3 |

Tasks A1 through A6 touch disjoint paths and run in parallel. A7 is serial and runs last.

---

### Task A1: pkg/obs/logging

**Files:**
- Create: `pkg/obs/logging/logging.go`, `pkg/obs/logging/context.go`, `pkg/obs/logging/bridge.go`
- Test: `pkg/obs/logging/logging_test.go`, `pkg/obs/logging/context_test.go`

**Path ownership:** `pkg/obs/logging/` only.

**Read first:** amendment lines 171-203 (§A2.1).

**Interfaces — Produces:**
```go
type Options struct {
    Level     slog.Level
    Format    string // "json" or "text"
    AddSource bool
    Output    io.Writer // nil means os.Stderr
}

func New(opts Options) *slog.Logger
func FromContext(ctx context.Context) *slog.Logger   // discard logger if absent
func NewContext(ctx context.Context, l *slog.Logger) context.Context
func With(ctx context.Context, args ...any) context.Context
func BindFlags(fs *pflag.FlagSet, opts *Options)
func LogrBridge(l *slog.Logger) logr.Logger          // for ctrl.SetLogger
```

- [ ] **Step 1: Write the failing test for context carry**

```go
func TestFromContextReturnsADiscardLoggerWhenAbsent(t *testing.T) {
	l := logging.FromContext(context.Background())
	require.NotNil(t, l)
	l.Info("must not panic")
}

func TestNewContextRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	want := logging.New(logging.Options{Format: "json", Output: &buf})
	ctx := logging.NewContext(context.Background(), want)
	logging.FromContext(ctx).Info("hello", "k", "v")
	require.Contains(t, buf.String(), `"msg":"hello"`)
	require.Contains(t, buf.String(), `"k":"v"`)
}

func TestWithAttachesAttributesForLaterCalls(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.NewContext(context.Background(), logging.New(logging.Options{Format: "json", Output: &buf}))
	ctx = logging.With(ctx, "movie", "tt0111161")
	logging.FromContext(ctx).Info("reconciling")
	require.Contains(t, buf.String(), `"movie":"tt0111161"`)
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./pkg/obs/logging/ -run TestFromContext -v`
Expected: FAIL, package does not exist.

- [ ] **Step 3: Implement `New`, `FromContext`, `NewContext`, `With`**

Use an unexported context key type (`type ctxKey struct{}`) so no other package can collide. `New` selects `slog.NewJSONHandler` or `slog.NewTextHandler` on `Format`, defaults `Output` to `os.Stderr`, and honours `AddSource`.

- [ ] **Step 4: Run and confirm pass**

Run: `go test ./pkg/obs/logging/ -v`

- [ ] **Step 5: Write the logr bridge test**

```go
func TestLogrBridgeWritesThroughSlog(t *testing.T) {
	var buf bytes.Buffer
	lr := logging.LogrBridge(logging.New(logging.Options{Format: "json", Output: &buf}))
	lr.Info("from controller-runtime", "controller", "movie")
	require.Contains(t, buf.String(), `"msg":"from controller-runtime"`)
	require.Contains(t, buf.String(), `"controller":"movie"`)
}
```

- [ ] **Step 6: Implement the bridge**

`logr.FromSlogHandler(l.Handler())`. This is what `ctrl.SetLogger` receives at startup so controller-runtime's own output joins the same stream.

- [ ] **Step 7: Run the suite and commit**

```bash
go test -count=1 ./pkg/obs/logging/...
git add pkg/obs/logging && git commit -m "feat(obs): slog logging carried through context"
```

---

### Task A2: pkg/obs/tracing

**Files:**
- Create: `pkg/obs/tracing/tracing.go`, `pkg/obs/tracing/propagation.go`
- Test: `pkg/obs/tracing/tracing_test.go`, `pkg/obs/tracing/propagation_test.go`

**Path ownership:** `pkg/obs/tracing/` only.

**Read first:** amendment lines 204-231 (§A2.2). Then read the existing header constants:
```bash
grep -n 'HeaderTrace\|Clustarr-Trace' pkg/events/*.go
```
`events.HeaderTrace` is `"Clustarr-Trace"` and already carries a W3C `traceparent`. **Do not invent a new header.**

**Interfaces — Consumes:** `logging.FromContext`, `logging.With` (Task A1); `events.Envelope` and `events.HeaderTrace`.

**Interfaces — Produces:**
```go
type Options struct {
    Enabled      bool
    Endpoint     string  // OTLP gRPC endpoint
    Insecure     bool
    ServiceName  string
    SampleRatio  float64 // parent-based; errors always sampled
}

func Setup(ctx context.Context, opts Options) (shutdown func(context.Context) error, err error)
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span)
func Inject(ctx context.Context, e *events.Envelope)   // writes events.HeaderTrace
func Extract(ctx context.Context, e *events.Envelope) context.Context
func RecordError(span trace.Span, err error)           // sets status and records
```

- [ ] **Step 1: Write the failing round-trip test**

```go
func TestInjectExtractCarriesTheTraceAcrossAnEnvelope(t *testing.T) {
	shutdown, err := tracing.Setup(context.Background(), tracing.Options{
		Enabled: false, ServiceName: "test", SampleRatio: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	ctx, span := tracing.Start(context.Background(), "producer")
	want := span.SpanContext().TraceID()
	e := &events.Envelope{Type: "test"}
	tracing.Inject(ctx, e)
	span.End()

	require.NotEmpty(t, e.Trace, "Clustarr-Trace must be populated")

	got := tracing.Extract(context.Background(), e)
	_, consumer := tracing.Start(got, "consumer")
	defer consumer.End()
	require.Equal(t, want, consumer.SpanContext().TraceID(),
		"consumer span must join the producer trace")
}
```

This is the single most valuable test in the package: it proves a trace survives the hop between two services over NATS.

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./pkg/obs/tracing/ -run TestInjectExtract -v`

- [ ] **Step 3: Implement `Setup`**

Build an `sdktrace.TracerProvider` with `resource.NewWithAttributes` carrying `service.name`, a `ParentBased(TraceIDRatioBased(SampleRatio))` sampler, and a batch span processor over `otlptracegrpc`. When `Enabled` is false install a provider with **no** exporter so `Start` still produces valid span contexts and tests need no collector. Return a `shutdown` that flushes.

- [ ] **Step 4: Implement `Start`, `Inject`, `Extract`**

`Start` wraps `otel.Tracer("clustarr").Start` and then calls `logging.With(ctx, "trace_id", ..., "span_id", ...)` so every log line under the span carries both ids. `Inject` uses `propagation.TraceContext{}.Inject` into a `propagation.MapCarrier`, then copies `traceparent` into `e.Trace`. `Extract` reverses it.

- [ ] **Step 5: Run and confirm pass**

Run: `go test -count=1 ./pkg/obs/tracing/ -v`

- [ ] **Step 6: Add the error-recording test and implementation**

```go
func TestRecordErrorSetsSpanStatus(t *testing.T) {
	_, span := tracing.Start(context.Background(), "op")
	tracing.RecordError(span, errors.New("boom"))
	span.End()
	// A recording span accepts both without panicking; assert via a
	// tracetest.SpanRecorder that status is codes.Error and one event exists.
}
```

Use `go.opentelemetry.io/otel/sdk/trace/tracetest.NewSpanRecorder` to assert the recorded status rather than asserting nothing.

- [ ] **Step 7: Commit**

```bash
git add pkg/obs/tracing && git commit -m "feat(obs): OpenTelemetry spans propagated over the bus trace header"
```

---

### Task A3: pkg/obs/metrics and docs/observability.md

**Files:**
- Create: `pkg/obs/metrics/metrics.go`, `pkg/obs/metrics/domain.go`, `docs/observability.md`
- Test: `pkg/obs/metrics/metrics_test.go`

**Path ownership:** `pkg/obs/metrics/` and `docs/observability.md` only.

**Read first:** amendment lines 232-279 (§A2.3 and §A2.4). The metric table there is the required set — all 21 series.

**Interfaces — Produces:** one exported collector struct per domain, registered into controller-runtime's registry.

```go
// Register adds every Clustarr collector to r. Safe to call once per process.
func Register(r prometheus.Registerer) error

// Download telemetry, used by grabarr.
var (
    DownloadBytesTotal   *prometheus.CounterVec   // protocol, client
    DownloadSpeedBytes   *prometheus.GaugeVec     // protocol, client
    DownloadsActive      *prometheus.GaugeVec     // protocol, client
    DownloadDuration     *prometheus.HistogramVec // protocol, outcome
    DownloadsCompleted   *prometheus.CounterVec   // protocol, outcome
)
// ...and the import, indexer, search, transcode, subtitle and work-queue
// families exactly as amendment §A2.3 tables them.
```

- [ ] **Step 1: Write the failing cardinality-guard test**

```go
// The rule that matters most: no metric may be labelled by anything unbounded.
func TestNoMetricIsLabelledByAnUnboundedDimension(t *testing.T) {
	banned := []string{"title", "path", "file", "release", "name", "url", "query"}
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, metrics.Register(reg))

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				for _, b := range banned {
					require.NotContains(t, strings.ToLower(l.GetName()), b,
						"metric %s labelled by unbounded dimension %s", f.GetName(), l.GetName())
				}
			}
		}
	}
}

func TestEveryMetricUsesTheClustarrPrefix(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, metrics.Register(reg))
	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
	for _, f := range families {
		require.True(t, strings.HasPrefix(f.GetName(), "clustarr_"), f.GetName())
	}
}
```

`Gather` only returns families that have at least one observation, so the test must first touch every collector. Add a `metrics.initForTest()` helper, or use `prometheus.Registerer` plus `testutil.CollectAndCount`. Prefer walking a package-level `all []prometheus.Collector` slice and asserting on `Desc()` strings, which works without observations.

- [ ] **Step 2: Run and confirm it fails**

Run: `go test ./pkg/obs/metrics/ -v`

- [ ] **Step 3: Implement the collectors**

Define all 21 series from the amendment table with those exact names, types and labels. Register them into a package-level slice so `Register` and the tests both walk one list.

- [ ] **Step 4: Run and confirm pass**

- [ ] **Step 5: Write docs/observability.md**

The metric catalogue as a table (name, type, labels, what a spike means, an example PromQL query), the trace layout (which operations get spans and how a trace flows end to end), and the readiness contract per §A2.4. Audience is an operator running this in their cluster, not a contributor.

- [ ] **Step 6: Commit**

```bash
git add pkg/obs/metrics docs/observability.md
git commit -m "feat(obs): Prometheus catalogue with a cardinality guard"
```

---

### Task A4: LibraryScan kind, RootFolder schedule, importarr field managers

**Files:**
- Create: `api/catalog/v1alpha1/libraryscan_types.go`
- Modify: `api/catalog/v1alpha1/rootfolder_types.go` (add `spec.scanSchedule`)
- Modify: `pkg/k8s/fieldmanager.go` (add two constants)
- Test: `pkg/k8s/fieldmanager_test.go` (existing test asserts the manager list; extend it)

**Path ownership:** those three files only. **Do not** touch other `api/catalog` files; another task may be editing them.

**Read first:** amendment lines 79-128 (§A1.4, which contains the full Go type) and lines 51-78 (§A1.3 ownership table).

- [ ] **Step 1: Add the two field managers**

```go
// ManagerImportarr is the importarr controller manager. It owns ImportList,
// ImportExclusion and LibraryScan status, and Download.status.import.
ManagerImportarr FieldManager = "importarr"

// ManagerImportarrWorker is an importarr scan, list or file-import worker. On
// MediaFile it applies status.file and status.probe only: what it observed,
// never what catalogarr decided.
ManagerImportarrWorker FieldManager = "importarr-worker"
```

Add both to the `FieldManagers()` slice in spec order and extend `TestFieldManagersAreTheOnesTheSpecLists` to expect them.

- [ ] **Step 2: Run the field manager test and confirm it passes**

Run: `go test ./pkg/k8s/ -run TestFieldManagers -v`

- [ ] **Step 3: Write `libraryscan_types.go`**

Transcribe the `LibraryScanSpec` and `LibraryScanStatus` from amendment §A1.4 verbatim, then add the surrounding boilerplate: `ScanMode` and `ScanPhase` string enums with `+kubebuilder:validation:Enum`, an `UnmatchedFile` struct (`Path string`, `Reason string`, `Candidates []string` capped at 10, `SeenAt metav1.Time`), the `LibraryScan` and `LibraryScanList` root types with `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, `+kubebuilder:ac:generate=true` on both, `+kubebuilder:resource:shortName=lscan,categories=clustarr`, print columns for phase, files seen, items created and age, and an `init()` registering both.

- [ ] **Step 4: Add `scanSchedule` to RootFolder**

```go
// ScanSchedule is a cron expression; importarr creates a LibraryScan per tick.
// Empty means no periodic rescan.
// +optional
// +kubebuilder:validation:MaxLength=120
ScanSchedule string `json:"scanSchedule,omitempty"`
```

- [ ] **Step 5: Generate and verify**

```bash
make generate && make manifests
go build ./... && go vet ./...
ls config/crd/bases/ | grep libraryscan   # must exist
export KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path)
go test -count=1 ./pkg/crdcheck/ -v       # must take seconds, not milliseconds
```

Expected: `catalog.clustarr.io_libraryscans.yaml` appears and installs into the apiserver. If `crdcheck` finishes in milliseconds it skipped; the assets export failed.

- [ ] **Step 6: Commit**

```bash
git add api pkg/k8s config/crd
git commit -m "feat(api): LibraryScan kind, RootFolder scan schedule, importarr field managers"
```

---

### Task A5: importarr service skeleton

**Files:**
- Create: `importarr/run.go`, `importarr/controller/doc.go`, `importarr/worker/doc.go`
- Test: `importarr/run_test.go`

**Path ownership:** `importarr/` only. **Does not touch `cmd/`** — Task A7 wires the subcommand.

**Read first:** amendment lines 149-165 (§A1.6 process topology). Then copy the structure of an existing service verbatim:
```bash
cat catalogarr/run.go
```

**Interfaces — Consumes:** `k8s.MustNewScheme`, `k8s.ConnectBus`, `k8s.BusReadyChecker`, `logging.New`, `tracing.Setup`, `metrics.Register`.

**Interfaces — Produces:** `func Run(ctx context.Context, o Options) error` and an `Options` struct matching the shape the other services use, plus `--role` values `controller`, `worker`, `all`.

- [ ] **Step 1: Write the failing options test**

```go
func TestRunRejectsAnUnknownRole(t *testing.T) {
	err := importarr.Run(context.Background(), importarr.Options{Role: "nonsense"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "role")
}

func TestManagerOptionsUseTheImportarrLeaderElectionID(t *testing.T) {
	o := importarr.Options{Role: "controller", LeaderElect: true}
	mo := o.ManagerOptions(k8s.MustNewScheme())
	require.Equal(t, "importarr.clustarr.io", mo.LeaderElectionID)
	require.True(t, mo.LeaderElection)
}

func TestWorkerRoleDoesNotLeaderElect(t *testing.T) {
	o := importarr.Options{Role: "worker", LeaderElect: true}
	require.False(t, o.ManagerOptions(k8s.MustNewScheme()).LeaderElection,
		"workers are queue consumers; electing a leader would idle every other replica")
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./importarr/ -v`

- [ ] **Step 3: Implement `run.go`**

Mirror `catalogarr/run.go`. Leader election id `importarr.clustarr.io`, enabled for the controller role only. `setupControllers` registers nothing yet and carries `// TODO(M1): ImportList, ImportExclusion, LibraryScan, RootFolder schedule controllers`. Readiness includes `k8s.BusReadyChecker` and a `/data` writability check, because a scan worker that cannot write the library must not accept work.

- [ ] **Step 4: Run and confirm pass, then commit**

```bash
go test -count=1 ./importarr/...
git add importarr && git commit -m "feat(importarr): service skeleton and manager wiring"
```

---

### Task A6: pkg/pipeline and the ui skeleton

**Files:**
- Create: `pkg/pipeline/stage.go`, `pkg/pipeline/project.go`
- Test: `pkg/pipeline/project_test.go`
- Create: `ui/run.go`, `ui/server.go`, `ui/routes.go`, `ui/sse.go`, `ui/views/layout.templ`, `ui/views/pipeline.templ`
- Test: `ui/server_test.go`

**Path ownership:** `pkg/pipeline/` and `ui/` only.

**Read first:** amendment lines 312-398 (§A3.3 the projection with the full `Stage` and `Entry` types, §A3.4 the page table, §A3.5 authentication).

**Interfaces — Produces:**
```go
// pkg/pipeline
type Stage string          // the 18 constants listed in §A3.3, verbatim
type Entry struct{ Ref commonv1.ObjectRef; Kind commonv1.MediaKind; Title string
                   Stage Stage; Percent int32; ETA *time.Duration; Detail string
                   Since time.Time; Failure string }
type Related struct{ Downloads []downloadv1.Download; Jobs []transcodev1.TranscodeJob
                     Subtitles []subtitlev1.SubtitleRequest; Search *catalogv1.Search }
func Project(item client.Object, related Related) Entry
```

`Project` is pure: resources in, an `Entry` out. That is the whole point — the stage logic is the part most likely to rot, and it must be testable without a cluster or a browser.

- [ ] **Step 1: Write the failing projection table test**

```go
func TestProjectDerivesTheStage(t *testing.T) {
	tests := []struct {
		name    string
		movie   *catalogv1.Movie
		related pipeline.Related
		want    pipeline.Stage
	}{
		{
			name:  "no metadata yet",
			movie: movieWith(t, notReady("MetadataReady")),
			want:  pipeline.StageMetadataSearching,
		},
		{
			name:    "metadata ready, search running",
			movie:   movieWith(t, ready("MetadataReady")),
			related: pipeline.Related{Search: &catalogv1.Search{Status: catalogv1.SearchStatus{Phase: "Searching"}}},
			want:    pipeline.StageReleaseSearching,
		},
		{
			name:    "download in flight",
			movie:   movieWith(t, ready("MetadataReady")),
			related: pipeline.Related{Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: "Downloading"}}}},
			want:    pipeline.StageDownloading,
		},
		{
			name:    "transcode outranks a finished download",
			movie:   movieWith(t, ready("MetadataReady")),
			related: pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: "Completed"}}},
				Jobs:      []transcodev1.TranscodeJob{{Status: transcodev1.TranscodeJobStatus{Phase: "Running"}}},
			},
			want: pipeline.StageTranscoding,
		},
		{
			name:    "failure wins over everything",
			movie:   movieWith(t, ready("MetadataReady")),
			related: pipeline.Related{Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: "Failed"}}}},
			want:    pipeline.StageFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, pipeline.Project(tc.movie, tc.related).Stage)
		})
	}
}
```

Add one case per stage constant. A stage with no test is a stage that will silently break.

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./pkg/pipeline/ -v`

- [ ] **Step 3: Implement `Project`**

Evaluate in reverse-completion order so the furthest-along signal wins, with failure and blocked checked first. Map exactly as §A3.3 specifies: metadata stages from the `MetadataReady` condition and `status.metadata`; release stages from an active `Search`; download stages from the owning `Download` phase; import from `Download.status.import`; subtitles from `SubtitleRequest` phases; transcode from `TranscodeJob` phase.

- [ ] **Step 4: Run and confirm pass**

- [ ] **Step 5: Write the ui server test**

```go
func TestPipelinePageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(ui.Options{Entries: func(context.Context) []pipeline.Entry {
		return []pipeline.Entry{{Title: "The Shawshank Redemption", Stage: pipeline.StageDownloading, Percent: 42}}
	}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Shawshank")
	require.Contains(t, rec.Body.String(), "42")
}

func TestHealthzDoesNotDependOnTheCluster(t *testing.T) {
	srv := ui.NewServer(ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}
```

Injecting an `Entries` function keeps the server testable without a cluster cache.

- [ ] **Step 6: Implement the server and the pipeline template**

`templ generate` produces the Go; **commit the generated `_templ.go` files** so a generator change cannot break a build unattended. Add a `templ` target to the Makefile. The layout gets a Tailwind CDN link for now; a real asset pipeline is Phase G. SSE endpoint at `/events/pipeline` writing `text/event-stream`. No auth, and `NewServer` logs a warning at startup saying the service must not be exposed directly (§A3.5).

- [ ] **Step 7: Run and commit**

```bash
go test -count=1 ./pkg/pipeline/... ./ui/...
git add pkg/pipeline ui Makefile
git commit -m "feat(ui): pipeline stage projection and the server skeleton"
```

---

### Task A7: wire seven services (serial — runs after A1-A6)

**Files:**
- Modify: `cmd/clustarr/root.go`, `cmd/clustarr/services.go`
- Modify: `config/manager/`, `config/default/`, `charts/clustarr/`
- Modify: every `*/run.go` to call the obs setup
- Test: `cmd/clustarr/root_test.go`

**Path ownership:** everything. No other agent runs concurrently.

- [ ] **Step 1: Write the failing subcommand test**

```go
func TestEveryServiceHasASubcommand(t *testing.T) {
	root := newRootCommand()
	want := []string{"catalogarr", "importarr", "indexarr", "grabarr", "squasharr", "captionarr", "ui"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	for _, w := range want {
		require.True(t, got[w], "missing subcommand %q", w)
	}
}

func TestVersionHasNoShorthand(t *testing.T) {
	// -v belongs to log verbosity by klog and zap convention; an operator who
	// writes -v into a manifest must get verbosity, not a version print.
	f := newRootCommand().Flags().Lookup("version")
	require.NotNil(t, f)
	require.Empty(t, f.Shorthand)
}
```

- [ ] **Step 2: Add the `importarr` and `ui` subcommands**

Follow the existing ones in `services.go`. Roles: `importarr` takes `controller|worker|all`; `ui` takes no role.

- [ ] **Step 3: Wire observability into every service's startup**

In each `run.go`, before building the manager:

```go
logger := logging.New(o.Logging)
ctrl.SetLogger(logging.LogrBridge(logger))
ctx = logging.NewContext(ctx, logger)

shutdown, err := tracing.Setup(ctx, o.Tracing)
if err != nil {
    return fmt.Errorf("tracing: %w", err)
}
defer func() { _ = shutdown(context.Background()) }()

if err := metrics.Register(ctrlmetrics.Registry); err != nil {
    return fmt.Errorf("metrics: %w", err)
}
```

Bind the logging and tracing flags on the root command so every subcommand inherits them.

- [ ] **Step 4: Add the two Deployments**

`importarr` (controller role, 1 replica, leader-elected) and `importarr-worker` (2 replicas, media image, `/data` mounted), plus `ui` (1 replica, distroless image, a Service on 8080). Mirror them in the Helm templates and values. Expected total: **9 Deployments** in `kustomize build config/default`, up from 7.

- [ ] **Step 5: Verify the whole tree**

```bash
make generate && make manifests
go build ./... && go vet ./...
export KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path)
go test -count=1 ./...
kustomize build config/default | grep -c '^kind: Deployment'   # expect 9
kustomize build config/default | grep -c '^kind: CustomResourceDefinition'  # expect 29
helm lint charts/clustarr && helm template charts/clustarr >/dev/null
golangci-lint run ./...
go run ./cmd/clustarr --help    # 7 services listed
go mod tidy && go build ./...
```

- [ ] **Step 6: Confirm the forbidigo guard still fires**

Write a throwaway file calling `c.Status().Update(ctx, obj)` outside `pkg/k8s`, run `golangci-lint run ./...`, confirm it is rejected, then delete the file. A guard nobody has tested is not a guard.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(m0): wire importarr and ui, observability in every service"
```

**Phase A is done when:** 9 Deployments render, 29 CRDs install, all suites pass with envtest assets set, and `clustarr --help` lists seven services.

---

# Part 2 — the milestone backlog

Each milestone below needs **its own detailed plan written before it runs**, in the style of Part 1. They are too large to specify in one document, and writing them early wastes the work if an earlier milestone changes an interface. What follows is the work order: scope, path ownership, dependencies and the gate that says it is done.

The library layer in Phase B is the largest single chunk and was lost to a rate limit before it was ever written. It is pure Go with no Kubernetes dependency, which makes it the most parallelisable work in the project.

### Phase B: the library layer (11 packages, parallel)

No Kubernetes types. Each package is owned by one agent; paths are disjoint.

| Package | Scope | Primary source |
| --- | --- | --- |
| `pkg/release` | Release-name parser: resolution, source, codec, HDR, audio, group, proper/repack, series/season/episode, anime absolute numbering, music and book forms | `docs/research/quality.md`, spec §9 |
| `pkg/quality` + `catalogue` | TRaSH quality model, custom formats with `regexp2`, score sets, profile evaluation, 13 embedded built-in profiles | `docs/research/quality.md` |
| `pkg/naming` | Token grammar and path building per media kind; Jellyfin, Plex and Emby presets; sidecar subtitle naming; path sanitising | `docs/research/naming.md` |
| `pkg/mediainfo` | ffprobe wrapper, HDR and Dolby Vision classification, `ProbeHash` | `docs/research/transcode.md` |
| `pkg/transcode` | `Plan`, `Args`, `Runner` with progress, `Verify`, hardware tier selection | `docs/research/transcode.md` |
| `pkg/torznab` + `pkg/newznab` | Caps, search, XML parsing, category mapping, rate limiting | `docs/research/indexers.md` |
| `pkg/cardigann` | v11 definition engine: selectors over HTML/JSON/XML, all 25 filters, login modes | `docs/research/indexers.md` + the schema and two definitions already in `testdata/cardigann/` |
| `pkg/subtitles` | Provider interface, OpenSubtitles and Gestdown, moviehash, Bazarr scoring, post-processing | `docs/research/subtitles.md` |
| `pkg/metadata` + clients | Normalized model, ID crosswalk, TMDB, TVDB, MusicBrainz, OpenLibrary, Audnexus, ComicVine | `docs/research/metadata.md` |
| `pkg/importlist` | Trakt device flow, Plex Discover, MDBList, StevenLu, IMDb CSV | `docs/research/metadata.md` |
| `pkg/fsops` + `pkg/ratelimit` | Hardlink-else-copy with EXDEV fallback, atomic replace, recycle bin, permissions; token bucket, backoff, circuit breaker | `docs/research/naming.md`, `docs/research/download.md` |

**Gate:** every package builds, vets and tests green; each has real fixtures under `testdata/`; no network in tests. Then a second agent per package adversarially reviews and fixes: tautological tests, happy-path-only coverage, swallowed errors, panics on malformed input, ignored contexts, leaked resources.

**Dependencies to pre-add serially:** `dlclark/regexp2`, `moistari/rls`, `vansante/go-ffprobe.v2`, `PuerkitoBio/goquery`, `antchfx/xmlquery`, `tidwall/gjson`, `goccy/go-yaml`, `santhosh-tekuri/jsonschema/v6`, `Masterminds/sprig/v3`, `asticode/go-astisub`, `cyruzin/golang-tmdb`, `musicbrainzws2`, `lithammer/fuzzysearch`, `gabriel-vasile/mimetype`, `hashicorp/golang-lru/v2`, `golang.org/x/time`, `golang.org/x/net`.

**Pre-adding is not enough on its own.** `go mod tidy` removes every module
nothing imports, and a pre-add step by definition adds modules nothing imports
yet. `templui`, `tailwind-merge-go`, `robfig/cron` and `otelhttp` were all
pre-added in Phase A and all silently stripped by the next tidy (commit
`361d348`); the agent that needed them then had to add them again, serially,
which is exactly what pre-adding was supposed to avoid.

So the pre-add step must also commit a blank-import file that keeps them
alive:

```go
//go:build tools

// Package deps exists only to keep `go mod tidy` from removing modules that
// are pre-added for a later task but not yet imported by real code. Delete an
// entry the moment a real importer lands -- an entry that outlives its task is
// a dependency nobody can account for.
package deps

import (
	_ "github.com/dlclark/regexp2"
	_ "github.com/moistari/rls"
	// ... one line per pre-added module
)
```

Put it at `hack/deps/deps.go`. The `tools` build tag keeps it out of every
normal build while still counting as an import for `tidy`. The tidying task at
the end of the phase deletes the entries whose real importers have landed, and
reports any that are left.


### Phase C: M1 catalog core and library rescan

`catalogarr` controllers for Movie, Series, Episode, MediaFile, RootFolder, QualityProfile, DelayProfile, MetadataProvider, ImportExclusion and Search; the metadata gateway; search, grab and RSS-matcher workers with the KV grab lease and delay profiles; the wanted cron. `importarr` controllers for LibraryScan and the RootFolder schedule, plus the rescan worker with the never-guess rule.

**Depends on:** Phase A, and Phase B's `release`, `quality`, `naming`, `mediainfo`, `metadata`, `fsops`.

**Gate:** an envtest suite that creates a Movie, drives it to `Wanted`, and a `LibraryScan` that discovers a planted file and creates the MediaFile. Plus the **two-writer MediaFile test** amendment §A1.3 demands: `importarr` and `catalogarr` both apply, neither clobbers the other's fields.

**Also in this phase — trace propagation across the bus.** `pkg/obs/tracing`
landed as a library: `Inject`, `Extract` and `Start` have no production call
sites, and `pkg/events` defines the `Clustarr-Trace` header but nothing writes
or reads it, so today a trace ends at the process that started it. Wire it
INSIDE `pkg/events` — a publish hook that injects the active span context and
a subscribe hook that extracts it — so no caller can forget, rather than at
each call site. It carries a dependency-direction question worth settling
first: `pkg/events` must not import `pkg/obs`, so the hook is a function field
or small interface on the bus that `pkg/obs` (or the service wiring) supplies.
Amendment §A4 assigns the first end-to-end exercise of one trace to M1, which
is this phase, and docs/observability.md currently marks the whole leg "not
wired yet".

**E2E (Phase H rule):** stands up `test/e2e`, `hack/e2e.sh`, `config/e2e` and the fixture image, and lands scenarios 5, 7 and 8 against kind.

### Phase D: M2 indexers, M3 downloads and import

`indexarr` with the generic Torznab and Newznab path, caps, health, backoff, the SQLite FTS5 release index and the RSS worker. Then `grabarr` download clients and engines, and `importarr`'s file-import worker.

**Gate:** the first end-to-end test. A wanted movie is searched, a release is grabbed, a download completes against a local fixture, and the file is imported into a root folder with a MediaFile created. Also the first UI slice: the pipeline and downloads pages against real resources.

**Watch:** `grabarr` engine roles must not report ready until torrent re-attach completes, or the controller hands them work they would double-download.

**E2E (Phase H rule):** lands scenarios 1 (through import), 2, 3, 4 and 6, and the pipeline and downloaders pages of 14; adds the Torznab/Newznab fixture indexer, the seeder and the NNTP stub to the fixture image.

### Phase E: M4 transcode

`squasharr` profiles, jobs, the slot scheduler and the worker; libx265 CPU and NVENC GPU tiers; HDR10 parameters; Dolby Vision passthrough, downgrade or reject; verification, replace and recycle.

**Gate:** a real transcode of a small generated clip inside a Job, verified for duration and stream layout, with the transcode metrics populated and the pipeline page showing the stage.

**E2E (Phase H rule):** lands scenario 12 and extends scenario 1 through the TranscodeJob; adds the HDR10 and Dolby Vision clips to the fixture image.

### Phase F: M5 subtitles

`captionarr` profiles, providers and requests; the planner; fetch workers; OpenSubtitles, embedded and Gestdown providers; Bazarr scoring and post-processing; throttles and the upgrade cron.

**E2E (Phase H rule):** lands scenario 13 and extends scenario 1 through the SubtitleRequest; adds the mock OpenSubtitles and Gestdown fixtures.

### Phase G: M6 parity, lists and non-video inventory

`pkg/cardigann` wired into `IndexerDefinition` and `IndexerProxy`; the Torznab facade; ImportList Trakt and Plex moved into `importarr`; the history sink and dead-letter projector; Artist, Album, Author, Book, Audiobook, Comic and Issue controllers with their metadata providers and manual import. The remaining UI pages: library, import lists, settings and unmatched. A real Tailwind asset pipeline.

**E2E (Phase H rule):** lands scenarios 9, 10 and 11 and the remaining pages of 14; adds the Cardigann tracker page, the import-list stubs and the non-video metadata stubs.

### Phase H: end-to-end proof on a kind cluster (after G — the project is not done until this is green)

**Requirement (user, 2026-09-18):** after the implementation is complete, every
piece of functionality is proven by end-to-end tests running against a kind
cluster with the shipped images and manifests. Unit, envtest and build-tagged
integration suites do not satisfy this; a scenario counts only when it drives
real CRs through real controllers, real NATS and real files on the cluster's
`/data` mount. `make e2e` (the existing Makefile target; the spec §14 calls it
`make test-e2e`) is the gate.

**Harness.**

- `test/e2e/` — Go tests behind the `e2e` build tag, using a controller-runtime
  client on the current kubeconfig context (`kind-clustarr`). `TestMain`
  refuses to run unless the 29 CRDs are installed, NATS is ready and every
  Clustarr Deployment is Available; a scenario never installs anything.
- `hack/e2e.sh` — the one command: `make kind-up` → build the controller and
  media images → `hack/kind.sh load` → `make install` → apply the
  `config/e2e` overlay → wait for readiness → `make e2e`, then on any failure
  dump every service's logs, `kubectl get events -A`, all Clustarr CRs as
  YAML and the NATS stream/consumer info into `test/e2e/artifacts/`.
- `config/e2e/` — kustomize overlay over `config/default`: the fixture
  services below, small resource requests, `--slots cpu=1`, short requeue
  intervals and AckWait so the suite fits the 30-minute Makefile timeout, and
  scenario 1 alone finishes in the spec's ≤10 minutes.
- `test/fixtures/` + `images/Dockerfile.e2e-fixtures` — one binary,
  `clustarr-e2e-fixtures <name>`, one Deployment per fixture, all in-cluster,
  no Internet: a Torznab/Newznab indexer whose results point at the seeder and
  the NNTP stub; a Cardigann-style HTML tracker page for the bundled
  definition path; a torrent seeder serving a lavfi-generated 30 s H.264 clip
  with an embedded English subtitle track (plus HDR10 and Dolby Vision
  variants generated at image build); an NNTP stub with yEnc articles, a
  multi-volume RAR and par2 set; stub TMDB, TVDB, MusicBrainz, Open Library,
  Audnexus and ComicVine serving the recorded JSON from the unit fixtures; a
  mock OpenSubtitles (406/429 paths included) and Gestdown; Trakt (device
  flow), Plex Discover and MDBList stubs.
- CI: `.github/workflows/e2e.yml` runs `hack/e2e.sh` on kind, CPU only. GPU
  tiers are proven by argv goldens in unit tests and marked skipped on kind.

**Scenarios** — each is one `Test*` function that creates its own uniquely
prefixed resources and deletes them at the end. Together they cover every
milestone in spec §16 and every amendment section:

1. **Movie happy path (M1–M5, A2).** Providers, Indexer, torrent
   DownloadClient, RootFolder, QualityProfile, Movie → Search → grab →
   Download `Imported` → MediaFile at the Jellyfin-dialect path with labels
   and probe → TranscodeJob `Succeeded` with hevc/main10 → SubtitleRequest
   `Satisfied` with `<stem>.en.sdh.srt`; DLQ empty; the expected
   `CLUSTARR_EVENTS` subjects observed; one `trace_id` appears in the logs of
   catalogarr, indexarr, grabarr and importarr for the same grab.
2. **RSS upgrade.** A better release appears in RSS → upgrade grabbed and
   imported → the old file is in `.recycle`.
3. **Failed download → Blocklist → redownload.**
4. **Idempotency.** `kubectl rollout restart` of every controller mid-flight,
   then a rerun of scenario 1's inputs: no duplicate Download, TranscodeJob or
   SubtitleRequest; no duplicate files.
5. **Series and Episodes (M1).** A Series with two Episodes, one daily-numbered
   and one anime absolute-numbered, each gaining a MediaFile. In Phase C the
   files arrive through the rescan importarr owns, since nothing grabs yet;
   Phase D extends this scenario so a season pack is grabbed once and both
   Episodes gain MediaFiles from that download.

   > **OUTSTANDING as of Phase C — the file leg is not delivered.** The
   > catalog leg is done and exceeds this text (the e2e asserts episode
   > titles, air dates and `absoluteNumber` from the stub, not just a
   > non-empty `status.path`). The file leg cannot work as written:
   > `importarr/worker/rescan` refuses any non-`movie` root folder with
   > `unsupported_root_kind`, and its own comment defers series to M6 — so
   > "the files arrive through the rescan" is not implementable in Phase C.
   > `test/e2e/series_test.go`'s `TestSeriesRootFolderScanIsNotSupportedYet`
   > pins today's behaviour plus the never-guess invariant and will fail
   > loudly when series rescan lands, which is the intended signal.
   > **Still uncovered anywhere: `ReleaseType=seasonPack`, episode-file
   > matching, and daily/anime file matching.** Phase H's audit must treat
   > this scenario as partially satisfied, not green.
6. **Usenet (M3).** Usenet DownloadClient against the NNTP stub; NZB from the
   fixture Newznab; par2 repair and RAR unpack; `Imported`.
7. **Library rescan (A1).** Files planted in a RootFolder — matchable,
   unmatchable, a sample and an extra — then a LibraryScan: MediaFiles for the
   matchable ones, everything else in `status.unmatched` with a reason, no
   speculative item; the RootFolder schedule fires a second scan.
8. **Two-writer MediaFile (A1.3).** After a probe refresh, `managedFields` shows
   `importarr` on `status.file`/`status.probe` and `catalogarr` on
   `status.quality`/`status.formatScore`/conditions, and neither clobbered the
   other.
9. **Import lists (M6, A1).** Trakt via the device flow, Plex Discover and
   MDBList → Movies created with the list's monitor/search sync level;
   ImportExclusion respected; a rerun adds nothing twice.
10. **Cardigann (M6).** An IndexerDefinition from the bundled corpus against
    the fixture HTML tracker → results → grab through the Torznab facade; an
    IndexerProxy on the HTTP path.
11. **Non-video inventory (M6).** Artist/Album, Author/Book, Audiobook and
    Comic/Issue through manual import (Download with `Manual=true` and the
    `import-target` annotation) → files stored under the naming presets with
    metadata from the stubs.
12. **Transcode variants (M4).** HDR10 preserves MDCV/CLL; Dolby Vision
    passthrough, downgrade and reject each per profile; with `--slots cpu=1`
    two TranscodeJobs → one runs, one is suspended, then both `Succeeded`; a
    Job failure → TranscodeJob `Failed` with the reason; verify, replace and
    recycle.
13. **Subtitle variants (M5).** Embedded extraction; provider 429 → throttle
    metric and retry; the upgrade cron replaces a lower-scored subtitle; HI and
    forced flags land in the sidecar name.
14. **UI (A3).** Every page (pipeline, library, downloaders, import lists,
    settings, unmatched) returns 200 with rows for the resources above; the
    SSE stream emits a fragment when a Download progresses; a UI action
    results in a spec patch and `managedFields` shows no UI manager on any
    status.
15. **Observability (A2).** After scenario 1, `/metrics` on each service
    exposes the documented `clustarr_` series with non-zero values (download
    rate, transcode fps, queue pending, indexer duration); readiness flips
    when NATS is scaled to zero and back; a poisoned work message reaches
    `CLUSTARR_DLQ` and the DLQ projector surfaces it.
16. **Deployment parity.** The Helm chart with e2e values passes scenario 1
    as the kustomize overlay does; `clustarr all` in one pod passes
    scenario 1; the KEDA opt-in renders.

**Rule for Phases C–G, effective now:** each phase lands the scenarios its
milestone enables (C: 5, 7, 8 and the envtest-only parts of 1; D: 1 through
the import, 2, 3, 4, 6 and the pipeline/downloaders pages of 14; E: 12 and the
transcode leg of 1; F: 13 and the subtitle leg of 1; G: 9, 10, 11 and the
rest of 14) and keeps `hack/e2e.sh` green for everything landed so far.
Phase H is then the audit that fills the gaps — 15 and 16 in full, the
trace assertion in 1, CI — and the final proof, not the first time the
system is deployed.

**Gate:** `hack/e2e.sh` exits 0 from a clean machine with only docker and
kind installed; every scenario above is present and not skipped; the CI job
is green; `README.md` documents the one command. Phase H gets its own
step-by-step plan (writing-plans) immediately before it runs, like every
other phase.

---

## Carried defects to fix along the way

- [ ] `go mod tidy` to fix the `mousetrap` `go.sum` entry (Windows builds only).
- [ ] Pick a real `clustarr-data` PVC size and require a storage class when no existing claim is set.
- [ ] Set `GOMEMLIMIT` to 80% of the memory limit for torrent engines (§12); the Downward API only gives 100%, so compute it in the chart.
- [ ] Confirm or change the KEDA version, which an agent picked rather than chose.
- [ ] Replace `config/rbac/role.yaml` from `make manifests` once controllers exist, and stop the chart's copy from drifting.
- [ ] Add per-service readiness beyond the JetStream ping: the release index for `indexarr`, torrent re-attach for `grabarr`.
- [ ] Add `charts/clustarr/README.md` and `values.schema.json` so bad values fail at install rather than at render.
- [ ] Add `docs/adr/README.md` with the ADR index and supersede lifecycle when ADR-0009 appears.


### Carried out of Phase B (2026-09-18)

- [ ] Phase C: `hack/gen-catalogue` generates `pkg/quality/catalogue/data` from `testdata/trash` (the parity test already proves equivalence); `pkg/decision` proper on top of `quality.Profile.UpgradeDecision`; `quality.Condition.ExceptLanguage` evaluation.
- [ ] Phase D: map Torznab `DownloadVolumeFactor`/`UploadVolumeFactor` to `ReleaseInfo.IndexerFlags` (freeleech, halfleech, doubleupload) so TRaSH `IndexerFlag` conditions fire; `torznab.wireCaps.Limits` stays typed (a malformed caps doc is a typed error).
- [ ] Phase F: `subtitles.Plan`, `SidecarName`, `ParseSidecar` (deferred from B8); Bazarr `fix_uppercase` port; Gestdown show-id cache single-flight (duplicate lookups on a cold cache are harmless).
- [ ] Phase G: extend `torznab.Release` with non-video fields (artist, album, author, publisher) now carried in `Attrs`; convert `importlist.ExternalIDs` (struct) ↔ `metadata.ExternalIDs` (map) in the ImportList controller.
- [ ] Minor debt: `metadata/clients/musicbrainz` maps `ClientError{StatusCode:0}` (transport or decode) to `ErrDecode`; `transcode.Runner.Run` reports only `waitErr` when both wait and scan fail; `golang-tmdb.SetCustomBaseURL` is process-global (one TMDB base URL per process).
- [ ] Phase C: `pkg/release.parseLanguages` cannot detect Chinese, so the anime dual-audio Language group only fires for Japanese/Korean tags today.
- [ ] `hack/deps/deps.go` still keeps `mimetype`, `sprig/v3` and `x/net/proxy` alive (no importer yet); prune each when its phase lands.

## Self-review notes

Checked against both specs on 2026-09-18.

- **Spec coverage.** Amendment §A1 maps to A4, A5 and Phase C. §A2 maps to A1, A2, A3 and A7. §A3 maps to A6 and Phases D and G. Base spec §16's M1 through M6 map to Phases C through G. The library layer has no milestone of its own in the spec; it is Phase B here because M1 cannot start without it.
- **Type consistency.** `pipeline.Stage`, `pipeline.Entry` and `pipeline.Related` are used identically in A6 and Phase D. `k8s.PatchStatus`, the field managers and the condition helpers are quoted from the real source, not from memory.
- **Known gap, stated rather than hidden.** Phases C through G are work orders, not step-by-step tasks. Each needs its own plan written immediately before it runs. Writing them now would be guessing at interfaces that Phase B has not yet defined.
