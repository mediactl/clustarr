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
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/release"
)

// IndexDownloadTarget is the field index name, used both by
// FieldIndexes and by the worker's own List calls. It is exported
// so a task that shares a manager with this worker can reuse the index
// instead of registering a second, conflicting one under a different name.
//
// It indexes non-terminal Downloads by their target catalog item: the live
// queue for one media key. "Non-terminal" is rollup.DownloadNonTerminal, the
// set the item reconcilers derive status.activeDownloadRef from and the grab
// path's double-grab guard reads, so the three can never disagree about one
// Download: a Seeding torrent still occupies the queue (its content is one
// import away), Imported, Failed, Blocklisted and Removing do not, and
// neither does a Download already being deleted.
const IndexDownloadTarget = "search.clustarr.io/download-target"

// FieldIndexes declares the one index the search worker reads on Download:
// the per-target live queue (IndexDownloadTarget). The RSS matcher reads it
// too, so the catalog and events domains both declare it and the process
// registers it once (spec §5.7).
func FieldIndexes() []k8s.FieldIndex {
	return []k8s.FieldIndex{{Object: &downloadv1alpha1.Download{}, Name: IndexDownloadTarget, Extract: downloadTargetKeys}}
}

// downloadTargetKeys is IndexDownloadTarget's value function: a non-terminal
// Download's target, and nothing for any other.
func downloadTargetKeys(o client.Object) []string {
	d, ok := o.(*downloadv1alpha1.Download)
	if !ok || !rollup.DownloadNonTerminal(d) {
		return nil
	}
	return []string{TargetIndexValue(d.Spec.Target)}
}

// TargetIndexValue is the IndexDownloadTarget key for one catalog item. It is
// not events.MediaKey: the index is scoped to a namespace by the List call
// itself, so the kind and name alone identify the target, and keeping the
// value readable makes `kubectl get downloads` debugging match what the
// worker sees.
func TargetIndexValue(ref commonv1.MediaRef) string {
	return string(ref.Kind) + "/" + ref.Name
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
