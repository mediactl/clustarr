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

package artwork

import (
	"context"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"

	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// FetchDurable is the consumer a fetch task is admitted on (R23).
const FetchDurable = events.ConsumerCatalogArtworkFetch

// indexWait is how soon a pass that found the index unsynced asks again.
const indexWait = 10 * time.Second

// Options is what the artwork stages read.
type Options struct {
	Bus events.Bus
	// Index is the manager's objindex over clustarr-artwork. Required.
	Index    *objindex.Index
	Dispatch *dispatch.Ledger
	// Clock is the stage's clock; nil is time.Now.
	Clock func() time.Time
}

// Stage is the artwork fetch stage (remediation.ItemStage and Watcher).
type Stage struct {
	o        Options
	records  *records.Reader[*schema.ItemMetadataRecord]
	failures *catalogartwork.FailureBook
}

var (
	_ remediation.ItemStage = (*Stage)(nil)
	_ remediation.Watcher   = (*Stage)(nil)
)

// New builds the artwork fetch stage.
func New(o Options) *Stage {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Stage{
		o:        o,
		records:  records.NewReader(o.Bus.KV(events.BucketItemMetadata), agentrecords.ItemMetadata()),
		failures: catalogartwork.NewFailureBook(),
	}
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageArtwork }

// Applies implements remediation.ItemStage: the six loop kinds with
// artwork.
func (s *Stage) Applies(k remediation.KeyKind) bool { return applies(k) }

func applies(k remediation.KeyKind) bool {
	switch k {
	case remediation.KindMovie, remediation.KindSeries, remediation.KindAlbum, remediation.KindBook,
		remediation.KindAudiobook, remediation.KindComic:
		return true
	}
	return false
}

// Plan implements remediation.ItemStage: raise ArtworkFetchFailed for each
// failure new to this leader, plan the fetch, and publish it once admitted;
// with nothing to fetch, answer the ledger.
func (s *Stage) Plan(ctx context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	now := v.Now.Time
	if s.o.Index == nil || !s.o.Index.Synced() {
		// Nothing is known of the objects until the index replayed: a plan
		// now would refetch the whole library.
		return remediation.ItemResult{Due: now.Add(indexWait)}, nil
	}
	if v.Item.GetDeletionTimestamp() != nil {
		return remediation.ItemResult{}, nil
	}
	it, err := catalogartwork.ItemOf(v.Item)
	if err != nil {
		return remediation.ItemResult{}, nil
	}
	uid := v.Item.GetUID()
	rec, _, ok, err := s.records.Get(ctx, agentrecords.ItemMetadataKey(string(uid)))
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	if !ok {
		rec = nil
	}

	var res remediation.ItemResult
	if rec != nil {
		for _, f := range rec.ArtworkFailures {
			if s.failures.First(string(uid), f, now) {
				res.Events = append(res.Events, remediation.ItemEvent{
					Recorder: catalogartwork.RecorderGateway, Type: corev1.EventTypeWarning,
					Reason: catalogartwork.ReasonFetchFailed, Action: "FetchArtwork",
					Message: catalogartwork.FailureMessage(f),
				})
			}
		}
	}

	plan := catalogartwork.PlanFetch(
		catalogartwork.ResolveSources(it.Overrides, it.Images),
		catalogartwork.ObjectsFromIndex(s.o.Index, it.Kind, uid),
		catalogartwork.FailuresOf(rec), now)
	res.Due = plan.Due
	key := remediation.ItemLedgerKey(v.Key)
	if plan.Empty() {
		// Every original the item wants is stored: its fetch, if one was
		// in flight, was answered (R23).
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: FetchDurable, Ledger: key, Seq: math.MaxInt64})
		return res, nil
	}
	seq := now.Unix()
	if s.o.Dispatch != nil {
		d := s.o.Dispatch.Admit(FetchDurable, key, seq)
		if !d.Admitted {
			s.o.Dispatch.Waiting(FetchDurable, key, d.Reason)
			res.Due = earliest(res.Due, now.Add(max(d.RetryAfter, time.Second)))
			return res, nil
		}
	}
	subject, env, err := catalogartwork.FetchTask(v.Item, it.Kind, metadatainputs.ItemRef(v.Item), plan, now)
	if err != nil {
		return remediation.ItemResult{}, err
	}
	res.Effects = append(res.Effects, remediation.DispatchPublish{
		Publish: remediation.Publish{Subject: subject, MsgID: env.ID, ExpectStream: events.StreamWorkCatalog, Envelope: env},
		Durable: FetchDurable, Ledger: key, Seq: seq,
	})
	return res, nil
}

// earliest is the earlier of two times, a zero one ignored.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	}
	return b
}
