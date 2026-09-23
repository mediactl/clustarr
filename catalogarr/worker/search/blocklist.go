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
	"github.com/mediactl/clustarr/pkg/release"
)

// Field index names, used both by RegisterDownloadIndexes and by the worker's
// own List calls. They are exported so a task that shares a manager with this
// worker can reuse the indexes instead of registering a second, conflicting
// one under a different name.
const (
	// IndexBlocklistInfoHash indexes blocklisted Downloads by
	// spec.release.infoHash.
	//
	// Neither blocklist index is read by the decision paths any more: both
	// load the whole namespace blocklist in one List (LoadBlocklist) instead
	// of two indexed lookups per release. They stay registered because
	// catalogarr's startup assertion (assertWorkerIndexes) names them.
	IndexBlocklistInfoHash = "search.clustarr.io/blocklist-infohash"
	// IndexBlocklistTitle indexes blocklisted Downloads by the normalized
	// spec.release.title, which is how a usenet release -- one with no info
	// hash -- is recognised again.
	IndexBlocklistTitle = "search.clustarr.io/blocklist-title"
	// IndexDownloadTarget indexes non-terminal Downloads by their target
	// catalog item: the live queue for one media key.
	IndexDownloadTarget = "search.clustarr.io/download-target"
)

// RegisterDownloadIndexes adds the three field indexes the search worker needs
// on Download: two for the live blocklist (spec.release.infoHash and a
// normalized spec.release.title, restricted to Downloads carrying
// download.clustarr.io/blocklisted -- see that label's doc comment for why the
// blocklist has no CRD of its own), and one for "is there already an active
// Download for this target" (the queue). Call it once per manager, before the
// cache starts.
//
// Expiry is deliberately NOT part of the blocklist indexes. A field index is
// computed when an object changes, so a time-based predicate baked into it
// would go stale the moment the deadline passed without anything writing the
// object. The index narrows to the labelled set; blocklisted() applies
// status.blocklistedUntil at read time, against the worker's own clock.
func RegisterDownloadIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexBlocklistInfoHash, func(o client.Object) []string {
		d, ok := o.(*downloadv1alpha1.Download)
		if !ok || !isBlocklisted(d) || d.Spec.Release.InfoHash == "" {
			return nil
		}
		return []string{normalizeInfoHash(d.Spec.Release.InfoHash)}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexBlocklistTitle, func(o client.Object) []string {
		d, ok := o.(*downloadv1alpha1.Download)
		if !ok || !isBlocklisted(d) || d.Spec.Release.Title == "" {
			return nil
		}
		return []string{release.CleanTitle(d.Spec.Release.Title)}
	}); err != nil {
		return err
	}
	return idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexDownloadTarget, func(o client.Object) []string {
		d, ok := o.(*downloadv1alpha1.Download)
		if !ok || isTerminal(d.Status.Phase) {
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

// isTerminal reports whether a Download can no longer occupy the queue. A
// Seeding torrent is NOT terminal: its content is on disk and a second grab
// for the same item would be a duplicate. Blocklisted and Removing are
// terminal for queue purposes -- the first is why the blocklist indexes
// exist, the second is on its way out.
func isTerminal(p downloadv1alpha1.DownloadPhase) bool {
	switch p {
	case downloadv1alpha1.DownloadPhaseImported,
		downloadv1alpha1.DownloadPhaseFailed,
		downloadv1alpha1.DownloadPhaseBlocklisted,
		downloadv1alpha1.DownloadPhaseRemoving:
		return true
	default:
		return false
	}
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
// grab the release again). Expiry is applied here, at read time, for the same
// reason RegisterDownloadIndexes leaves it out of the indexes: nothing writes
// the object when its deadline passes.
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
