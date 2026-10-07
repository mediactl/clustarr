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
	"fmt"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/app/import/worker/probe"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec"
)

// probeConsumers are the import domain's two probe lanes (spec 2026-10-06 §6.5.1).
var probeConsumers = []string{events.ConsumerImportProbeHigh, events.ConsumerImportProbeLow}

// registerProbeWorkers subscribes w to both probe lanes on every replica, as
// the other importarr consumers are: a probe is answered by whichever import
// pod takes it.
func registerProbeWorkers(add func(manager.Runnable) error, bus events.Subscriber, top events.Topology, w *probe.Worker) error {
	for _, name := range probeConsumers {
		spec, ok := top.Consumer(name)
		if !ok {
			return fmt.Errorf("importarr: consumer %s missing from topology", name)
		}
		sub := spec.Subscription()
		if err := add(k8s.EveryReplica(func(ctx context.Context) error {
			stop, err := bus.Subscribe(ctx, sub, w.Handle)
			if err != nil {
				return fmt.Errorf("importarr: subscribe %s: %w", name, err)
			}
			defer stop()
			<-ctx.Done()
			return nil
		})); err != nil {
			return fmt.Errorf("importarr: add %s consumer: %w", name, err)
		}
	}
	return nil
}

// newProbeWorker is the process's probe worker. It answers through ffprobe
// until the native probe lands (spec §6.9 step 2), which replaces
// ffprobeexec.Prober{} here with native.New().
func newProbeWorker(bus events.Bus, dataRoot string) *probe.Worker {
	return probe.NewWorker(bus, ffprobeexec.Prober{}, probe.Options{Pod: podName(), DataRoot: dataRoot})
}

// podName is $POD_NAME, else the hostname (a pod's own name).
func podName() string {
	if n := os.Getenv("POD_NAME"); n != "" {
		return n
	}
	n, _ := os.Hostname()
	return n
}
