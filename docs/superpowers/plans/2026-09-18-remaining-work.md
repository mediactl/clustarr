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
- Tests are table-driven with testify; fixtures under `test/data/`. No network access in tests; use `httptest`.

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

> Historical snapshot from before Phase A. The current state is `CLAUDE.md`'s
> Status section; what is still open is under "Carried defects" below.

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
| **`app/import/`, `ui/`, `pkg/obs`, `pkg/pipeline`** | **Do not exist** |
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
| `app/import/` | Service skeleton and manager wiring | A5 |
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
- Create: `app/import/run.go`, `app/import/controller/doc.go`, `app/import/worker/doc.go`
- Test: `app/import/run_test.go`

**Path ownership:** `app/import/` only. **Does not touch `cmd/`** — Task A7 wires the subcommand.

**Read first:** amendment lines 149-165 (§A1.6 process topology). Then copy the structure of an existing service verbatim:
```bash
cat app/catalog/run.go
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

Run: `go test ./app/import/ -v`

- [ ] **Step 3: Implement `run.go`**

Mirror `app/catalog/run.go`. Leader election id `importarr.clustarr.io`, enabled for the controller role only. `setupControllers` registers nothing yet and carries `// TODO(M1): ImportList, ImportExclusion, LibraryScan, RootFolder schedule controllers`. Readiness includes `k8s.BusReadyChecker` and a `/data` writability check, because a scan worker that cannot write the library must not accept work.

- [ ] **Step 4: Run and confirm pass, then commit**

```bash
go test -count=1 ./app/import/...
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
| `pkg/cardigann` | v11 definition engine: selectors over HTML/JSON/XML, all 25 filters, login modes | `docs/research/indexers.md` + the schema and two definitions already in `test/data/cardigann/` |
| `pkg/subtitles` | Provider interface, OpenSubtitles and Gestdown, moviehash, Bazarr scoring, post-processing | `docs/research/subtitles.md` |
| `pkg/metadata` + clients | Normalized model, ID crosswalk, TMDB, TVDB, MusicBrainz, OpenLibrary, Audnexus, ComicVine | `docs/research/metadata.md` |
| `pkg/importlist` | Trakt device flow, Plex Discover, MDBList, StevenLu, IMDb CSV | `docs/research/metadata.md` |
| `pkg/fsops` + `pkg/ratelimit` | Hardlink-else-copy with EXDEV fallback, atomic replace, recycle bin, permissions; token bucket, backoff, circuit breaker | `docs/research/naming.md`, `docs/research/download.md` |

**Gate:** every package builds, vets and tests green; each has real fixtures under `test/data/`; no network in tests. Then a second agent per package adversarially reviews and fixes: tautological tests, happy-path-only coverage, swallowed errors, panics on malformed input, ignored contexts, leaked resources.

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

> **Split into three plans on 2026-09-19 (user's call).** As written this phase
> carried two milestones, a UI slice and five e2e scenarios — roughly twice
> Phase C, which ran to 14 tasks and 176 commits on one branch. The three
> pieces are separable subsystems with distinct CRD groups, distinct binaries
> and a contract between them that is already fixed and already called, so
> each can produce working, testable software on its own and merge on its own
> green gate. Phase numbering for E–H is unchanged.
>
> - **D1 — `indexarr` (M2).** Generic Torznab/Newznab, caps, health, backoff,
>   the SQLite FTS5 release index, the RSS worker, and the three RPC verbs
>   `rpc.indexarr.search|download|query`. This is what makes Phase C's search
>   path live: `app/catalog/worker/search` already calls `rpc.indexarr.search`
>   and today reaches no server.
>
>   **E2E correction (2026-09-19).** This note first assigned scenarios 2, 3
>   and 4 to D1. That was wrong, and research caught it before any code was
>   written: scenario 2 ends "upgrade **grabbed and imported**", 3 is a
>   **failed download** → blocklist → redownload, and 4 asserts "no duplicate
>   **Download**, TranscodeJob or SubtitleRequest". All three need grabarr and
>   the file-import worker, so none can pass with indexarr alone. They move to
>   **D2**. D1 instead lands a **new** scenario the sixteen do not cover,
>   because the original phase bundled the subsystems: an Indexer reconciling
>   to healthy against the fixture indexer, a `Search` CR returning ranked
>   results end to end through `rpc.indexarr.search`, and the release firehose
>   reaching `catalogarr`'s RSS matcher with a legal envelope key. It is
>   **scenario 17** in the roster below, so Phase H's audit sees seventeen.
>
>   **D1 done (2026-09-22).** Eleven tasks (D1-0..D1-9 plus a consolidation
>   pass) landed: the three controllers, the caps probe and its 12-hour
>   refresh, the health/backoff escalation ladder, `pkg/relindex` (SQLite
>   FTS5), the federated search with dedupe and the KV-backed query-limit
>   window, the RSS poll worker and firehose, and the three RPC verbs. The
>   gate — `make generate`, `make manifests`, `make build`, `make lint`,
>   `go test` — is green over the D1 surface (`./app/indexer/...`,
>   `./app/catalog/...`, `./app/import/...`, `./ui/...`, `./cmd/...`,
>   `./api/...`, `./pkg/...` excluding `pkg/download`), with Phase D2's
>   in-flight `pkg/download` and `app/grab/status` excluded as another agent's
>   work. Scenario 17 is written but has **not** been run against kind,
>   deferred by user instruction until D1-D3 are all in. CLAUDE.md's
>   `## Status` carries the verified detail; the six-plus entries this phase
>   carried forward are below, in the Phase D defects block.
> - **D2 — `grabarr` + `importarr`'s file-import worker (M3).** DownloadClient,
>   the torrent and usenet engines, the Download controller and its re-attach
>   semantics, and the completed-download import. Lands scenarios 1 (through
>   import), 2, 3, 4 and 6.
>
>   **D2 done (2026-09-23).** D2-0 through D2-10 landed: `pkg/download/torrent`
>   (anacrolix), `pkg/download/usenet` (`Tensai75/nntp` plus pure-Go
>   `javi11/rapidyenc`, own connection pool per R9's cgo ruling), `app/grab/status`'s
>   `ControllerFields`/`EngineFields` split, the `DownloadClient` and `Download`
>   controllers, the torrent and usenet engines with re-attach-gated readiness,
>   a level-driven orphan `Reaper` in each engine, and
>   `app/import/worker/fileimport`. D2-8a (`f8a1a4a`) closed the hole the other
>   three tasks each left at one edge: nothing advanced `status.phase` past
>   `Assigned` (it is `ControllerFields`, not an engine-writable field) and
>   nothing published the import task, so `grep -rn WorkFileImportSubject`
>   found the subject builder and the consumer but no publisher — D2's own
>   gate could not complete until this task added phase derivation and a
>   once-per-completion `schema.ImportTask` publish. D2-8 (`3930cb0`) found
>   the grabarr reconcilers, both engines, both reapers and the file-import
>   worker registered nowhere, wired them into `app/grab/run.go` and
>   `cmd/clustarr`, added `grabarr` to the registration guard, ran the one
>   permitted serial `go mod tidy`, and regenerated RBAC. `c5e86d5` fixed a
>   Critical found along the way: `Download`'s release-identity CEL rule
>   compared five `+optional` fields unguarded, so an absent one raised "no
>   such key" and the apiserver rejected the write — fatal for usenet, which
>   never carries `infoHash`, on every write after creation including the
>   status-only applies grabarr reports progress through;
>   `pkg/crdcheck/download_cel_test.go` guards it with a dynamic-client
>   fixture so a typed client's always-marshalled empty string can't mask the
>   bug again. The gate — `make generate`, `make manifests`, `make build`,
>   `make lint`, `make test` with `KUBEBUILDER_ASSETS` exported — is green
>   with no skips and no `go.mod`/`go.sum` diff. Scenarios 1 (through
>   import), 2, 3, 4 and 6 (`test/e2e/download_test.go`,
>   `test/e2e/import_test.go`) are written but **not** run against kind,
>   deferred the same way as D1's scenario 17. Two fixture/deploy gaps that
>   will block those scenarios' first kind run are filed below, in the Phase
>   D defects block.
> - **D3 — the first UI slice.** The pipeline and downloads pages over real
>   resources. Lands the corresponding parts of scenario 14.
>
>   **D3 done (2026-09-23).** D3-0 through D3-5 landed: `ui/reader.go`'s
>   standalone `cache.Cache` (`NewClusterReader`, no manager, nil `Reader`
>   stays legal), `/readyz` gating on cache sync while `/healthz` stays
>   unconditional, a hand-written read-only `config/rbac/ui_role.yaml` bound
>   to the previously-unbound `ServiceAccount ui`, `ui/projection`'s
>   process-wide tick replacing the old per-connection poll and fanning out
>   pipeline and downloads rows alike, the downloads page and its
>   `/events/downloads` SSE stream, and D3-4's two never-writes guards
>   (`6597877`): `ui/guard_test.go`'s `TestUINeverWrites` (an AST guard
>   rejecting any `client.Writer` selector, including `Status`, on a
>   controller-runtime `client`-typed value anywhere in non-test `ui/` code,
>   plus any import of `pkg/k8s`) and `cmd/clustarr/ui_rbac_test.go`'s
>   `TestUIRoleGrantsOnlyReadVerbs`/`TestUIRoleChartMatchesConfig` (the role's
>   verbs are a subset of `{get,list,watch}`, no `/status` resource, chart
>   copy byte-identical). The invariant is now those two guards, not an
>   accident of no RBAC grant existing. Same gate as D2, green. Scenario 14's
>   pipeline and downloads pages (`test/e2e/ui_test.go`) are written but
>   **not** run against kind, deferred the same way.
>
> The fixture image grows in the phase that needs it: the Torznab/Newznab
> fixture indexer in D1, the seeder and NNTP stub in D2.

`indexarr` with the generic Torznab and Newznab path, caps, health, backoff, the SQLite FTS5 release index and the RSS worker. Then `grabarr` download clients and engines, and `importarr`'s file-import worker.

**Gate:** the first end-to-end test. A wanted movie is searched, a release is grabbed, a download completes against a local fixture, and the file is imported into a root folder with a MediaFile created. Also the first UI slice: the pipeline and downloads pages against real resources.

**Watch:** `grabarr` engine roles must not report ready until torrent re-attach completes, or the controller hands them work they would double-download.

**E2E (Phase H rule):** lands scenarios 1 (through import), 2, 3, 4 and 6, and the pipeline and downloaders pages of 14; adds the Torznab/Newznab fixture indexer, the seeder and the NNTP stub to the fixture image.

### Phase E: M4 transcode

`squasharr` profiles, jobs, the slot scheduler and the worker; libx265 CPU and NVENC GPU tiers; HDR10 parameters; Dolby Vision passthrough, downgrade or reject; verification, replace and recycle.

**Gate:** a real transcode of a small generated clip inside a Job, verified for duration and stream layout, with the transcode metrics populated and the pipeline page showing the stage.

**E2E (Phase H rule):** lands scenario 12 and extends scenario 1 through the TranscodeJob; adds the HDR10 and Dolby Vision clips to the fixture image.

> **E done (2026-09-23).** Plan `docs/superpowers/plans/2026-09-23-phase-e-transcode.md`,
> E-0 through E-5; `CLAUDE.md`'s Status has the verified detail. Scenario 12
> and scenario 1's transcode leg are written and **never executed**; the
> Dolby Vision clip could not be made with ffmpeg alone, so that case skips
> with a named reason. Carried items are in the E/F/G block under "Carried
> defects".

### Phase F: M5 subtitles

`captionarr` profiles, providers and requests; the planner; fetch workers; OpenSubtitles, embedded and Gestdown providers; Bazarr scoring and post-processing; throttles and the upgrade cron.

**E2E (Phase H rule):** lands scenario 13 and extends scenario 1 through the SubtitleRequest; adds the mock OpenSubtitles and Gestdown fixtures.

> **F done (2026-09-23).** Plan `docs/superpowers/plans/2026-09-23-phase-f-subtitles.md`,
> F-0 through F-7; `CLAUDE.md`'s Status has the verified detail. Scenario 13
> and scenario 1's subtitle leg are written and **never executed**. Carried
> items are in the E/F/G block under "Carried defects".

### Phase G: M6 parity, lists and non-video inventory

`pkg/cardigann` wired into `IndexerDefinition` and `IndexerProxy`; the Torznab facade; ImportList Trakt and Plex moved into `importarr`; the history sink and dead-letter projector; Artist, Album, Author, Book, Audiobook, Comic and Issue controllers with their metadata providers and manual import. The remaining UI pages: library, import lists, settings and unmatched. A real Tailwind asset pipeline.

**E2E (Phase H rule):** lands scenarios 9, 10 and 11 and the remaining pages of 14; adds the Cardigann tracker page, the import-list stubs and the non-video metadata stubs.

> **G done (2026-09-23).** Plan `docs/superpowers/plans/2026-09-23-phase-g-parity.md`,
> G1-0 through G4-1 plus the Q-1..Q-3 library fixes; `CLAUDE.md`'s Status has
> the verified detail. Scenarios 9, 10, 11 and the rest of 14 are written and
> **never executed**; scenario 9 only checked that Trakt and Plex ImportLists
> are accepted, because neither client took a base-URL override (gap fix X14,
> `9b0b516`, added the override and un-skipped both). E-6, F-8 and
> G4-2 ran as one final gate. Carried items are in the E/F/G block under
> "Carried defects".

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
   > `app/import/worker/rescan` refuses any non-`movie` root folder with
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
17. **Indexer, federated search and the release firehose (M2).** An `Indexer`
    against the in-cluster Torznab fixture reconciles to healthy with
    `status.caps` from a live capabilities fetch and `status.protocol`
    resolved; a `Search` CR returns ranked `status.results` end to end
    through `rpc.indexarr.search`, worst-first on the wire and best-first in
    status, with every result keyed by the Indexer's object name; the RSS
    worker's release reaches `catalogarr`'s rss-matcher with a legal
    `<namespace>/<indexer>` envelope key, proven by the matched Movie's
    `status.pendingGrab`; and a failing indexer escalates, is disabled, and
    is then neither queried by the fan-out (`skipped`) nor polled again
    (the fixture's request log stays quiet).

**Rule for Phases C–G, effective now:** each phase lands the scenarios its
milestone enables (C: 5, 7, 8 and the envtest-only parts of 1; D: 1 through
the import, 2, 3, 4, 6, **17** and the pipeline/downloaders pages of 14;
E: 12 and the transcode leg of 1; F: 13 and the subtitle leg of 1;
G: 9, 10, 11 and the rest of 14) and keeps `hack/e2e.sh` green for everything landed so far.
Phase H is then the audit that fills the gaps — 15 and 16 in full, the
trace assertion in 1, CI — and the final proof, not the first time the
system is deployed.

**Gate:** `hack/e2e.sh` exits 0 from a clean machine with only docker and
kind installed; every scenario above is present and not skipped; the CI job
is green; `README.md` documents the one command. Phase H gets its own
step-by-step plan (writing-plans) immediately before it runs, like every
other phase.

---

## Carried defects — the list Phase H inherits

> **Consolidated 2026-09-23 at the Phase E/F/G final gate.** This section is
> the whole of what Phase H inherits: every unchecked item in it is open, and
> nothing open lives anywhere else in this plan. The blocks keep the phase
> that found each item. The last block, *Carried out of Phases E, F and G*,
> was verified item by item at HEAD. The older blocks were not re-verified
> wholesale; they were spot-checked wherever E, F or G touched the same code,
> and the items that check found already fixed are ticked in place with the
> commit that fixed them.
>
> **Gap fixes, 2026-09-23** (`docs/superpowers/plans/2026-09-23-gap-fixes.md`,
> tasks X1-X16, rulings R-1..R-13). Every entry below was verified at HEAD by
> the task that owned it and is now ticked with its commit or its ruling. The
> SHAs are the ones on `main`: a `git pull --rebase` mid-wave rewrote every
> W1a commit, so the task reports' SHAs for those are stale -- find a commit
> by subject with `git log --grep`. **What is still open is exactly the
> block that follows.**
>
> **Z-wave, 2026-09-23** (Z1-Z6 and the final pass F; briefs and reports in
> `.superpowers/sdd/2026-09-23-gap-fixes/`). Every fixable item in the block
> that follows was taken; each is ticked below with its commits or its
> ruling, and the items still unchecked are exactly what remains open.
> Excluded from the wave by the controller: the Phase H e2e items, the items
> that need external verification (live provider responses, a KEDA release
> tested against 1.37), the pre-deploy-harmless R-5 co-owner note, and the
> owner's `importRejected` decision.

### Still open after the gap fixes (2026-09-23)

Harvested from every gap-fix task report ("Carried", concerns, needs nobody
took) and from the spec pass, which checked §5 and §8 against the code.
Gap fixes Y1-Y3 (2026-09-23, the four failure-handling behaviours the spec
named and nothing built) ticked their four items under *Spec paths that were
never built* and added *Downloads and events* from their reports. The
Z-wave ticked what it fixed or closed and added the items its reports left
open, each marked with the task that found it.

**End to end (Phase H).**

- [ ] Every scenario written since Phase C -- 1-4, 6, 9-17 -- is written and has never run on kind.
- [ ] Scenario 16's Helm-chart and `clustarr all` legs skip: both need a second deploy path in `hack/e2e.sh`, and under per-service roles an all-in-one pod needs one binding per role (X12c, X14).
- [ ] Scenario 1's trace check covers grabarr and importarr only; the four-service proof needs a real Search-driven grab whose release points at the seeder (X12c).
- [x] **Fixed `c049c26` (Z1), `29e6781` (Z6).** The test serves its torrent through a test-local tracker with a 1 s re-announce interval and fails only after 30 s with no byte, and the fixture seeder's tracker now answers with a 2 s interval, so every e2e consumer recovers too. Root cause from the anacrolix v1.61.0 source: a dropped peer is not re-dialled until the next announce, and a tracker with no interval gets the 300 s default. Was: `TestRealLoopCompletesSeedsAndRemovesOnPolicy` stalled once under heavy parallel load (X9).
- [x] **Fixed `d5620a0` (Z6).** The start envtest and `nonvideostub` serve a real, trimmed Kid A release browse (`test/data/metadata/musicbrainz/browse_releases_kid_a.json`), and the envtest waits for a selected Kid A release and checks its tracks. Was: the `cmd/clustarr` start envtest answered Kid A's release browse with The Bends' releases (W1a gate).
- [ ] No e2e scenario drives a real download failure. Scenario 3 still hand-sets the blocklist label (read as `manual`); a torrent with no seeder under a short `stallTimeout`, or the nntp stub denying every article, would prove Y2's blocklisting and Y3's redownload search end to end (Y2).

**Spec paths that were never built** (spec §5 and §8.3 say so inline).

- [x] **Fixed `813145b`, `0b607c0` (Y2).** A release fault (`missingArticles`, `encrypted`, `stalled`, `timeout`, `importRejected`, `manual`) is labelled blocklisted with `blocklistedUntil`, the label going on before `failed` is published; a local fault (`diskFull`, `writeError`) fails unlabelled (spec §4.4). Was: a Failed Download is never blocklisted automatically -- nothing sets `download.clustarr.io/blocklisted`, grabarr only honours it.
- [x] **Fixed `dd1ea96` (Y3; the lease delete made revision-checked by `1ab8ce2`, Y1).** The `catalogarr-redownload` worker frees the lease and publishes one `redownload` search per monitored target, none for a local fault (spec §8.3). Was: no `redownload` search is published (`SearchReasonRedownload` has no producer); the wanted sweep re-searches instead.
- [x] **Fixed `813145b`, `56c10c1` (Y2).** The engines report every reason they can observe through the new engine-owned `status.engineFailureReason`, and `status.seedGoalReached` becomes `seedGoalMetAt`, `SeedGoalMet` and the `seedGoalMet` event. Was: `derivePhase` reaches only the `encrypted` failure reason; `stalled`, `diskFull`, `writeError`, `timeout`, `missingArticles` and `manual` are never derived, and neither is `SeedGoalMet`, so `download.seedGoalMet` has no producer (`app/grab/controller/download/doc.go`, X9).
- [x] **Fixed `c4998de` (Z1), `feaa4f1` (Z5).** The grabarr engines write `DownloadProgress` under `download.<uid>` (on change, at most 1 Hz, deleted when the transfer leaves) and the squasharr worker `TranscodeProgress` under `transcode.<uid>` (at most 1 Hz, one Put in flight, only when given `NATS_URL`), both bounded and unable to fail the transfer (spec §5). The core-NATS `clustarr.progress.*` subjects stay unpublished: the `Bus` has no core publish. Was: nothing wrote 1 Hz telemetry into `clustarr-progress` (X9).
- [x] **Fixed `bd6227e` (Y1).** natsbus watches the advisory per subscription, queue-grouped per durable, and copies the message under the in-process path's Msg-Id; membus sweeps a lapsed final delivery (spec §5). Was: the `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES` watcher was never built: a message whose final delivery expires on AckWait (a hung handler, not a failing one) is dropped without a DLQ copy.
- [x] **Pruned `08ac269`, `4e04c44` (Z2).** `git grep` found no producer, subscriber or reader of any of them; the topology is now eight streams, fourteen consumers and ten buckets. Was: declared, never used: the `catalogarr-import` consumer and `clustarr.work.catalogarr.import` subject (pre-amendment), the `indexarr-definitions` consumer and `index.DefinitionsSync` (no worker), and the `clustarr-search-cache` bucket.
- [x] **Fixed `3104bf7` (Z1).** `download.Client.SetPriority`, called level-driven by both engines: torrent moves the connection budget, usenet changes the job's class at the next batch boundary and persists it. Was: a `spec.priority` change after the Add reached neither download client (X9).

**Downloads and events** (carried from gap fixes Y1-Y3).

- [ ] Policy, for the owner: `importRejected` blocklists at once, which leaves no manual force-import (import annotations plus `Retrigger`) for a rejected download; Sonarr holds import-blocked downloads for the user instead (Y2).
- [x] **Fixed `d8768bb`, `3104bf7` (Z1).** `pause` (the default) holds the job paused for the operator with `status.healthPaused` and phase `Paused`, acknowledged by toggling `spec.paused` (NZBGet's `HealthCheck=pause`); `delete` fails it as `missingArticles` and blocklists. The torrent engine removes an imported torrent past its goal only when `removeCompleted` and `removeOnImport` both hold (Sonarr's Remove Completed). Was: `UsenetSpec.healthAction` and `TorrentSpec.removeCompleted` were never read (Y2).
- [x] **Fixed `3104bf7` (Z1).** The re-attach descriptor carries `seed{uploadedBytes, seedTimeSeconds, goalMet, savedAt}`, never going backwards, and re-attach hands it back as `AddRequest.SeedHistory`; a restored met goal disallows upload at once. Was: torrent seed counters and the met goal were in memory (Y2).
- [x] **Fixed `2f93164`, `cdde136`, `93f1f5e` (Z1).** The engine pod template's `download.clustarr.io/engine-config-hash` covers every setting read at start and, for usenet, a digest of each provider Secret's data read by name with `get` alone (no Secret list or watch, by ruling: RBAC cannot narrow those to a label), so a change rolls the engine and a rotated credential rolls it at the next reconcile, within 5 minutes. The usenet Deployment moved to `Recreate`, with a one-off merge-patch migration. RBAC regenerated in `0ba2c01` (F). Was: `stallTimeout` and `downloadTimeout` were read at engine start with no rollout (Y2).
- [x] **Fixed `d8768bb`, `3104bf7` (Z1).** New reason `payloadMismatch`, a release fault, reported by the torrent engine as `engineFailureReason`; the redownload worker treats it as not local. Was: `download.ErrPayloadMismatch` had no `DownloadFailureReason` and was retried (Y2).
- [x] **Fixed `92c1065` (Z2).** natsbus runs up to `MaxInFlight` handlers per subscription and its JetStream callback never blocks (a busy delivery is parked, a redelivery replaces it); an unset `MaxInFlight` is 1; the hung and saturated cases are contract tests on both buses. Was: natsbus ran one handler at a time per subscription (Y1).
- [x] **Fixed `0a082b4` (Z2).** `CLUSTARR_ADVISORIES` (WorkQueue, 30d, 64 MiB) captures `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.>`, and each watcher is a durable consumer on it, `clustarr-dlq-watch-<stream>-<durable>`, shared by the replicas; a failed copy is retried, never dropped. Was: the advisory was core NATS and lost when no replica listened (Y1).
- [ ] A hung handler that returns an error more than 10 minutes after the advisory (`CLUSTARR_DLQ`'s duplicate window) adds a second DLQ copy; the projector's annotation is idempotent (Y1).
- [x] **Fixed `7edfc2d` (Z2).** membus times delivery n on `BackOff[n-1]` (the last entry past the end) when BackOff is set, with a contract test using unequal values. Was: membus timed a redelivery on `AckWait` (Y1).
- [x] **Found and fixed `ba00b53` (Z2).** A delayed nak on a natsbus consumer with BackOff waited `d + BackOff[n-1] - BackOff[0]`, because nats-server stamps the entry `now - AckWait + d` and redelivers on `BackOff[n-1]` with AckWait overridden by `BackOff[0]`: every step past the first came out nearly doubled (`catalogarr-search-normal` waited about 2h, not 1h, after attempt 4). natsbus now naks for `d - (BackOff[n-1] - BackOff[0])`, floored at the smallest delayed nak; the contract test measures the real gaps.
- [ ] `natsbus.Ensure` never deletes, so a broker created before Z2 keeps the pruned `catalogarr-import` and `indexarr-definitions` consumers and the `clustarr-search-cache` bucket, idle, until an operator removes them; and a durable nobody consumes any more keeps its `clustarr-dlq-watch-*` consumer, with its advisories held in `CLUSTARR_ADVISORIES` until 30 days or 64 MiB (Z2).

**Dead code and stale strings.**

- [x] **Pruned `3c214e5` (Z6), `0b49659` (F).** Each was verified with `git grep` to have no reader outside its own tests; the blocklist envtests read `search.LoadBlocklist` against the real cache instead. Was: prune candidates with no reader: `pkg/subtitles.Registry` (X11b), `decision.Target.FreeBytes` (struck from spec §7), and `search.IndexBlocklistInfoHash`/`IndexBlocklistTitle` (X4b/X14).
- [x] **Fixed `335f71f`.** Was: two user-facing strings still said Clustarr ships no Cardigann corpus, which `7b6fc4a` made false: `hack/sync-cardigann/sync.go`'s `licenceNotice` and the `ErrDefinitionNotFound` message in `app/indexer/controller/indexer/cardigann.go`'s `definitionByID` (X13 fixed only the comments).

**Chart and deploy.**

- [x] **Fixed `8dc52c8`** (the `keda` object admits the subchart's own keys; the three Clustarr keys stay typed). Was: `--set keda.enabled=true` failed `values.schema.json`: the enabled KEDA subchart's values merge into `.Values.keda`, whose schema is `additionalProperties: false`, so the KEDA path cannot render at all (X16).
- [x] **Fixed `4fdae18` (Z6).** `cmd/clustarr/chart_cardigann_test.go` pins the rendered env, volume and mount and runs the rendered container's env and args through the real command tree; a render with both a configMap and a claim must fail. Was: no Go test pinned the render of `indexarr.cardigann.*` (X16).
- [ ] No KEDA release has been tested against Kubernetes 1.37 yet (2.20.2 is the newest); the `nats`/`nack` dependency versions were not re-checked (X12a). The transcode ScaledJob stays a placeholder template (ruling R7 of Phase E).
- [ ] Engine pods work only in the release namespace, where their ServiceAccount, data claim and NATS are (X14); a `listenPort` below 1024 binds only where the runtime allows unprivileged low ports (X16).
- [ ] The RBAC-enforced start envtest proves each role only for the paths it exercises; importarr's Trakt token Secret writes, for one, are held only by marker-equals-role (X14).

**Indexers and Cardigann.**

- [x] **Fixed `416a868`, `5dc8f2e`, `9861a3b`, `aeb83b3`, `ddcd119`, `6cbb8e6`; captchas closed by ruling in `53d6834` (Z6).** `login.test` runs after every login method; the infohash is tried before the selectors (and `infohash.usebeforeresponse` is honoured) and `download.before` follows no redirect, as Prowlarr's DownloadRequest; search-path substitutions are URL-encoded and `$raw` split into encoded pairs, as Prowlarr and Jackett do (double encoding included) -- which also fixed a GET that replaced the path's own query, so 47 bundled definitions had searched with no query; 750 of 752 bundled definitions load, the other two reported as stale duplicates. Captchas are never solved, by ruling: the `CaptchaRequired` condition names the captcha and the manual-cookie workaround (a browser session's Cookie header in the Indexer Secret's `cookie` key), which the engine honours; documented in `app/indexer/controller/indexer/doc.go`, spec §6.2 and the chart README. Was: the Cardigann gaps against Prowlarr (X8a).
- [ ] Prowlarr's `BuildPublicMagnetLink` adds public trackers to an infohash-only magnet; ours adds none (predates Z6, noted there).
- [ ] FlareSolverr is tested against a fake `/v1` only (X8b).
- [ ] Optional (X8b): Cardigann releases leave `torznab.Release`'s typed artist/album/author/publisher empty (they are in `Attrs`); `indexerdefinition` keeps its own 800-byte truncation instead of `cardigann.SchemaError`.
- [ ] The query and grab timestamp rings saturate at 4096 entries per window (Phase D1 note).

**Catalog, search and decision.**

- [x] **Fixed `57874db` (Z4), `7f017ce`, `3d0af5d` (Z6).** `ReleaseSummary.releaseDate` is written by the gateway from MusicBrainz, and `Identity.EditionYears` accepts a release within 1 year of any edition's year (every release while `anyReleaseOk`, else the selected or pinned one) before the 5-year reject, as Lidarr's AlbumYearMatcher. Was: album identity carried no edition year (X3).
- [x] **Fixed `68720c1` (Z3).** A scene-mapped episode is searched by its scene season, episode and absolute number from the TheXEM row, each falling back to TVDB's (Sonarr's ReleaseSearchService); the identity keeps TVDB numbering. Was: the search request sent TVDB numbering (X4b).
- [x] **Fixed `9ef5b1f`, `f48eb3a` (Z3), `c1e7cc9` (Z4); the search identity already had it (`0ff8f1c`).** The RSS index keys every alternate title of a series (with and without its first-aired year) and of a movie (with its year), and TVDB `aliases` and TMDB `alternative_titles` now fill them. Was: series title matching indexed the primary title only (X4b).
- [x] **Fixed `bb87926`, `d8c0616`, `bd38a43` (Z3), `8d47d8b`, `4fee0b2` (Z6), `0b49659`, `30b133d` (F).** `schema.Release` gains optional `artist`, `album`, `author` and `issue`, filled by indexarr's projection, and the matcher finds albums, books, audiobooks and issues as Lidarr, Readarr and Mylar do; scene-style music names now parse; the seven new indexes are asserted at startup and keyed through `pkg/decision`'s own `IssueNumberKey` and `CoCredits`. Was: the RSS matcher could not match non-video releases (X4b).
- [ ] Releases replayed from the release index (`rpc.indexarr.query`) do not carry the four non-video names, because `pkg/relindex` does not store them; the RSS matcher does not read that path (Z3).
- [x] **Fixed `bd38a43` (Z3).** The RSS path reads the current file through `search.CurrentFile` from the MediaFile (quality, revision, format score, matched formats, source title and hash, transcoded), as the search worker does. Was: it read the status rollup (X4b).
- [x] **Fixed `fc139c3` (Z4).** `album.FileState` reads every MediaFile of the album: the cutoff is met only when every file meets it and the quality and score are the lowest-ranked file's (Lidarr's CutoffSpecification). Was: one whole-album MediaFile decided (X5b).
- [ ] An album's files are attributed to tracks only for a one-track album, and `AlbumStatus` records no other file list, so an album's MediaFileEvents fire only for track-attributed files and Lidarr's "a track with no file leaves the cutoff unmet" clause is left out (documented in `album.FileState`). Covering either needs the importer to attribute tracks or an `AlbumStatus` file list like Audiobook's `fileRefs`, an API ruling (Z4).
- [x] **Fixed `1d1ec07` (Z4).** `SkipMissingDate` excludes a work with no `FirstPublished`, as Readarr's FilterBooks does; `SkipMissingISBN` stays a documented no-op (the works listing fetches no editions). Was: `SkipMissingDate` was a documented no-op (X5b).
- [x] **Fixed `a047c78` (Z4).** Book and Issue publish on `fileRef`, Audiobook on `fileRefs` (imported and deleted, as Readarr), Album on `tracks[].fileRef` (see the album attribution item above). Was: non-video kinds published no MediaFileEvent (X5b).
- [x] **Closed by ruling (Z-wave).** TheXEM stays a per-process cache: low value, and sharing it would need a topology bucket. Was: TheXEM is cached per catalogarr process (LRU), not shared through KV (X14).
- [ ] Objects written before R-5 may keep `catalogarr-grab` as a co-owner of `activeDownloadRef` until that manager's next apply releases it (X5a; pre-deploy, harmless).

**Metadata.**

- [ ] The fanart.tv, Hardcover and Metron fixtures follow the providers' docs, not live responses (no credentials, X6b); so do the alias values Z4 added to the TMDB movie and TVDB series fixtures (`alternative_titles`, `aliases`).
- [ ] Artwork is best effort: a fanart failure during the fetch that fills the cache leaves those images missing until the next refresh (X6b).
- [x] **Fixed `f25499e` (Z4), stub route `d5620a0` (Z6).** The client requests `/volume/{guid}/`. Was: ComicVine answered `/volume/{guid}` with a 301, an extra round trip per volume (X6a).

**Import and lists.**

- [x] **Fixed `2c1c9cc` (Z5).** The rescan recognises a kept output with no job by squasharr's `<stem> - <profile>` naming beside a recorded source and the profile confirmed by the source's `profileTag` or the file's own `CLUSTARR_PROFILE` (an unreadable tag goes to unmatched), spec §8.4. Was: a `replaceSource=false` output was protected only while its Succeeded TranscodeJob existed (X7a).
- [ ] An output written to an explicit `spec.outputPath` follows no naming convention, so it is still protected from the rescan only while its TranscodeJob exists (Z5).
- [x] **Closed by ruling (Z-wave).** Attributing every wanted episode in a season pack matches Sonarr. Was: a season-pack import attributes every file in the pack, not only the episodes in `spec.target.keys`; each still passes the per-episode upgrade check (X7a).
- [ ] Non-video import lists work only through the arr and custom providers, which spec §17 defers as stubs, and no album, book, audiobook or comic catalog writer exists for a list item (X7b).

**Transcode, subtitles, parser.**

- [x] **Fixed `26d3d17` (Z5).** A profile with no CPU limit plans against a literal `CLUSTARR_CPU_LIMIT` (the request rounded up, else 8) that the Job carries, and `transcode.Plan` rejects 64 or more audio or subtitle streams as its first check, identically in plan and worker. Was: plan/worker parity gaps the worker logged at run time (X10).
- [ ] The arm64 media image is built by CI only (X10).
- [x] **Fixed `558f54d`, `61bd672` (Z6).** A later season matches through its own year when the IMDb search pinned the show; OpenSubtitles.com follows the login's `base_url` (a bare `*.opensubtitles.com` host only) and shares it with the token, as Bazarr's `server_url()`. Was: SubSource's year filter could find nothing for a later season, and the VIP `base_url` was unused (X11a).
- [x] **Fixed `0daf3df`, `d55c8d2`, `ac39f2c` (Z6).** `ParseQualityModifiers` ported whole (REPACK2 is version 3), `ParsePath` keeps a comic's extension, and `parseLanguages` returns every language. Was: the three `pkg/release` gaps (X2).

- [x] **Already fixed before the gap fixes:** `go mod tidy -diff` was empty at `31cf996` (gap-fix ledger X0). `go mod tidy` to fix the `mousetrap` `go.sum` entry (Windows builds only) **and the three direct/indirect misclassifications Phase C's new tests introduced** — see "Build and test hygiene" under *Carried out of Phase C*. One serial run fixes both; never from a parallel agent.
- [x] **Gap fix X12a (`fe94e90`):** the size was already real (100Gi since `61449f8`); **no render-time failure on an empty storage class, by ruling** -- the chart's own defaults are that state and CI renders them, so NOTES.txt warns, `values.schema.json` validates sizes and access modes, and the README explains. Pick a real `clustarr-data` PVC size and require a storage class when no existing claim is set.
- [x] **Fixed:** the engines `730e3c4` (grabarr computes 80% of `DownloadClient.spec.resources.limits.memory`), every Deployment `fe94e90`. Set `GOMEMLIMIT` to 80% of the memory limit for torrent engines (§12); the Downward API only gives 100%, so compute it in the chart.
- [x] **Fixed `fe94e90`:** 2.20.2, the newest release on 2026-09-23; no KEDA release is tested against Kubernetes 1.37 yet. Confirm or change the KEDA version, which an agent picked rather than chose.
- [x] Replace `config/rbac/role.yaml` from `make manifests` once controllers exist, and stop the chart's copy from drifting. **Done in Phase C:** the role generates from `+kubebuilder:rbac` markers, and `TestChartRBACMatchesTheGeneratedRole` compares the chart's copy byte-for-byte between sentinel comments, so drift in either direction fails the build.
- [x] **Done in Phase D:** `app/indexer/run.go`'s `readinessChecks` gates on the release index, and grabarr's engines gate readiness on re-attach (ruling R4). Original entry: Add per-service readiness beyond the JetStream ping. **Phase C did `catalogarr` (informer caches synced) and `importarr` (`/data` present and writable)**, and made every readiness runnable non-leader-elected so a non-leader replica can reach Ready. Still outstanding: the release index for `indexarr` (Phase D) and torrent re-attach for `grabarr` (Phase D) — **reporting ready early lets the controller hand an engine work it would double-download.**
- [x] **Fixed `fe94e90`.** Add `charts/clustarr/README.md` and `values.schema.json` so bad values fail at install rather than at render.
- [x] **Fixed `91fe87b`.** Add `docs/adr/README.md` with the ADR index and supersede lifecycle when ADR-0009 appears.
- [x] **Fixed `fa6cf1b`:** the reflow had landed, and the table now lists all eighteen managers, exactly `k8s.FieldManagers()`. Task G1-0 added three field managers to `pkg/k8s/fieldmanager.go` and `FieldManagers()` -- `catalogarr-fanout` (Artist/Author/Comic fanning out onto Album/Book/Issue, the role `catalogarr-series` plays for Series/Episode), `clustarr-dlq-projector` (R1) and `clustarr-ui` (R2) -- but did **not** update `docs/superpowers/specs/2026-09-18-clustarr-design.md`'s field-manager table (§2, the `| Field managers (SSA) | ... |` row) to match. That file had another session's uncommitted markdown reflow sitting in the working tree at the time, and any commit touching it would have swept that reflow in. Add the three rows once the reflow lands and the file is clean.


### Carried out of Phase B (2026-09-18)

- [x] **Already fixed** (`0b7a726` the generator and ExceptLanguage; `6373985`, `28f4135` the decision proper), verified by gap fix X3. Phase C: `hack/gen-catalogue` generates `pkg/quality/catalogue/data` from `test/data/trash` (the parity test already proves equivalence); `pkg/decision` proper on top of `quality.Profile.UpgradeDecision`; `quality.Condition.ExceptLanguage` evaluation.
- [x] **Fixed `0c17ea5`, `ed021a2`** (Sonarr's `GetFlags`; `torznab.ErrMalformedCaps`). Phase D: map Torznab `DownloadVolumeFactor`/`UploadVolumeFactor` to `ReleaseInfo.IndexerFlags` (freeleech, halfleech, doubleupload) so TRaSH `IndexerFlag` conditions fire; `torznab.wireCaps.Limits` stays typed (a malformed caps doc is a typed error).
- [x] **Fixed `63fba2f` (fix_uppercase), `be89e3a` (Gestdown single-flight).** ~~Phase F: `subtitles.Plan`, `SidecarName`, `ParseSidecar` (deferred from B8)~~ (done at F-2, `d84cde6`); Bazarr `fix_uppercase` port; Gestdown show-id cache single-flight (duplicate lookups on a cold cache are harmless).
- [x] **Fixed `3c59d29`.** Phase G: extend `torznab.Release` with non-video fields (artist, album, author, publisher) now carried in `Attrs`; ~~convert `importlist.ExternalIDs` (struct) ↔ `metadata.ExternalIDs` (map) in the ImportList controller~~ (done at G1-3: `app/import/worker/importlist/externalids.go`).
- [x] **Fixed `5e7bde6` (transport told from decode), `902e244` (`Runner.Run` reports both errors), `903dd1b` (a base URL per TMDB client).** Minor debt: `metadata/clients/musicbrainz` maps `ClientError{StatusCode:0}` (transport or decode) to `ErrDecode`; `transcode.Runner.Run` reports only `waitErr` when both wait and scan fail; `golang-tmdb.SetCustomBaseURL` is process-global (one TMDB base URL per process).
- [x] **Already fixed `0b7a726`** (verified by gap fix X2). Phase C: `pkg/release.parseLanguages` cannot detect Chinese, so the anime dual-audio Language group only fires for Japanese/Korean tags today.
- [x] **Fixed `e0eee98` (gap fixes, W3):** none had an importer, so `mimetype`, `sprig/v3` and `x/net/proxy` left go.mod with `antchfx/xmlquery` and `antchfx/xpath` (unimported since X8a queried XML with CSS); the keeper entries whose real importers had landed went too, and one serial `go mod tidy` ran. `hack/deps` now keeps nothing. Was: `hack/deps/deps.go` still keeps `mimetype`, `sprig/v3` and `x/net/proxy` alive (no importer yet); prune each when its phase lands.

### Carried out of Phase C (2026-09-18)

Phase C's own ledger is `.superpowers/sdd/2026-09-18-phase-c-catalog-core/progress.md`;
every item below is harvested from it, with the phase that owns it. Nothing
here is a regression introduced by Phase C unless it says so.

**Phase D — blocks or distorts M2/M3's own work, so fix these first.**

- [x] **Fixed `903dd1b`, `5e7bde6`;** the only `ErrUnsupported` left in `pkg/metadata` is `tmdb.FindMovie`'s real no-usable-id answer. `tmdb.SearchMovies` and `musicbrainz.SearchArtists` are one-line `ErrUnsupported` stubs that Phase B's task review *and* its whole-branch review both missed. Import lists resolve by title when they carry no id, so **the import-list path cannot work until these exist.** Grep every `pkg/metadata` client for other one-line `ErrUnsupported` returns while fixing them — "implemented" in a report meant the file existed, not that every method did something.
- [x] **Fixed `1421ec8`** (Radarr's ReleaseGroupParser; the rescan guard deleted) **and `3c4ac89`** (fileimport's second copy deleted). `pkg/release` mis-parses the release group for both common *arr filename layouts (`- Bluray-1080p` yields group `"1080p"`), so `spec.releaseGroup` would carry garbage. `app/import/worker/rescan/releasegroup.go` holds a temporary guard — **delete the guard when the parser is fixed**, do not leave two behaviours.
- [x] **Fixed `e0a2f35`, `83b79c2`.** The rescan scanner leaves `FormatScore` zero, because scoring a scanned file needs `pkg/quality/catalogue` integration that Phase C bounded out. `Quality` *is* set from the parsed release so tier comparisons stay correct, but **every scanned file reads as score 0** and within-tier tie-breaks are wrong.
- [x] **Fixed by ruling R-5:** one writer, the item's reconciler (`65fd8fa`, `fef2e13`, `82e52ce`, `58a4171`); the grab path stopped writing it (`f547ecf`); the manager doc `2af2256`. `status.activeDownloadRef` has **two field managers**: the Movie/Episode reconcilers write it under `catalogarr`, the grab worker under `catalogarr-grab` (it was `catalogarr-worker` until the per-consumer split). `k8s.PatchStatus` passes `client.ForceOwnership`, so ownership **migrates rather than conflicting**, and the reconciler's "omit to clear on a terminal Download" mechanism only works while it happens to hold the field. `movie/reconciler.go`'s "sole writer of status.activeDownloadRef" comment is false today. Pick one writer, or make the handover explicit.
- [x] **Fixed `4a5c9c6`, `f547ecf`, `d18645d`, `7cf9204`:** one resolver, `downloads.ResolveSource`, and a guard that lists Downloads live; the stranded-Delayed case is proven fixed. **The two grab paths already disagree, and the guard between them is dead code.** Filed originally as a DRY item; the whole-branch review found it is a live failure, not a risk. `search/grab.go`'s `BuildDownloadSource` — documented as "exported because the automatic-grab path needs the identical mapping" — and `grab/perform.go`'s `chooseSource` build `spec.source` differently, and both paths produce the **identical** deterministic Download name, while `DownloadSpec.Source` carries `self == oldSelf`. The second guard at `grab/perform.go:181` can never fire, because **nothing sets `status.activeDownloadRef` on the interactive path** — `perform.go:257` is its only writer, and the reconcilers merely re-assert an existing value. So: a user grabs from `status.results`, the RSS matcher approves the same release, the apiserver rejects with "source is immutable", `performGrab` returns *before* `clearPendingGrab`, the task dead-letters, and the item is stranded at `Phase=Delayed` forever — the exact failure `clearPendingGrab` exists to prevent. Export one resolver implementation, call it from both, and either make the guard's precondition real or delete it.
- [x] **Fixed `e0a6a90`.** `rpc.go`'s `search()` falls through to "search does not support kind %q" when the kind *is* supported but every registered provider for it failed. An operator debugging a provider outage is told the kind is unsupported. Distinguish "no provider for this kind" from "every provider failed", and surface the underlying errors.
- [x] **Re-read at ruling R-12:** `3d3cf54` (every Download phase before Imported reads Downloading), `2512eaf` (pkg/pipeline's `""` phase). ~~`mediaKey` has no cross-kind collision guard~~ (shipped as `events.MediaKey`, 49961d2) and ~~`PendingGrab`/`Delayed` has no owner~~ (C9); `DownloadOverlay`'s phase mapping is a documented judgment call worth re-reading once real Downloads exist.
- [x] **Done in Phase C (`c0aeee4`):** both reconcilers watch `QualityProfile` through `mapQualityProfile`, and the episode test's `spec.path` bump is gone. Original entry: Neither the Movie nor the Episode reconciler watches `QualityProfile`, so **a cutoff change does not re-evaluate `cutoffMet`** until some other event wakes the item. The episode watch test works around it by bumping the MediaFile's `spec.path` — a workaround that masks the gap, so remove it with the fix.
- [x] **Fixed `b072e91`** (one labelled List per decision). The per-release N+1 in `blocklistPredicate`: up to 400 cache round-trips per search.
- [x] **Fixed `36fd3e7`;** the guard test this entry cites never existed. `Search` carries `ac:generate=false` and a **hand-written apply configuration**, because controller-tools v0.22.0 panics on the embedded `commonv1.ReleaseInfo`. Restructure the embedding so the generator works, then delete the hand-written file; a test already fails if controller-gen ever starts generating one, so the two cannot silently coexist.
- [x] **Fixed `57a64bb`.** `series/reconciler.go` computes the episode rollup from the **pre-fan-out** episode list. Harmless on the normal path; wrong if the episode RPC retries while the metadata refresh lands first.
- [x] **Fixed `b072e91`:** no field index is left to miss, and a read error fails the decision; the e2e assertion is written (`1d92ce7`), never executed. The blocklist path is **warn-and-degrade**: a missing field index yields "nothing is blocklisted, empty queue" plus a warning. C12a guaranteed `RegisterDownloadIndexes` runs once on the right manager (`app/catalog/wiring.go`, asserted in `wiring_envtest_test.go`), but nothing yet proves in a real cluster that the path is **live rather than degraded**. Add that e2e assertion with M3's download scenarios.
- [x] **Fixed `2722310`** (`CutoffUnevaluated`). Neither `MoviePhase` nor `EpisodePhase` has a value meaning **"cutoff not evaluated"**, so `kubectl get`'s phase column shows the misleading `CutoffUnmet` for an item whose quality profile could not be resolved. Only the condition carries the distinction. Adding a phase value is an API change — take it with the next API break.
- [x] **Fixed `a48d7bb`:** one generated ClusterRole per identity. `importarr` needs `update;patch` on `rootfolders` for its last-tick annotation, and **the single shared Role grants that to every service.** SSA scopes ownership per annotation key, so the practical blast radius is one key, but RBAC has no sub-object granularity. Split the Role per service, or keep it and document the grant where it is granted.
- [x] **Done (9189f37..39a4e03):** no call to `mgr.GetEventRecorderFor` remains; one stale doc example survives in `app/grab/engine/usenet/doc.go`. Original entry: `mgr.GetEventRecorderFor` is deprecated and **six controllers pin it with `//nolint:staticcheck`**. Migrating to `mgr.GetEventRecorder` retires all six and leaves one Events group (`events.k8s.io/v1`) instead of today's mix with core/v1 — Phase C settled which controllers are on which, so the migration is now mechanical.
- [x] **Fixed `ad7f454`, `e0a2f35`, `3e67892`** (schedule guard, honest counters, the checkpoint clock, redelivery resume, a walk that carries on). Rescan-worker minors from C10's review: no concurrency guard on the RootFolder schedule (an `@hourly` schedule over a 3-hour walk overlaps); `FilesSkipped` still conflates transcoded files, files unchanged on an incremental scan, and every non-media, part, extra or name-matched sample file; ~~`fsops.IsSample` flags any media file under 50 MiB~~ — since Q-2 (`80e79da`) the size floor is `fsops.Classifier.SampleMaxBytes` (`--sample-max-bytes`, default 50 MiB), video-only, and a size-suspected file goes to `status.unmatched` as `suspected_sample` instead of being skipped (the e2e fixture clip stays ≥50 MiB on purpose, so e2e exercises the default); `noProgressTimeout` is measured from `startedAt` rather than the last checkpoint; redelivery drives counters backwards; `fsops.Walk` aborts the whole walk on one unreadable file.

- [x] ~~The Movie, Series and Episode reconcilers have no spans.~~ **Done in Phase C (41a68d4).** It was filed for Phase D, which was the wrong phase: `CLAUDE.md` requires spans on every `Reconcile`, and amendment §A4 promises the first end-to-end trace at **M1**, which is Phase C. With the bus propagating a trace end to end, the three busiest reconcile loops were the hole it fell into. Still outstanding: span `ffmpeg` runs when Phase E lands.

**Phase D — registration and guard debt, before `grabarr` adds runnables.**

- [x] **Fixed `3e3770f`.** The registration guard's `runnableServices` list is **hand-maintained** — the very anti-pattern the RBAC guard in the same commit was rewritten to avoid. A forgotten runnable in Phase D's `grabarr` would be invisible.
- [x] **Fixed `3e3770f`.** That guard only catches types with **both** `Start` and `NeedLeaderElection`, so a bare-`Start` `manager.RunnableFunc` — precisely the shape that deadlocked every catalogarr and importarr rollout (C12a critical C2) — is **not** caught.
- [x] **Fixed `296153f`.** `app/catalog/worker/search/worker.go`'s private `runnableFunc` duplicates `k8s.EveryReplica` exactly and, being unexported, is skipped by the guard. Delete it and use `k8s.EveryReplica`.
- [x] **Fixed `3e3770f`** (`TestControllerNamesAreUniqueAcrossTheBinary`). controller-runtime's controller-name registry is **process-global**, so `clustarr all` works only because catalogarr's and importarr's controller names are disjoint (now proven by running both in one test binary). Phase D's new controllers must keep them disjoint or `clustarr all` stops starting.
- [x] ~~`mediafile`'s `+kubebuilder:rbac` markers live in `app/catalog/wiring.go`.~~ Moved back in `1c82f7c`; the generated role was verified byte-identical.
- [x] **Fixed `47df838`.** The otel provider is left dead after a full tracing teardown (the refcount reaching zero clears the state), so a later `Setup` in the same process gets a no-op tracer. Harmless in production, a trap in a long-lived test binary.

- [x] **Fixed `57a64bb`** (Sonarr's monitored-only statistics). `Series.status.nextAiring` and `status.previousAiring` have **no writer anywhere.** `series/reconciler.go:547` reads `PreviousAiring` to pick the metadata refresh TTL bucket, so an ended series always falls through to `RefreshStateEndedOld` and never gets the short "recently ended" refresh. Sibling asymmetry: `movieRefreshState` reads fields that *are* populated.
- [x] **Fixed `f547ecf`** (a resourceVersion-checked declare with retry on Conflict, proven with a real interleaved writer). `RecordSearchAttempt`'s read-modify-declare races the grab consumer **across replicas.** In-process ordering against the sink is handled, but every replica runs all three consumers: a search worker on replica A reading a stale informer cache while replica B runs `performGrab` re-declares the pre-grab `activeDownloadRef`/`pendingGrab` under `ManagerCatalogarrGrab` with `ForceOwnership` — releasing the ref and resurrecting the pending grab. Narrow window; at minimum document it, since read-modify-declare is inherently lossy under concurrency.
- [x] **Fixed `296153f`, `01be626`, `e608901`; wired `5cec59d`.** Consumer lookup is inconsistent: the search/grab/rssmatcher subscriptions read `events.Default()` while `app/import/run.go` reads `o.BusTopology()`. Benign only because `ForSingleNode` never touches `Consumers` — an invariant nothing enforces.
- [x] **Fixed `3e3770f`.** A **third** blind spot in the registration guard, on top of the two above: it cannot see an inline `k8s.EveryReplica` closure, which is now the dominant registration shape (6 of 9 `mgr.Add` sites).

**Found while researching D1 (2026-09-19), owned elsewhere.**

- [x] **Fixed `ae75d74`, `903dd1b`, `5e7bde6`, `28b4c72`, `2460e7a`, `84b7724`;** the eight new clients read through the same cap. **Every `pkg/metadata` client decodes straight off the socket with no response cap**, in all five clients. CLAUDE.md states the convention plainly — "Every HTTP response body is read through a cap: a package-level max, an `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel" — and `pkg/torznab` and `pkg/cardigann` do implement it at 8 MiB. The metadata clients simply do not. A hostile or broken provider can exhaust the gateway's memory. The convention exists, is documented, and was never applied to half the code it names.
- [x] **Fixed `7f62097`** (every field listed). **Partly done at G1-1 (`a7dd0cd`): `SearchBlock.Error` is now a `*cardigann.SearchError` (`ErrSearchFailed`) that escalates the indexer, and the definition's `RequestDelay` raises the indexer's pacing (`app/indexer/controller/indexer/controller_definition.go`). Still never read at HEAD: `PreprocessingFilters`, `ResponseBlock.NoResultsMessage`, `Definition.Encoding`, `FollowRedirect`, `Certificates`, `TestLinkTorrent`, `LegacyLinks`, `RowsBlock.Multiple`, `LoginBlock.GetSelectorInputs`.** Original entry: **`pkg/cardigann` has eight undocumented unimplemented features**, on top of the three its docs admit (`rows.after`, `rows.dateheaders`, `|append`). All eight are decoded, schema-validated and exposed on public structs, then never read: `SearchBlock.Error`, `PreprocessingFilters`, `ResponseBlock.NoResultsMessage`, `Definition.Encoding`, `RequestDelay`, `FollowRedirect`, `Certificates`, and the `TestLinkTorrent`/`LegacyLinks`/`Replaces`/`RowsBlock.Multiple`/`LoginBlock.GetSelectorInputs` group. **`search.error` is the dangerous one**: a tracker's error or rate-limit page parses as zero rows and reads to the caller as "no results", so an indexer that is failing looks like an indexer with nothing to offer. Phase G owns wiring Cardigann; it inherits this list.
- [x] **Done:** `8d5fbb3` (`cardigann.LoadBundle`), `0bba1d2` (`hack/sync-cardigann`), `37b48b2` (the startup loader), and `7b6fc4a`: the project owner added Prowlarr's Cardigann definitions on 2026-09-23, embedded as a deflated zip (749 of 752 load), which supersedes ruling R-13. `pkg/cardigann` embeds only `schema.json`. The `.yml` files under `test/data/` are fixtures, not a shipped corpus — **sourcing and vendoring the definition corpus is unbuilt work**, not a wiring task. Size it before Phase G plans around it.
- [x] **Done:** `torznab.WithRateLimit` now takes the injected `*ratelimit.Limiter`, `cardigann.Engine` has an injectable `Limiter` (G1-1, `a7dd0cd`), and the `torznab` package doc no longer cites `spec.rateLimit`. Original entry: `torznab.WithRateLimit(rate.Limit, int)` constructs a private `*rate.Limiter` internally, so it **cannot accept the injected `ratelimit.Limiter`** the project convention requires ("the caller owns rate limiting; a library package accepts an injected limiter and never defaults one on"). `cardigann.Engine` has no limiter at all. Also stale: the `torznab` package doc cites `Indexer.spec.rateLimit`, a field that does not exist — the CRD has `spec.requestDelay` and `spec.limits`.
- [x] **Fixed `68ca190`.** `app/catalog/worker/search` sets `DeadlineMillis: 45000` on the indexer RPC but **never wraps the call in a `context.WithTimeout`**, so the real outer bound is the consumer's `AckWait: 120s`. Either honour the deadline caller-side or stop advertising it.
- [x] **Fixed `296153f`.** The search RPC's `IndexerOutcome` handling **silently drops nameless outcomes** and only logs `Truncated`. An indexer that fails before it is named contributes nothing an operator can see.

**Found during Phase D1 (2026-09-22).**

- [x] **Fixed `b27e5fe`.** **`pkg/cardigann` schema errors leak the process working directory and can exceed Kubernetes' condition-message cap.** `schema.go:45,48` registers the embedded schema as the bare name `schema-v11.json`, so jsonschema/v6 resolves it against the process CWD and a validation failure renders as `file:///<workdir>/schema-v11.json#…` — the binary's working directory, in `kubectl describe`, for a file that is `//go:embed`-ed and not on disk at all. Worse, the message is unbounded: a 73 KB definition (7% of the field's 1 MiB limit) produced a **105,599-byte** error, 3.2× the `maxLength: 32768` on `conditions[].message`. Over that limit the apply is **rejected**, not truncated — so the report explaining why a definition is invalid would itself fail to write, and the reconcile would error-loop on a terminal condition. D1-4 truncates at 800 bytes at its own call site, which makes that controller safe; M6's bundled-definition load at startup and the admission fast-path `Validate`'s doc advertises both still inherit it. Fix `AddResource` to use an absolute non-file URL, and keep the truncation regardless.
- [x] **Fixed by ruling R-9 `1d1b076`, `7c71b3b`.** **`IndexerProxy.spec.port` is a constraint the schema does not express.** It is optional with no default, but `net.JoinHostPort(host, "0")` is meaningless and the three defensible values are per-type (flaresolverr 8191, http 3128, socks 1080), so no single default works and guessing would probe an endpoint nobody configured and report it Ready. The controller therefore rejects an empty port as an invalid spec — which means `kubectl apply` accepts an object the controller will always refuse. Make it `+required`, or add per-type CEL.
- [x] **Resolved at G4-0 (`5ef8c56`):** `IndexerSpec.RequestDelay` is a `*metav1.Duration` — unset means 2s, an explicit `0s` means unpaced — while `timeout` and `rssInterval` stay floored in code; the whole class is now guarded by `pkg/crdcheck`'s `TestNoCRDDefaultIsUnreachableFromGo` (see `CLAUDE.md`'s typed-client gotcha). Original entry: **A typed Go client is never defaulted, so `spec.requestDelay: 0s` and `spec.rssInterval: 0s` are indistinguishable from "unset".** `omitempty` has no effect on a struct field, so `metav1.Duration` is **always** marshalled: a typed client sends `{"rssInterval":"0s","requestDelay":"0s","timeout":"0s"}` and a kubebuilder default fills only a genuinely *absent* field. Verified against a real apiserver — an unstructured create gets `requestDelay=2s timeout=30s rssInterval=15m`, a typed create gets `0s` for all three. The consequence is that `"0s"` on the wire carries two incompatible meanings nothing downstream can separate: an operator writing `requestDelay: 0s` means "do not pace me" (`ratelimit.Config` documents `RPS <= 0` as unlimited), while a Go client leaving the field zero means "I did not specify". The more common producer in this codebase is the typed client, so an Indexer created in-cluster is silently **unpaced**, against the design's 2s-per-host intent. Latent today — nothing creates Indexers in-cluster yet — but M6's definition ingestion and any UI create path hit it, and it is already live in every envtest fixture. `spec.timeout` and `spec.rssInterval` are floored in code because zero has no coherent meaning for either; `requestDelay` deliberately is **not**, because flooring it would overrule an operator who meant it. The real fix is a pointer type in `api/index/**` or a defaulting webhook, so that absent and explicit-zero stop colliding. Neither belongs to a D1 task.
- [x] **Fixed `567dae0`.** **Any `{...}` token in a title classifies it as an audiobook, so an id-carrying release can end up with no parsed fields at all.** `audiobookMarkerRegex` (`pkg/release/classify.go:36`) is `\[ASIN\s[A-Z0-9]{10}\]|\(Unabridged\)|\{[^}]+\}` — the last alternative matches *any* braced token. Verified directly: `"Some Show S01E01 {tvdbid-121361}"` and `"{imdbid-tt0133093} The Matrix 1999 1080p"` both classify as `audiobook`, while the bracket form `"[tmdbid-603] Show - 12 [1080p].mkv"` correctly classifies as `episode`. `Parse` then refuses with "does not match any book title pattern" and the release ships with **no parsed fields whatsoever** — failing closed, so nothing matches rather than the wrong thing matching. Stripping the id first does not help: `extractIDs` removes the id's *value* but leaves the braces, so the stripped title still carries `{tvdbid-}` and the same marker fires (measured by D1-7 against both code paths). **Two earlier versions of this entry named the wrong cause** — first D1-7's classify-once workaround, then "`extractIDs` does not recognise the brace form". Neither is right, and a `Kind` field on `ParsedRelease` would fix none of it: the two real fixes are narrowing `audiobookMarkerRegex`'s brace alternative to actual audiobook markers, and having `extractIDs` remove the delimiters along with the value. `pkg/release` change, no D1 task owns it.
- [x] **Fixed `78e893c`, `b57cd5e`, `8883db0`** (TitleNorm on both sides in one change). **`release.CleanTitle` is ASCII-only, so a non-Latin release is findable only by its metadata — and a non-Latin query degrades into a very broad match.** `CleanTitle` keeps only `[a-z0-9 ]`, and *both* the indexed `TitleNorm` column and `Query.Text` go through it, so the two sides agree and nothing errors. Measured against a real `relindex` store: a release is indexed **iff its name carries at least one ASCII alphanumeric**, which real release names almost always do — `"Матрица.1999.1080p.BluRay"` indexes fine as `"1999 1080p bluray"`. Only a wholly non-Latin name (`"Матрица"`, `"日本語のタイトル"`, `"마마마"`, `"Ω"`) is refused outright by `relindex.Upsert` with `TitleNorm is empty`. **The severity is not in the dropped case, which is now visible** — D1-7 publishes such a release to the firehose when it still carries matchable ids, and `metrics.IndexerReleasesDropped` counts it. The severity is in the *indexed* case: the row loses its non-Latin title tokens, so it cannot be found by its own title, while a query like `"日本語のタイトル 2026"` normalises to `"2026"` and silently matches **every release published that year**. `app/indexer/query`'s `errUnmatchable` guard does not fire, because `q.Text` is non-empty — it only catches a query that normalises to nothing at all. Fix: a normaliser in `pkg/release` that keeps non-ASCII letters, applied to both sides in the same change. That same change retires the NUL-welding limitation noted in `app/indexer/query/doc.go` (`CleanTitle` strips control runes before `pkg/relindex/fts.go` can map them to spaces, so `"dune\x00matrix"` becomes the single term `"dunematrix"`). `pkg/release` change, no D1 task owns it.

- [x] ~~The federated search is ids-only, so spec §6.2's `t=search&q=` fallback is unreachable from catalogarr.~~ **Fixed at G1-6 (`bacaee4`).** `app/catalog/worker/search.BuildSearchRequest` now renders the item's resolved title (`TargetIDs.Title`, populated from `Movie`/`Series.status.metadata.title` in `snapshot.go`) into `SearchRequest.Text` — `"<title> <year>"` for a movie, `"<series> SxxEyy"` for a standard episode, `"<series> <absolute>"` for anime (matching Radarr's and Sonarr's own query shapes; no title yet means no text, not a guessed one). **This entry's own "the frozen payload has no field that carries one" was wrong**, and had been since it was first written in `0c2399e` (Phase D1): `SearchRequest.Text` and `.Year` have carried a free-text query and a year since M0 (`f452f45`), already used by the Torznab facade and an interactive `Search` — the actual gap was catalogarr never having a resolved title to put there, not a schema gap. No new payload version was needed; `pkg/events/schema`'s own contract (`schema.go`'s package doc) only requires one for an incompatible change, and populating an already-optional, already-documented field is not one. `app/indexer/search.buildQuery` (`app/indexer/search/query.go`) now prefers ids wherever an indexer supports one and falls back to the text query **only** when it supports none of the request's id parameters — previously it applied `Text` unconditionally whenever set, even alongside a working id, which `TestBuildQuery/an_id_that_works_wins_even_when_text_is_also_set` falsifies against the reverted guard. Which mode a per-indexer query actually used is now recorded on the outcome as `SearchOutcome.QueryMode` (`"id"`/`"text"`, another additive field under the same `index.SearchResponse.v1` schema string). **Checked, not just assumed:** the task brief's claim that "results still pass through `pkg/decision`, which rejects a wrong title" does **not** hold — `pkg/decision.Evaluate`/`evaluateOne` (`pkg/decision/evaluate.go`, `checks.go`) runs no check anywhere in its §8.2 checklist that compares a release's title, or any id, against the target item; `Target` (`pkg/decision/types.go`) carries no title or id field at all, and `pkg/decision/reasons.go` defines no wrong-title/mismatch `Reason`. For an id-based search this is safe today because the *indexer* does the matching server-side; a text fallback has no such guarantee, and nothing downstream (`RankAndCap`, the grab worker) adds one either. This is a real gap, out of this task's scope (`pkg/decision/` belongs to another task), flagged here rather than silently assumed closed. **That gap is closed at G1-6b (`0ff8f1c`).** `decision.Target` now carries an `Identity` (titles, year, external ids, season/episodes, absolute, air date, and `IDQueryIndexers` — which indexers answered an *id* query in this search, from the reply's per-indexer `QueryMode`), and `identityRejection` (`pkg/decision/identity.go`) runs first on the checklist with two new Permanent reasons, `WrongItem` and `UnknownItem`: ids decide when both sides carry the same key (any conflict rejects even when the title matches; a match settles it regardless of title), a release from an id-query indexer with no ids of its own is vouched for (a movie's year is still bounded), otherwise cleaned titles plus the movie year within ±1, and an episode must also cover the target's numbering. The unevaluable case — a wholly non-Latin title from a text query with no ids, or an empty `Identity` — **fails closed** as `UnknownItem`, reasoned in the doc comment. Both construction sites fill it from one builder (`search.MovieIdentity`/`EpisodeIdentity`, used by the search snapshot and `rssmatcher`'s `resolve`, which now reads a pack's `Keys` episodes). **Radarr's real year rule is exact `Year` or `SecondaryYear`, not ±1** (`ParsingService.TryGetMovieBySearchCriteria`; `docs/research/` has no note on it) — Clustarr's `MovieMetadata` has no `SecondaryYear`, so ±1 stands in for it, documented on `movieYearTolerance`.
- [x] **Closed by ruling R-2:** per indexer by design; `0dc89b2`'s comment cites it. **A usenet release offered by two indexers is not collapsed.** `app/indexer/search.dedupeKey` implements exactly the two keys spec §6.2 names — infohash, then `(indexer, guid)` — and the second is per-indexer by construction, so cross-indexer collapse happens for torrents only. A title+size key was considered and rejected: a wrong dedupe silently *loses* releases, which is worse than a duplicate, and `pkg/decision` ranks duplicates sanely downstream. Revisit only with a key that cannot collide across genuinely different releases.
- [x] **Fixed `74effee`, `0dc89b2`.** **Spec §6.2's `alsoOn` provenance has nowhere to live.** When the merge collapses one release offered by several indexers, which indexers those were is discarded: neither `schema.Release` nor `commonv1.ReleaseInfo` has a field for it and both are frozen. `app/indexer/search` logs `fetched` alongside `releases` so the collapse is at least visible in aggregate, but a specific release's other sources are not recoverable. Needs a payload field, which is a wire change no D1 task may make.
- [x] **Fixed `e2b1c7c` (RSS pages), `646a6a4` (direct grabs).** **The query-limit window still under-reports, because an RSS poll counts nothing.** `app/indexer/search` now projects `status.queriesInWindow` from a CAS timestamp ring at `<indexer-uid>.query` in `clustarr-indexer-limits`, which is what spec §5's KV table calls for and what makes the limit recoverable — an unwindowed counter would pass `spec.limits.queryLimit` once and skip the indexer for good, since nothing in the system lowers it. But `app/indexer/worker/rss` makes up to four Torznab requests per poll and counts none of them, so a polled-and-searched indexer's real traffic is higher than the window says. The direction of the error is the safe one (the limit is reached later than it should be, never earlier) and the fix is one call from the poll path, which is D1-7's file. Two smaller notes on the same ring: the count saturates at 4096 entries per window, far above any realistic Torznab limit but not infinite; and a grab issued through `torrentURL`/`magnetURL`/`nzbURL` bypasses `rpc.indexarr.download` entirely, so the *grab* ring under-reports for the same structural reason.


- [x] **Fixed `fef2e13`, `82e52ce`, `57a64bb`** (Events on phase edges). **Movie, Series and Episode hold an event recorder they never call, and now carry an `events.k8s.io` RBAC rule they never exercise.** Found during the `GetEventRecorderFor` -> `GetEventRecorder` migration (9189f37..39a4e03), which moved the marker with the type rather than deciding the larger question. Either the three controllers should emit the events their siblings emit -- `mediafile` and `search` do -- or the recorder field and its `+kubebuilder:rbac` marker should both go. Granting a permission nothing uses is the milder failure, so the migration left it standing; deciding it is a separate judgement about what those controllers should surface to `kubectl describe`.

- [x] **Done — TVDB now normalizes to BCP-47 at the provider boundary** (`pkg/metadata/clients/tvdb`'s `originalLanguage`, via `pkg/lang.Normalize` from F-2b `a489f9c`; guarded by `TestSeriesOriginalLanguageIsBCP47`, which runs against the real fixture's `"eng"` and was falsified). `pkg/decision` also normalizes as a fallback now, so a 639-2/3 tag from any source is handled. Original entry follows. **TVDB writes ISO-639-3 into a CRD field the whole decision path reads as BCP-47.** `pkg/metadata/clients/tvdb/tvdb.go:136` passes the provider's `"eng"`/`"jpn"` through verbatim into `Movie.status.metadata.originalLanguage`, which every other producer fills with a BCP-47 tag (`"en"`, from TMDB) and which `pkg/decision` now converts on that assumption. The repo's own TVDB fixtures prove the three-letter form. After 6ae2c8b the failure is safe rather than silent — an unconvertible tag is treated as unknown and fails open, so nothing is wrongly rejected — but every TVDB-sourced series logs one `Warn` per `Evaluate`, and language conditions are simply inert for them. Fix belongs in the TVDB client, converting to BCP-47 at the provider boundary the way the other five clients already do.
- [x] ~~A CRD-default quality profile still cannot approve a *non*-English release.~~ **Decided and fixed at 42512f1.** Separate mechanism from the BCP-47 defect fixed in 6ae2c8b, same symptom: `language-not-english` (a NEGATED "contains English" condition, `-10000`) was unconditionally active regardless of the item's original language, so a Japanese release of a Japanese-original film scored `-10000` for lacking English audio it was never going to have. The policy: a release in the item's own original language must be approvable at CRD defaults, whatever that language is; a release in neither English nor the item's original language (e.g. French on an English-original movie) is still penalized. Scoring `language-not-english` per profile at build time (the "Radarr ships the format, profiles score it" alternative floated below) cannot express this — profiles don't see a given item's original language when built, and Radarr's own score-set mechanism varies by profile family (e.g. `german`), not per item at evaluation time. Fixed instead in `pkg/quality/catalogue.evalCondition`'s `CondLanguage` case, sibling to the existing `want==LanguageOriginal` early return: a literal-language condition does not apply when the release is actually in the item's own known original language. No embedded TRaSH data changed.
- [x] **Fixed `8dc8082`** (in Radarr MULTi contributes no language). **`MULTi` releases parse to `Languages: ["Original"]`, which collides with the language conditions from the release side.** `release.ParsedRelease` renders the MULTi marker as the literal token `Original`, which is not a language name, so a comparison against the item's original language cannot evaluate it and the `language-not-original` format's behaviour against a MULTi release is undefined rather than decided. Same family as the two entries above. Radarr's semantics here are specific (MULTi means "includes the original plus at least one dub") and need establishing from its source before anything is changed, rather than guessed at from the token name.

- [x] **Closed by ruling R-12 with evidence:** both fields still unread in v1.61.0; canary tests re-test the behaviour on every run (`e713aa7`). **`anacrolix/torrent` v1.61.0 declares two `AddTorrentOpts` fields it never reads.** `DisallowDataDownload` and `DisallowDataUpload` are settable but `newTorrentOpt` never consults them — verified by grep across the module during D2-1, not inferred. Setting them does nothing and reads as working code, which is the dangerous part. `pkg/download/torrent` works around it by calling `t.DisallowDataDownload()`/`t.DisallowDataUpload()` explicitly after add. Related, same task: a freshly-added torrent never dials peers until something marks its pieces wanted, so the client runs a `wantAllOnInfo` goroutine that calls `DownloadAll()` once metadata arrives. Both workarounds should be re-tested against any future anacrolix bump — if upstream fixes either, the workaround becomes redundant rather than wrong, but the explicit-call form is what proves the behaviour.
- [x] **Fixed `e713aa7`, `85bb44a`;** `eece062` narrows an RSS pack's keys to the episodes that want it. **A torrent's files cannot be selected individually, so every file in a release is always downloaded.** `download.AddRequest` carries no file-selection field, so `pkg/download/torrent` wants all files unconditionally. This is correct for a single-movie torrent and wasteful for a season pack where only one episode is missing — the case Radarr and Sonarr both handle by deselecting files. Needs an `AddRequest` field, which is a seam change, plus engine support. Nothing in D2 owns it.
- [x] **Ruling R-12:** torrent closed with evidence (rationale in `pkg/download/torrent/client.go`, `e713aa7`); usenet fixed `9e062dd` (strict priority classes). **`DownloadPriority` maps onto anacrolix's connection budget by judgement, not by specification.** D2-1 found no spec citation for what a priority should mean to a torrent client and chose a mapping. It is plausible and it is unsourced. D2-3 and D2-5 drive the real controller loop and should confirm or correct it against what the spec actually intends by `spec.priority`.

- [x] ~~**CRITICAL, and it blocks D2-4/D2-6/D2-7: `Download`'s "release identity is immutable" CEL rule dereferences five optional fields unguarded, so a usenet Download is unwritable after creation.**~~ **Fixed at `c5e86d5`.** `api/download/v1alpha1/download_types.go:292` read `self.guid == oldSelf.guid && self.indexerRef == oldSelf.indexerRef && self.title == oldSelf.title && self.protocol == oldSelf.protocol && self.infoHash == oldSelf.infoHash`. Every one of those five is `+optional` with `omitempty` in `api/common/v1alpha1/release_types.go` (GUID:73, IndexerRef:77, Title:85, Protocol:89, InfoHash:121). An absent optional field is simply not in the object map, so CEL raised "no such key" rather than comparing — the rule errored, and **the apiserver rejected the write**. Found by D2-3 and confirmed empirically under envtest, then re-confirmed here by reading the types. The field that made this certain rather than theoretical is `infoHash`: **usenet releases do not have one**, by protocol, so every usenet `Download` failed every update after creation — including a status-only apply, which is the only way grabarr reports progress at all. Torrent Downloads survived only because they happened to carry all five. `c5e86d5` guards every comparison with `has()` on both `self` and `oldSelf`, matching the shape `clientRef`'s own rule at line 270 already used correctly, and regenerated `config/rbac/role.yaml`/the chart's copy in the same commit. `pkg/crdcheck/download_cel_test.go` is the regression guard, built through a dynamic client on purpose — a typed Go client always marshals `infoHash: ""`, which puts the key in the object and would pass against the bug.

- [x] **Fixed by ruling R-6 `85bb44a`, `080eed6`, `da62611`.** **The engine's `Client.Remove` can still finish after the controller has torn down the files.** D2-8b's reaper (`d5c01d2`) guarantees no transfer runs forever, which was the severe half of the problem. It does not resolve the narrower ordering race: `app/grab/controller/download`'s finalizer runs `fsops.SafeRemove` against `status.outputPath` and drops the finalizer without waiting for any engine, so an engine may still hold those files open when they are unlinked. On Linux the data survives until close, so the practical effect is delayed space reclamation and writes into unlinked inodes rather than corruption — which is why it is filed rather than fixed. Resolving it properly means the controller waiting on an engine acknowledgement, which is a controller change and a coordination protocol that D2-8b was explicitly scoped out of. Decide deliberately rather than letting three tasks converge on best-effort again, which is how the reaper came to be needed.
- [x] **Fixed `e713aa7`, `9e062dd`, `85bb44a`** (`Item.AddedAt`). **The reaper's grace period is process-local, not a true transfer age.** `download.Item` carries no creation timestamp, so `Reaper` tracks first-seen-unmatched in memory. A process restart re-extends every in-flight orphan's window. Conservative — never less safe, only less prompt — but it means a crash-looping engine could defer reaping indefinitely. A timestamp on `download.Item` would fix it and is a seam change no D2 task owns. `ReapInterval` (2m) and `OrphanGrace` (10m) are also unsourced judgement calls; the spec says nothing about reaper timing.

**Found during the D2/D3 gate (2026-09-23) — both block the first kind run of scenarios 1, 2 and 6.**

- [x] **Fixed `1d92ce7`.** **Both download fixtures name their served content `clustarr-fixture.bin`, which is not in `pkg/fsops.MediaExtensions`, so the file-import worker will skip it as unattributable and the `MediaFile` tail of scenarios 1, 2 and 6 is permanently blocked until a fixture serves a real media extension.** Verified directly, and still true at the E/F/G gate: `test/fixtures/seeder/seeder.go` and `test/fixtures/nntpstub/fixture.go` both name the payload `clustarr-fixture.bin`, and `pkg/fsops.MediaExtensions` — per kind since Q-1 (`de4891f`); video is `.mkv .mp4 .m4v .avi .mov .wmv .ts .m2ts .mpg .mpeg .webm` — has no `.bin` entry, by design. Scenario 1's transcode and subtitle legs (`TestDownloadScenario1TranscodeLegBlocked`, `TestDownloadScenario1SubtitleLegBlocked`) skip on this gap by name. `app/import/worker/fileimport` never guesses (CLAUDE.md's never-guess rule), so an unattributable file goes to `LibraryScan.status.unmatched` rather than becoming a `MediaFile`. The download and import halves of D2-10's scenarios can still run to completion; only the `MediaFile`-creation assertion at the end of scenarios 1, 2 and 6 is blocked. Fix is one field on each fixture's served filename (e.g. `clustarr-fixture.mkv`), not a code change — owned by whoever runs D2-10's scenarios against kind first.
- [x] **Fixed `1d92ce7`.** **`config/e2e` deploys neither `test/fixtures/seeder` nor `test/fixtures/nntpstub` as a Service, so D2-10's download scenarios skip before creating anything.** Verified: `config/e2e/kustomization.yaml` and its sibling patches list `tmdb-stub.yaml`, `tvdb-stub.yaml` and `torznab-stub.yaml`, all three wired as the D1 pattern established, but no `seeder.yaml` or `nntpstub.yaml` exists in the directory and neither fixture binary is referenced anywhere under `config/`. D1's Torznab/Newznab fixture is the working pattern to copy (a `Deployment` plus a `Service` the relevant controller's spec points at); D2-9 built the two binaries and D2-10 wrote scenarios that assume they are reachable in-cluster, but no task actually added the manifests. Until they exist, `TestDownloadTorrentGrabToImportAttempt` and `TestDownloadUsenetNoInfoHashWithCrossServerFailover` have nothing to grab against and will skip or fail at setup, before the code under test runs at all.

**Phase E — transcode.**

- [x] **Fixed `e0a2f35`, `5ceec2d`, `eca8b96`** (the observed-fingerprint hand-over, spec §8.5). Define what a rescan should do when a **transcoded file's bytes legitimately changed on disk.** `k8s.Apply` always forces ownership, so a rescan re-applying `MediaFileSpec` would silently reclaim `sizeBytes`/`modTime`/`original` from catalogarr after the post-transcode handover. The rescan worker checks `Spec.Original` first and skips such files, which is right for Phase C but is not the final answer.

**Phase G — parity, lists and non-video inventory.**

- [x] **Fixed `b33dda9`, `87a08bc`, `002777e`, `ea911c1`, `948fcff`, `ec5cdc0`, `f490a74`, `831611c`; Ready `42d3960`.** 8 of `MetadataProvider`'s 14 enum values have no client; they return `ErrProviderNotImplemented` → `Ready=Unknown` today, which is honest but not useful.
- [x] **Fixed `20212cc`, `056ab10`.** The CRD's `ImageType` enum allows `poster;fanart;logo` while `pkg/metadata.ImageType` has nine values. Widen the enum if the other six are actually wanted.
- [x] **Closed by ruling R-12 with evidence** (the lowest shipped ratio is 0.995), pinned by `4e20236`. `preferLargest`'s 0.99 ratio separates a profile's sentinel size limit from an ordinary one. It is a chosen threshold with room to spare against all three real tables (movies 0.9995, series 0.9950, anime 0.9995); revisit if a real profile ever lands between.
- [x] **Closed by ruling R-12:** `TestSeriesRollupIsPostFanOut` clears `status.seasons` by omission (`57a64bb`). `WithSeasons(vals ...)` is a no-op at zero elements — **downgraded by the whole-branch review; probably a non-issue.** The list *can* be cleared: omitting the field releases it, and a release removes the field. Nothing else owns `status.seasons`, so omission is sufficient. It is not the same shape as `status.results`, which is the opposite problem — there the worker *wants* to declare an explicit empty list to mean "searched, found nothing" rather than release ownership. Confirm once and delete this item rather than spending a slot on it.

**Docs and spec passes (no code).**

- [x] **Fixed `fa6cf1b`.** Spec §5's stream table has no `CLUSTARR_WORK_IMPORTARR` entry — the implemented topology wins; bring the table up to date.
- [x] **Fixed `fa6cf1b`** (both struck). Decide whether spec §7's `k8sbridge` is still wanted or strike it; Phase C publishes directly through `events.Bus.Publish` with the real helpers. Strike `decision.Upgradable` (it is `quality.Profile.UpgradeDecision`) and `Target.FreeBytes` (deliberately dropped) from the same section.
- [x] **Fixed `91fe87b`.** `docs/research/naming.md` §A6 flags its own summary as "unverified order" and omits two real comparator steps (episode count, indexer flags). `pkg/decision.Rank` was re-verified against `DownloadDecisionComparer.cs`/`ScoreFlags`; fix the note to match.
- [x] **Fixed `d4cab4a`.** `MetadataResponse.Results`' doc comment says "hits of a search request", which the episode listing slightly repurposes.
- [x] **Already fixed `c0aeee4`** (verified by gap fix X5a). `QualityProfile` is cluster-scoped yet movie/episode `Get` it with a namespace set. controller-runtime drops the namespace for root-scoped resources so it works, but it misleads the reader.

**Build and test hygiene.**

- [x] **Already fixed** (empty at `31cf996`, gap-fix ledger X0). **`go mod tidy -diff` is non-empty on the Phase C branch.** Phase C's new tests import `github.com/prometheus/client_model`, `k8s.io/apiextensions-apiserver` and `sigs.k8s.io/yaml` directly (`app/catalog/metadata/tieredcache_test.go`, `test/e2e/main_test.go`, `cmd/clustarr/rbac_markers_test.go`), but `go.mod` still lists all three as `// indirect`. No version changes and nothing added or removed — a direct/indirect reclassification only — so builds and tests are unaffected, but the gate fails. Fold it into the same `go mod tidy` that fixes the `mousetrap` `go.sum` entry above, serially, and never from a parallel agent.
- [x] **Fixed `1d92ce7`** (a roster). `deploymentsAvailable` in `test/e2e/main_test.go` **cannot see a Deployment scaled to zero** — Kubernetes reports `Available=True` for `replicas: 0`, so an overlay that mis-scales a service sails through the readiness gate (verified live: `tmdb-stub` at `replicas: 0` reported `Available=True Progressing=True`). The condition catches what matters — pods that cannot start, pull or pass probes — but anyone adding a "the right services are deployed" check must compare against a **roster**, not this condition. Owned by Phase H, or by whoever first needs that check.

### Carried out of Phases E, F and G (2026-09-23)

Harvested from `.superpowers/sdd/2026-09-23-phases-efg/progress.md` and the
three phase plans, and each re-verified at HEAD during the final gate.

**Events, history and the DLQ.**

- [x] **Fixed:** item and media-file events `65fd8fa`, `fef2e13`, `82e52ce`, `57a64bb`, `a4baa88`; indexer `a278a75`; download `da62611`; transcode `143b724`. **The history sink has producers for only three of its eight event types.** Only `CatalogReleaseSubject` (`app/catalog/worker/grab/perform.go`), `CatalogImportListSyncedSubject` (`app/import/worker/importlist/sync.go`) and `SubtitleEventSubject` (`app/caption/worker/fetch/apply.go`) are ever published. `CatalogItemSubject`, `CatalogMediaFileSubject`, `IndexerEventSubject`, `DownloadEventSubject` and `TranscodeJobSubject` have no production caller, so the sink turns none of those into Events.
- [x] **Fixed `705603f`.** A dead letter that resolves only to a namespace gets its Event on the `Namespace` object (`app/catalog/history/dlq.go`), which is cluster-scoped, so the Event lands in `default` rather than in the namespace it is about.
- [x] **Fixed:** `e3b805c` and every owning controller's fold; the replay handler `705603f`, wired `5cec59d`. Per ruling R1 the DLQ projector annotates (`clustarr.io/dead-lettered`) instead of setting a condition; no owning controller folds that annotation into a condition yet. The `clustarr.io/replay` handler is not built (`app/catalog/history/doc.go` documents the manual workaround).
- [x] **Fixed `3c147b0`.** Pre-amendment dead code: `schema.ImportListTask`, `events.ConsumerCatalogImportList` and `events.FilterCatalogList` are still declared, and the consumer is still in the topology, although importarr's `WorkListSubject` replaced them. Prune them.

**Import lists.**

- [x] **Fixed `0baffb3`, `3f5eda5`.** `ImportList.status` lags a full refresh interval: the controller watches the ImportList with a generation predicate and otherwise requeues only at `nextSyncAt`, so the worker's sync result is not projected until the next scheduled sync.
- [x] **Fixed `0baffb3`, `6d57c6e`, `9b0b516`.** Trakt and Plex are untestable end to end: `app/import/worker/importlist.BuildProvider` threads no base-URL override into `trakt.New` or `plex.New`, and `Reconciler.TraktBaseURL` (the device-flow seam) has no flag. Scenario 9 checks only that those two ImportLists are accepted.
- [x] **Fixed by ruling R-10 `0baffb3`, `3f5eda5`, `4047009`.** Non-video import lists are skipped (`syncKind`'s `supportedKinds`, `app/import/worker/importlist/sync.go`), with a log line and nothing on status.
- [x] **Fixed `0baffb3`.** `SyncActionRemoveAndDelete` behaves exactly like `SyncActionRemove`: deleting the files is not implemented.

**Search, decision and the RSS matcher.**

- [x] **Fixed `f42d2df`, `4ed53f7`.** **Automatic search is movie and episode only.** `app/catalog/worker/search` dispatches `MediaKindMovie` and `MediaKindEpisode` and `snapshot.go` fails any other kind as unsupported, so no non-video item is ever searched or grabbed automatically.
- [x] **Fixed `ebb699d`, `e8e7220`.** **A non-video release cannot be quality-decided.** `pkg/release` fills `Quality` only for movie, TV and anime titles, and `quality.FromCRD` scores every catalogue custom format — all TRaSH video data — against music, book, audiobook and comic profiles alike.
- [x] **Fixed `eece062`.** The RSS matcher's yearless series-title fallback is unreachable: it looks up `TitleYearKey(parsedTitle, rel.Year)` against an index keyed by the series' first-aired year, so a release without a year only ever matches by TVDB id. `TestMatch_SeriesShapes` hides it with a fixture that carries a year.
- [x] **Fixed by ruling R-3 `76324c8`, `124840c`.** A season pack passes the identity check for a single-episode target (`pkg/decision/identity.go` accepts `FullSeason`), so an automatic single-episode search may grab a whole season. Sonarr has a separate rule; this is a policy decision.
- [x] **Fixed `c6fca04`, `c267ccb`, `124840c`, `eece062`, `5cec59d`.** No scene-to-TVDB numbering map for anime: a scene-numbered release with no absolute number is rejected `WrongItem`.
- [x] **Fixed by ruling R-7 `e4f1f87`, `ae75d74`, `903dd1b`, `056ab10`, `40aabab`, `124840c`.** Clustarr has no `SecondaryYear`; the identity check's year ±1 stands in for Radarr's exact-year-or-secondary-year rule.
- [x] **Fixed `2af2256`, `d4cab4a`.** `k8s.ManagerCatalogarrGrab`'s doc says it applies `status.activeDownloadRef`, but the Movie and Episode reconcilers also write that field under `ManagerCatalogarr` — the Phase C co-ownership entry above, still undocumented where the manager is declared.

**Indexers.**

- [x] **Fixed `18dca8e`, `f5def34`.** `IndexerProxy.spec.selector` is not implemented; there is no FlareSolverr client, and `socks4` is refused (`app/indexer/controller/indexer/proxy.go`).
- [x] **Fixed `f9769ec`.** The Indexer controller does not watch `IndexerDefinition`, so a definition edit reaches an Indexer only at its next reprobe (≤15 minutes).
- [x] **Fixed `7f62097`.** The Cardigann fields still never read are listed in the Phase D1 entry above.

**Non-video metadata and catalog.**

- [x] **Fixed `5e7bde6`, `1a0b4e9`, `85d7065`, `e0a2f35`.** **MusicBrainz `mapAlbum` never populates `Album.Releases`**, so an Album's `status.tracks` is always empty in production and `MetadataProfile.ReleaseStatuses` cannot be evaluated (it passes conservatively). `AlbumStatus.SelectedReleaseID` has no writer, and a `MediaRef` cannot address a Track, so there is no per-track file attribution.
- [x] **Fixed `911d21b`, `17d2b51`.** MusicBrainz's "Field recording" secondary type has no member in the CRD's secondary-type enum.
- [x] **Fixed `28b4c72`.** Open Library returns minimal data: `Books` gives ids and title only, and `Book` never fills `Editions` or `FirstPublished`, which makes the metadata profile's `SkipMissingDate`/`SkipMissingISBN` documented no-ops.
- [x] **Fixed `2460e7a`.** ComicVine never fills `ComicVolume.Status`, and its body-level `status_code` is unmapped (TODO in `pkg/metadata/clients/comicvine/comicvine.go`).
- [x] **Fixed `a029f9b`, `a1e6194`;** `9212bdf` ports Kapowarr's calculated issue number (GPL-3.0). `IssueStatus` has `fileQuality` but no `cutoffMet`, so comics get no upgrade tracking although `ComicSpec` carries a quality profile. `CalculatedNumberCentis`' parse is Clustarr's own, not a verified *arr precedent.
- [x] **Fixed `4799c5c`** (Album and Book; Audiobook's premise was false: its profile is required, never inherited). Album, Book and Audiobook `mapQualityProfile` wake only items that name the profile directly; an item inheriting it from its parent picks up an edit on its next reconcile.
- [x] **Fixed `e0a2f35`, `d9bc64f`, `2dc9b59`.** Non-video import: an album's or audiobook's manual import adds files and never replaces old ones; lossy music has no frozen quality (bitrate needs a probe); Episode import is `Ignored`; a Series root folder rescan reports `unsupported_root_kind`.
- [x] **Fixed `e0a2f35`.** Sample and promo handling: a release titled like "Free Samples" is dropped by the name rule, and a manual folder import takes promo clips along.

**Transcode.**

- [x] **Fixed `2b683ef`.** Nothing in the binary applies `$UMASK`, though design §11 requires it (every service, not only squasharr).
- [x] **Fixed `30ab9e5`.** Transcode Job pods get no `securityContext` (`app/squash/controller/transcodejob/job.go`), unlike every Deployment: no non-root, seccomp or read-only root filesystem.
- [x] **Fixed `f44cb41`.** `config/keda/transcode-scaledjob.yaml` (example only, ruling R7, but listed in `config/keda/kustomization.yaml`) cannot work as written: it runs as the `squasharr` ServiceAccount instead of `squasharr-worker`, and starts the worker without `--job`, which `squasharr`'s option validation rejects.
- [x] **Fixed by ruling R-11 `8a31fbe`, `83f7e2b`, `ef087fc`, `81b6bfd`, `c9f3aff`.** `policy.replaceSource: false` is rejected by CEL — deferred like chunking, because honouring it needs an output location that does not exist.
- [x] **Fixed by ruling R-11 `83f7e2b`, `81b6bfd`, `c9f3aff`.** A container change is planned `Skipped` (ruling R8). Making it work means the worker writing `<stem>.<container>`, catalogarr taking over `spec.path` on the swap, and closing the rescan race between rename and path update.
- [x] **Fixed `ecf5bf8`, `e874c95`.** Per-profile transcode concurrency has no API field; only `Reconciler.ProfileLimits` exists.
- [x] **Fixed `30ab9e5`, `77a4980`.** Intel GPU Jobs get no `supplementalGroups` for `/dev/dri`.
- [x] **Fixed `143b724`, `069b559`, `83f7e2b`.** The TranscodeJob controller emits no Kubernetes Events, and the HDR arguments it renders into `status.plan` may differ from the worker's (decision and tier agree; the worker re-renders).

**Subtitles.**

- [x] **Fixed `d99854b`.** `clustarr all` never fetches subtitles: it runs captionarr's controller role only.
- [x] **Recorded in the spec, `fa6cf1b`.** Fetch MsgIDs extend spec §6.5's `<uid>/<langKey>/<probeHash>` with `/<attempt>` or `/force-<generation>` (`events.MsgIDForSubtitle`, `MsgIDForForcedSubtitle`). The spec should record the deviation once its reflow lands.
- [x] **Fixed `0617b94`, `2311f89`.** `SubtitleProfile.spec.embedded.extract` has no effect: under the §6.5 planner an embedded track already counts as existing, so its language is never wanted and the embedded provider is never tasked. A spec-level conflict.
- [x] **Fixed `1004502`, `870e3e7`, `2311f89`.** OpenSubtitles.com ignores `parent_imdb`/`parent_tmdb`, so an episode is searched by moviehash only; no JWT is injected, so `throttle.SetAuth` has no caller.
- [x] **Fixed by ruling R-4 `2311f89`.** The first provider with an acceptable candidate wins, not Bazarr's pooling; two providers of one type means only the higher-priority one is searched.
- [x] **Fixed `2311f89`.** Sidecars are always written 0664 (`fetch.DefaultSidecarMode`); `Worker.SidecarMode` is never set from the root folder's `perms.fileMode`.
- [x] **Fixed `5f5682d`, `81f2a5f`, `870e3e7`;** whisper, sync and Whisper generation closed by ruling R-1. `subdl`, `subsource` and `whisper` have no client (ruling R5), and sync/Whisper stay CEL-forced off.
- [x] **Fixed `870e3e7`.** `hiVerifiable` is still a separate table in the SubtitleProvider controller (`provider.go`), not read from `providerset`.

**UI and `clustarr all`.**

- [x] **Fixed `926b3e3`.** `/readyz` goes green on cache sync, before the first projection round, so a fresh ui pod serves empty pages for ~12 s while reporting Ready.
- [x] **Fixed `d99854b`** (`--ui-bind-address`). `clustarr all`'s ui always binds `:8080`; there is no flag.

**API.**

- [x] **Ruling R-8:** the three named become pointers (`283c8b6`); the other 41 have meaningless zeros and are closed. 44 defaulted `omitempty` scalars cannot send their zero from Go (the "zero omitted and defaulted" class `TestNoCRDDefaultIsUnreachableFromGo` logs). Most zeros mean nothing; three may matter: `CRFTable.HDROffset` (default -1, so "no HDR offset" cannot be sent), `IndexerSpec.MinimumSeeders` (0 becomes 1) and `RecycleBin.CleanupDays` (0 becomes 7). Make those pointers if their zero is wanted.
- [x] **Fixed `fa6cf1b`.** The design spec's field-manager table still lacks G1-0's three managers — the global entry at the top of this section.

**Fixed during E, F and G, or at the final gate — not carried:** Cardigann `SearchBlock.Error` (`a7dd0cd`); grabarr's hard-coded engine PVC (`--data-claim`, G1-5); `Download.status.import.rejections` uncapped (`app/import/worker/fileimport/caps.go`, G4-0); OpenSubtitles.com JSON read uncapped (`7fbe132`); the stale "M6 / not applied" comments in indexarr (G1-5); `ManagerCatalogarrFanout`'s doc and `ManagerCatalogarr`'s missing Audiobook (it now says every `catalog.clustarr.io` kind); the `capMatchedFormats` wiring left untested (`0d820a9`); `ImportedFile` and every other 1024-capped path raised to 4096 (`8c76baf`); the pre-existing gofumpt and staticcheck findings (`7747c8c`, `11dfc86`); and the stale comments the gate brief listed (`f7a9a56`).

### M7 carried items (2026-09-24)

Harvested from the M7 execution ledger
(`.superpowers/sdd/2026-09-24-index-artwork-ratings-plex/`) — every line
marked minor (deferred), a Carry-to note, a Follow-up, or a ruling carrying
a stated cost if it turns out wrong, transcribed close to verbatim and left
unchecked pending Phase H or a dedicated follow-up, the way "Still open
after the gap fixes" started before later waves ticked items off. Design:
`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md`.

**Release index (Part A).**

- [ ] A1: every `OpenPostgres` pays one advisory-lock round trip before the version check (startup-only); the categories read path hand-parses `array_to_string` instead of a pgtype scanner (correct, documented).
- [ ] A2: `clustarr all --index-dsn` with no `--namespace` fails at manager construction with controller-runtime's generic leader-election-namespace error instead of a `Validate` message; `Validate` still requires `IndexPath` non-empty under a DSN (its default is already non-empty).
- [ ] A3: no test renders `postgres.enabled=false && indexarr.replicas>1` to prove the `clustarr.validate` template fail; the `cnpg-system` namespace assumption is unverified until Phase H.
- [ ] A3: `charts/clustarr/README.md`'s Postgres section does not address `helm upgrade --install` users directly (the guard fires correctly; the docs phrase install/upgrade as two verbs).
- [ ] Ruling (A2, cost if wrong: a Postgres deployment with `replicas>1` double-reconciles): with `--index-dsn` set, leader election is enabled and `--leader-elect` is accepted; an empty DSN keeps the single-replica SQLite shape. Unverified against a real multi-replica Postgres deployment until Phase H.
- [ ] Final review (Imp6): indexarr replicas under a DSN do not share limiter state, nor the leader's `spec.requestDelay` or a Cardigann definition's minimum delay -- each follower paces every tracker for itself, so N replicas can reach a tracker up to N times as often as one. Fix: a shared per-indexer token bucket in NATS KV, as `app/caption/throttle` does for subtitle providers, or a non-leader watch of the Indexer config so followers at least apply the same delay. `charts/clustarr/README.md`'s Postgres section says so for now.

**Artwork store (Part B).**

- [ ] B1: no shared-contract assertion for `Put` to an unknown bucket (a pre-existing KV gap too); the `objectDigestHex` empty-digest branch is effectively unreachable.
- [ ] B1: `natsbus`'s object `Delete` is `GetInfo`-then-`Delete`, not atomic — two racing deletes can both return nil (membus is atomic under its mutex); needs a doc note.
- [ ] B2: the metadata consumer's heartbeat is a free-running timer — a hung handler would never dead-letter (every inner call is bounded today); give the handler a deadline or a beat per image.
- [ ] B2: the SSRF surface on `spec.artwork` (no private-address deny-list, a redirect bypasses the per-host limiter, Event notes are a blind oracle) is accepted while `spec.artwork` is `kubectl`-only reachable (ADR-0011); it needs a `CheckRedirect` follow-up before any lower-trust caller (e.g. `ui/actions`) gains a write path to it.
- [ ] B2: artwork drift detection compares the source URL only; there is no interleaving test for the pre-apply re-read; the gateway's render-task publish path has no metrics (`domain.go` not owned by this work).
- [ ] B2/C3: the render Msg-Id is keyed by the poster digest alone, so a poster that changes and changes back within the 1h dedup window absorbs the second render task until a later pass republishes it — the same window absorbs a ratings-only change with an unchanged poster. Fix: fold a ratings digest into the render Msg-Id (follow-up).
- [ ] C3: a known-undecodable original is re-downloaded and re-attempted on every later task — no negative cache.
- [ ] Peer commit `dabd496` (ForSingleNode): the artwork object store had to move off memory storage onto file storage to boot on a single-node kind cluster, recorded as a CLAUDE.md gotcha; flagged here so a future single-node topology change re-checks every bucket, not only KV and streams.

**Ratings and overlays (Part C).**

- [ ] C1: no test for "a failing higher-priority ratings provider frees the source for a lower one that also declares it" (needs overlapping sources — mdblist/omdb, pending R5). Two `BuildRegistry` copies (gateway vs. controller) must be kept in agreement for mdblist/omdb — cleanup candidate.
- [ ] C1: the ratings envtest exercises only the `noopCache` path (cache-hit seeding argued by inspection, not tested); `enrichRatings`' `out` slice ranges an unordered map (callers sort — reconfirm this whenever a new caller is added).
- [ ] Follow-up (ruling R5): record MDBList and OMDb fixtures and build the two clients once API keys are available (`test/data/metadata/mdblist/`, `test/data/metadata/omdb/`, `docs/research/ratings-providers.md`); until then `MetadataProviderType: mdblist|omdb` CRs report `Ready=False, InvalidSpec`.
- [ ] Final review (Imp2): **series carry no ratings in M7.** TMDB declares its `tmdb` source for movies only (`pkg/metadata/clients/tmdb.RatingSources` answers nil for a series) and TVDB, the series provider, supplies none, so `Series.status.metadata.ratings` stays empty, a series `OverlayProfile` renders nothing and Plex gets no `Rating[]` for a show. Fix one: TMDB TV ratings -- record a real `/tv/{id}` response into `test/data/metadata/tmdb/`, build the call against it (TVDB's remote ids already carry a series' TMDB id, `pkg/metadata/clients/tvdb`), and declare `tmdb` for `MediaKindSeries`.
- [ ] Final review (Imp2), fix two: MDBList (`/tmdb/show/{id}`) and OMDb, once built under the R5 follow-up above, declare their sources for series as well as movies (spec §C.2's table), and a test proves a series is rated through them.
- [ ] `overlay.FormatScore` treats `centis == 0` as "no rating", so a genuine 0 with votes -- a real 0% Rotten Tomatoes score -- draws no badge. `Rating.Votes` is what tells the two apart; `Badges` would have to pass it.
- [ ] An `OverlayProfile` whose badge sources no configured provider can supply (the default metacritic badge while MDBList and OMDb are unbuilt, any badge on a series today) reports `Ready` and renders nothing, with no condition saying why. It should surface that, e.g. a `Ready` reason naming the unsuppliable sources.
- [ ] C2: the `alpha_base` overlay golden's badge sits over the near-opaque end of the background gradient; `paddingPx` is computed twice; `radiusPx` is not re-derived after the `minBoxPx` clamp; there is no `pkg/overlay/testdata` directory (the poster is generated in code, not a fixture).
- [ ] C2: the small-poster test asserts full containment only for badge 0 of the stack (badges 1 and 2 also fit, unasserted); badges' X coordinate is not asserted equal across the stack.
- [ ] Ruling (C2, cost if wrong: an unreadable badge stack on a thumbnail-sized poster): four 24px-minimum badges cannot fit a 60x90 poster; badges beyond the poster's edge are clipped by `image/draw` and accepted, since real posters are ≥500px and the 24px floor exists only to keep one badge legible.
- [ ] C3: a profile deleted while no leader runs strands its overlays — a `ProfileRef` naming a missing profile should be treated as owned by every reconcile, not skipped.
- [ ] C3: the render envelope is built independently in `artwork.Publish` and in the gateway's `publishRender` — merge into one renderer in a follow-up.
- [ ] C3: no manager-driven watch test proves the label → `OverlayProfile` selector mapping wakes the right items.
- [x] C3: a max-size 8000x8000 poster decodes to roughly 512 MB per concurrent render — cap decode dimensions for rendering, or set `GOMEMLIMIT` accordingly. Final fix wave: a decoded original wider than `MaxRenderWidth` (2000) is downscaled before drawing, so the canvas and JPEG are capped; the decode itself still holds the full original (96-256 MB at 8000x8000), bounded by `gateway.MaxImageDimension` and the two render slots. A decoder that downsamples while decoding would take that last step.
- [ ] Ruling (C3, cost if wrong: renders re-trigger more often than strictly needed): the profile hash includes badges; the OverlayProfile controller keys render Msg-Ids by the item's `resourceVersion` and publishes a render task for a deselected item too, so its overlay is cleared.
- [ ] W0-2: minor, harmless. `Rating.ValueCentis`/`Votes` gained `+kubebuilder:validation:Minimum`/`Maximum` markers beyond what the design brief specified.
- [ ] W0-3: minor, harmless. A doc comment names a non-existent type `ArtworkVariant`; the fetch consumer's comment calls the task `ImportArtwork` (the spec's informal name for it); `events.ArtworkMaxBytes` is written `5 * GiB` where the design brief showed the literal `5 << 30` (identical value).

**Plex provider (Part D) and e2e.**

- [ ] D1: `buildEpisodeImages` is a stub — Episode carries no artwork of its own yet.
- [ ] B3: `detail_test.go` repeats the literal `"nerve-poster-digest"` instead of referencing its constant.
- [ ] Verify: the renderer treats a missing original as "none" (deletes `poster/overlay`, clears `status.overlay`) and checks `status.overlay`'s `Clustarr-Rendered-From` against the current poster digest on every render task (Carry to C3; implemented per C3's DONE report, not independently re-verified here).
- [ ] Design spec §D.2: whether Plex's `X-Plex-Container-Start` paging is 0- or 1-based is unverified against a real PMS; 0-based is implemented and Phase H's real-server run settles it.
- [ ] Final fix wave: the Plex provider's catalogue index is memoised for `projection.IndexTTL` (5 s) and invalidated on TTL only. The value is a guess sized to a PMS scan burst; tune it against a real PMS scan in Phase H (a shorter TTL shows a catalogue edit sooner, a longer one lists less during a scan), or invalidate on the projection's own tick instead.
- [ ] Scenario 18's thumb-fetch leg skips by name instead of asserting: `config/e2e` has no egress and no in-cluster fixture serves image bytes (`test/fixtures/tmdbstub` serves JSON only; `pkg/metadata/clients/tmdb` hard-codes `posterBaseURL` to the real CDN). Phase H follow-up: an image-serving fixture (extend `tmdbstub` or add one) plus a TMDB image base-URL override (a flag or a `MetadataProvider` spec field) so the leg can assert a 200 `image/*` response; until then the art round trip is proven only by `ui/art.go`'s membus tests and B2's envtests.

### Found by the first real NZB grab on kind-cluster-plex (2026-09-24)

An interactive Search against nzbgeek, a grab through `spec.grab`, and the
`frugal` usenet DownloadClient. The blocker (NATS `max_payload`, a reply
natsbus dropped silently) is fixed in the chart and in natsbus; these are
what it left behind.

- [ ] **indexarr's download budget should follow the connection's real
  limit.** `app/indexer/download.MaxPayloadBytes` is a 4 MiB constant; the
  reply that matters must fit `nc.MaxPayload()`, which a Bus could expose.
  When the .nzb would not fit, hand back `RedirectURL` (the engine already
  fetches one directly) instead of attempting a reply the connection
  refuses -- then a misconfigured server degrades to a direct fetch rather
  than a stranded grab, and `TestServeReportsAReplyTheServerRefuses` is
  the backstop rather than the behaviour.
- [ ] **A newznab download URL carries the API key in the clear inside CR
  fields.** `Search.status.results[].downloadURL` and
  `Download.spec.source.indexerDownload.url` hold nzbgeek's
  `apikey=` query parameter, readable by anyone who can read Searches or
  Downloads (the ui role included) and visible in `kubectl get -o yaml`.
  The error-string rule ("API keys never in error strings") does not
  cover object fields. Options: strip the key when the ReleaseDecision is
  recorded and let indexarr re-append it from the Indexer's Secret when it
  fetches (it already resolves the Indexer), or record only the guid and
  resolve the URL at grab time (blocked today: "release has no download
  URL; indexarr cannot resolve a guid without one").
- [ ] **The engine's requeue backoff after a failed payload resolve grows
  to many minutes** (controller-runtime's per-item exponential backoff,
  reached ~5 min by the twentieth attempt), so a fixed upstream is not
  retried promptly; a bounded backoff (or a `RequeueAfter` of the poll
  interval) for the resolve step would recover within a minute.
- [ ] **A connected nats.go client does not see a reloaded `max_payload`**
  (it keeps the `INFO` from connect); document in the chart README that
  raising it needs the publishers restarted, or reconnect on the reload
  advisory.

- [ ] **A failed usenet job re-downloads from scratch on retry; the
  10 GB of articles that were on `/scratch` are discarded.** The
  2026-09-24 grab failed only in post-processing (par2 could not match
  obfuscated names). Once a manifest records `Failed` at stage `done`, an
  operator fix (a new image, a par2 binary) should be able to re-run
  post-processing over the content that is already there -- a
  `clustarr.io/retry-postprocess` annotation, or re-queueing a job whose
  transfer completed at the post-process stage rather than at `Add`.
- [ ] **The usenet engine logged one `Failed to watch downloads ... cannot
  watch resource "downloads" ... at the cluster scope` under
  `clustarr-grabarr-engine`** while a helm upgrade re-applied the release's
  RBAC and the apiserver was saturated (18:54:43, 2026-09-24). The
  ClusterRole grants `watch`; the message was not repeated. Check whether a
  ClusterRole re-apply can produce a window in which its rules are empty,
  and whether the engine's informer recovers without a restart.

- [ ] **Every controller re-applies status for every object after a
  restart or deploy.** In the six minutes after the 19:21 apiserver
  restart on 2026-09-24: 31,262 Episode, 12,605 MediaFile and 820 Movie
  status APPLYs, against near zero in steady state. Each is a full
  apiserver round trip and an etcd read even when nothing changed. Skip
  the apply when the rendered status equals what the object already
  carries under that manager (a cheap deep-equal on the rendered
  configuration against the live status), and stagger the initial
  reconciles, so a deploy does not land a write storm on a control plane
  that may already be under I/O.
- [ ] **Leader-election and status-write timeouts are the defaults, which a
  homelab control plane under I/O does not meet.** grabarr lost its lease
  and exited when a lease renewal took over 5 s; the lease durations and
  `RenewDeadline` in `pkg/k8s.ManagerOptions` should tolerate a slow
  apiserver (and the exit-on-lost-lease is right, so the fix is the
  timings, not the behaviour). ADR-0014 removes the I/O cause on the
  owner's cluster; this is the second line of defence.
- [ ] **The engine reports every failed repair as `missingArticles`.** A
  repair that failed for another reason (par2 could not match the set's
  names before the extras fix) carried the same reason and was blocklisted
  the same way. Give repair failures that are not article loss their own
  reason, or at least their own condition message.
- [ ] **Critical health is estimated in bytes, but par2 recovers in
  blocks.** With 5 MB blocks a lost 700 KB article costs a whole block, so
  the byte-based NZBGet estimate (98% on the 2026-09-24 grab) overstates
  what the set can lose; reading the block size from a par2 volume's main
  packet once it lands would make the floor exact.

### Deferred by decision: the unified manager topology (2026-09-24)

- [ ] Adopt `docs/superpowers/specs/2026-09-24-unified-manager-design.md`
      (one `clustarr manager` Deployment hosting every reconciler under one
      lease and one cache; domain workers `clustarr-catalogarr`,
      `-importarr`, `-indexarr`, `-captionarr` with their own
      ServiceAccounts; metadata gateway, ui, engines and pools unchanged)
      once the system is production ready and battle tested — ADR-0013.
      Not before Phase H is green. Tasks are listed in the design's §5.

### Settings CRUD (2026-09-24, `docs/superpowers/specs/2026-09-24-settings-crud-design.md`)

- [ ] **The metadata gateway reads MetadataProviders once, at start.** A
      provider added, edited or deleted from the Settings page (or with
      kubectl) is used only after `catalogarr-metadata` restarts; the
      controller's Ready probe runs on every change, the gateway's registry
      does not. Rebuild the registry on a MetadataProvider or Secret change.
      (Fixed the same day: a provider whose credentials cannot be read no
      longer fails that startup -- it is skipped and logged, since one
      hardcover provider made from the Settings page against a hand-made
      Secret with the wrong key name crash-looped the gateway on kind.)
- [ ] **Per-definition Cardigann settings in the Indexer form.** The form
      offers a definition picker, the credential entries and a free
      settings map; the definition's own `settings:` fields (text, password,
      checkbox, select with options and defaults, from the
      IndexerDefinition's YAML) are not yet rendered as typed inputs. A
      password-type setting should go to the Secret, the rest to
      `spec.settings`.
- [ ] **Copy a built-in quality profile.** The CRD refuses edits to a
      built-in; the form renders it read-only, but a "copy" that opens the
      new form prefilled from it is not there yet.

## Self-review notes

Checked against both specs on 2026-09-18.

- **Spec coverage.** Amendment §A1 maps to A4, A5 and Phase C. §A2 maps to A1, A2, A3 and A7. §A3 maps to A6 and Phases D and G. Base spec §16's M1 through M6 map to Phases C through G. The library layer has no milestone of its own in the spec; it is Phase B here because M1 cannot start without it.
- **Type consistency.** `pipeline.Stage`, `pipeline.Entry` and `pipeline.Related` are used identically in A6 and Phase D. `k8s.PatchStatus`, the field managers and the condition helpers are quoted from the real source, not from memory.
- **Known gap, stated rather than hidden.** Phases C through G are work orders, not step-by-step tasks. Each needs its own plan written immediately before it runs. Writing them now would be guessing at interfaces that Phase B has not yet defined.
