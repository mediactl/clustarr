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

package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/app/caption/subtitleplan"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
	"github.com/mediactl/clustarr/pkg/version"
)

// Options configures the subtitles planner.
type Options struct {
	// Bus carries clustarr-subtitles' waker (S19) and the stored-task
	// reads and purges (a StreamAdmin).
	Bus events.Bus
	// Store is clustarr-subtitles, as the loop (writer "").
	Store *subtitlestore.Store
	// DataDir is where the manager mounts /data.
	DataDir string
	// BacklogOpen is §6.4.6's gate (remediation.CaptionBacklog.Open); nil
	// is always closed, so nothing scheduled goes out.
	BacklogOpen func(now time.Time) bool
}

// Adapter is the subtitles planner.
type Adapter struct {
	o      Options
	listed *listedSet
}

// In is what Gather read.
type In = subtitleplan.Input

// New is the subtitles planner over o.
func New(o Options) *Adapter { return &Adapter{o: o, listed: newListedSet()} }

func (a *Adapter) bus() events.Bus { return a.o.Bus }

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerSubtitles }

// Applies: only movie and episode files have subtitles.
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	return subtitleplan.EligibleKind(mf.Spec.MediaRef.Kind)
}

// Gather reads the item, the profiles, the embedded providers and the
// RootFolders from the cache; reserves the file's pacer slot, reads the
// records and lists the directory only as far as subtitleplan.Needs says.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (In, error) {
	mf := v.File
	f := *mf
	f.Status = *v.Draft
	in := In{
		File: &f, Labels: v.Labels(), ProbeCurrent: v.ProbeCurrent(), DataDir: a.o.DataDir,
		Now: v.Now.Time, LastListed: a.listed.get(mf.UID),
		ForceNonce: mf.Annotations[catalogv1alpha1.AnnotationSubtitleSearch],
	}
	var err error
	if in.Item, err = item(ctx, env.Reader, mf); err != nil {
		return in, remediation.Transient(err)
	}
	var profiles subtitlev1alpha1.SubtitleProfileList
	if err := env.Reader.List(ctx, &profiles, client.UnsafeDisableDeepCopy); err != nil {
		return in, remediation.Transient(fmt.Errorf("subtitles: list SubtitleProfiles: %w", err))
	}
	in.Profiles = profiles.Items
	var providers subtitlev1alpha1.SubtitleProviderList
	if err := env.Reader.List(ctx, &providers, client.InNamespace(mf.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return in, remediation.Transient(fmt.Errorf("subtitles: list SubtitleProviders: %w", err))
	}
	in.Extractors = subtitleplan.EmbeddedEntries(providers.Items)
	in.OCR = subtitleplan.OCREntries(providers.Items)
	var roots catalogv1alpha1.RootFolderList
	if err := env.Reader.List(ctx, &roots, client.InNamespace(mf.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return in, remediation.Transient(fmt.Errorf("subtitles: list RootFolders: %w", err))
	}
	in.RootFolders = roots.Items
	if a.o.BacklogOpen != nil {
		in.BacklogOpen = a.o.BacklogOpen(in.Now)
	}

	need := subtitleplan.Needs(in)
	if need.Reserve {
		// The reservation is checked before any I/O that serves only the
		// dispatch, so a paced-out file neither lists nor reads (§4.7).
		in.PacerSlot = in.Now
		if env.Pacer != nil {
			in.PacerSlot = env.Pacer.Reserve(subtitlestore.Remediation, string(mf.UID))
		}
		need = subtitleplan.Needs(in)
	}
	in.Records = make(map[string]subtitlestore.Entry, len(need.Records))
	for _, k := range need.Records {
		e, err := a.o.Store.Get(ctx, string(mf.UID), k)
		if err != nil {
			return in, remediation.Transient(err) // a NATS outage must not write onto every file
		}
		in.Records[k] = e
	}
	if err := a.queued(ctx, env, &in); err != nil {
		return in, err
	}
	if subtitleplan.Needs(in).List {
		if err := a.list(ctx, env, &in); err != nil {
			return in, err
		}
	}
	return in, nil
}

// item reads the file's Movie, or its Episode and Series, from the cache.
func item(ctx context.Context, r client.Reader, mf *catalogv1alpha1.MediaFile) (subtitleplan.Item, error) {
	key := types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		switch err := r.Get(ctx, key, &m, client.UnsafeDisableDeepCopy); {
		case apierrors.IsNotFound(err):
			return subtitleplan.Item{}, nil
		case err != nil:
			return subtitleplan.Item{}, fmt.Errorf("subtitles: get Movie %s: %w", key, err)
		}
		return subtitleplan.ItemFromMovie(&m), nil
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		switch err := r.Get(ctx, key, &ep, client.UnsafeDisableDeepCopy); {
		case apierrors.IsNotFound(err):
			return subtitleplan.Item{}, nil
		case err != nil:
			return subtitleplan.Item{}, fmt.Errorf("subtitles: get Episode %s: %w", key, err)
		}
		if ep.Spec.SeriesRef == "" {
			return subtitleplan.ItemFromEpisode(&ep, nil), nil
		}
		var s catalogv1alpha1.Series
		switch err := r.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: ep.Spec.SeriesRef}, &s, client.UnsafeDisableDeepCopy); {
		case apierrors.IsNotFound(err):
			return subtitleplan.ItemFromEpisode(&ep, nil), nil
		case err != nil:
			return subtitleplan.Item{}, fmt.Errorf("subtitles: get Series %s: %w", ep.Spec.SeriesRef, err)
		}
		return subtitleplan.ItemFromEpisode(&ep, &s), nil
	}
	return subtitleplan.Item{}, nil
}

// queued reads, for every record Gather read that is requested inside the
// republish window, whether its task is still stored (§6.4.6): Queued is
// allocated and a langKey is set only when its fetch subject holds a
// message, so a key absent from it is a task gone from the stream. With no
// such record, or no StreamAdmin, Queued stays nil and a republish reuses
// the Msg-Id. Subjects carry no Seq, so an older, superseded task still
// queued on the same subject masks an evicted current one; the 26 h timeout
// then closes it and the schedule dispatches again.
func (a *Adapter) queued(ctx context.Context, env *remediation.Env, in *In) error {
	admin, ok := env.Bus.(events.StreamAdmin)
	if !ok {
		return nil
	}
	uid := string(in.File.UID)
	for _, k := range slices.Sorted(maps.Keys(in.Records)) {
		e := in.Records[k]
		if !e.OK || e.Record.State != records.StateRequested || in.Now.Sub(e.Record.RequestedAt) >= records.RepublishWindow {
			continue
		}
		if in.Queued == nil {
			in.Queued = map[string]bool{}
		}
		subjects, err := admin.Subjects(ctx, events.StreamWorkCaption, events.WorkFetchSubjectAnyPriority(uid, k))
		if err != nil {
			return remediation.Transient(fmt.Errorf("subtitles: read the stored fetch tasks: %w", err))
		}
		if len(subjects) > 0 {
			in.Queued[k] = true
		}
	}
	return nil
}

// list reads the media directory through the I/O executor (§6.4.3). An EIO,
// a timeout, a busy pool or an open breaker is transient, never a block;
// any other error -- a directory gone or unreadable -- is the listing's,
// which Plan blocks as MediaDirUnreadable.
func (a *Adapter) list(ctx context.Context, env *remediation.Env, in *In) error {
	local, err := datapath.Local(a.o.DataDir, in.File.Spec.Path)
	if err != nil {
		return nil // Plan blocks MediaFileNotOnDataVolume
	}
	entries, err := env.IO.ReadDir(ctx, filepath.Dir(local))
	switch {
	case errors.Is(err, syscall.EIO), remediation.IsTransient(err):
		return remediation.Transient(err)
	case err != nil:
		in.Listing = &subtitleplan.Listing{Err: err}
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	in.Listing = &subtitleplan.Listing{Names: names}
	return nil
}

// Plan is subtitleplan.Plan over the draft, its dispatches and withdrawals
// turned into effects.
func (a *Adapter) Plan(v *remediation.View, in In, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	if in.File == nil {
		return remediation.Result{}, nil // Gather did not run
	}
	f := *in.File
	f.Status = *v.Draft // the draft the planners before subtitles left
	in.File = &f
	d := subtitleplan.Plan(in, v.Issue)
	if d.Held {
		return remediation.Result{}, nil // out holds the draft's block, sidecars and nonce already
	}
	stampDestinations(&d)
	out.Subtitles, out.Sidecars = d.Block, d.Sidecars
	setNonce(out, d.HandledNonce)
	res := remediation.Result{Due: d.Wake}
	var stored string
	if v.Prev.HandledNonces != nil {
		stored = v.Prev.HandledNonces.SubtitleSearch
	}
	// A person's forced search is exempt from the bulk status-write limiter
	// (§3.7): the apply that records its nonce goes out at once.
	res.Unpaced = d.HandledNonce != stored
	res.Effects = effectsOf(d, in.Now)
	if d.SidecarsListed {
		a.listed.set(v.File.UID, subtitleplan.Listed{Path: v.File.Spec.Path, ProbeHash: v.Draft.ProbeHash})
	}
	for _, e := range d.Events {
		res.Events = append(res.Events, remediation.Event{Type: e.Type, Reason: e.Reason, Message: e.Message, Action: "subtitles"})
	}
	return res, nil
}

// Copy is subtitles' row of §3.5's table: status.subtitles, status.sidecars
// and handledNonces.subtitleSearch.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Subtitles = from.Subtitles.DeepCopy()
	into.Sidecars = slices.Clone(from.Sidecars)
	var nonce string
	if from.HandledNonces != nil {
		nonce = from.HandledNonces.SubtitleSearch
	}
	setNonce(into, nonce)
}

// setNonce sets handledNonces.subtitleSearch alone, beside the transcode
// leaves.
func setNonce(st *catalogv1alpha1.MediaFileStatus, nonce string) {
	if st.HandledNonces == nil {
		if nonce == "" {
			return
		}
		st.HandledNonces = &catalogv1alpha1.HandledNonces{}
	} else {
		cp := *st.HandledNonces
		st.HandledNonces = &cp
	}
	st.HandledNonces.SubtitleSearch = nonce
	if *st.HandledNonces == (catalogv1alpha1.HandledNonces{}) {
		st.HandledNonces = nil
	}
}

// durable is the fetch durable a lane's task goes to: high, or normal for
// the normal and low lanes.
func durable(p subtitleplan.Priority) string {
	if p == subtitleplan.PriorityHigh {
		return events.ConsumerCaptionFetchHigh
	}
	return events.ConsumerCaptionFetchNormal
}

// stampDestinations records, on each item a new dispatch went out for, the
// durable it went to (ADR-0019 §8.3's destination).
func stampDestinations(d *subtitleplan.Decision) {
	if d.Block == nil {
		return
	}
	for _, x := range d.Dispatches {
		if !x.New {
			continue
		}
		for i := range d.Block.Items {
			it := &d.Block.Items[i]
			if it.LangKey == x.LangKey && it.Dispatch != nil && it.Dispatch.Seq == x.Seq {
				it.Dispatch.Destination = durable(x.Priority)
			}
		}
	}
}

// effectsOf renders withdrawals first, then for each dispatch its record
// write and its publish (§3.8: the record, then the task).
func effectsOf(d subtitleplan.Decision, now time.Time) []remediation.Effect {
	var out []remediation.Effect
	for _, w := range d.Withdraws {
		out = append(out, remediation.Withdraw{UID: d.FileUID, Sub: w.LangKey, Seq: w.Seq})
	}
	for _, x := range d.Dispatches {
		uid := x.Task.MediaFile.UID
		if x.Record {
			rec := subtitlestore.Requested(x.Task, now)
			b, _ := json.Marshal(rec) // strings, ints, times and a map of strings: Marshal cannot fail
			rw := remediation.RecordWrite{
				Bucket: events.BucketSubtitles, Key: subtitlestore.Key(uid, x.LangKey), Revision: x.Revision, Value: b,
				Remediation: subtitlestore.Remediation, Lane: string(x.Priority),
			}
			if x.New && !x.Task.Forced { // a forced dispatch took no pacer slot
				rw.Pace = subtitlestore.Remediation
			}
			out = append(out, rw)
		}
		id := events.MsgIDForSubtitleFetch(uid, x.LangKey, x.Seq)
		if x.Evicted {
			// The task is gone from a DiscardOld stream, and its Msg-Id stays
			// in the dedupe map for the window (§6.4.6).
			id = fmt.Sprintf("%s/r%d", id, now.Unix()/60)
		}
		name, data, _ := schema.Encode(x.Task) // as above: a FetchTask always encodes
		out = append(out, remediation.Publish{
			Subject:      events.WorkFetchSubject(events.Priority(x.Priority), uid, x.LangKey),
			MsgID:        id,
			ExpectStream: events.StreamWorkCaption,
			Envelope: &events.Envelope{
				ID: id, Type: schema.FetchTaskType, Schema: name,
				Source: "catalog@" + version.String(), Key: x.Task.MediaFile.Key(), Time: now, Data: data,
			},
		})
	}
	return out
}

// Withdraw runs a subtitle withdrawal (§4.7): withdrawn{seq} at a fresh
// read, then the purge of the language's task subjects on every lane.
func (a *Adapter) Withdraw(ctx context.Context, env *remediation.Env, w remediation.Withdraw) error {
	for range records.CASAttempts {
		e, err := a.o.Store.Get(ctx, w.UID, w.Sub)
		if err != nil {
			return remediation.Transient(err)
		}
		err = a.o.Store.Withdraw(ctx, w.UID, w.Sub, w.Seq, e.Revision, w.Seq)
		if errors.Is(err, records.ErrRaced) {
			continue
		}
		if err != nil {
			return err
		}
		if admin, ok := env.Bus.(events.StreamAdmin); ok {
			if err := admin.PurgeSubject(ctx, events.StreamWorkCaption, events.WorkFetchSubjectAnyPriority(w.UID, w.Sub)); err != nil {
				return remediation.Transient(err)
			}
		}
		return nil
	}
	return records.ErrRaced
}
