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

package imageset

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	catalogimagesets "github.com/mediactl/clustarr/app/catalog/imagesets"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Durable is the consumer an image-set task is admitted on (R23).
const Durable = events.ConsumerCatalogImageSet

// keyTMDB is metadata.KeyTMDB's value, spelled here so the manager links
// no pkg/metadata.
const keyTMDB = "tmdb"

// Options is what the stage reads.
type Options struct {
	Reader   client.Reader
	Bus      events.Bus
	Dispatch *dispatch.Ledger
}

// Stage is the image-set stage (remediation.ItemStage and Watcher).
type Stage struct {
	o       Options
	records *catalogimagesets.Records
}

var (
	_ remediation.ItemStage = (*Stage)(nil)
	_ remediation.Watcher   = (*Stage)(nil)
)

// New builds the stage.
func New(o Options) *Stage {
	return &Stage{o: o, records: catalogimagesets.NewRecords(o.Bus)}
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageImageSet }

// Applies implements remediation.ItemStage: movies and series.
func (s *Stage) Applies(k remediation.KeyKind) bool { return applies(k) }

func applies(k remediation.KeyKind) bool {
	return k == remediation.KindMovie || k == remediation.KindSeries
}

// Plan implements remediation.ItemStage.
func (s *Stage) Plan(ctx context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	if v.Item == nil || v.Item.GetDeletionTimestamp() != nil {
		return remediation.ItemResult{}, nil
	}
	in, mode, prev, ok := inputsOf(v.Item)
	if !ok {
		return remediation.ItemResult{}, nil
	}
	pin := catalogimagesets.Input{
		Item: metadatainputs.ItemRef(v.Item), Inputs: in, Mode: mode, Prev: prev, Now: v.Now.Time,
	}
	if mode != catalogv1alpha1.ImageSetNone {
		ready, refresh, err := s.provider(ctx, v.Item.GetNamespace())
		if err != nil {
			return remediation.ItemResult{}, remediation.Transient(err)
		}
		pin.ProviderReady, pin.Refresh = ready, refresh
	}
	uid := string(v.Item.GetUID())
	if pin.ProviderReady && catalogimagesets.Askable(in) {
		rec, rev, ok, err := s.records.Get(ctx, uid)
		if err != nil {
			return remediation.ItemResult{}, remediation.Transient(err)
		}
		pin.Record, pin.RecordRev, pin.RecordOK = rec, rev, ok
	}
	d := catalogimagesets.Plan(pin)
	var res remediation.ItemResult
	res.Due = d.Due
	key := remediation.ItemLedgerKey(v.Key)
	switch {
	case d.Clear:
		res.Sets = []itempass.Set{{Manager: k8s.ManagerCatalogImageSet, Fields: map[string]any{"imageSet": nil}}}
	case d.ImageSet != nil:
		res.Sets = []itempass.Set{{Manager: k8s.ManagerCatalogImageSet, Fields: map[string]any{"imageSet": d.ImageSet}}}
	}
	if d.Answered {
		res.Effects = append(res.Effects, remediation.DispatchAnswered{Durable: Durable, Ledger: key, Seq: math.MaxInt64})
	}
	if d.Republish != nil {
		subject, id, env, err := catalogimagesets.TaskMessage(*d.Republish, v.Now.Time)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.Publish{Subject: subject, MsgID: id, ExpectStream: events.StreamWorkCatalog, Envelope: env})
	}
	if d.Resume != nil {
		subject, id, env, err := catalogimagesets.ResumeMessage(*d.Resume, v.Now.Time)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.Publish{Subject: subject, MsgID: id, ExpectStream: events.StreamWorkCatalog, Envelope: env})
	}
	if d.Request != nil {
		seq := v.Now.Unix()
		if s.o.Dispatch != nil {
			if dec := s.o.Dispatch.Admit(Durable, key, seq); !dec.Admitted {
				s.o.Dispatch.Waiting(Durable, key, dec.Reason)
				res.Due = v.Now.Add(max(dec.RetryAfter, time.Second))
				return res, nil
			}
		}
		value, err := json.Marshal(d.Request)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.RecordWrite{
			Bucket: events.BucketImageSets, Key: events.RecordKey(uid),
			Revision: pin.RecordRev, Value: value, Remediation: "imageset", Lane: "none",
		})
		subject, id, env, err := catalogimagesets.TaskMessage(*d.Request, v.Now.Time)
		if err != nil {
			return res, err
		}
		res.Effects = append(res.Effects, remediation.DispatchPublish{
			Publish: remediation.Publish{Subject: subject, MsgID: id, ExpectStream: events.StreamWorkCatalog, Envelope: env},
			Durable: Durable, Ledger: key, Seq: seq,
		})
	}
	return res, nil
}

// provider reports whether a mediux MetadataProvider in ns is enabled and
// Ready, and the refresh of the first such.
func (s *Stage) provider(ctx context.Context, ns string) (ready bool, refresh time.Duration, err error) {
	var list catalogv1alpha1.MetadataProviderList
	if err := s.o.Reader.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return false, 0, err
	}
	for _, p := range list.Items {
		if p.Spec.Type != catalogv1alpha1.MetadataProviderMediux || (p.Spec.Enabled != nil && !*p.Spec.Enabled) {
			continue
		}
		if !k8s.IsConditionTrue(p.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady) {
			continue
		}
		if m := p.Spec.Mediux; m != nil && m.Refresh != nil && m.Refresh.Duration > 0 {
			refresh = m.Refresh.Duration
		}
		return true, refresh, nil
	}
	return false, 0, nil
}

// inputsOf reads the item's ids and shape: a movie's TMDB id; a series'
// TMDB id from its metadata's externalIDs, its TheTVDB id, and its seasons
// (specials left out) with their episode counts.
func inputsOf(obj client.Object) (in schema.ImageSetInputs, mode catalogv1alpha1.ImageSetMode, prev *catalogv1alpha1.ImageSetStatus, ok bool) {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		in.Kind = string(commonv1.MediaKindMovie)
		if o.Spec.TmdbID > 0 {
			in.TMDBID = strconv.FormatInt(o.Spec.TmdbID, 10)
		}
		in.SetID = o.Spec.ImageSetID
		return in, o.Spec.ImageSet, o.Status.ImageSet, true
	case *catalogv1alpha1.Series:
		in.Kind = string(commonv1.MediaKindSeries)
		if o.Spec.TvdbID > 0 {
			in.TVDBID = strconv.FormatInt(o.Spec.TvdbID, 10)
		}
		if md := o.Status.Metadata; md != nil {
			in.TMDBID = md.ExternalIDs[keyTMDB]
		}
		for _, se := range o.Status.Seasons {
			if se.Number == 0 {
				continue
			}
			in.Seasons = append(in.Seasons, se.Number)
			if se.EpisodeCount > 0 {
				if in.Episodes == nil {
					in.Episodes = map[string]int32{}
				}
				in.Episodes[strconv.FormatInt(int64(se.Number), 10)] = se.EpisodeCount
			}
		}
		in.SetID = o.Spec.ImageSetID
		return in, o.Spec.ImageSet, o.Status.ImageSet, true
	}
	return in, "", nil, false
}

// Sources implements remediation.Watcher: a worker's clustarr-image-sets
// answer wakes its item.
func (s *Stage) Sources(ctrl.Manager) ([]remediation.Source, error) {
	src := recordsource.NewItems(s.o.Bus, events.BucketImageSets, func(item schema.ItemRef, _ []byte) []remediation.Key {
		k := remediation.KeyKind(item.Kind)
		if !applies(k) || item.Name == "" {
			return nil
		}
		return []remediation.Key{{Kind: k, Namespace: item.Namespace, Name: item.Name}}
	})
	wake := func(ctx context.Context, o client.Object, q workqueue.TypedRateLimitingInterface[remediation.Key]) {
		wakeLow(q, itemsOf(ctx, s.o.Reader, o.GetNamespace()))
	}
	return []remediation.Source{
		{Name: "S27/" + events.BucketImageSets, Raw: src},
		// A mediux provider created, deleted, edited or turning Ready wakes
		// its namespace's Movies and Series: an item with no image set and
		// no provider is due never, so without this nothing was asked once
		// a provider went Ready (talos, 2026-10-28). Low priority: the
		// dispatch ledger admits the lookups a few at a time anyway.
		{
			Name: "S33/MetadataProvider", Object: &catalogv1alpha1.MetadataProvider{},
			Handler: handler.TypedFuncs[client.Object, remediation.Key]{
				CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
					wake(ctx, e.Object, q)
				},
				UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
					wake(ctx, e.ObjectNew, q)
				},
				DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
					wake(ctx, e.Object, q)
				},
			},
			Predicates: []predicate.Predicate{k8s.And(predicate.NewPredicateFuncs(isMediux), k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(providerReady)))},
		},
	}, nil
}

// isMediux is whether o is a mediux MetadataProvider.
func isMediux(o client.Object) bool {
	p, ok := o.(*catalogv1alpha1.MetadataProvider)
	return ok && p.Spec.Type == catalogv1alpha1.MetadataProviderMediux
}

// providerReady is a provider's Ready condition, which a turn of wakes
// its namespace's items.
func providerReady(o client.Object) bool {
	p, ok := o.(*catalogv1alpha1.MetadataProvider)
	return ok && k8s.IsConditionTrue(p.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
}

// itemsOf is every Movie and Series key in ns, from the cache.
func itemsOf(ctx context.Context, r client.Reader, ns string) []remediation.Key {
	var keys []remediation.Key
	var movies catalogv1alpha1.MovieList
	if err := r.List(ctx, &movies, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("imageset: list Movies for a mediux provider change", "namespace", ns, "err", err)
	}
	for i := range movies.Items {
		keys = append(keys, remediation.Key{Kind: remediation.KindMovie, Namespace: ns, Name: movies.Items[i].Name})
	}
	var series catalogv1alpha1.SeriesList
	if err := r.List(ctx, &series, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("imageset: list Series for a mediux provider change", "namespace", ns, "err", err)
	}
	for i := range series.Items {
		keys = append(keys, remediation.Key{Kind: remediation.KindSeries, Namespace: ns, Name: series.Items[i].Name})
	}
	return keys
}

// wakeLow enqueues keys at handler.LowPriority when q is a priority queue.
func wakeLow(q workqueue.TypedRateLimitingInterface[remediation.Key], keys []remediation.Key) {
	if pq, ok := q.(priorityqueue.PriorityQueue[remediation.Key]); ok {
		pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(handler.LowPriority)}, keys...)
		return
	}
	for _, k := range keys {
		q.Add(k)
	}
}
