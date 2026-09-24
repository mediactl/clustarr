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

package worker

import (
	"context"
	"sync"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
	"github.com/mediactl/clustarr/squasharr/task"
)

// reporter publishes one delivery's status events in order. Progress arrives
// from Process's reporter goroutine, so publishes are serialised.
type reporter struct {
	mu       sync.Mutex
	s        *server
	t        task.Task
	delivery uint64
	seq      uint64
}

func (p *reporter) publish(ctx context.Context, ev task.StatusEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	ev.Job, ev.Attempt, ev.Delivery, ev.Seq = p.t.Job, p.t.Attempt, p.delivery, p.seq
	ev.Class = taskClass(p.t)
	ev.Pod, ev.Node, ev.At = p.s.o.PodName, p.s.o.Node, p.s.clock.Now().UTC()
	sch, data, err := schema.Encode(ev)
	if err != nil {
		return err
	}
	id := events.MsgIDForTranscodeEvent(p.t.Job.UID, p.t.Attempt, p.delivery, p.seq)
	env := &events.Envelope{
		ID: id, Type: "transcode.StatusEvent", Schema: sch,
		Source: "squasharr-worker@" + version.Version, Key: p.t.Job.Namespace + "/" + p.t.Job.Name,
		Time: ev.At, Data: data,
	}
	_, err = p.s.bus.Publish(ctx, events.WorkTranscodeResultSubject(p.t.Job.UID), env,
		events.WithMsgID(id), events.WithExpectStream(events.StreamWorkSquasharr))
	return err
}

// taskClass is the class t was dispatched to: BuildTask sets both Class and
// Profile.Hardware to it.
func taskClass(t task.Task) transcodev1alpha1.Hardware {
	if t.Class != "" {
		return transcodev1alpha1.Hardware(t.Class)
	}
	if t.Profile.Hardware != nil {
		return *t.Profile.Hardware
	}
	return ""
}

// reasonFor names a Process outcome: Process's own reason when it has one
// (SourceChanged, GPUUnavailable, GPUEncodeFailed), else the code's.
func reasonFor(out Outcome) task.Reason {
	if out.Reason != "" {
		return out.Reason
	}
	switch out.Code {
	case ExitInvalidSource:
		return task.ReasonInvalidSource
	case ExitVerifyFailed:
		return task.ReasonVerifyFailed
	default:
		return task.ReasonRetriable
	}
}
