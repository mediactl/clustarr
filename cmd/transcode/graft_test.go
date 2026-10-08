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

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/test/nativetest"
	"github.com/mediactl/clustarr/test/starttest"
)

// graftKV is clustarr-grafts on an embedded JetStream server with the
// default topology, and the server's URL for the pod to dial.
func graftKV(t *testing.T) (string, events.KV) {
	t.Helper()
	url := starttest.EmbeddedNATS(t)
	ensureTopology(t, url)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	return url, bus.KV(events.BucketGrafts)
}

var graftFile = schema.Ref{Namespace: "media", Name: "pb", UID: "mf-uid"}

// putGraftRecord writes the loop's record of the file at seq in state, as the
// graft planner does before it creates the Job (loop spec §4.7).
func putGraftRecord(t *testing.T, kv events.KV, seq int64, state string) {
	t.Helper()
	b, err := json.Marshal(grafttask.Record{RecordHeader: schema.RecordHeader{Schema: grafttask.RecordSchema,
		MediaFile: graftFile, Seq: seq, State: state, RequestedAt: time.Now()}})
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), events.RecordKey(graftFile.UID), b)
	require.NoError(t, err)
}

func readGraftRecord(t *testing.T, kv events.KV) grafttask.Record {
	t.Helper()
	e, err := kv.Get(context.Background(), events.RecordKey(graftFile.UID))
	require.NoError(t, err)
	var got grafttask.Record
	require.NoError(t, json.Unmarshal(e.Value, &got))
	return got
}

// graftTaskJSON is a --graft-task the loop issues for seq; its target does
// not exist under the pod's data dir.
func graftTaskJSON(t *testing.T, seq int64) string {
	t.Helper()
	b, err := json.Marshal(grafttask.Task{Schema: grafttask.TaskSchema, File: graftFile, Seq: seq,
		Target: "/data/media/movies/pb.mkv", TargetProbeHash: "h1", Root: "/data/media/movies",
		Donor: "/data/media/movies/.clustarr/donors/u/pb.mka", Language: "en", Anchor: "ja",
		Inputs: grafttask.Inputs{TargetProbeHash: "h1", Language: "en", Anchor: "ja"}})
	require.NoError(t, err)
	return string(b)
}

// TestAGraftTaskAnswersInTheRecordsBucket (loop spec §4.12 "Pod", D9): the
// pod claims its record, runs, and answers there under its own name; the
// target is gone, so the answer is TargetChanged and the pod exits 1.
func TestAGraftTaskAnswersInTheRecordsBucket(t *testing.T) {
	nativetest.FFmpeg(t, ffruntime.Needs{}) // past the gate, graft.Run judges the task
	url, kv := graftKV(t)
	putGraftRecord(t, kv, 7, records.StateRequested)
	code := run([]string{"--graft-task", graftTaskJSON(t, 7), "--data-dir", t.TempDir()},
		env(map[string]string{"NATS_URL": url, "POD_NAME": "pb-graft-7-abcde"}))
	assert.Equal(t, 1, code)
	got := readGraftRecord(t, kv)
	assert.Equal(t, int64(7), got.Seq)
	assert.Equal(t, records.StateAnswered, got.State)
	require.NotNil(t, got.Answer)
	assert.Equal(t, grafttask.PhaseFailed, got.Answer.Phase)
	assert.Equal(t, grafttask.ReasonTargetChanged, got.Answer.Reason, got.Answer.Message)
	assert.Equal(t, "pb-graft-7-abcde", got.Writer)
	assert.Equal(t, grafttask.Inputs{TargetProbeHash: "h1", Language: "en", Anchor: "ja"}, got.Inputs,
		"the answer carries the dispatch's inputs")
}

// A task whose record has moved on -- the loop requested seq 8 -- is
// superseded: the pod runs nothing, writes nothing and exits 0.
func TestASupersededGraftTaskExitsWithoutRunning(t *testing.T) {
	url, kv := graftKV(t)
	putGraftRecord(t, kv, 8, records.StateRequested)
	before, err := kv.Get(context.Background(), events.RecordKey(graftFile.UID))
	require.NoError(t, err)
	code := run([]string{"--graft-task", graftTaskJSON(t, 7), "--data-dir", t.TempDir()},
		env(map[string]string{"NATS_URL": url, "POD_NAME": "pb-graft-7-abcde"}))
	assert.Equal(t, 0, code)
	got := readGraftRecord(t, kv)
	assert.Equal(t, int64(8), got.Seq)
	assert.Equal(t, records.StateRequested, got.State, "a superseded task writes nothing")
	after, err := kv.Get(context.Background(), events.RecordKey(graftFile.UID))
	require.NoError(t, err)
	assert.Equal(t, before.Revision, after.Revision, "not even a claim")
}

// A graft pod answers over NATS under its pod's name: without either it is
// misconfigured, before it dials anything.
func TestAGraftPodNeedsNATSAndItsName(t *testing.T) {
	task := graftTaskJSON(t, 7)
	for name, e := range map[string]map[string]string{
		"neither":     nil,
		"no POD_NAME": {"NATS_URL": "nats://127.0.0.1:1"},
		"no NATS_URL": {"POD_NAME": "pb-graft-7-abcde"},
	} {
		assert.Equal(t, jobspec.WorkerExitMisconfigured, run([]string{"--graft-task", task}, env(e)), name)
	}
}

// The graft's result rides clustarr-grafts (loop spec §4.12, ADR-0016 D9):
// the pod reads and writes no termination message, so the flag is gone and
// passing it is a flag error.
func TestTheTerminationLogFlagIsGone(t *testing.T) {
	assert.Nil(t, newFlags().fs.Lookup("termination-log"), "no --termination-log flag")
	assert.Equal(t, jobspec.WorkerExitMisconfigured, run([]string{"--graft-task", "{}", "--termination-log", "/x"}, env(nil)),
		"the result rides clustarr-grafts now (§4.13)")
}

// A graft pod loads FFmpeg through the runtime gate before graft.Run: with
// FFmpeg 9 and the shim loadable, an empty task reaches graft.Run, which
// judges it InvalidTask, never the gate's ReasonError (spec 2026-10-06 §7.4;
// the pod answers in clustarr-grafts, loop spec §4.4).
func TestAGraftLoadsFFmpegThroughTheGate(t *testing.T) {
	nativetest.FFmpeg(t, ffruntime.Needs{})
	res := graftWithFFmpeg(context.Background(), grafttask.Task{}, t.TempDir(), slog.New(slog.DiscardHandler))
	assert.Equal(t, grafttask.PhaseFailed, res.Phase)
	assert.Equal(t, grafttask.ReasonInvalidTask, res.Reason, "the gate passed and graft.Run judged the task: %s", res.Message)
}
