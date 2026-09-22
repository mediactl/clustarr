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
>   path live: `catalogarr/worker/search` already calls `rpc.indexarr.search`
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
>   reaching `catalogarr`'s RSS matcher with a legal envelope key. Number it
>   when the D1 plan is written and add it to the roster below, so Phase H's
>   audit sees seventeen.
> - **D2 — `grabarr` + `importarr`'s file-import worker (M3).** DownloadClient,
>   the torrent and usenet engines, the Download controller and its re-attach
>   semantics, and the completed-download import. Lands scenarios 1 (through
>   import), 2, 3, 4 and 6.
> - **D3 — the first UI slice.** The pipeline and downloads pages over real
>   resources. Lands the corresponding parts of scenario 14.
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

- [ ] `go mod tidy` to fix the `mousetrap` `go.sum` entry (Windows builds only) **and the three direct/indirect misclassifications Phase C's new tests introduced** — see "Build and test hygiene" under *Carried out of Phase C*. One serial run fixes both; never from a parallel agent.
- [ ] Pick a real `clustarr-data` PVC size and require a storage class when no existing claim is set.
- [ ] Set `GOMEMLIMIT` to 80% of the memory limit for torrent engines (§12); the Downward API only gives 100%, so compute it in the chart.
- [ ] Confirm or change the KEDA version, which an agent picked rather than chose.
- [x] Replace `config/rbac/role.yaml` from `make manifests` once controllers exist, and stop the chart's copy from drifting. **Done in Phase C:** the role generates from `+kubebuilder:rbac` markers, and `TestChartRBACMatchesTheGeneratedRole` compares the chart's copy byte-for-byte between sentinel comments, so drift in either direction fails the build.
- [ ] Add per-service readiness beyond the JetStream ping. **Phase C did `catalogarr` (informer caches synced) and `importarr` (`/data` present and writable)**, and made every readiness runnable non-leader-elected so a non-leader replica can reach Ready. Still outstanding: the release index for `indexarr` (Phase D) and torrent re-attach for `grabarr` (Phase D) — **reporting ready early lets the controller hand an engine work it would double-download.**
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

### Carried out of Phase C (2026-09-18)

Phase C's own ledger is `.superpowers/sdd/2026-09-18-phase-c-catalog-core/progress.md`;
every item below is harvested from it, with the phase that owns it. Nothing
here is a regression introduced by Phase C unless it says so.

**Phase D — blocks or distorts M2/M3's own work, so fix these first.**

- [ ] `tmdb.SearchMovies` and `musicbrainz.SearchArtists` are one-line `ErrUnsupported` stubs that Phase B's task review *and* its whole-branch review both missed. Import lists resolve by title when they carry no id, so **the import-list path cannot work until these exist.** Grep every `pkg/metadata` client for other one-line `ErrUnsupported` returns while fixing them — "implemented" in a report meant the file existed, not that every method did something.
- [ ] `pkg/release` mis-parses the release group for both common *arr filename layouts (`- Bluray-1080p` yields group `"1080p"`), so `spec.releaseGroup` would carry garbage. `importarr/worker/rescan/releasegroup.go` holds a temporary guard — **delete the guard when the parser is fixed**, do not leave two behaviours.
- [ ] The rescan scanner leaves `FormatScore` zero, because scoring a scanned file needs `pkg/quality/catalogue` integration that Phase C bounded out. `Quality` *is* set from the parsed release so tier comparisons stay correct, but **every scanned file reads as score 0** and within-tier tie-breaks are wrong.
- [ ] `status.activeDownloadRef` has **two field managers**: the Movie/Episode reconcilers write it under `catalogarr`, the grab worker under `catalogarr-grab` (it was `catalogarr-worker` until the per-consumer split). `k8s.PatchStatus` passes `client.ForceOwnership`, so ownership **migrates rather than conflicting**, and the reconciler's "omit to clear on a terminal Download" mechanism only works while it happens to hold the field. `movie/reconciler.go`'s "sole writer of status.activeDownloadRef" comment is false today. Pick one writer, or make the handover explicit.
- [ ] **The two grab paths already disagree, and the guard between them is dead code.** Filed originally as a DRY item; the whole-branch review found it is a live failure, not a risk. `search/grab.go`'s `BuildDownloadSource` — documented as "exported because the automatic-grab path needs the identical mapping" — and `grab/perform.go`'s `chooseSource` build `spec.source` differently, and both paths produce the **identical** deterministic Download name, while `DownloadSpec.Source` carries `self == oldSelf`. The second guard at `grab/perform.go:181` can never fire, because **nothing sets `status.activeDownloadRef` on the interactive path** — `perform.go:257` is its only writer, and the reconcilers merely re-assert an existing value. So: a user grabs from `status.results`, the RSS matcher approves the same release, the apiserver rejects with "source is immutable", `performGrab` returns *before* `clearPendingGrab`, the task dead-letters, and the item is stranded at `Phase=Delayed` forever — the exact failure `clearPendingGrab` exists to prevent. Export one resolver implementation, call it from both, and either make the guard's precondition real or delete it.
- [ ] `rpc.go`'s `search()` falls through to "search does not support kind %q" when the kind *is* supported but every registered provider for it failed. An operator debugging a provider outage is told the kind is unsupported. Distinguish "no provider for this kind" from "every provider failed", and surface the underlying errors.
- [ ] ~~`mediaKey` has no cross-kind collision guard~~ (shipped as `events.MediaKey`, 49961d2) and ~~`PendingGrab`/`Delayed` has no owner~~ (C9); `DownloadOverlay`'s phase mapping is a documented judgment call worth re-reading once real Downloads exist.
- [ ] Neither the Movie nor the Episode reconciler watches `QualityProfile`, so **a cutoff change does not re-evaluate `cutoffMet`** until some other event wakes the item. The episode watch test works around it by bumping the MediaFile's `spec.path` — a workaround that masks the gap, so remove it with the fix.
- [ ] The per-release N+1 in `blocklistPredicate`: up to 400 cache round-trips per search.
- [ ] `Search` carries `ac:generate=false` and a **hand-written apply configuration**, because controller-tools v0.22.0 panics on the embedded `commonv1.ReleaseInfo`. Restructure the embedding so the generator works, then delete the hand-written file; a test already fails if controller-gen ever starts generating one, so the two cannot silently coexist.
- [ ] `series/reconciler.go` computes the episode rollup from the **pre-fan-out** episode list. Harmless on the normal path; wrong if the episode RPC retries while the metadata refresh lands first.
- [ ] The blocklist path is **warn-and-degrade**: a missing field index yields "nothing is blocklisted, empty queue" plus a warning. C12a guaranteed `RegisterDownloadIndexes` runs once on the right manager (`catalogarr/wiring.go`, asserted in `wiring_envtest_test.go`), but nothing yet proves in a real cluster that the path is **live rather than degraded**. Add that e2e assertion with M3's download scenarios.
- [ ] Neither `MoviePhase` nor `EpisodePhase` has a value meaning **"cutoff not evaluated"**, so `kubectl get`'s phase column shows the misleading `CutoffUnmet` for an item whose quality profile could not be resolved. Only the condition carries the distinction. Adding a phase value is an API change — take it with the next API break.
- [ ] `importarr` needs `update;patch` on `rootfolders` for its last-tick annotation, and **the single shared Role grants that to every service.** SSA scopes ownership per annotation key, so the practical blast radius is one key, but RBAC has no sub-object granularity. Split the Role per service, or keep it and document the grant where it is granted.
- [ ] `mgr.GetEventRecorderFor` is deprecated and **six controllers pin it with `//nolint:staticcheck`**. Migrating to `mgr.GetEventRecorder` retires all six and leaves one Events group (`events.k8s.io/v1`) instead of today's mix with core/v1 — Phase C settled which controllers are on which, so the migration is now mechanical.
- [ ] Rescan-worker minors from C10's review: no concurrency guard on the RootFolder schedule (an `@hourly` schedule over a 3-hour walk overlaps); `FilesSkipped` conflates four causes; `fsops.IsSample` flags any media file under 50 MiB (the e2e fixture clip is 56.7 MiB *because* of this); `noProgressTimeout` is measured from `startedAt` rather than the last checkpoint; redelivery drives counters backwards; `fsops.Walk` aborts the whole walk on one unreadable file.

- [x] ~~The Movie, Series and Episode reconcilers have no spans.~~ **Done in Phase C (41a68d4).** It was filed for Phase D, which was the wrong phase: `CLAUDE.md` requires spans on every `Reconcile`, and amendment §A4 promises the first end-to-end trace at **M1**, which is Phase C. With the bus propagating a trace end to end, the three busiest reconcile loops were the hole it fell into. Still outstanding: span `ffmpeg` runs when Phase E lands.

**Phase D — registration and guard debt, before `grabarr` adds runnables.**

- [ ] The registration guard's `runnableServices` list is **hand-maintained** — the very anti-pattern the RBAC guard in the same commit was rewritten to avoid. A forgotten runnable in Phase D's `grabarr` would be invisible.
- [ ] That guard only catches types with **both** `Start` and `NeedLeaderElection`, so a bare-`Start` `manager.RunnableFunc` — precisely the shape that deadlocked every catalogarr and importarr rollout (C12a critical C2) — is **not** caught.
- [ ] `catalogarr/worker/search/worker.go`'s private `runnableFunc` duplicates `k8s.EveryReplica` exactly and, being unexported, is skipped by the guard. Delete it and use `k8s.EveryReplica`.
- [ ] controller-runtime's controller-name registry is **process-global**, so `clustarr all` works only because catalogarr's and importarr's controller names are disjoint (now proven by running both in one test binary). Phase D's new controllers must keep them disjoint or `clustarr all` stops starting.
- [x] ~~`mediafile`'s `+kubebuilder:rbac` markers live in `catalogarr/wiring.go`.~~ Moved back in `1c82f7c`; the generated role was verified byte-identical.
- [ ] The otel provider is left dead after a full tracing teardown (the refcount reaching zero clears the state), so a later `Setup` in the same process gets a no-op tracer. Harmless in production, a trap in a long-lived test binary.

- [ ] `Series.status.nextAiring` and `status.previousAiring` have **no writer anywhere.** `series/reconciler.go:547` reads `PreviousAiring` to pick the metadata refresh TTL bucket, so an ended series always falls through to `RefreshStateEndedOld` and never gets the short "recently ended" refresh. Sibling asymmetry: `movieRefreshState` reads fields that *are* populated.
- [ ] `RecordSearchAttempt`'s read-modify-declare races the grab consumer **across replicas.** In-process ordering against the sink is handled, but every replica runs all three consumers: a search worker on replica A reading a stale informer cache while replica B runs `performGrab` re-declares the pre-grab `activeDownloadRef`/`pendingGrab` under `ManagerCatalogarrGrab` with `ForceOwnership` — releasing the ref and resurrecting the pending grab. Narrow window; at minimum document it, since read-modify-declare is inherently lossy under concurrency.
- [ ] Consumer lookup is inconsistent: the search/grab/rssmatcher subscriptions read `events.Default()` while `importarr/run.go` reads `o.BusTopology()`. Benign only because `ForSingleNode` never touches `Consumers` — an invariant nothing enforces.
- [ ] A **third** blind spot in the registration guard, on top of the two above: it cannot see an inline `k8s.EveryReplica` closure, which is now the dominant registration shape (6 of 9 `mgr.Add` sites).

**Found while researching D1 (2026-09-19), owned elsewhere.**

- [ ] **Every `pkg/metadata` client decodes straight off the socket with no response cap**, in all five clients. CLAUDE.md states the convention plainly — "Every HTTP response body is read through a cap: a package-level max, an `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel" — and `pkg/torznab` and `pkg/cardigann` do implement it at 8 MiB. The metadata clients simply do not. A hostile or broken provider can exhaust the gateway's memory. The convention exists, is documented, and was never applied to half the code it names.
- [ ] **`pkg/cardigann` has eight undocumented unimplemented features**, on top of the three its docs admit (`rows.after`, `rows.dateheaders`, `|append`). All eight are decoded, schema-validated and exposed on public structs, then never read: `SearchBlock.Error`, `PreprocessingFilters`, `ResponseBlock.NoResultsMessage`, `Definition.Encoding`, `RequestDelay`, `FollowRedirect`, `Certificates`, and the `TestLinkTorrent`/`LegacyLinks`/`Replaces`/`RowsBlock.Multiple`/`LoginBlock.GetSelectorInputs` group. **`search.error` is the dangerous one**: a tracker's error or rate-limit page parses as zero rows and reads to the caller as "no results", so an indexer that is failing looks like an indexer with nothing to offer. Phase G owns wiring Cardigann; it inherits this list.
- [ ] `pkg/cardigann` embeds only `schema.json`. The `.yml` files under `testdata/` are fixtures, not a shipped corpus — **sourcing and vendoring the definition corpus is unbuilt work**, not a wiring task. Size it before Phase G plans around it.
- [ ] `torznab.WithRateLimit(rate.Limit, int)` constructs a private `*rate.Limiter` internally, so it **cannot accept the injected `ratelimit.Limiter`** the project convention requires ("the caller owns rate limiting; a library package accepts an injected limiter and never defaults one on"). `cardigann.Engine` has no limiter at all. Also stale: the `torznab` package doc cites `Indexer.spec.rateLimit`, a field that does not exist — the CRD has `spec.requestDelay` and `spec.limits`.
- [ ] `catalogarr/worker/search` sets `DeadlineMillis: 45000` on the indexer RPC but **never wraps the call in a `context.WithTimeout`**, so the real outer bound is the consumer's `AckWait: 120s`. Either honour the deadline caller-side or stop advertising it.
- [ ] The search RPC's `IndexerOutcome` handling **silently drops nameless outcomes** and only logs `Truncated`. An indexer that fails before it is named contributes nothing an operator can see.

**Found during Phase D1 (2026-09-22).**

- [ ] **`pkg/cardigann` schema errors leak the process working directory and can exceed Kubernetes' condition-message cap.** `schema.go:45,48` registers the embedded schema as the bare name `schema-v11.json`, so jsonschema/v6 resolves it against the process CWD and a validation failure renders as `file:///<workdir>/schema-v11.json#…` — the binary's working directory, in `kubectl describe`, for a file that is `//go:embed`-ed and not on disk at all. Worse, the message is unbounded: a 73 KB definition (7% of the field's 1 MiB limit) produced a **105,599-byte** error, 3.2× the `maxLength: 32768` on `conditions[].message`. Over that limit the apply is **rejected**, not truncated — so the report explaining why a definition is invalid would itself fail to write, and the reconcile would error-loop on a terminal condition. D1-4 truncates at 800 bytes at its own call site, which makes that controller safe; M6's bundled-definition load at startup and the admission fast-path `Validate`'s doc advertises both still inherit it. Fix `AddResource` to use an absolute non-file URL, and keep the truncation regardless.
- [ ] **`IndexerProxy.spec.port` is a constraint the schema does not express.** It is optional with no default, but `net.JoinHostPort(host, "0")` is meaningless and the three defensible values are per-type (flaresolverr 8191, http 3128, socks 1080), so no single default works and guessing would probe an endpoint nobody configured and report it Ready. The controller therefore rejects an empty port as an invalid spec — which means `kubectl apply` accepts an object the controller will always refuse. Make it `+required`, or add per-type CEL.
- [ ] **A typed Go client is never defaulted, so `spec.requestDelay: 0s` and `spec.rssInterval: 0s` are indistinguishable from "unset".** `omitempty` has no effect on a struct field, so `metav1.Duration` is **always** marshalled: a typed client sends `{"rssInterval":"0s","requestDelay":"0s","timeout":"0s"}` and a kubebuilder default fills only a genuinely *absent* field. Verified against a real apiserver — an unstructured create gets `requestDelay=2s timeout=30s rssInterval=15m`, a typed create gets `0s` for all three. The consequence is that `"0s"` on the wire carries two incompatible meanings nothing downstream can separate: an operator writing `requestDelay: 0s` means "do not pace me" (`ratelimit.Config` documents `RPS <= 0` as unlimited), while a Go client leaving the field zero means "I did not specify". The more common producer in this codebase is the typed client, so an Indexer created in-cluster is silently **unpaced**, against the design's 2s-per-host intent. Latent today — nothing creates Indexers in-cluster yet — but M6's definition ingestion and any UI create path hit it, and it is already live in every envtest fixture. `spec.timeout` and `spec.rssInterval` are floored in code because zero has no coherent meaning for either; `requestDelay` deliberately is **not**, because flooring it would overrule an operator who meant it. The real fix is a pointer type in `api/index/**` or a defaulting webhook, so that absent and explicit-zero stop colliding. Neither belongs to a D1 task.
- [ ] **Any `{...}` token in a title classifies it as an audiobook, so an id-carrying release can end up with no parsed fields at all.** `audiobookMarkerRegex` (`pkg/release/classify.go:36`) is `\[ASIN\s[A-Z0-9]{10}\]|\(Unabridged\)|\{[^}]+\}` — the last alternative matches *any* braced token. Verified directly: `"Some Show S01E01 {tvdbid-121361}"` and `"{imdbid-tt0133093} The Matrix 1999 1080p"` both classify as `audiobook`, while the bracket form `"[tmdbid-603] Show - 12 [1080p].mkv"` correctly classifies as `episode`. `Parse` then refuses with "does not match any book title pattern" and the release ships with **no parsed fields whatsoever** — failing closed, so nothing matches rather than the wrong thing matching. Stripping the id first does not help: `extractIDs` removes the id's *value* but leaves the braces, so the stripped title still carries `{tvdbid-}` and the same marker fires (measured by D1-7 against both code paths). **Two earlier versions of this entry named the wrong cause** — first D1-7's classify-once workaround, then "`extractIDs` does not recognise the brace form". Neither is right, and a `Kind` field on `ParsedRelease` would fix none of it: the two real fixes are narrowing `audiobookMarkerRegex`'s brace alternative to actual audiobook markers, and having `extractIDs` remove the delimiters along with the value. `pkg/release` change, no D1 task owns it.
- [ ] **`release.CleanTitle` is ASCII-only, so a non-Latin release is findable only by its metadata — and a non-Latin query degrades into a very broad match.** `CleanTitle` keeps only `[a-z0-9 ]`, and *both* the indexed `TitleNorm` column and `Query.Text` go through it, so the two sides agree and nothing errors. Measured against a real `relindex` store: a release is indexed **iff its name carries at least one ASCII alphanumeric**, which real release names almost always do — `"Матрица.1999.1080p.BluRay"` indexes fine as `"1999 1080p bluray"`. Only a wholly non-Latin name (`"Матрица"`, `"日本語のタイトル"`, `"마마마"`, `"Ω"`) is refused outright by `relindex.Upsert` with `TitleNorm is empty`. **The severity is not in the dropped case, which is now visible** — D1-7 publishes such a release to the firehose when it still carries matchable ids, and `metrics.IndexerReleasesDropped` counts it. The severity is in the *indexed* case: the row loses its non-Latin title tokens, so it cannot be found by its own title, while a query like `"日本語のタイトル 2026"` normalises to `"2026"` and silently matches **every release published that year**. `indexarr/query`'s `errUnmatchable` guard does not fire, because `q.Text` is non-empty — it only catches a query that normalises to nothing at all. Fix: a normaliser in `pkg/release` that keeps non-ASCII letters, applied to both sides in the same change. That same change retires the NUL-welding limitation noted in `indexarr/query/doc.go` (`CleanTitle` strips control runes before `pkg/relindex/fts.go` can map them to spaces, so `"dune\x00matrix"` becomes the single term `"dunematrix"`). `pkg/release` change, no D1 task owns it.

- [ ] **The federated search is ids-only, so spec §6.2's `t=search&q=` fallback is unreachable from catalogarr.** `catalogarr/worker/search.BuildSearchRequest` never sets `SearchRequest.Text` — it builds `IDs` from tmdb/imdb/tvdb and nothing else — so an indexer that advertises a mode but none of the request's id parameters is skipped with `no supported id parameter for this request` rather than falling back to a title query. `indexarr/search.buildQuery` honours `Text` when it is set, so the Torznab facade (M6) and an interactive `Search` will exercise the fallback; closing it for catalogarr needs a resolved title, and the frozen payload has no field that carries one. Either add one with the next payload version, or accept that id-less indexers are unsearchable for automatic searches.
- [ ] **A usenet release offered by two indexers is not collapsed.** `indexarr/search.dedupeKey` implements exactly the two keys spec §6.2 names — infohash, then `(indexer, guid)` — and the second is per-indexer by construction, so cross-indexer collapse happens for torrents only. A title+size key was considered and rejected: a wrong dedupe silently *loses* releases, which is worse than a duplicate, and `pkg/decision` ranks duplicates sanely downstream. Revisit only with a key that cannot collide across genuinely different releases.
- [ ] **Spec §6.2's `alsoOn` provenance has nowhere to live.** When the merge collapses one release offered by several indexers, which indexers those were is discarded: neither `schema.Release` nor `commonv1.ReleaseInfo` has a field for it and both are frozen. `indexarr/search` logs `fetched` alongside `releases` so the collapse is at least visible in aggregate, but a specific release's other sources are not recoverable. Needs a payload field, which is a wire change no D1 task may make.
- [ ] **The query-limit window still under-reports, because an RSS poll counts nothing.** `indexarr/search` now projects `status.queriesInWindow` from a CAS timestamp ring at `<indexer-uid>.query` in `clustarr-indexer-limits`, which is what spec §5's KV table calls for and what makes the limit recoverable — an unwindowed counter would pass `spec.limits.queryLimit` once and skip the indexer for good, since nothing in the system lowers it. But `indexarr/worker/rss` makes up to four Torznab requests per poll and counts none of them, so a polled-and-searched indexer's real traffic is higher than the window says. The direction of the error is the safe one (the limit is reached later than it should be, never earlier) and the fix is one call from the poll path, which is D1-7's file. Two smaller notes on the same ring: the count saturates at 4096 entries per window, far above any realistic Torznab limit but not infinite; and a grab issued through `torrentURL`/`magnetURL`/`nzbURL` bypasses `rpc.indexarr.download` entirely, so the *grab* ring under-reports for the same structural reason.


**Phase E — transcode.**

- [ ] Define what a rescan should do when a **transcoded file's bytes legitimately changed on disk.** `k8s.Apply` always forces ownership, so a rescan re-applying `MediaFileSpec` would silently reclaim `sizeBytes`/`modTime`/`original` from catalogarr after the post-transcode handover. The rescan worker checks `Spec.Original` first and skips such files, which is right for Phase C but is not the final answer.

**Phase G — parity, lists and non-video inventory.**

- [ ] 8 of `MetadataProvider`'s 14 enum values have no client; they return `ErrProviderNotImplemented` → `Ready=Unknown` today, which is honest but not useful.
- [ ] The CRD's `ImageType` enum allows `poster;fanart;logo` while `pkg/metadata.ImageType` has nine values. Widen the enum if the other six are actually wanted.
- [ ] `preferLargest`'s 0.99 ratio separates a profile's sentinel size limit from an ordinary one. It is a chosen threshold with room to spare against all three real tables (movies 0.9995, series 0.9950, anime 0.9995); revisit if a real profile ever lands between.
- [ ] `WithSeasons(vals ...)` is a no-op at zero elements — **downgraded by the whole-branch review; probably a non-issue.** The list *can* be cleared: omitting the field releases it, and a release removes the field. Nothing else owns `status.seasons`, so omission is sufficient. It is not the same shape as `status.results`, which is the opposite problem — there the worker *wants* to declare an explicit empty list to mean "searched, found nothing" rather than release ownership. Confirm once and delete this item rather than spending a slot on it.

**Docs and spec passes (no code).**

- [ ] Spec §5's stream table has no `CLUSTARR_WORK_IMPORTARR` entry — the implemented topology wins; bring the table up to date.
- [ ] Decide whether spec §7's `k8sbridge` is still wanted or strike it; Phase C publishes directly through `events.Bus.Publish` with the real helpers. Strike `decision.Upgradable` (it is `quality.Profile.UpgradeDecision`) and `Target.FreeBytes` (deliberately dropped) from the same section.
- [ ] `docs/research/naming.md` §A6 flags its own summary as "unverified order" and omits two real comparator steps (episode count, indexer flags). `pkg/decision.Rank` was re-verified against `DownloadDecisionComparer.cs`/`ScoreFlags`; fix the note to match.
- [ ] `MetadataResponse.Results`' doc comment says "hits of a search request", which the episode listing slightly repurposes.
- [ ] `QualityProfile` is cluster-scoped yet movie/episode `Get` it with a namespace set. controller-runtime drops the namespace for root-scoped resources so it works, but it misleads the reader.

**Build and test hygiene.**

- [ ] **`go mod tidy -diff` is non-empty on the Phase C branch.** Phase C's new tests import `github.com/prometheus/client_model`, `k8s.io/apiextensions-apiserver` and `sigs.k8s.io/yaml` directly (`catalogarr/metadata/tieredcache_test.go`, `test/e2e/main_test.go`, `cmd/clustarr/rbac_markers_test.go`), but `go.mod` still lists all three as `// indirect`. No version changes and nothing added or removed — a direct/indirect reclassification only — so builds and tests are unaffected, but the gate fails. Fold it into the same `go mod tidy` that fixes the `mousetrap` `go.sum` entry above, serially, and never from a parallel agent.
- [ ] `deploymentsAvailable` in `test/e2e/main_test.go` **cannot see a Deployment scaled to zero** — Kubernetes reports `Available=True` for `replicas: 0`, so an overlay that mis-scales a service sails through the readiness gate (verified live: `tmdb-stub` at `replicas: 0` reported `Available=True Progressing=True`). The condition catches what matters — pods that cannot start, pull or pass probes — but anyone adding a "the right services are deployed" check must compare against a **roster**, not this condition. Owned by Phase H, or by whoever first needs that check.


## Self-review notes

Checked against both specs on 2026-09-18.

- **Spec coverage.** Amendment §A1 maps to A4, A5 and Phase C. §A2 maps to A1, A2, A3 and A7. §A3 maps to A6 and Phases D and G. Base spec §16's M1 through M6 map to Phases C through G. The library layer has no milestone of its own in the spec; it is Phase B here because M1 cannot start without it.
- **Type consistency.** `pipeline.Stage`, `pipeline.Entry` and `pipeline.Related` are used identically in A6 and Phase D. `k8s.PatchStatus`, the field managers and the condition helpers are quoted from the real source, not from memory.
- **Known gap, stated rather than hidden.** Phases C through G are work orders, not step-by-step tasks. Each needs its own plan written immediately before it runs. Writing them now would be guessing at interfaces that Phase B has not yet defined.
