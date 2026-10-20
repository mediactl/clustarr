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

package downloads

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// importBook is the stage's leader-local record of when it last published
// each entry's in-flight import task, by seq: importplan republishes an
// unanswered task under its Msg-Id only when RepublishEvery has passed
// (after a leader change the book is empty, and the next pass republishes;
// the stream's dedup window drops a duplicate).
type importBook struct {
	mu sync.Mutex
	m  map[types.UID]publishedAt
}

type publishedAt struct {
	seq int64
	at  time.Time
}

func newImportBook() *importBook { return &importBook{m: map[types.UID]publishedAt{}} }

func (b *importBook) get(uid types.UID, seq int64) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.m[uid]; ok && p.seq == seq {
		return p.at
	}
	return time.Time{}
}

func (b *importBook) set(uid types.UID, seq int64, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[uid] = publishedAt{seq: seq, at: at}
}

// importKey is an entry's import in the dispatch ledger.
func importKey(ow owner, entryUID string) dispatch.Key {
	return dispatch.Key{Kind: ow.ref.Kind, Namespace: ow.ref.Namespace, Name: ow.ref.Name, Sub: entryUID + "/import"}
}

// importDecisions fills v.Import with importplan's decision for each entry
// lifecycle may import (A3.8 step 4): its records at the entry's
// import.dispatch, the covered items' MediaFiles, the profile, the donor's
// item, and a pending download.clustarr.io/import intent naming it.
func (s *Stage) importDecisions(ctx context.Context, ow owner, v *lifecycle.View) error {
	var pending *lifecycle.ImportIntent
	if it := v.Intents.Import; it != nil && v.Intents.NonceKey(catalogv1alpha1.AnnotationDownloadImport) != v.Nonces.Import {
		pending = it
	}
	recordOf := func(e *catalogv1alpha1.DownloadEntry) *schema.TransferRecord {
		if t, ok := v.Transfers[types.UID(e.UID)]; ok {
			r := t.Record
			return &r
		}
		return nil
	}
	for i := range ow.entries {
		e := &ow.entries[i]
		uid := types.UID(e.UID)
		rec := recordOf(e)
		if !lifecycle.Importable(e, rec) {
			continue
		}
		// A set (a movie released in parts) imports once, from its first
		// part, when every part is done; the others follow it.
		if e.Set != nil && (e.Set.Part != 1 || !lifecycle.SetReady(ow.entries, e, recordOf)) {
			continue
		}
		in, err := s.importInput(ctx, ow, v, e, rec)
		if err != nil {
			return err
		}
		s.observeGroup(ctx, in)
		if e.Set != nil {
			in.Parts = setParts(ow.entries, e, recordOf)
		}
		if pending != nil && pending.ID == e.ID {
			in.Intent = pending
		}
		if v.Import == nil {
			v.Import = map[types.UID]lifecycle.ImportDecision{}
		}
		v.Import[uid] = importplan.Decide(in)
	}
	return nil
}

// setParts are the other parts of e's set, by part, each with its
// transfer record.
func setParts(
	entries []catalogv1alpha1.DownloadEntry, e *catalogv1alpha1.DownloadEntry,
	recordOf func(*catalogv1alpha1.DownloadEntry) *schema.TransferRecord,
) []importplan.PartInput {
	var out []importplan.PartInput
	for i := range entries {
		s := &entries[i]
		if s.UID != e.UID && s.Set != nil && s.Set.ID == e.Set.ID {
			out = append(out, importplan.PartInput{Entry: *s.DeepCopy(), Transfer: recordOf(s)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entry.Set.Part < out[j].Entry.Set.Part })
	return out
}

// importInput gathers one entry's importplan.Input from the cache and the
// clustarr-imports records.
func (s *Stage) importInput(
	ctx context.Context, ow owner, v *lifecycle.View, e *catalogv1alpha1.DownloadEntry, rec *schema.TransferRecord,
) (importplan.Input, error) {
	ns := ow.ref.Namespace
	in := importplan.Input{
		Owner: ow.ref, OwnerKind: ow.kind, Entry: *e, Transfer: rec, Files: v.Files[e.ID], Now: v.Now,
		Existing: map[string][]catalogv1alpha1.MediaFile{},
		Named:    map[string]*catalogv1alpha1.MediaFile{},
	}
	if covered := v.Covered[types.UID(e.UID)]; len(covered) > 0 {
		in.Targets = covered
	} else {
		in.Targets = []schema.ItemRef{ow.ref}
	}
	for _, phase := range []string{schema.ImportSubInspect, schema.ImportSubExecute} {
		r, _, ok, err := s.imports.Get(ctx, agentrecords.ImportKey(e.UID, phase))
		if err != nil {
			return in, fmt.Errorf("downloads: %s record of %s: %w", phase, e.ID, err)
		}
		if !ok {
			continue
		}
		if phase == schema.ImportSubInspect {
			in.Inspect = r
		} else {
			in.Execute = r
		}
	}

	// The covered items' current MediaFiles, by every key the inspection's
	// files name.
	if r := in.Inspect; r != nil && r.Inspect != nil {
		for _, f := range r.Inspect.Files {
			if f.Proposed == nil {
				continue
			}
			kind := importplan.MediaKindOf(f.Proposed.Kind)
			keys := f.Keys
			if len(keys) == 0 {
				keys = []string{f.Proposed.Name}
			}
			for _, k := range keys {
				key := importplan.ItemKey(kind, k)
				if _, done := in.Existing[key]; done {
					continue
				}
				var list catalogv1alpha1.MediaFileList
				if err := s.o.Reader.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{mfindex.Item: mfindex.ItemKey(kind, k)}); err != nil {
					return in, fmt.Errorf("downloads: MediaFiles of %s: %w", key, err)
				}
				in.Existing[key] = list.Items
			}
		}
	}
	// The MediaFiles the execute's placements name.
	if r := in.Execute; r != nil && r.Execute != nil {
		for _, f := range r.Execute.Placed {
			var mf catalogv1alpha1.MediaFile
			if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: f.MediaFileName}, &mf); err == nil {
				in.Named[f.MediaFileName] = &mf
			} else if client.IgnoreNotFound(err) != nil {
				return in, fmt.Errorf("downloads: MediaFile %s: %w", f.MediaFileName, err)
			}
		}
	}

	in.ProfileRef = e.QualityProfileRef
	if in.ProfileRef == "" {
		in.ProfileRef = s.ownerProfileRef(ctx, ow)
	}
	if in.ProfileRef != "" {
		var qp catalogv1alpha1.QualityProfile
		if err := s.o.Reader.Get(ctx, client.ObjectKey{Name: in.ProfileRef}, &qp); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return in, fmt.Errorf("downloads: QualityProfile %s: %w", in.ProfileRef, err)
			}
			in.ProfileError = "it does not exist"
		} else if p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue()); len(errs) > 0 {
			in.ProfileError = fmt.Sprintf("%v", errs[0])
		} else {
			in.Profile = &p
		}
	} else {
		in.ProfileError = "neither the grab nor its item names one"
	}
	in.OriginalLanguage = originalLanguageOf(ow.obj)
	if m, ok := ow.obj.(*catalogv1alpha1.Movie); ok {
		in.Identity = itemread.MovieIdentity(m)
		if m.Status.Metadata != nil {
			in.RuntimeMinutes = int(m.Status.Metadata.RuntimeMinutes)
		}
	}

	if e.Purpose == commonv1.DownloadPurposeAudioDonor {
		d, err := s.donorItem(ctx, ow, in.Targets, in.Profile)
		if err != nil {
			return in, err
		}
		in.Donor = d
	}

	key := importKey(ow, e.UID)
	if s.o.Dispatch != nil {
		in.Admit = func(seq int64) dispatch.Decision { return s.o.Dispatch.Admit(importplan.Durable, key, seq) }
	}
	if e.Import != nil {
		in.Published = s.importsPublished.get(types.UID(e.UID), e.Import.Dispatch.Seq)
	}
	return in, nil
}

// ownerProfileRef is the quality profile an owner (or its Artist or
// Author) names, "" for none.
func (s *Stage) ownerProfileRef(ctx context.Context, ow owner) string {
	ns := ow.ref.Namespace
	switch t := ow.obj.(type) {
	case *catalogv1alpha1.Movie:
		return t.Spec.QualityProfileRef
	case *catalogv1alpha1.Series:
		return t.Spec.QualityProfileRef
	case *catalogv1alpha1.Audiobook:
		return t.Spec.QualityProfileRef
	case *catalogv1alpha1.Comic:
		return t.Spec.QualityProfileRef
	case *catalogv1alpha1.Album:
		if r := ptr.Deref(t.Spec.QualityProfileRef, ""); r != "" {
			return r
		}
		var a catalogv1alpha1.Artist
		if s.o.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: t.Spec.ArtistRef}, &a) == nil {
			return a.Spec.QualityProfileRef
		}
	case *catalogv1alpha1.Book:
		if r := ptr.Deref(t.Spec.QualityProfileRef, ""); r != "" {
			return r
		}
		var a catalogv1alpha1.Author
		if ref := ptr.Deref(t.Spec.AuthorRef, ""); ref != "" &&
			s.o.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref}, &a) == nil {
			return a.Spec.QualityProfileRef
		}
	}
	return ""
}

// originalLanguageOf is a Movie's or Series' original language tag.
func originalLanguageOf(o client.Object) string {
	switch t := o.(type) {
	case *catalogv1alpha1.Movie:
		if t.Status.Metadata != nil {
			return t.Status.Metadata.OriginalLanguage
		}
	case *catalogv1alpha1.Series:
		if t.Status.Metadata != nil {
			return t.Status.Metadata.OriginalLanguage
		}
	}
	return ""
}

// donorItem reads the Episode or Movie a donor entry is for, its audio
// state and the donor it records (status.audio.donor, which a newer donor
// replaces); nil when it is gone.
func (s *Stage) donorItem(ctx context.Context, ow owner, targets []schema.ItemRef, p *quality.Profile) (*importplan.DonorItem, error) {
	ns := ow.ref.Namespace
	d := &importplan.DonorItem{Original: originalLanguageOf(ow.obj)}
	if p != nil {
		d.AudioDefault = p.AudioDefault
	}
	var audio *catalogv1alpha1.AudioState
	switch t := ow.obj.(type) {
	case *catalogv1alpha1.Movie:
		d.Item, audio = ow.ref, t.Status.Audio
	case *catalogv1alpha1.Series:
		if len(targets) == 0 || targets[0].Kind != "Episode" {
			return nil, nil
		}
		var ep catalogv1alpha1.Episode
		if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: targets[0].Name}, &ep); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		d.Item = schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: ep.Name, UID: string(ep.UID)}}
		audio = ep.Status.Audio
	default:
		return nil, nil
	}
	if audio != nil {
		d.Missing = audio.Missing
		if audio.Donor != nil {
			d.RecordedDonor = audio.Donor.Path
		}
	}
	return d, nil
}

// importedDonors are the audio donors entries delivered (loop spec §2.6;
// ADR-0019 F7.1): each audioDonor entry whose import finished and whose
// execute record in clustarr-imports names the reduced audio, as the
// item's status.audio.donor would record it. nil when there is none, so
// the item keeps the donor it recorded.
func (s *Stage) importedDonors(ctx context.Context, entries []catalogv1alpha1.DownloadEntry) ([]catalogv1alpha1.AudioDonor, error) {
	var out []catalogv1alpha1.AudioDonor
	for i := range entries {
		e := &entries[i]
		if !rollup.IsDonor(e) || e.Import == nil || e.Import.Phase != catalogv1alpha1.ImportPhaseImported || e.Import.ImportedAt == nil {
			continue
		}
		rec, _, ok, err := s.imports.Get(ctx, agentrecords.ImportKey(e.UID, schema.ImportSubExecute))
		if err != nil {
			return nil, fmt.Errorf("downloads: execute record of %s: %w", e.ID, err)
		}
		if !ok || rec.Execute == nil || rec.Execute.DonorPath == "" {
			continue // the record is gone: the item keeps the donor it recorded
		}
		out = append(out, catalogv1alpha1.AudioDonor{
			Path: rec.Execute.DonorPath, Release: e.Release.Title, DownloadRef: e.ID, ImportedAt: *e.Import.ImportedAt.DeepCopy(),
		})
	}
	return out, nil
}

// importEffects renders the plan's import dispatches as DispatchPublish
// effects on import-fileimport (admitted by the ledger in importplan),
// and tells the ledger of each dispatch an answer closed.
func (s *Stage) importEffects(ow owner, v lifecycle.View, plan lifecycle.Plan) []remediation.Effect {
	var out []remediation.Effect
	for uid, dec := range v.Import {
		if dec.Answered > 0 {
			out = append(out, remediation.DispatchAnswered{
				Durable: importplan.Durable, Ledger: importKey(ow, string(uid)), Seq: dec.Answered,
			})
		}
	}
	for _, d := range plan.Imports {
		p, ok := d.Task.(schema.Payload)
		if !ok {
			continue
		}
		name, data, err := schema.Encode(p)
		if err != nil {
			continue
		}
		typ := "import.ImportInspectTask"
		if d.Phase == schema.ImportSubExecute {
			typ = "import.ImportExecuteTask"
		}
		now := v.Now.UTC()
		out = append(out, remediation.DispatchPublish{
			Publish: remediation.Publish{
				Subject: d.Subject, MsgID: d.MsgID, ExpectStream: events.StreamWorkImport,
				Envelope: &events.Envelope{
					ID: d.MsgID, Type: typ, Schema: name, Source: source(),
					Key: ow.ref.Namespace + "/" + ow.ref.Name, Time: now, Data: data,
				},
				At: now,
			},
			Durable: importplan.Durable, Ledger: importKey(ow, string(d.EntryUID)), Seq: d.Seq,
		})
		s.importsPublished.set(d.EntryUID, d.Seq, now)
	}
	return out
}

// materialise renders the plan's MediaFile applies (under import-worker,
// through the item path's SpecWriter), the replaced MediaFiles' deletes
// with UID preconditions (W22 had none), and the removal of a donor a newer
// one superseded (loop spec §2.14).
func (s *Stage) materialise(ow owner, plan lifecycle.Plan) []remediation.Effect {
	ns := ow.ref.Namespace
	var out []remediation.Effect
	for _, m := range plan.Materialise {
		switch {
		case m.Apply != nil:
			a := m.Apply
			out = append(out, remediation.ApplyMediaFileSpec{
				Namespace: ns, Name: a.Name, RV: a.RV, Ref: a.Ref, Path: a.Path, SizeBytes: a.SizeBytes, ModTime: a.ModTime,
				Frozen: mediafilespec.FrozenFromSchema(a.Frozen, a.From),
			})
		case m.Delete != nil:
			mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: m.Delete.Name}}
			out = append(out, remediation.DeleteObject{Object: mf, UID: types.UID(m.Delete.UID)})
		}
		for _, f := range m.Remove {
			out = append(out, remediation.SafeRemove{Path: f})
		}
	}
	return out
}
