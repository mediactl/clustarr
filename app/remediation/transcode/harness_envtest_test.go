/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package transcode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	remediationmanager "github.com/mediactl/clustarr/app/remediation/manager"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	transcodemanager "github.com/mediactl/clustarr/app/transcode/manager"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/app/transcode/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
	"github.com/mediactl/clustarr/pkg/records"
)

// ns is the namespace every harness object lives in.
const ns = "default"

// harness is one manager running the MediaFile loop with the planners
// cmd/manager binds (remediationmanager.Planners), transcode-admission and
// the slim TranscodeProfile reconciler (transcodemanager.Register) on one
// admission ledger, over a membus with the shipped topology, with one fake
// clock for the loop and the ledger so the republish, unclaimed and
// lost-worker timers run in tests.
//
// The library is a temporary directory mounted at /data: the loop's I/O
// executor and the harness's probe agent read a logical /data path under
// it, as the manager and import pods read their /data mounts.
type harness struct {
	cfg     *rest.Config
	c       client.Client // uncached, field owner "transcode-test"
	raw     *membus.Bus
	bus     *harnessBus // what the loop and admission publish through
	ledger  *admission.Ledger
	clock   *clockwork.FakeClock
	dir     string
	profile *transcodev1alpha1.TranscodeProfile

	publishes atomic.Int32 // task publishes the loop made
	down      atomic.Bool  // task publishes fail while set (NATS down)
	failPurge atomic.Bool  // subject purges fail while set
	pokes     atomic.Int32
	swapTag   atomic.Value // string: the CLUSTARR_PROFILE an .mp4 probes with; "" is outputTag
}

// harnessBus counts and optionally fails the loop's task publishes, and
// fails purges while failPurge is set.
type harnessBus struct {
	events.Bus
	events.StreamAdmin
	h *harness
}

func (b *harnessBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	if strings.HasPrefix(subject, "clustarr.work.transcode.task.") {
		if b.h.down.Load() {
			return events.Receipt{}, errors.New("nats: connection closed")
		}
		b.h.publishes.Add(1)
	}
	return b.Bus.Publish(ctx, subject, e, opts...)
}

func (b *harnessBus) PurgeSubject(ctx context.Context, stream, subject string) error {
	if b.h.failPurge.Load() {
		return errors.New("nats: purge refused")
	}
	return b.StreamAdmin.PurgeSubject(ctx, stream, subject)
}

// harnessOptions shape a harness: the admission budget and window, and the
// objects to create before the manager starts.
type harnessOptions struct {
	slots  map[string]int32
	window int // 0: 32
	// seed creates objects before the manager starts, so the loop's
	// applied-status memo (§3.7) never sees a write it did not make.
	seed func(h *harness)
}

// newHarness starts the manager. Seeded objects exist when it returns.
func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	h := &harness{dir: t.TempDir(), clock: clockwork.NewFakeClockAt(time.Now().UTC().Truncate(time.Second))}
	window := o.window
	if window == 0 {
		window = 32
	}
	h.ledger = admission.NewLedger(admission.Options{Slots: o.slots, Window: window, Clock: h.clock})
	h.cfg = remediationtest.StartManager(t, func(mgr ctrl.Manager, bus events.Bus) error {
		h.raw = bus.(*membus.Bus)
		h.bus = &harnessBus{Bus: bus, StreamAdmin: h.raw, h: h}
		c, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
		if err != nil {
			return err
		}
		h.c = client.WithFieldOwner(c, "transcode-test")
		h.fixtures(t)
		if o.seed != nil {
			o.seed(h)
		}
		if err := remediation.RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
			return err
		}
		env := &remediation.Env{
			Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: h.bus,
			IO:    remediation.NewIOExecutorFuncs(4, h.stat, h.readDir),
			Pacer: records.NewPacer(records.DefaultRates(), time.Now), DataDir: h.dir,
		}
		d := remediationmanager.Deps{Env: env, Manager: mgr,
			Options: remediationmanager.Options{DataDir: h.dir, Ledger: h.ledger, Clock: h.clock.Now}}
		r, err := remediation.NewReconciler(mgr.GetClient(), env, mgr.GetEventRecorder(remediation.ControllerName),
			remediation.Config{Concurrency: 4}, remediationmanager.Planners(d), remediationmanager.Actuators(mgr, d)...)
		if err != nil {
			return err
		}
		r.Ledger, r.Clock = h.ledger, h.clock.Now
		if err := r.SetupWithManager(mgr); err != nil {
			return err
		}
		h.probeAgent(t)
		return transcodemanager.Register(mgr, h.bus, transcodemanager.Options{
			Options: k8s.Options{Namespace: ns}, Ledger: h.ledger,
			WorkerImage: "native:test", DataClaimName: "clustarr-data", DataDir: jobspec.LogicalDataRoot,
		})
	}).Config
	return h
}

// local maps a logical /data path into the harness's library.
func (h *harness) local(logical string) string {
	p, err := jobspec.LocalPath(h.dir, logical)
	if err != nil {
		return logical
	}
	return p
}

func (h *harness) stat(path string) (fs.FileInfo, error)       { return os.Stat(h.local(path)) }
func (h *harness) readDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(h.local(path)) }

// fixtures creates the movies RootFolder, the QualityProfile hd and the
// default TranscodeProfile hevc (auto hardware: cpu, with no GPU node).
func (h *harness) fixtures(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, h.c.Create(ctx, &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "movies"},
		Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie}}))
	remediationtest.MustQualityProfile(t, ctx, h.c, "hd")
	h.profile = &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc"},
		Spec: transcodev1alpha1.TranscodeProfileSpec{Default: true}}
	require.NoError(t, h.c.Create(ctx, h.profile))
}

// h264Summary is what the harness's prober reads of every source: 8-bit
// 1080p H.264, two hours, English AC-3 5.1 -- a file the standard encodes.
func h264Summary() *commonv1.MediaInfo {
	return &commonv1.MediaInfo{
		Container: "matroska,webm", VideoCodec: "h264", VideoProfile: "High", PixelFormat: "yuv420p",
		VideoBitDepth: 8, Width: 1920, Height: 1080, FpsMilli: 23976, Hdr: commonv1.HdrFormatNone,
		RuntimeMillis: 2 * 60 * 60 * 1000,
		Audio:         []commonv1.AudioStream{{Index: 1, Codec: "ac3", Channels: 6, ChannelLayout: "5.1(side)", Language: "eng", Default: true}},
	}
}

// probeAgent answers the loop's probe tasks as the import domain's probe
// worker does (probe.Worker.Handle: superseded, stat, probe, answer),
// reading the logical path under the harness's library. A file whose name
// ends ".mp4" probes as the transcode of the tag in h.outputTag.
func (h *harness) probeAgent(t *testing.T) {
	t.Helper()
	store := probestore.New(h.raw)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	handle := func(ctx context.Context, m events.Message) error {
		var pt schema.ProbeTask
		if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &pt); err != nil {
			return events.Discard("undecodable", err)
		}
		if sup, err := store.Superseded(ctx, pt); err != nil || sup {
			return err
		}
		rec := schema.ProbeRecord{Path: pt.Path, ProbeHash: pt.ProbeHash, ProbeVersion: mediainfo.ProbeVersion, Prober: "transcode-test"}
		fi, err := os.Stat(h.local(pt.Path))
		if err != nil {
			rec.State, rec.ProbedAt, rec.Transient, rec.Failure = schema.ProbeFailed, time.Now().UTC(), true, err.Error()
		} else {
			rec.ProbeHash = mediainfo.ProbeHash(pt.Path, fi.Size(), fi.ModTime())
			mi := h264Summary()
			if strings.HasSuffix(pt.Path, ".mp4") {
				mi = &commonv1.MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "hevc", Width: 1920, Height: 1080,
					RuntimeMillis: 2 * 60 * 60 * 1000, TranscodeProfile: h.outputTag(t)}
			}
			rec.State, rec.ProbedAt, rec.MediaInfo = schema.ProbeProbed, time.Now().UTC(), mi
		}
		_, err = store.Answer(ctx, pt, rec)
		return err
	}
	top := events.Default()
	for _, name := range []string{events.ConsumerImportProbeHigh, events.ConsumerImportProbeLow} {
		spec, ok := top.Consumer(name)
		require.True(t, ok, name)
		stop, err := h.raw.Subscribe(ctx, spec.Subscription(), handle)
		require.NoError(t, err)
		t.Cleanup(stop)
	}
}

// outputTag is the CLUSTARR_PROFILE a transcode of the harness's profile
// writes into its output: the profile's tag at its current hash.
func (h *harness) outputTag(t *testing.T) string {
	t.Helper()
	if tag, _ := h.swapTag.Load().(string); tag != "" {
		return tag
	}
	return transcodeplan.Tag(h.profile.Name, jobspec.ProfileHash(h.profile.Spec))
}

// write writes the source bytes of a movie name under the library and
// returns its logical path.
func (h *harness) write(t *testing.T, name string) string {
	t.Helper()
	logical := "/data/media/movies/" + name + "/" + name + ".mkv"
	require.NoError(t, os.MkdirAll(filepath.Dir(h.local(logical)), 0o755))
	require.NoError(t, os.WriteFile(h.local(logical), []byte("source "+name), 0o644))
	// A whole second, as spec.modTime carries it: the probe hash of the
	// seeded status is then the one the probe planner computes from a stat.
	mt := time.Now().Truncate(time.Second)
	require.NoError(t, os.Chtimes(h.local(logical), mt, mt))
	return logical
}

// create creates a movie, its bytes and its MediaFile as the import
// domain does (spec only), and returns the file.
func (h *harness) create(t *testing.T, name string) *catalogv1alpha1.MediaFile {
	t.Helper()
	ctx := context.Background()
	logical := h.write(t, name)
	fi, err := os.Stat(h.local(logical))
	require.NoError(t, err)
	remediationtest.MustMovie(t, ctx, h.c, ns, name, "hd")
	remediationtest.ImportCreatesMediaFileFor(t, ctx, h.c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, logical, fi.Size(), fi.ModTime(), remediationtest.Bluray1080p)
	mf := &catalogv1alpha1.MediaFile{}
	require.NoError(t, h.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, mf))
	return mf
}

// file creates a movie file on a running harness and returns it once its
// probe is current.
func (h *harness) file(t *testing.T, name string) *catalogv1alpha1.MediaFile {
	t.Helper()
	mf := h.create(t, name)
	return waitFile(t, h.c, mf, "probed", func(m *catalogv1alpha1.MediaFile) bool { return m.ProbeCurrent() })
}

// seeded creates a movie file whose status is already in its steady state
// -- probed and current for the bytes on disk, with a transcode block in
// phase at seq on the harness's cpu pool (phase "": a probed backlog file
// with no block). Call it from harnessOptions.seed, before the manager
// starts.
func (h *harness) seeded(t *testing.T, name string, phase catalogv1alpha1.TranscodePhase, seq int64) *catalogv1alpha1.MediaFile {
	t.Helper()
	mf := h.create(t, name)
	now := metav1.NewTime(h.clock.Now())
	mf.Status = catalogv1alpha1.MediaFileStatus{
		ProbeHash: mediainfo.ProbeHash(mf.Spec.Path, mf.Spec.SizeBytes, mf.Spec.ModTime.Time), ProbedAt: &now,
		ProbeVersion: mediainfo.ProbeVersion, MediaInfo: h264Summary(),
		Conditions: []metav1.Condition{
			{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed", LastTransitionTime: now},
		},
	}
	seedStatus(t, h.c, mf, h.blockFor(t, mf, phase, seq))
	require.NoError(t, h.c.Get(context.Background(), client.ObjectKeyFromObject(mf), mf))
	return mf
}

// blockFor is the steady-state status of mf dispatched at seq to the
// harness's cpu pool, in phase.
func (h *harness) blockFor(t *testing.T, mf *catalogv1alpha1.MediaFile, phase catalogv1alpha1.TranscodePhase, seq int64) catalogv1alpha1.MediaFileStatus {
	t.Helper()
	tp := h.currentProfile(t)
	hash := jobspec.ProfileHash(tp.Spec)
	st := mf.Status.DeepCopy()
	if phase == "" {
		st.Transcode = nil
		return *st
	}
	st.LastSeq = seq
	st.Transcode = &catalogv1alpha1.TranscodeState{Phase: phase, Profile: tp.Name, ProfileHash: hash,
		ProfileTag: transcodeplan.Tag(tp.Name, hash), ProbeHash: st.ProbeHash, Class: "cpu", Attempts: 1, Hardware: "auto",
		Priority: tp.Spec.Priority,
		Pool:     pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: transcodev1alpha1.HardwareCPU}),
		Plan:     &catalogv1alpha1.TranscodePlan{Mode: catalogv1alpha1.TranscodeModeTranscode, Encoder: "libx265", PlanHash: "p"},
		Dispatch: &catalogv1alpha1.Dispatch{Seq: seq, DispatchedAt: metav1.NewTime(h.clock.Now())}}
	if phase == catalogv1alpha1.TranscodePhaseRunning {
		st.Transcode.WorkerPod, st.Transcode.StartedAt = "pool-0", new(metav1.NewTime(h.clock.Now()))
	}
	return *st
}

// currentProfile reads the harness's TranscodeProfile.
func (h *harness) currentProfile(t *testing.T) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, h.c.Get(context.Background(), client.ObjectKeyFromObject(h.profile), &tp))
	return &tp
}

// cpuPool is the harness profile's cpu pool key.
func (h *harness) cpuPool(t *testing.T) pool.Key {
	t.Helper()
	tp := h.currentProfile(t)
	return pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: transcodev1alpha1.HardwareCPU}
}

// seedStatus writes st as the loop's own manager would have, so the seed is
// an object already in its steady state (CLAUDE.md: tests miss releases
// unless they act on an object that already has status).
func seedStatus(t *testing.T, c client.Client, mf *catalogv1alpha1.MediaFile, st catalogv1alpha1.MediaFileStatus) {
	t.Helper()
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	ac := &catalogac.MediaFileStatusApplyConfiguration{}
	require.NoError(t, json.Unmarshal(raw, ac))
	_, err = k8s.PatchStatus(context.Background(), c, k8s.ManagerCatalog, catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(ac))
	require.NoError(t, err)
}

// poke wakes the file's pass (a user label's change), after the test moved
// the fake clock.
func (h *harness) poke(t *testing.T, mf *catalogv1alpha1.MediaFile) {
	t.Helper()
	patch := fmt.Appendf(nil, `{"metadata":{"labels":{"test.clustarr.io/poke":"%d"}}}`, h.pokes.Add(1))
	require.NoError(t, h.c.Patch(context.Background(), mf.DeepCopy(), client.RawPatch(types.MergePatchType, patch)))
}

// annotate sets one annotation on mf.
func (h *harness) annotate(t *testing.T, mf *catalogv1alpha1.MediaFile, key, v string) {
	t.Helper()
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`, key, v)
	require.NoError(t, h.c.Patch(context.Background(), mf.DeepCopy(), client.RawPatch(types.MergePatchType, patch)))
}

// worker serves the profile's class pool with a stub Process, through the
// real worker.Serve, so lease, record and answer are the production code's.
func (h *harness) worker(t *testing.T, class string, process func(context.Context, task.Task, worker.Options) worker.Outcome) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	tp := h.currentProfile(t)
	go func() {
		defer close(done)
		_ = worker.Serve(ctx, h.raw, worker.ServeOptions{ProfileUID: string(tp.UID), Class: class, Node: "node-a",
			Options: worker.Options{DataDir: h.dir, PodName: "pool-0"}, Process: process, HeldRetry: 100 * time.Millisecond,
			Leases: h.raw.KV(events.BucketTranscodeLeases), Records: h.raw.KV(events.BucketTranscodes)})
	}()
}

// storedTasks counts the task subjects the stream holds for a file.
func (h *harness) storedTasks(t *testing.T, uid types.UID) int {
	t.Helper()
	subs, err := h.raw.Subjects(context.Background(), events.StreamWorkTranscode, events.WorkTranscodeTaskSubjectAnyPool(string(uid)))
	require.NoError(t, err)
	return len(subs)
}

// record reads the file's clustarr-transcodes record.
func (h *harness) record(t *testing.T, uid types.UID) (*task.Record, bool) {
	t.Helper()
	rec, _, ok, err := task.Requests(h.raw.KV(events.BucketTranscodes)).Get(context.Background(), events.RecordKey(string(uid)))
	require.NoError(t, err)
	return rec, ok
}

// refOf is mf as a record names it.
func refOf(mf *catalogv1alpha1.MediaFile) schema.Ref {
	return schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)}
}

// waitFile reads mf until cond holds, failing after a minute.
func waitFile(t *testing.T, c client.Client, mf *catalogv1alpha1.MediaFile, what string,
	cond func(*catalogv1alpha1.MediaFile) bool,
) *catalogv1alpha1.MediaFile {
	t.Helper()
	got := &catalogv1alpha1.MediaFile{}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if c.Get(context.Background(), client.ObjectKeyFromObject(mf), got) == nil && cond(got) {
			return got
		}
		if time.Now().After(deadline) {
			last, _ := json.Marshal(got.Status.Transcode)
			t.Fatalf("%s: %s never held; status.transcode is %s, lastSeq %d", mf.Name, what, last, got.Status.LastSeq)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// phase reports a file whose block is in p.
func phase(p catalogv1alpha1.TranscodePhase) func(*catalogv1alpha1.MediaFile) bool {
	return func(m *catalogv1alpha1.MediaFile) bool { return m.Status.Transcode != nil && m.Status.Transcode.Phase == p }
}

// requested reports a record requested at seq.
func requested(seq int64) func(*task.Record, bool) bool {
	return func(rec *task.Record, ok bool) bool { return ok && rec.Seq == seq && rec.State == records.StateRequested }
}
