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

package projection

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// LibraryItem is one row on the Library page (amendment §A3.4): a thin,
// generic view over any catalog kind the shared projection already lists for
// the Pipeline page (Task G3-3). It deliberately does not duplicate
// pkg/pipeline's per-kind title derivation -- Ref, Kind and Title come
// straight from the [pipeline.Entry] built for the same item in the same
// tick, per [buildLibraryItems] -- and adds only what the Pipeline page has
// no use for: whether the item is monitored, and its own status.phase and
// hasFile (or their closest equivalent for a collection kind that has
// neither -- see [describeLibraryItem]).
type LibraryItem struct {
	// Ref identifies the catalog item, exactly as pipeline.Entry.Ref does.
	Ref types.NamespacedName

	// Kind is the item's media kind.
	Kind commonv1.MediaKind

	// Title is the item's display title.
	Title string

	// Monitored is spec.monitored, defaulting to true for a nil pointer --
	// every catalog kind's CRD defaults spec.monitored=true, so a hand-built
	// object with the field left nil (a test fixture, or an object read
	// before a defaulting webhook/apiserver default has applied) renders the
	// same as the CRD default rather than as unmonitored.
	Monitored bool

	// Phase is status.phase, or "" for a kind with no phase concept: Artist,
	// Author and Comic are collection parents (they own Albums, Books and
	// Issues respectively) and report counts instead; Issue has no phase
	// field at all.
	Phase string

	// HasFile is true once at least one file is imported for this item, or
	// the closest available proxy for a collection kind with no hasFile
	// field of its own (Artist, Author and Comic report it via their
	// child-file counts instead -- see [describeLibraryItem]).
	HasFile bool
}

// UnmatchedEntry is one row on the Unmatched page (amendment §A3.4): one file
// from one LibraryScan's status.unmatched list (A1.5's never-guess output),
// plus enough of the scan's own identity for the row to be useful without a
// second lookup. This task's Unmatched page is read-only (G3-4 adds the
// manual-assign action once G2-4 defines its mechanism in importarr), so
// this type carries no action-oriented fields.
type UnmatchedEntry struct {
	// ScanRef identifies the LibraryScan this file was reported by.
	ScanRef types.NamespacedName

	// RootFolder is the scan's spec.rootFolderRef: which RootFolder was
	// being walked when this file turned up unattributable.
	RootFolder string

	// Path is the file path relative to the root folder.
	Path string

	// Reason explains why the scanner could not attribute the file.
	Reason string

	// Candidates lists catalog items the scanner considered but could not
	// confidently match to.
	Candidates []string

	// SeenAt is when the scanner last observed this file.
	SeenAt metav1.Time
}

// buildLibraryItems derives one [LibraryItem] per item, reusing the
// [pipeline.Entry] already computed for the same item in the same tick
// (project's own loop) for Ref, Kind and Title rather than re-deriving them:
// pkg/pipeline's describeItem already solves per-kind title extraction (the
// provider metadata's title, falling back to the object's own name) and
// nothing here needs to duplicate that. items and entries must be the same
// length and in the same order -- project's loop guarantees this by
// construction, building both from one range over items.
func buildLibraryItems(items []client.Object, entries []pipeline.Entry) []LibraryItem {
	out := make([]LibraryItem, 0, len(items))
	for i, item := range items {
		monitored, phase, hasFile := describeLibraryItem(item)
		out = append(out, LibraryItem{
			Ref:       entries[i].Ref,
			Kind:      entries[i].Kind,
			Title:     entries[i].Title,
			Monitored: monitored,
			Phase:     phase,
			HasFile:   hasFile,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
	return out
}

// describeLibraryItem reads the monitored/phase/hasFile shape of one catalog
// item. Movie, Series, Episode, Album, Book and Audiobook each carry
// status.phase directly; Issue carries hasFile but no phase at all; Artist,
// Author and Comic are collection parents with neither -- they report
// hasFile via whether any child has an imported file yet (AlbumFileCount,
// BookFileCount, IssueFileCount), the closest reading of "this collection has
// something on disk" their status offers.
func describeLibraryItem(item client.Object) (monitored bool, phase string, hasFile bool) {
	switch v := item.(type) {
	case *catalogv1.Movie:
		return monitoredOrDefault(v.Spec.Monitored), string(v.Status.Phase), v.Status.HasFile
	case *catalogv1.Series:
		return monitoredOrDefault(v.Spec.Monitored), string(v.Status.Phase), false
	case *catalogv1.Episode:
		return monitoredOrDefault(v.Spec.Monitored), string(v.Status.Phase), v.Status.HasFile
	case *catalogv1.Album:
		return monitoredOrDefault(v.Spec.Monitored), string(v.Status.Phase), v.Status.TrackFileCount > 0
	case *catalogv1.Artist:
		return monitoredOrDefault(v.Spec.Monitored), "", v.Status.AlbumFileCount > 0
	case *catalogv1.Author:
		return monitoredOrDefault(v.Spec.Monitored), "", v.Status.BookFileCount > 0
	case *catalogv1.Book:
		return monitoredOrDefault(v.Spec.Monitored), string(v.Status.Phase), v.Status.HasFile
	case *catalogv1.Audiobook:
		return monitoredOrDefault(v.Spec.Monitored), string(v.Status.Phase), v.Status.HasFile
	case *catalogv1.Comic:
		return monitoredOrDefault(v.Spec.Monitored), "", v.Status.IssueFileCount > 0
	case *catalogv1.Issue:
		return monitoredOrDefault(v.Spec.Monitored), "", v.Status.HasFile
	default:
		return true, "", false
	}
}

// monitoredOrDefault reads a spec.monitored pointer, defaulting a nil one to
// true -- every catalog kind's CRD sets +kubebuilder:default=true on this
// field, so a nil pointer (a hand-built object in a test, or one read before
// defaulting applied) means the same thing the apiserver would eventually
// write, not "false".
func monitoredOrDefault(m *bool) bool {
	return m == nil || *m
}

// listLibraryScans lists every current LibraryScan through r -- one List
// call, added to the same tick [project] already performs for the Pipeline
// and Downloads streams, per ruling R4: a third stream shares the one list
// round rather than starting a ticker of its own.
func listLibraryScans(ctx context.Context, r client.Reader) ([]catalogv1.LibraryScan, error) {
	var scans catalogv1.LibraryScanList
	if err := r.List(ctx, &scans); err != nil {
		return nil, fmt.Errorf("projection: list library scans: %w", err)
	}
	return scans.Items, nil
}

// unmatchedFromScans flattens every listed LibraryScan's status.unmatched
// into one slice, newest first across scans (each scan's own list is already
// newest-first per its doc comment, but multiple scans' entries must be
// merged and re-sorted rather than concatenated in list order, which is
// undefined across separate objects).
func unmatchedFromScans(scans []catalogv1.LibraryScan) []UnmatchedEntry {
	var out []UnmatchedEntry
	for i := range scans {
		scan := &scans[i]
		for _, u := range scan.Status.Unmatched {
			out = append(out, UnmatchedEntry{
				ScanRef:    types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name},
				RootFolder: scan.Spec.RootFolderRef,
				Path:       u.Path,
				Reason:     u.Reason,
				Candidates: u.Candidates,
				SeenAt:     u.SeenAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeenAt.After(out[j].SeenAt.Time) })
	return out
}
