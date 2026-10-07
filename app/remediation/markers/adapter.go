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

package markers

import (
	"context"
	"encoding/json"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	catalogmarkers "github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/app/catalog/segmentplan"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/segments"
)

// Options configures the markers planner.
type Options struct{ Bus events.Bus }

// Adapter is the markers planner.
type Adapter struct {
	bus      events.Bus
	records  *catalogmarkers.Records
	segments *segments.Store
}

// New is the markers planner over o.Bus's clustarr-markers and
// clustarr-segments buckets.
func New(o Options) *Adapter {
	return &Adapter{bus: o.Bus, records: catalogmarkers.NewRecords(o.Bus), segments: segments.NewStore(o.Bus.KV(events.BucketSegments))}
}

// Input is what Gather read.
type Input struct {
	catalogmarkers.Input
	Episode *catalogv1alpha1.Episode
	Read    bool
}

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerMarkers }

// Applies: only movie and episode files have markers.
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	k := mf.Spec.MediaRef.Kind
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// Gather reads the file's segments record, and, when its markers or its
// analysis are due by the stored status, the query from the cache and its
// clustarr-markers record.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (Input, error) {
	in := Input{Read: true}
	uid := string(v.File.UID)
	rec, _, ok, err := a.segments.Get(ctx, uid)
	if err != nil {
		return in, remediation.Transient(err)
	}
	in.Segments, in.SegmentsOK = rec, ok
	due, _ := catalogmarkers.Due(v.File, v.Now.Time)
	if !due && !segmentplan.Due(v.File, v.Now.Time) {
		return in, nil
	}
	q, ep, err := query(ctx, env.Reader, v.File)
	if err != nil {
		return in, remediation.Transient(err)
	}
	in.Query, in.Episode = q, ep
	if !due || q.NotAsked != "" {
		return in, nil
	}
	if in.Record, in.RecordRev, in.RecordOK, err = a.records.Get(ctx, uid); err != nil {
		return in, remediation.Transient(err)
	}
	in.RecordRead = true
	if env.Pacer != nil {
		// Reserve only when this pass would write a new request: an
		// incorporation or an outstanding request spends no slot (§4.7).
		dry := a.input(v, in, v.Prev)
		if d := catalogmarkers.Plan(dry); d.Request != nil {
			if at := env.Pacer.Reserve(records.PaceMarkers, uid); at.After(v.Now.Add(time.Second)) {
				in.PacedUntil = at.Add(mediafile.PacerJitter(uid))
			}
		}
	}
	return in, nil
}

// input is the pure planner's input from what Gather read and the status s
// the pass plans over.
func (a *Adapter) input(v *remediation.View, in Input, s *catalogv1alpha1.MediaFileStatus) catalogmarkers.Input {
	pin := in.Input
	pin.Kind, pin.Now, pin.Prev = v.File.Spec.MediaRef.Kind, v.Now.Time, v.Prev.Markers
	pin.File = schema.Ref{Namespace: v.File.Namespace, Name: v.File.Name, UID: string(v.File.UID)}
	pin.ProbeHash, pin.Probed = s.ProbeHash, s.MediaInfo != nil
	if s.MediaInfo != nil {
		pin.DurationMs = s.MediaInfo.RuntimeMillis
	}
	return pin
}

// Plan is catalogmarkers.Plan over the draft's probe, its request and
// republish turned into effects (the record write, then the task), and the
// segment plan published when analysis is due.
func (a *Adapter) Plan(v *remediation.View, in Input, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	var res remediation.Result
	if !in.Read {
		return res, nil
	}
	pin := a.input(v, in, v.Draft)
	d := catalogmarkers.Plan(pin)
	out.Markers = d.Markers.DeepCopy()
	res.Due, res.Again, res.Unpaced = d.Due, d.Again, d.Unpaced
	for _, n := range d.Notes {
		res.Records = append(res.Records, remediation.RecordNote{State: n.State, TimedOut: n.TimedOut})
	}
	if d.Request != nil {
		value, err := json.Marshal(d.Request)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.RecordWrite{
			Bucket: events.BucketMarkers, Key: events.RecordKey(pin.File.UID),
			Revision: in.RecordRev, Value: value, Remediation: "markers", Lane: "none", Pace: records.PaceMarkers,
		})
	}
	for _, rec := range []*schema.MarkersRecord{d.Request, d.Republish} {
		if rec == nil {
			continue
		}
		subject, id, env, err := catalogmarkers.TaskMessage(*rec, v.Now.Time)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.Publish{Subject: subject, MsgID: id, ExpectStream: events.StreamWorkMarkers, Envelope: env})
	}
	planned := v.File.DeepCopy()
	planned.Status = *out
	if segmentplan.Due(planned, v.Now.Time) && (v.File.Spec.MediaRef.Kind == commonv1.MediaKindMovie || in.Episode != nil) {
		subject, id, env, at, err := segmentplan.PlanMessage(planned, in.Episode, v.Now.Time)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.Publish{Subject: subject, MsgID: id, Envelope: env, At: at})
	}
	return res, nil
}

// Copy is markers' row of §3.5's table.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Markers = from.Markers.DeepCopy()
}
