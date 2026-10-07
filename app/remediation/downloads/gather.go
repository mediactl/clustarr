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
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// owner is what the stage reads of an owner item.
type owner struct {
	obj        client.Object
	ref        schema.ItemRef
	kind       commonv1.MediaKind
	entries    []catalogv1alpha1.DownloadEntry
	nonces     *catalogv1alpha1.DownloadNonces
	monitored  bool
	hasFile    bool
	providerID string
	entryCap   int
	searched   *catalogv1alpha1.Dispatch
}

// ownerOf reads an owner kind's fields; false for any other object.
func ownerOf(o client.Object) (owner, bool) {
	ref := func(kind string) schema.ItemRef {
		return schema.ItemRef{Kind: kind, Ref: schema.Ref{Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID())}}
	}
	switch t := o.(type) {
	case *catalogv1alpha1.Movie:
		return owner{
			obj: o, ref: ref("Movie"), kind: commonv1.MediaKindMovie, entries: t.Status.Downloads, nonces: t.Status.DownloadNonces,
			monitored: ptr.Deref(t.Spec.Monitored, true), hasFile: t.Status.HasFile,
			providerID: "tmdb:" + strconv.FormatInt(t.Spec.TmdbID, 10), entryCap: catalogv1alpha1.MaxOwnerEntries,
			searched: t.Status.SearchDispatch,
		}, true
	case *catalogv1alpha1.Series:
		return owner{
			obj: o, ref: ref("Series"), kind: commonv1.MediaKindSeries, entries: t.Status.Downloads, nonces: t.Status.DownloadNonces,
			monitored:  ptr.Deref(t.Spec.Monitored, true),
			providerID: "tvdb:" + strconv.FormatInt(t.Spec.TvdbID, 10), entryCap: catalogv1alpha1.MaxContainerEntries,
		}, true
	case *catalogv1alpha1.Album:
		return owner{
			obj: o, ref: ref("Album"), kind: commonv1.MediaKindAlbum, entries: t.Status.Downloads, nonces: t.Status.DownloadNonces,
			monitored: ptr.Deref(t.Spec.Monitored, true), hasFile: t.Status.Quality != nil,
			providerID: "musicbrainz:" + t.Spec.ReleaseGroupID, entryCap: catalogv1alpha1.MaxOwnerEntries,
			searched: t.Status.SearchDispatch,
		}, true
	case *catalogv1alpha1.Book:
		return owner{
			obj: o, ref: ref("Book"), kind: commonv1.MediaKindBook, entries: t.Status.Downloads, nonces: t.Status.DownloadNonces,
			monitored: ptr.Deref(t.Spec.Monitored, true), hasFile: t.Status.HasFile,
			providerID: "openlibrary:" + t.Spec.WorkID, entryCap: catalogv1alpha1.MaxOwnerEntries,
			searched: t.Status.SearchDispatch,
		}, true
	case *catalogv1alpha1.Audiobook:
		return owner{
			obj: o, ref: ref("Audiobook"), kind: commonv1.MediaKindAudiobook, entries: t.Status.Downloads, nonces: t.Status.DownloadNonces,
			monitored: ptr.Deref(t.Spec.Monitored, true), hasFile: t.Status.HasFile,
			providerID: "asin:" + t.Spec.ASIN, entryCap: catalogv1alpha1.MaxOwnerEntries,
			searched: t.Status.SearchDispatch,
		}, true
	case *catalogv1alpha1.Comic:
		return owner{
			obj: o, ref: ref("Comic"), kind: commonv1.MediaKindComic, entries: t.Status.Downloads, nonces: t.Status.DownloadNonces,
			monitored:  ptr.Deref(t.Spec.Monitored, true),
			providerID: string(t.Spec.Source) + ":" + t.Spec.SourceID, entryCap: catalogv1alpha1.MaxContainerEntries,
		}, true
	}
	return owner{}, false
}

// providerIDOf is an owner object's provider id, "" for any other object.
func providerIDOf(o client.Object) string {
	ow, _ := ownerOf(o)
	return ow.providerID
}

// gather builds lifecycle's view of one owner from the cache and the
// records buckets: no apiserver read on the hot path (A3.5 step 1).
func (s *Stage) gather(ctx context.Context, ow owner, now time.Time) (lifecycle.View, error) {
	ns := ow.ref.Namespace
	v := lifecycle.View{
		Owner: ow.ref, ProviderID: ow.providerID, Kind: ow.kind, Now: now,
		Deleting:  ow.obj.GetDeletionTimestamp() != nil,
		Monitored: ow.monitored, HasFile: ow.hasFile, EntryCap: ow.entryCap,
		Stored:    ow.entries,
		Intents:   lifecycle.ParseIntents(ow.obj.GetAnnotations()),
		Transfers: map[types.UID]lifecycle.Transfer{},
		Engines:   map[string]lifecycle.Engine{},
		Files:     map[string]int{},
		Covered:   map[types.UID][]schema.ItemRef{},
		Searched:  map[schema.ItemRef]time.Time{},
	}
	if ow.nonces != nil {
		v.Nonces = *ow.nonces
	}

	uids := make([]types.UID, 0, len(ow.entries))
	var outputs []string
	for i := range ow.entries {
		e := &ow.entries[i]
		uids = append(uids, types.UID(e.UID))
		if e.OutputPath != "" {
			outputs = append(outputs, e.OutputPath)
		}
		rec, rev, ok, err := s.transfers.Get(ctx, agentrecords.TransferKey(e.UID))
		if err != nil {
			return v, fmt.Errorf("downloads: transfer record of %s: %w", e.ID, err)
		}
		if ok {
			v.Transfers[types.UID(e.UID)] = lifecycle.Transfer{Record: *rec, Revision: rev}
		}
		var files catalogv1alpha1.MediaFileList
		if err := s.o.Reader.List(ctx, &files, client.InNamespace(ns), client.MatchingFields{mfindex.DownloadRef: e.ID}); err != nil {
			return v, fmt.Errorf("downloads: MediaFiles of %s: %w", e.ID, err)
		}
		v.Files[e.ID] = len(files.Items)
	}
	v.Unclaimed = s.unclaimed.forOwner(keyOfRef(ow.ref))
	for _, t := range v.Unclaimed {
		if t.Record.OutputPath != "" {
			outputs = append(outputs, t.Record.OutputPath)
		}
	}

	clients, err := s.clients(ctx, ns)
	if err != nil {
		return v, err
	}
	v.Clients = clients
	for _, c := range clients {
		for o := range max(c.Replicas, 1) {
			if err := s.engine(ctx, c, o, v.Engines); err != nil {
				return v, err
			}
		}
	}

	var idxs indexv1alpha1.IndexerList
	if err := s.o.Reader.List(ctx, &idxs, client.InNamespace(ns)); err != nil {
		return v, fmt.Errorf("downloads: list Indexers: %w", err)
	}
	v.IndexerClient = map[string]string{}
	for i := range idxs.Items {
		if ref := idxs.Items[i].Spec.DownloadClientRef; ref != nil && *ref != "" {
			v.IndexerClient[idxs.Items[i].Name] = *ref
		}
	}

	if err := s.covered(ctx, ow, &v); err != nil {
		return v, err
	}

	v.Blocks, v.Unblocked = s.blocks.replies(uids)
	v.Removed = s.removed.snapshot(outputs)
	v.Issued = s.issued.get(uids)
	return v, nil
}

// clients is the namespace's DownloadClients as lifecycle reads them, with
// today's defaults: removeCompleted true, a torrent stall of 24 h, a usenet
// stall only when set.
func (s *Stage) clients(ctx context.Context, ns string) ([]lifecycle.Client, error) {
	var list downloadv1alpha1.DownloadClientList
	if err := s.o.Reader.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("downloads: list DownloadClients: %w", err)
	}
	out := make([]lifecycle.Client, 0, len(list.Items))
	for i := range list.Items {
		dc := &list.Items[i]
		c := lifecycle.Client{
			Name: dc.Name, UID: dc.UID, Protocol: dc.Spec.Protocol,
			Enabled: ptr.Deref(dc.Spec.Enabled, true), Priority: dc.Spec.Priority, Replicas: dc.Spec.Replicas,
			RemoveCompleted: true, Categories: dc.Spec.Categories, ResyncSeq: dc.Status.ResyncSeq,
		}
		if t := dc.Spec.Torrent; t != nil {
			c.RemoveCompleted = ptr.Deref(t.RemoveCompleted, true)
			c.StallTimeout = lifecycle.StallTimeout
			if t.StallTimeout != nil {
				c.StallTimeout = t.StallTimeout.Duration
			}
			c.Seed = t.Seed
		}
		if u := dc.Spec.Usenet; u != nil {
			c.HealthAction = u.HealthAction
			if u.StallTimeout != nil {
				c.StallTimeout = u.StallTimeout.Duration
			}
			if u.DownloadTimeout != nil {
				c.DownloadTimeout = u.DownloadTimeout.Duration
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// engine reads one engine instance's record into engines.
func (s *Stage) engine(ctx context.Context, c lifecycle.Client, ordinal int32, engines map[string]lifecycle.Engine) error {
	name := lifecycle.EngineName(c.Name, ordinal)
	rec, _, ok, err := s.engines.Get(ctx, agentrecords.EngineKey(string(c.UID), ordinal))
	if err != nil {
		return fmt.Errorf("downloads: engine record of %s: %w", name, err)
	}
	if !ok {
		return nil
	}
	s.boots.see(name, rec.BootID, rec.Reattached, rec.At)
	engines[name] = lifecycle.Engine{
		Name: name, Client: c.Name, Ordinal: ordinal,
		Ready: rec.Ready, Reattached: rec.Reattached, BootID: rec.BootID, ResyncSeq: rec.ResyncSeq,
		Active: rec.Active, Queued: rec.Queued, At: rec.At,
		ReattachedAt: s.boots.reattachedAt(name, rec.BootID),
	}
	return nil
}

// covered fills each entry's covered items and their last search dispatch
// (R25): a Series' or Comic's Episodes or Issues by number, else the owner.
func (s *Stage) covered(ctx context.Context, ow owner, v *lifecycle.View) error {
	ns := ow.ref.Namespace
	switch ow.kind {
	case commonv1.MediaKindSeries:
		var eps catalogv1alpha1.EpisodeList
		if err := s.o.Reader.List(ctx, &eps, client.InNamespace(ns), client.MatchingFields{series.EpisodeBySeriesRefIndex: ow.ref.Name}); err != nil {
			return fmt.Errorf("downloads: list Episodes: %w", err)
		}
		for i := range ow.entries {
			e := &ow.entries[i]
			for j := range eps.Items {
				ep := &eps.Items[j]
				if !coversNumber(e.Episodes, ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber) {
					continue
				}
				ref := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: ep.Name, UID: string(ep.UID)}}
				v.Covered[types.UID(e.UID)] = append(v.Covered[types.UID(e.UID)], ref)
				if d := ep.Status.SearchDispatch; d != nil {
					v.Searched[ref] = d.DispatchedAt.UTC()
				}
			}
		}
	case commonv1.MediaKindComic:
		var issues catalogv1alpha1.IssueList
		if err := s.o.Reader.List(ctx, &issues, client.InNamespace(ns), client.MatchingFields{comic.IssueByComicRefIndex: ow.ref.Name}); err != nil {
			return fmt.Errorf("downloads: list Issues: %w", err)
		}
		for i := range ow.entries {
			e := &ow.entries[i]
			for j := range issues.Items {
				iss := &issues.Items[j]
				if !containsString(e.Issues, iss.Spec.Number) {
					continue
				}
				ref := schema.ItemRef{Kind: "Issue", Ref: schema.Ref{Namespace: ns, Name: iss.Name, UID: string(iss.UID)}}
				v.Covered[types.UID(e.UID)] = append(v.Covered[types.UID(e.UID)], ref)
				if d := iss.Status.SearchDispatch; d != nil {
					v.Searched[ref] = d.DispatchedAt.UTC()
				}
			}
		}
	default:
		if ow.searched != nil {
			v.Searched[ow.ref] = ow.searched.DispatchedAt.UTC()
		}
	}
	return nil
}

func coversNumber(ns []catalogv1alpha1.EpisodeNumber, season, number int32) bool {
	for _, n := range ns {
		if n.Season == season && n.Number == number {
			return true
		}
	}
	return false
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s && s != "" {
			return true
		}
	}
	return false
}

// episodesOf lists a Series' Episodes from the cache, nil on an error (a
// command then selects by season and episode numbers alone).
func (s *Stage) episodesOf(ctx context.Context, ow owner) []catalogv1alpha1.Episode {
	var eps catalogv1alpha1.EpisodeList
	if err := s.o.Reader.List(ctx, &eps, client.InNamespace(ow.ref.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: ow.ref.Name}); err != nil {
		return nil
	}
	return eps.Items
}

// enrichSelection adds to a command's episode selection what the engine
// used to read off each covered Episode (ADR-0019 §6.7: the selection
// replaces the engine's Episode reads): its absolute number, its scene
// numbering, and its air date with a day either side (a release dated in
// another timezone).
func enrichSelection(sel *schema.TransferSelection, eps []catalogv1alpha1.Episode) *schema.TransferSelection {
	out := *sel
	out.Episodes = append([]schema.EpisodeNo(nil), sel.Episodes...)
	out.Absolutes = append([]int32(nil), sel.Absolutes...)
	out.AirDates = append([]string(nil), sel.AirDates...)
	for i := range eps {
		ep := &eps[i]
		covered := false
		for _, n := range sel.Episodes {
			if n.Season == ep.Spec.SeasonNumber && n.Number == ep.Spec.EpisodeNumber {
				covered = true
				break
			}
		}
		if !covered {
			continue
		}
		if ep.Status.AbsoluteNumber != nil {
			out.Absolutes = append(out.Absolutes, *ep.Status.AbsoluteNumber)
		}
		if sn := ep.Status.SceneNumbering; sn != nil {
			if sn.Episode != nil {
				season := ep.Spec.SeasonNumber
				if sn.Season != nil {
					season = *sn.Season
				}
				out.Episodes = append(out.Episodes, schema.EpisodeNo{Season: season, Number: *sn.Episode})
			}
			if sn.Absolute != nil {
				out.Absolutes = append(out.Absolutes, *sn.Absolute)
			}
		}
		if ep.Status.AirDate != nil {
			day := ep.Status.AirDate.UTC()
			for _, d := range []int{-1, 0, 1} {
				out.AirDates = append(out.AirDates, day.AddDate(0, 0, d).Format(time.DateOnly))
			}
		}
	}
	return &out
}
