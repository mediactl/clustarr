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

package admission_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/test/starttest"
)

// TestSweepWithdrawsADeletedFilesTaskAfterRestart (§5.17, §5.6 "Sweep"): a
// task whose MediaFile UID no live file has is withdrawn (record at
// MaxInt64, lease cancelled, subject purged) by the first pass of a fresh
// manager; a live file's task that is neither in flight nor granted is
// purged; an in-flight file's task is its own and stays.
func TestSweepWithdrawsADeletedFilesTaskAfterRestart(t *testing.T) {
	const ns = "sweep"
	c := requireEnvtest(t, ns)
	ctx := t.Context()
	nc, err := nats.Connect(starttest.EmbeddedNATS(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))

	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc"}}
	require.NoError(t, c.Create(ctx, tp))
	t.Cleanup(func() { _ = c.Delete(context.Background(), tp) })
	publish := func(uid string) string {
		subject := events.WorkTranscodeTaskSubject(string(tp.UID), "cpu", uid)
		_, err := bus.Publish(ctx, subject,
			&events.Envelope{Type: "transcode.Task", Schema: "transcode.Task.v2", Data: []byte(`{}`)},
			events.WithMsgID(events.MsgIDForTranscodeTask(uid, 1)))
		require.NoError(t, err)
		return subject
	}
	publish("deleted-file-uid")
	idle := seed(t, c, ns, "live-idle", catalogv1alpha1.MediaFileStatus{ProbeHash: "p"})
	publish(string(idle.UID))
	running := seed(t, c, ns, "live-running", block(catalogv1alpha1.TranscodePhaseRunning, 1, "cpu"))
	kept := publish(string(running.UID))

	admin := events.StreamAdmin(bus)
	subjects, err := admin.Subjects(ctx, events.StreamWorkTranscode, "clustarr.work.transcode.task.>")
	require.NoError(t, err)
	require.Len(t, subjects, 3, "the three tasks are stored before the manager starts")

	ledger := admission.NewLedger(admission.Options{Slots: map[string]int32{"cpu": 1}, Window: 32})
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme: k8s.MustNewScheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, mfindex.Register(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, (&admission.Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Ledger: ledger, Bus: bus, Admin: admin,
		Namespaces: []string{ns}, Pool: pool.Config{Namespace: ns},
		Withdrawal: task.Withdrawal{Records: task.Requests(bus.KV(events.BucketTranscodes)),
			Leases: bus.KV(events.BucketTranscodeLeases), Admin: admin},
	}).SetupWithManager(mgr))
	mctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Start(mctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })

	require.Eventually(t, func() bool {
		subs, err := admin.Subjects(ctx, events.StreamWorkTranscode, "clustarr.work.transcode.task.>")
		return err == nil && len(subs) == 1 && subs[0] == kept
	}, 30*time.Second, 200*time.Millisecond, "only the in-flight file's task is left")
	assert.True(t, ledger.Rebuilt())

	e, err := bus.KV(events.BucketTranscodes).Get(ctx, events.RecordKey("deleted-file-uid"))
	require.NoError(t, err)
	var rec task.Record
	require.NoError(t, json.Unmarshal(e.Value, &rec))
	assert.Equal(t, records.StateWithdrawn, rec.State)
	assert.Equal(t, int64(math.MaxInt64), rec.Seq)
	lease, err := bus.KV(events.BucketTranscodeLeases).Get(ctx, events.TranscodeLeaseKey("deleted-file-uid"))
	require.NoError(t, err)
	var l task.Lease
	require.NoError(t, json.Unmarshal(lease.Value, &l))
	assert.Equal(t, task.LeaseCancelled, l.State)
	assert.Equal(t, int64(math.MaxInt64), l.Seq)

	_, err = bus.KV(events.BucketTranscodes).Get(ctx, events.RecordKey(string(idle.UID)))
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "a live file's stray task is purged, its record left alone")
}
