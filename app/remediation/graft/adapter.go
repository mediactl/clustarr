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

package graft

import (
	"context"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Options configures the graft planner.
type Options struct {
	// Pool is the cpu pool's pod template a graft Job runs on.
	Pool pool.Config
	// Ledger is the admission ledger, through transcodeplan.Admission: a
	// graft-slot grant is taken (TakeGrant) and confirmed by the dispatch;
	// nil grants nothing, so nothing is dispatched.
	Ledger transcodeplan.Admission
}

// Adapter is the graft planner.
type Adapter struct {
	o          Options
	bus        events.Bus
	grafts     *records.Requester[*grafttask.Record]
	transcodes *records.Requester[*task.Record]
}

// New is the graft planner over bus's clustarr-grafts and
// clustarr-transcodes.
func New(bus events.Bus, o Options) *Adapter {
	return &Adapter{
		o: o, bus: bus, grafts: grafttask.NewRequester(bus),
		transcodes: task.Requests(bus.KV(events.BucketTranscodes)),
	}
}

// In is what Gather read.
type In struct {
	Plan graftplan.Input
	// Revision is the clustarr-grafts revision Plan.Record was read at; 0
	// absent (a request creates the key).
	Revision uint64
	// Read reports that Gather ran.
	Read bool
}

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerGraft }

// Applies implements remediation.Planner: only a movie's or an episode's
// file is grafted.
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	k := mf.Spec.MediaRef.Kind
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// Sources: S18 (clustarr-grafts' answers, §4.9) and S15 (a graft Job
// finishing or going, §3.3).
func (a *Adapter) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	var srcs []remediation.Source
	if a.bus != nil {
		srcs = append(srcs, remediation.Source{
			Name: "S18/clustarr-grafts", Raw: recordsource.New(a.bus, events.BucketGrafts, remediation.FileKey),
		})
	}
	return append(srcs, remediation.Source{Name: "S15/graft-jobs", Raw: remediation.GraftJobSource(mgr.GetCache())}), nil
}

// Gather reads the file's item, its audio policy and the RootFolder holding
// the file from the cache; takes a graft-slot grant when the ledger holds
// one for the file; reads the clustarr-grafts record while a standalone
// graft is in flight, a grant is held or a withdrawn dispatch's fact can
// still land (§4.10); the transcode record while a joined graft waits for
// it; and the standalone graft's Job.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (In, error) {
	in := In{Read: true}
	mf, b := v.File, v.Prev.Graft
	item, root, bin, err := GatherItem(ctx, env.Reader, mf)
	if err != nil {
		return in, remediation.Transient(err)
	}
	in.Plan.Item, in.Plan.Root, in.Plan.RecycleBin = item, root, bin
	if a.o.Ledger != nil && v.Ledger.Grant == transcodeplan.GrantGraftSlot {
		snap, ok := a.o.Ledger.TakeGrant(mf.UID)
		in.Plan.Granted = ok && snap.Grant == transcodeplan.GrantGraftSlot
	}
	key := events.RecordKey(string(mf.UID))
	withdrawn := b != nil && b.Dispatch != nil && b.Dispatch.Withdrawn && v.Now.Time.Before(b.Dispatch.DispatchedAt.Add(graftplan.FactWindow))
	if graftplan.Standalone(b) || in.Plan.Granted || withdrawn {
		rec, rev, found, err := a.grafts.Get(ctx, key)
		if err != nil {
			return in, remediation.Transient(err)
		}
		in.Revision = rev
		if found {
			in.Plan.Record = rec
		}
	}
	if b != nil && b.Phase == catalogv1alpha1.GraftPhaseRunning && b.Reason == graftplan.ReasonJoinedTranscode {
		rec, _, found, err := a.transcodes.Get(ctx, key)
		if err != nil {
			return in, remediation.Transient(err)
		}
		if found {
			in.Plan.Transcode = rec
		}
	}
	if graftplan.Standalone(b) && b.JobName != "" {
		var j batchv1.Job
		switch err := env.Reader.Get(ctx, client.ObjectKey{Namespace: mf.Namespace, Name: b.JobName}, &j); {
		case err == nil:
			done, at, msg := pool.JobFinished(&j)
			in.Plan.Job = &graftplan.Job{Name: j.Name, Finished: done, FinishedAt: at, Message: msg}
		case !apierrors.IsNotFound(err):
			return in, remediation.Transient(fmt.Errorf("graft: get Job %s: %w", b.JobName, err))
		}
	}
	return in, nil
}

// Plan is graftplan.Plan over the draft, its effects mapped onto the loop's
// in §3.8's order: the record write, the Job, the ledger's confirmation.
func (a *Adapter) Plan(v *remediation.View, in In, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	if !in.Read {
		return remediation.Result{}, nil
	}
	gv := graftplan.View{File: v.File, Prev: v.Prev, Draft: v.Draft, Now: v.Now, ProbeCurrent: v.ProbeCurrent(), Issue: v.Issue}
	d := graftplan.Plan(gv, in.Plan, out)
	res := remediation.Result{Due: d.Due, Unpaced: in.Plan.Granted || d.Unpaced}
	key := events.RecordKey(string(v.File.UID))
	for _, e := range d.Effects {
		switch e.Kind {
		case graftplan.EffectRequest:
			val, err := json.Marshal(e.Record)
			if err != nil {
				return res, err
			}
			res.Effects = append(res.Effects, remediation.RecordWrite{
				Bucket: events.BucketGrafts, Key: key, Revision: in.Revision, Value: val, Remediation: grafttask.Spec.Remediation,
			})
		case graftplan.EffectWithdraw:
			w := &grafttask.Record{RecordHeader: schema.RecordHeader{
				Schema: grafttask.RecordSchema, MediaFile: schema.Ref{Namespace: v.File.Namespace, Name: v.File.Name, UID: string(v.File.UID)},
				Seq: e.Seq, State: records.StateWithdrawn, RequestedAt: v.Now.UTC(),
			}}
			if r := in.Plan.Record; r != nil {
				w.Inputs, w.RequestedAt = r.Inputs, r.RequestedAt
			}
			val, err := json.Marshal(w)
			if err != nil {
				return res, err
			}
			res.Effects = append(res.Effects, remediation.RecordWrite{Bucket: events.BucketGrafts, Key: key, Revision: in.Revision, Value: val})
		case graftplan.EffectCreateJob:
			job, err := Job(a.o.Pool, v.File, *e.Task, e.Job)
			if err != nil {
				return res, err
			}
			res.Effects = append(res.Effects, remediation.CreateGraftJob{Job: job})
		case graftplan.EffectDeleteJob:
			if e.Job != "" {
				res.Effects = append(res.Effects, remediation.DeleteGraftJob{Namespace: v.File.Namespace, Name: e.Job})
			}
		case graftplan.EffectConfirm:
			if in.Plan.Granted {
				res.Effects = append(res.Effects, remediation.LedgerConfirm{UID: v.File.UID, Seq: e.Seq})
			}
		}
	}
	for _, ev := range d.Events {
		res.Events = append(res.Events, remediation.Event{Type: ev.Type, Reason: ev.Reason, Message: ev.Message, Action: "graft"})
	}
	for _, st := range d.Records {
		res.Records = append(res.Records, remediation.RecordNote{State: st})
	}
	return res, nil
}

// Copy is the graft planner's row of §3.5's table: status.graft and the
// flat status.graftTag and graftedAt.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Graft = from.Graft.DeepCopy()
	into.GraftTag, into.GraftedAt = from.GraftTag, from.GraftedAt.DeepCopy()
}

// GatherItem reads the file's item, its QualityProfile's audio policy and
// the RootFolder holding the file, from the cache: the graft planner's and
// the transcode planner's join's one gather. A gone item or profile is a nil
// item, not an error.
func GatherItem(ctx context.Context, r client.Reader, mf *catalogv1alpha1.MediaFile) (*graftplan.Item, string, string, error) {
	key := client.ObjectKey{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	it := &graftplan.Item{}
	var profileRef, original string
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := r.Get(ctx, key, &m); err != nil {
			return nil, "", "", client.IgnoreNotFound(err)
		}
		it.UID, it.Audio, profileRef = string(m.UID), m.Status.Audio, m.Spec.QualityProfileRef
		if m.Status.Metadata != nil {
			original = m.Status.Metadata.OriginalLanguage
		}
	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := r.Get(ctx, key, &e); err != nil {
			return nil, "", "", client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := r.Get(ctx, client.ObjectKey{Namespace: mf.Namespace, Name: e.Spec.SeriesRef}, &s); err != nil {
			return nil, "", "", client.IgnoreNotFound(err)
		}
		it.UID, it.Audio, profileRef = string(e.UID), e.Status.Audio, s.Spec.QualityProfileRef
		if s.Status.Metadata != nil {
			original = s.Status.Metadata.OriginalLanguage
		}
	default:
		return nil, "", "", nil
	}
	if t, ok := lang.Normalize(original); ok {
		it.Anchor = string(t)
	}
	if profileRef == "" {
		return nil, "", "", nil
	}
	var qp catalogv1alpha1.QualityProfile // cluster-scoped
	if err := r.Get(ctx, client.ObjectKey{Name: profileRef}, &qp); err != nil {
		return nil, "", "", client.IgnoreNotFound(err)
	}
	if a := qp.Spec.Audio; a != nil {
		it.Grafts, it.Default = a.Graft, a.Default
	}
	var folders catalogv1alpha1.RootFolderList
	if err := r.List(ctx, &folders, client.InNamespace(mf.Namespace)); err != nil {
		return nil, "", "", fmt.Errorf("graft: list RootFolders: %w", err)
	}
	if rf := jobspec.RootFolderFor(folders.Items, mf.Spec.Path); rf != nil {
		return it, rf.Spec.Path, jobspec.RecycleBinOf(rf), nil
	}
	return it, "", "", nil
}
