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

// Package probetest is test support for every package whose tests need the
// import domain's probe worker answering the probe queue: the MediaFile
// reconciler's envtests above all. Production code never imports it.
package probetest

import (
	"context"
	"errors"
	"testing"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/probe"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// ProberFunc adapts a probe function to mediainfo.Prober; its ProbeAudio fails.
type ProberFunc func(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)

// Probe calls f.
func (f ProberFunc) Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return f(ctx, path)
}

// ProbeAudio is not faked.
func (ProberFunc) ProbeAudio(context.Context, string) (mediainfo.AudioProbe, error) {
	return mediainfo.AudioProbe{}, errors.New("probetest: ProbeAudio is not faked")
}

// Agent is the real probe worker subscribed to both probe lanes of a test bus.
type Agent struct {
	Worker *probe.Worker
	store  *probestore.Store
}

// Start subscribes the real probe worker, answering through p, to both
// probe durables of bus until t ends. bus must carry events.Default()'s probe
// stream and bucket. Every absolute path is under its data root ("/").
func Start(t testing.TB, bus events.Bus, p mediainfo.Prober) *Agent {
	t.Helper()
	w := probe.NewWorker(bus, p, probe.Options{Pod: "probetest", DataRoot: "/"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	top := events.Default()
	for _, name := range []string{events.ConsumerImportProbeHigh, events.ConsumerImportProbeLow} {
		spec, ok := top.Consumer(name)
		if !ok {
			t.Fatalf("probetest: consumer %s is not in the default topology", name)
		}
		stop, err := bus.Subscribe(ctx, spec.Subscription(), w.Handle)
		if err != nil {
			t.Fatalf("probetest: subscribe %s: %v", name, err)
		}
		t.Cleanup(stop)
	}
	return &Agent{Worker: w, store: probestore.New(bus)}
}

// Wait blocks until uid's record answers request seq (probed or failed at
// seq or later) and returns it, failing t after ten seconds.
func (a *Agent) Wait(t testing.TB, uid string, seq int64) schema.ProbeRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := a.store.Get(context.Background(), uid)
		if err != nil {
			t.Fatalf("probetest: read the record of %s: %v", uid, err)
		}
		if cur.OK && cur.Record.Seq >= seq && cur.Record.State != schema.ProbeRequested {
			return cur.Record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("probetest: the probe of %s (request %d) was never answered", uid, seq)
	return schema.ProbeRecord{}
}
