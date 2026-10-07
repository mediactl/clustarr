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

package search

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// OwnerEntries is the live grab entries covering ref (ADR-0019 §6.2): an
// Episode's are its Series' entries covering its number, an Issue's its
// Comic's, every other item's its own status.downloads. "Live" is
// rollup.EntryNonTerminal, the set the item kinds derive
// status.activeDownloadRef from, so the queue and the ref never disagree. A
// missing item reads as no entries.
func OwnerEntries(ctx context.Context, c client.Reader, ns string, ref commonv1.MediaRef) ([]catalogv1alpha1.DownloadEntry, error) {
	key := client.ObjectKey{Namespace: ns, Name: ref.Name}
	var (
		entries []catalogv1alpha1.DownloadEntry
		covers  func(*catalogv1alpha1.DownloadEntry) bool
	)
	switch ref.Kind {
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := c.Get(ctx, key, &ep); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ep.Spec.SeriesRef}, &s); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		entries = s.Status.Downloads
		covers = func(e *catalogv1alpha1.DownloadEntry) bool {
			return rollup.CoversEpisode(e, ep.Name, ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber)
		}
	case commonv1.MediaKindIssue:
		var is catalogv1alpha1.Issue
		if err := c.Get(ctx, key, &is); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		var co catalogv1alpha1.Comic
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: is.Spec.ComicRef}, &co); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		entries = co.Status.Downloads
		covers = func(e *catalogv1alpha1.DownloadEntry) bool { return rollup.CoversIssue(e, is.Name, is.Spec.Number) }
	default:
		obj, ok := ownerObject(ref.Kind)
		if !ok {
			return nil, nil
		}
		if err := c.Get(ctx, key, obj); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		entries = entriesOf(obj)
	}
	out := make([]catalogv1alpha1.DownloadEntry, 0, len(entries))
	for i := range entries {
		if e := &entries[i]; rollup.EntryNonTerminal(e) && (covers == nil || covers(e)) {
			out = append(out, *e)
		}
	}
	return out, nil
}

// QueuedFor is the decision engine's queue for ref: the live video grabs
// covering it (an audio donor is no video candidate, so an upgrade never
// waits on a dub).
func QueuedFor(ctx context.Context, c client.Reader, ns string, ref commonv1.MediaRef) ([]decision.Queued, error) {
	entries, err := OwnerEntries(ctx, c, ns, ref)
	if err != nil {
		return nil, fmt.Errorf("read the grabs of %s/%s: %w", ref.Kind, ref.Name, err)
	}
	out := make([]decision.Queued, 0, len(entries))
	for i := range entries {
		if rollup.IsDonor(&entries[i]) {
			continue
		}
		rel := entries[i].Release
		out = append(out, quality.Candidate{Quality: rel.Quality, Revision: rel.Revision, FormatScore: int(rel.FormatScore)})
	}
	return out, nil
}

// ownerObject is an empty object of an owner kind.
func ownerObject(kind commonv1.MediaKind) (client.Object, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.Movie{}, true
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.Series{}, true
	case commonv1.MediaKindAlbum:
		return &catalogv1alpha1.Album{}, true
	case commonv1.MediaKindBook:
		return &catalogv1alpha1.Book{}, true
	case commonv1.MediaKindAudiobook:
		return &catalogv1alpha1.Audiobook{}, true
	case commonv1.MediaKindComic:
		return &catalogv1alpha1.Comic{}, true
	}
	return nil, false
}

// entriesOf is an owner object's status.downloads.
func entriesOf(o client.Object) []catalogv1alpha1.DownloadEntry {
	switch t := o.(type) {
	case *catalogv1alpha1.Movie:
		return t.Status.Downloads
	case *catalogv1alpha1.Series:
		return t.Status.Downloads
	case *catalogv1alpha1.Album:
		return t.Status.Downloads
	case *catalogv1alpha1.Book:
		return t.Status.Downloads
	case *catalogv1alpha1.Audiobook:
		return t.Status.Downloads
	case *catalogv1alpha1.Comic:
		return t.Status.Downloads
	}
	return nil
}

// normalizeInfoHash lower-cases an info hash so a v1 hash written in upper
// case by one indexer still matches the same torrent reported in lower case
// by another.
func normalizeInfoHash(h string) string { return strings.ToLower(h) }

// ownerRef is a grab owner's schema.ItemRef: the object whose
// status.downloads would hold the grab, and whose BlockScopeOf names its item
// blocks (ADR-0019 §6.1, §6.14).
func ownerRef(kind string, o client.Object) schema.ItemRef {
	return schema.ItemRef{Kind: kind, Ref: schema.Ref{
		Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID()),
	}}
}

// AnswerBlocklist is decision.Target.Blocklist over an index answer's block
// state (ADR-0019 §6.14): every release the answer marked Blocked -- for the
// request's scope or globally -- by its info hash and, for usenet, which has
// no hash, by its normalized title. The release index decides what is
// blocked; this only looks the answer up.
func AnswerBlocklist(rels []schema.Release) func(infohash, title string) bool {
	hashes := map[string]struct{}{}
	titles := map[string]struct{}{}
	for i := range rels {
		if rels[i].Blocked == nil {
			continue
		}
		addBlocked(hashes, titles, rels[i].Info.InfoHash, rels[i].Info.Title)
	}
	return lookup(hashes, titles)
}

// ScopedBlocklist is decision.Target.Blocklist over firehose releases'
// Blocks: each release blocked for scope or globally (the RSS matcher keeps
// only those naming its matched item or every item).
func ScopedBlocklist(rels []schema.Release, scope string) func(infohash, title string) bool {
	hashes := map[string]struct{}{}
	titles := map[string]struct{}{}
	for i := range rels {
		for _, b := range rels[i].Blocks {
			if b.Scope == scope || b.Scope == schema.BlockScopeGlobal {
				addBlocked(hashes, titles, rels[i].Info.InfoHash, rels[i].Info.Title)
				break
			}
		}
	}
	return lookup(hashes, titles)
}

func addBlocked(hashes, titles map[string]struct{}, infohash, title string) {
	if infohash != "" {
		hashes[normalizeInfoHash(infohash)] = struct{}{}
	}
	if t := blocklistTitleKey(title); t != "" {
		titles[t] = struct{}{}
	}
}

func lookup(hashes, titles map[string]struct{}) func(infohash, title string) bool {
	return func(infohash, title string) bool {
		if infohash != "" {
			if _, ok := hashes[normalizeInfoHash(infohash)]; ok {
				return true
			}
		}
		if t := blocklistTitleKey(title); t != "" {
			_, ok := titles[t]
			return ok
		}
		return false
	}
}

// blocklistTitleKey normalizes a release title for blocklist equality. It is
// release.TitleNorm, which keeps letters and digits in every script, rather
// than release.CleanTitle, which keeps only ASCII: under CleanTitle a
// non-Latin usenet title keyed as its ASCII residue, so "マトリックス.1999.
// 1080p-GRP" and every other non-Latin release that year from that group
// shared the key "1999 1080pgrp" -- blocklisting one blocklisted them all.
// On printable ASCII the two agree (pkg/release pins it), so every Latin
// title keys exactly as before.
func blocklistTitleKey(title string) string { return release.TitleNorm(title) }
