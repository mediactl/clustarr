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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/probe"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// TestTheImportAgentAnswersBothProbeLanes (W4.7): the import domain
// subscribes the probe worker to both lanes on every replica, and a task on
// either lane is answered into clustarr-probes. The request is the one the
// remediation loop's probe planner writes: the requested record, then its
// task (probestore.NewRequest, TaskMessage).
func TestTheImportAgentAnswersBothProbeLanes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	top := events.Default().ForSingleNode()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, top))
	t.Cleanup(func() { _ = bus.Close() })
	root := t.TempDir()
	w := probe.NewWorker(bus, probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return &commonv1.MediaInfo{VideoCodec: "h264"}, nil, nil
	}), probe.Options{Pod: "import-agent-0", DataRoot: root})

	var runnables []manager.Runnable
	require.NoError(t, registerProbeWorkers(func(r manager.Runnable) error {
		runnables = append(runnables, r)
		return nil
	}, bus, top, w))
	require.Len(t, runnables, 2, "one subscription per lane")
	for _, r := range runnables {
		le, ok := r.(manager.LeaderElectionRunnable)
		require.True(t, ok, "a bare runnable waits for the lease")
		assert.False(t, le.NeedLeaderElection(), "every replica answers probes")
		go func() { _ = r.Start(ctx) }()
	}

	store := probestore.New(bus)
	for i, lane := range []events.Priority{events.PriorityHigh, events.PriorityLow} {
		path := filepath.Join(root, fmt.Sprintf("f%d.mkv", i))
		require.NoError(t, os.WriteFile(path, []byte("bytes"), 0o644))
		info, err := os.Stat(path)
		require.NoError(t, err)
		uid := fmt.Sprintf("uid-%d", i)
		now := time.Now()
		rec := probestore.NewRequest(probestore.Want{
			MediaFile: schema.Ref{Namespace: "media", Name: fmt.Sprintf("f%d", i), UID: uid}, Path: path,
			ProbeHash: mediainfo.ProbeHash(path, info.Size(), info.ModTime()), ProbeVersion: mediainfo.ProbeVersion, Lane: lane,
		}, probestore.Current{}, now)
		value, err := json.Marshal(rec)
		require.NoError(t, err)
		_, err = bus.KV(events.BucketProbes).Create(ctx, events.ProbeKey(uid), value)
		require.NoError(t, err)
		subject, id, env, err := probestore.TaskMessage(rec, now)
		require.NoError(t, err)
		_, err = bus.Publish(ctx, subject, env, events.WithMsgID(id))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			cur, err := store.Get(ctx, uid)
			return err == nil && cur.OK && cur.Record.State == schema.ProbeProbed
		}, 10*time.Second, 20*time.Millisecond, "the %s lane was never answered", lane)
		cur, err := store.Get(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, string(lane), cur.Record.Lane)
		assert.Equal(t, "import-agent-0", cur.Record.Prober, "the answer names its pod")
		require.NotNil(t, cur.Record.MediaInfo)
		assert.Equal(t, "h264", cur.Record.MediaInfo.VideoCodec)
	}
}

// TestTheProbeWorkerIsNamedForItsPod (W4.7): a probe record names the pod
// that answered it, $POD_NAME, else the host name.
func TestTheProbeWorkerIsNamedForItsPod(t *testing.T) {
	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	t.Setenv("POD_NAME", "import-agent-5c7d9")
	assert.Equal(t, "import-agent-5c7d9", podName())
	w := newProbeWorker(bus, nil, "/data")
	assert.Equal(t, "import-agent-5c7d9", w.Opts.Pod, "the domain's probe worker carries it")
	assert.Equal(t, "/data", w.Opts.DataRoot)
	t.Setenv("POD_NAME", "")
	host, err := os.Hostname()
	require.NoError(t, err)
	assert.Equal(t, host, podName())
}
