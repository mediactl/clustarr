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

package probe

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/probestore"
	"github.com/mediactl/clustarr/pkg/records"
)

// Options configures the probe planner.
type Options struct {
	Bus     events.Bus
	Probes  *probestore.Store
	Version int32 // the wanted probe version; 0 is mediainfo.ProbeVersion
}

// Adapter is the probe planner.
type Adapter struct{ o Options }

// New is the probe planner over o.
func New(o Options) *Adapter { return &Adapter{o: o} }

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerProbe }

// Applies implements remediation.Planner: every file is probed.
func (*Adapter) Applies(*catalogv1alpha1.MediaFile) bool { return true }

// Gather takes a Swapping transcode block's output as the file to probe
// (loop spec §5.9 "The swap") and a graft block's state (a Swapping graft's
// bytes are probed as a swap's), stats the file through the I/O executor,
// and reads its probe record when the probe is due or a swap waits.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (mediafile.ProbeInput, error) {
	mf := v.File
	in := mediafile.ProbeInput{File: mf, Prev: v.Prev, Version: a.o.Version, Now: v.Now}
	if swap := v.Prev.Transcode; swap != nil && swap.Phase == catalogv1alpha1.TranscodePhaseSwapping && swap.Result != nil {
		if done, _ := transcodeplan.SwapIncorporated(swap, v.Prev); !done {
			in.Swap, in.SwapTag = swap, transcodeplan.Tag(swap.Profile, swap.ProfileHash)
		}
	}
	in.GraftSwapping, in.GraftInFlight = graftState(v.Prev)
	in.Path = mediafile.SwapTarget(mf, in.Swap)
	info, err := env.IO.Stat(ctx, in.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		in.Missing = err
		return in, nil
	case err != nil:
		return in, err // the executor marks timeouts, a busy pool and EIO Transient; EACCES is the planner's error
	}
	in.Info = info
	need, low := mediafile.NeedsProbeRecord(in)
	if !need {
		return in, nil
	}
	key := string(mf.UID)
	if in.Record, err = a.o.Probes.Get(ctx, key); err != nil {
		return in, remediation.Transient(err)
	}
	in.Read = true
	if low && env.Pacer != nil {
		// Reserve only when this pass would write a new low-lane request:
		// an incorporation or an outstanding request spends no slot (§4.7).
		if o := mediafile.PlanProbe(in, v.Prev.DeepCopy()); o.Request != nil && o.Request.Lane == events.PriorityLow {
			if at := env.Pacer.Reserve(records.PaceProbeLow, key); at.After(v.Now.Add(time.Second)) {
				in.PacedUntil = at.Add(mediafile.PacerJitter(key))
			}
		}
	}
	return in, nil
}

// Plan is mediafile.PlanProbe over what Gather read, its request and its
// republish turned into effects: the record write, then the task.
func (a *Adapter) Plan(v *remediation.View, in mediafile.ProbeInput, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	if in.File == nil {
		return remediation.Result{}, nil
	}
	in.Now = v.Now
	o := mediafile.PlanProbe(in, out)
	res := remediation.Result{Due: o.Due, Again: o.Again, Unpaced: o.Unpaced}
	if t := o.Takeover; t != nil {
		res.Main = &remediation.MainIntent{Path: t.Path, SizeBytes: t.SizeBytes, ModTime: t.ModTime, Swap: t.Swap}
	}
	for _, e := range o.Events {
		res.Events = append(res.Events, remediation.Event{Type: e.Type, Reason: e.Reason, Message: e.Note})
	}
	for _, s := range o.Records {
		res.Records = append(res.Records, remediation.RecordNote{State: s})
	}
	if o.Request != nil {
		rec := probestore.NewRequest(*o.Request, in.Record, v.Now.Time)
		value, err := json.Marshal(rec)
		if err != nil {
			return res, err
		}
		write := remediation.RecordWrite{
			Bucket: events.BucketProbes, Key: events.ProbeKey(rec.MediaFile.UID), Revision: in.Record.Revision, Value: value,
			Remediation: "probe", Lane: rec.Lane,
		}
		if rec.Lane == string(events.PriorityLow) {
			write.Pace = records.PaceProbeLow // the reservation Gather took is spent once the request lands
		}
		res.Effects = append(res.Effects, write)
		pub, err := publish(rec, v.Now)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, pub)
	}
	if o.Republish != nil {
		pub, err := publish(*o.Republish, v.Now)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, pub)
	}
	return res, nil
}

func publish(rec schema.ProbeRecord, now metav1.Time) (remediation.Publish, error) {
	subject, id, env, err := probestore.TaskMessage(rec, now.Time)
	if err != nil {
		return remediation.Publish{}, err
	}
	return remediation.Publish{Subject: subject, MsgID: id, ExpectStream: events.StreamWorkProbe, Envelope: env}, nil
}

// Copy is the probe's row of §3.5's table: probeHash, probedAt,
// probeVersion, mediaInfo, and the Probed and Ready conditions.
// status.transcode is the transcode planner's (F6.2); status.graft,
// graftTag and graftedAt the graft planner's (F7.2).
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.ProbeHash, into.ProbedAt, into.ProbeVersion = from.ProbeHash, from.ProbedAt.DeepCopy(), from.ProbeVersion
	into.MediaInfo = from.MediaInfo.DeepCopy()
	remediation.CopyConditions(from, into, catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileConditionReady)
}

// graftState reads the stored graft block: Swapping (a graft swapped the
// file: its probe is read again, as a transcode swap's is, so the pass the
// graft planner ends it in carries the spec takeover), and a graft in
// flight or swapping at all.
func graftState(prev *catalogv1alpha1.MediaFileStatus) (swapping, inFlight bool) {
	b := prev.Graft
	if b == nil {
		return false, false
	}
	switch b.Phase {
	case catalogv1alpha1.GraftPhaseSwapping:
		return true, true
	case catalogv1alpha1.GraftPhaseQueued, catalogv1alpha1.GraftPhaseRunning:
		return false, true
	}
	return false, false
}
