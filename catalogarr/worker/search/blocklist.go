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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/release"
)

// IndexDownloadTarget is the field index name, used both by
// RegisterDownloadIndexes and by the worker's own List calls. It is exported
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

// RegisterDownloadIndexes adds the one field index the search worker needs on
// Download: "is there already an active Download for this target" (the
// queue). Call it once per manager, before the cache starts.
//
// The blocklist has no index. It is one List of the Downloads carrying
// download.clustarr.io/blocklisted per decision (LoadBlocklist) -- see that
// label's doc comment for why the blocklist has no CRD of its own -- and the
// two blocklist indexes this used to register, by info hash and by title,
// were read by nothing once LoadBlocklist replaced the per-release lookups.
func RegisterDownloadIndexes(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexDownloadTarget, func(o client.Object) []string {
		d, ok := o.(*downloadv1alpha1.Download)
		if !ok || !rollup.DownloadNonTerminal(d) {
			return nil
		}
		return []string{TargetIndexValue(d.Spec.Target)}
	})
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

func isBlocklisted(d *downloadv1alpha1.Download) bool {
	return d.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue
}

// blocklistActive reports whether a labelled Download is still blocklisted at
// now. A nil deadline means "forever": grabarr sets one when it blocklists,
// and a missing one is not a licence to grab the release again.
func blocklistActive(d *downloadv1alpha1.Download, now time.Time) bool {
	if d.Status.BlocklistedUntil == nil {
		return true
	}
	return d.Status.BlocklistedUntil.After(now)
}

// Blocklist is one namespace's live blocklist at one instant: the info hashes
// and normalized titles of every Download grabarr has labelled blocklisted
// whose status.blocklistedUntil has not passed. Contains is
// decision.Target.Blocklist.
//
// It is loaded once per decision (LoadBlocklist) rather than looked up per
// release. The per-release form cost two cache Lists for every candidate --
// up to a thousand for one 500-release search -- where one List of the
// labelled set answers all of them.
type Blocklist struct {
	hashes map[string]struct{}
	titles map[string]struct{}
}

// LoadBlocklist reads the namespace's blocklisted Downloads in ONE List and
// keeps those still blocklisted at now (a nil blocklistedUntil means forever:
// grabarr sets one when it blocklists, and its absence is not a licence to
// grab the release again). Expiry is applied here, at read time, because
// nothing writes the object when its deadline passes: a field index or label
// selector computed when the object changed would go stale the moment the
// deadline passed.
//
// A List failure is returned, not swallowed. The per-release form treated a
// failed lookup as "not blocklisted" with a warning, which turned a transient
// cache error into a grab of a release an operator had blocklisted -- and
// nothing downstream re-checks. Retrying the decision costs one redelivery.
func LoadBlocklist(ctx context.Context, c client.Reader, ns string, now time.Time) (Blocklist, error) {
	var list downloadv1alpha1.DownloadList
	if err := c.List(ctx, &list,
		client.InNamespace(ns),
		client.MatchingLabels{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue},
	); err != nil {
		return Blocklist{}, fmt.Errorf("list blocklisted Downloads in %s: %w", ns, err)
	}
	b := Blocklist{hashes: map[string]struct{}{}, titles: map[string]struct{}{}}
	for i := range list.Items {
		d := &list.Items[i]
		if !isBlocklisted(d) || !blocklistActive(d, now) {
			continue
		}
		if h := d.Spec.Release.InfoHash; h != "" {
			b.hashes[normalizeInfoHash(h)] = struct{}{}
		}
		if t := blocklistTitleKey(d.Spec.Release.Title); t != "" {
			b.titles[t] = struct{}{}
		}
	}
	return b, nil
}

// Contains reports whether a release is blocklisted: its info hash (torrent)
// or its normalized title (usenet, which has no hash) is on the list.
func (b Blocklist) Contains(infohash, title string) bool {
	if infohash != "" {
		if _, ok := b.hashes[normalizeInfoHash(infohash)]; ok {
			return true
		}
	}
	if t := blocklistTitleKey(title); t != "" {
		_, ok := b.titles[t]
		return ok
	}
	return false
}

// Len is how many distinct keys the blocklist holds, for logging.
func (b Blocklist) Len() int { return len(b.hashes) + len(b.titles) }

// blocklistTitleKey normalizes a release title for blocklist equality. It is
// release.TitleNorm, which keeps letters and digits in every script, rather
// than release.CleanTitle, which keeps only ASCII: under CleanTitle a
// non-Latin usenet title keyed as its ASCII residue, so "マトリックス.1999.
// 1080p-GRP" and every other non-Latin release that year from that group
// shared the key "1999 1080pgrp" -- blocklisting one blocklisted them all.
// On printable ASCII the two agree (pkg/release pins it), so every Latin
// title keys exactly as before.
func blocklistTitleKey(title string) string { return release.TitleNorm(title) }
