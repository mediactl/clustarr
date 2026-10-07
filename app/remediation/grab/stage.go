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

package grab

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;albums;books;audiobooks;comics;issues;artists;authors,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status;episodes/status;albums/status;books/status;audiobooks/status;comics/status;issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles;delayprofiles;mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch

// Options is what the grab stage reads.
type Options struct {
	// Reader is the manager's cache.
	Reader client.Reader
	Bus    events.Bus
	// Inbox is the candidate inbox the owner's parked candidates come
	// from; nil plans search answers and delayed grabs only.
	Inbox *intake.Inbox
	// Clock is the stage's clock; nil is time.Now.
	Clock func() time.Time
}

// Stage is the grab item stage (remediation.ItemStage, SetDeclarer).
type Stage struct {
	o        Options
	searches *records.Reader[*schema.SearchRecord]
}

var (
	_ remediation.ItemStage   = (*Stage)(nil)
	_ remediation.SetDeclarer = (*Stage)(nil)
)

// New builds the grab stage.
func New(o Options) *Stage {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Stage{o: o, searches: records.NewReader(o.Bus.KV(events.BucketSearches), agentrecords.Searches())}
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageGrab }

// Applies implements remediation.ItemStage: the six owner kinds, and the
// Episode and Issue views.
func (s *Stage) Applies(k remediation.KeyKind) bool {
	switch k {
	case remediation.KindMovie, remediation.KindSeries, remediation.KindAlbum, remediation.KindBook,
		remediation.KindAudiobook, remediation.KindComic, remediation.KindEpisode, remediation.KindIssue:
		return true
	}
	return false
}

// DeclaresSets implements remediation.SetDeclarer: pendingGrab or
// pendingGrabs, in catalog-grab's set on every kind it applies to.
func (s *Stage) DeclaresSets(remediation.KeyKind) []k8s.FieldManager {
	return []k8s.FieldManager{k8s.ManagerCatalogGrab}
}

// Plan implements remediation.ItemStage.
func (s *Stage) Plan(ctx context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	switch v.Key.Kind {
	case remediation.KindEpisode, remediation.KindIssue:
		return s.view(ctx, v)
	}
	if v.Item.GetDeletionTimestamp() != nil {
		// A deleting owner grabs nothing; its candidates are refused and
		// its pending grabs re-sent until the item is gone.
		return s.deleting(v), nil
	}
	g, err := s.gather(ctx, v.Item, v.Now.Time)
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	plan := grabplan.Decide(g.v)
	return s.result(ctx, v, g, plan), nil
}

// result is the plan as the pass's outcome.
func (s *Stage) result(ctx context.Context, v *remediation.ItemView, g *gathered, plan grabplan.Plan) remediation.ItemResult {
	gv := g.v
	res := remediation.ItemResult{
		NewEntries: plan.NewEntries,
		// Convergence reads the covered items' files; a pass that had
		// nothing to decide reads them only now. A file it cannot read
		// keeps the transfer: no evidence, no removal (§6.7).
		StillWants: func(c schema.TransferClaim) (bool, string) {
			if err := s.fillCurrent(ctx, g); err != nil {
				return true, "the owner's files could not be read"
			}
			return grabplan.StillWants(g.v, c)
		},
		Sets: []itempass.Set{pendingSet(v.Key.Kind, plan.Pending)},
		Due:  plan.Due,
	}
	for id, o := range plan.Settle {
		res.Effects = append(res.Effects, remediation.SettleCandidate{MsgID: id, Outcome: o})
	}
	for i := range plan.NewEntries {
		if eff, ok := grabbed(gv, &plan.NewEntries[i], v.Now.Time); ok {
			res.Effects = append(res.Effects, eff)
		}
	}
	for _, e := range plan.Events {
		res.Events = append(res.Events, remediation.ItemEvent{Recorder: e.Recorder, Type: e.Type, Reason: e.Reason, Message: e.Message})
	}
	for decision, n := range plan.Decisions {
		metrics.SearchDecisionsTotal.WithLabelValues(string(gv.Kind), decision, "grabplan").Add(float64(n))
	}
	if len(plan.NewEntries) > 0 || len(plan.Settle) > 0 {
		logging.FromContext(ctx).Debug("grab: planned", "new", len(plan.NewEntries), "pending", len(plan.Pending), "settled", len(plan.Settle))
	}
	return res
}

// deleting refuses every parked candidate of a deleting owner and keeps
// its pending grabs, so the set stays complete.
func (s *Stage) deleting(v *remediation.ItemView) remediation.ItemResult {
	res := remediation.ItemResult{Sets: []itempass.Set{pendingSet(v.Key.Kind, pendingOf(v.Item))}}
	if s.o.Inbox != nil {
		for _, p := range s.o.Inbox.Take(intake.OwnerOf(itemread.Ref(v.Item))) {
			res.Effects = append(res.Effects, remediation.SettleCandidate{MsgID: p.MsgID, Outcome: intake.Refused})
		}
	}
	return res
}

// view is an Episode's or Issue's pendingGrab: its container's pending
// grab covering it, or none (§6.2).
func (s *Stage) view(ctx context.Context, v *remediation.ItemView) (remediation.ItemResult, error) {
	var pg *catalogv1alpha1.PendingGrab
	switch t := v.Item.(type) {
	case *catalogv1alpha1.Episode:
		var ser catalogv1alpha1.Series
		if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: t.Spec.SeriesRef}, &ser); err != nil && client.IgnoreNotFound(err) != nil {
			return remediation.ItemResult{}, remediation.Transient(err)
		}
		for i := range ser.Status.PendingGrabs {
			for _, n := range ser.Status.PendingGrabs[i].Episodes {
				if n.Season == t.Spec.SeasonNumber && n.Number == t.Spec.EpisodeNumber {
					pg = ser.Status.PendingGrabs[i].DeepCopy()
				}
			}
		}
	case *catalogv1alpha1.Issue:
		var co catalogv1alpha1.Comic
		if err := s.o.Reader.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: t.Spec.ComicRef}, &co); err != nil && client.IgnoreNotFound(err) != nil {
			return remediation.ItemResult{}, remediation.Transient(err)
		}
		for i := range co.Status.PendingGrabs {
			for _, n := range co.Status.PendingGrabs[i].Issues {
				if n == t.Spec.Number && n != "" {
					pg = co.Status.PendingGrabs[i].DeepCopy()
				}
			}
		}
	}
	if pg != nil {
		pg.Episodes, pg.Issues = nil, nil
	}
	var val any
	if pg != nil {
		val = pg
	}
	return remediation.ItemResult{Sets: []itempass.Set{{Manager: k8s.ManagerCatalogGrab, Fields: map[string]any{"pendingGrab": val}}}}, nil
}

// pendingSet is catalog-grab's pending field for kind: pendingGrabs on a
// Series or Comic, pendingGrab (the one held, or none) elsewhere.
func pendingSet(kind remediation.KeyKind, pending []catalogv1alpha1.PendingGrab) itempass.Set {
	fields := map[string]any{}
	switch kind {
	case remediation.KindSeries, remediation.KindComic:
		if len(pending) > 0 {
			fields["pendingGrabs"] = pending
		} else {
			fields["pendingGrabs"] = nil
		}
	default:
		if len(pending) > 0 {
			pg := pending[0]
			fields["pendingGrab"] = &pg
		} else {
			fields["pendingGrab"] = nil
		}
	}
	return itempass.Set{Manager: k8s.ManagerCatalogGrab, Fields: fields}
}

// grabbed is the release.grabbed history event of one new entry (as the
// retired grab worker's publishGrabbed): the history sink records it, and
// it names the entry by id and uid.
func grabbed(gv grabplan.View, e *catalogv1alpha1.DownloadEntry, now time.Time) (remediation.Effect, bool) {
	evt := schema.ReleaseEvent{
		Media:        commonv1.MediaRef{Kind: gv.Kind, Name: gv.Owner.Name},
		Action:       events.ActionGrabbed,
		GUID:         e.Release.GUID,
		Indexer:      e.Release.IndexerName,
		ReleaseGroup: e.Release.ReleaseGroup,
		DownloadRef:  &schema.Ref{Namespace: gv.Owner.Namespace, Name: e.ID, UID: e.UID},
		Quality:      e.Release.Quality,
		FormatScore:  e.Release.FormatScore,
		At:           now.UTC(),
	}
	if e.Release.IndexerRef != "" {
		evt.IndexerRef = &schema.Ref{Namespace: gv.Owner.Namespace, Name: e.Release.IndexerRef}
	}
	name, data, err := schema.Encode(evt)
	if err != nil {
		return nil, false
	}
	id := e.UID + ":grabbed"
	return remediation.History{
		Subject: events.CatalogReleaseSubject(events.ActionGrabbed, gv.Owner.UID),
		MsgID:   id,
		Envelope: &events.Envelope{
			ID: id, Type: "catalog.ReleaseEvent", Schema: name, Source: "manager@" + version.String(),
			Key: gv.Owner.Namespace + "/" + gv.Owner.Name, Time: now.UTC(), Data: data,
		},
	}, true
}
