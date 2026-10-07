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
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// Options is what the downloads stage reads.
type Options struct {
	// Reader is the manager's cache; APIReader the uncached reader, read
	// only for owner-gone evidence and a provider id that disagrees.
	Reader    client.Reader
	APIReader client.Reader
	Bus       events.Bus
	Dispatch  *dispatch.Ledger
	Book      *dispatch.DeliveryBook
	Recorder  func(string) k8sevents.EventRecorder
	// Clock is the stage's clock; nil is time.Now.
	Clock func() time.Time
}

// Stage is the downloads item stage (remediation.ItemStage, Watcher and
// OwnerGone).
type Stage struct {
	o         Options
	transfers *records.Reader[*schema.TransferRecord]
	engines   *records.Reader[*schema.EngineRecord]
	imports   *records.Reader[*schema.ImportRecord]
	blocks    *BlockBook
	removed   *removalBook
	issued    *commandBook
	unclaimed *Unclaimed
	boots     *bootBook
	// importsPublished is the import tasks' publish book (A3.8).
	importsPublished *importBook
	seedOnce         sync.Once
}

var (
	_ remediation.ItemStage = (*Stage)(nil)
	_ remediation.Watcher   = (*Stage)(nil)
	_ remediation.OwnerGone = (*Stage)(nil)
)

// New builds the downloads stage.
func New(o Options) *Stage {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Stage{
		o:         o,
		transfers: records.NewReader(o.Bus.KV(events.BucketTransfers), agentrecords.Transfers()),
		engines:   records.NewReader(o.Bus.KV(events.BucketEngines), agentrecords.Engines()),
		imports:   records.NewReader(o.Bus.KV(events.BucketImports), agentrecords.Imports()),
		blocks:    NewBlockBook(),
		removed:   newRemovalBook(),
		issued:    newCommandBook(),
		unclaimed: NewUnclaimed(),
		boots:     newBootBook(),

		importsPublished: newImportBook(),
	}
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageDownloads }

// Applies implements remediation.ItemStage: the six owner kinds and the
// Episode and Issue views.
func (s *Stage) Applies(k remediation.KeyKind) bool {
	switch k {
	case remediation.KindMovie, remediation.KindSeries, remediation.KindAlbum, remediation.KindBook,
		remediation.KindAudiobook, remediation.KindComic, remediation.KindEpisode, remediation.KindIssue:
		return true
	}
	return false
}

// Plan implements remediation.ItemStage.
func (s *Stage) Plan(ctx context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	s.seedOnce.Do(func() { s.seedUnclaimed(ctx) })
	switch v.Key.Kind {
	case remediation.KindEpisode, remediation.KindIssue:
		return s.view(ctx, v)
	}
	ow, ok := ownerOf(v.Item)
	if !ok {
		return remediation.ItemResult{}, nil
	}
	lv, err := s.gather(ctx, ow, v.Now.Time)
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	// A claim naming another provider id is decided only on the
	// apiserver's word for the owner's own (§6.7).
	if s.o.APIReader != nil && needsProviderCheck(lv) {
		fresh := ow.obj.DeepCopyObject().(client.Object)
		if err := s.o.APIReader.Get(ctx, client.ObjectKeyFromObject(ow.obj), fresh); err == nil {
			lv.ProviderID, lv.ProviderVerified = providerIDOf(fresh), true
		}
	}
	lv.NewEntries = v.NewEntries
	if err := s.importDecisions(ctx, ow, &lv); err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	plan := lifecycle.Decide(lv)
	s.issued.set(plan.Issued)
	return s.result(ctx, ow, lv, plan), nil
}

// needsProviderCheck reports an unclaimed claim whose owner UID differs and
// whose provider id disagrees with the cached owner's.
func needsProviderCheck(v lifecycle.View) bool {
	for _, t := range v.Unclaimed {
		c := t.Record.Claim
		if c != nil && c.Owner.UID != v.Owner.UID && c.ProviderID != "" && c.ProviderID != v.ProviderID {
			return true
		}
	}
	return false
}

// view is an Episode's or Issue's view of its container's entries
// (A3.5 step 3): read-only, it owes nothing.
func (s *Stage) view(ctx context.Context, v *remediation.ItemView) (remediation.ItemResult, error) {
	var covering []catalogv1alpha1.DownloadEntry
	switch t := v.Item.(type) {
	case *catalogv1alpha1.Episode:
		var ser catalogv1alpha1.Series
		if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: t.Spec.SeriesRef}, &ser); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return remediation.ItemResult{}, remediation.Transient(err)
			}
		}
		for i := range ser.Status.Downloads {
			if rollup.CoversEpisode(&ser.Status.Downloads[i], t.Name, t.Spec.SeasonNumber, t.Spec.EpisodeNumber) {
				covering = append(covering, ser.Status.Downloads[i])
			}
		}
	case *catalogv1alpha1.Issue:
		var co catalogv1alpha1.Comic
		if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: t.Spec.ComicRef}, &co); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return remediation.ItemResult{}, remediation.Transient(err)
			}
		}
		for i := range co.Status.Downloads {
			if rollup.CoversIssue(&co.Status.Downloads[i], t.Name, t.Spec.Number) {
				covering = append(covering, co.Status.Downloads[i])
			}
		}
	default:
		return remediation.ItemResult{}, nil
	}
	c := itempass.Contribution{Viewed: true, Covering: covering, DonorOpen: rollup.DonorEntryOpen(covering, nil)}
	var phase commonv1.DownloadPhase
	if a := rollup.ActiveEntry(covering, nil); a != nil {
		id := a.ID
		c.ActiveDownloadRef, phase = &id, a.Phase
	}
	c.DownloadPhase = &phase
	return remediation.ItemResult{Contribution: c}, nil
}

// result translates a lifecycle plan into the pass's contribution and its
// effects, in the order A3.5 step 2 fixes.
func (s *Stage) result(ctx context.Context, ow owner, v lifecycle.View, plan lifecycle.Plan) remediation.ItemResult {
	entries := plan.Entries
	phase := plan.Phase
	nonces := plan.Nonces
	res := remediation.ItemResult{
		Contribution: itempass.Contribution{Downloads: &entries, DownloadPhase: &phase, DownloadNonces: &nonces},
		Downloads:    &remediation.DownloadsOutcome{Entries: entries, Redownloads: plan.Redownloads},
		Due:          plan.Due,
	}
	hasFinalizer := controllerutil.ContainsFinalizer(ow.obj, catalogv1alpha1.FinalizerTransfers)
	if plan.Finalizer != nil && *plan.Finalizer && !hasFinalizer && ow.obj.GetDeletionTimestamp() == nil {
		res.Effects = append(res.Effects, remediation.EnsureFinalizer{Name: catalogv1alpha1.FinalizerTransfers})
	}
	for _, b := range plan.Blocks {
		res.Effects = append(res.Effects, s.blockCall(b))
	}
	var eps []catalogv1alpha1.Episode
	if ow.kind == commonv1.MediaKindSeries && len(plan.Commands) > 0 {
		eps = s.episodesOf(ctx, ow)
	}
	for _, c := range plan.Commands {
		if c.Cmd.Selection != nil && len(eps) > 0 {
			c.Cmd.Selection = enrichSelection(c.Cmd.Selection, eps)
		}
		if eff, ok := s.command(ow, c, metav1.NewTime(v.Now)); ok {
			res.Effects = append(res.Effects, eff)
		}
	}
	res.Effects = append(res.Effects, s.importEffects(ow, v, plan)...)
	now := v.Now
	for _, p := range plan.Removals {
		path := p
		res.Effects = append(res.Effects, remediation.SafeRemove{Path: path, Done: func(err error) {
			if err == nil {
				s.removed.done(path, now)
			}
		}})
	}
	res.Effects = append(res.Effects, s.materialise(ow, plan)...)
	for _, g := range plan.DirectGrabs {
		res.Effects = append(res.Effects, remediation.CountGrab{
			Namespace: ow.ref.Namespace, IndexerRef: g.IndexerRef, Key: string(g.EntryUID), At: g.At,
		})
	}
	for _, h := range plan.History {
		if eff, ok := s.history(ow, v, plan, h); ok {
			res.Effects = append(res.Effects, eff)
		}
	}
	if plan.Finalizer != nil && !*plan.Finalizer && hasFinalizer {
		res.Effects = append(res.Effects, remediation.RemoveFinalizer{Name: catalogv1alpha1.FinalizerTransfers})
		if ow.obj.GetDeletionTimestamp() != nil {
			// The owner's own finalizer comes off on the next pass, which a
			// finalizer removal does not wake.
			res.Again = true
		}
	}
	for _, e := range plan.Events {
		res.Events = append(res.Events, s.event(ow, e))
	}
	var dropped []types.UID
	for i := range v.Stored {
		if !hasUID(entries, v.Stored[i].UID) {
			dropped = append(dropped, types.UID(v.Stored[i].UID))
		}
	}
	s.issued.forget(dropped)
	logging.FromContext(ctx).Debug("downloads: planned", "entries", len(entries), "commands", len(plan.Commands), "phase", phase)
	return res
}

func hasUID(entries []catalogv1alpha1.DownloadEntry, uid string) bool {
	for i := range entries {
		if entries[i].UID == uid {
			return true
		}
	}
	return false
}
