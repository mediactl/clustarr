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

package mediafilespec

import (
	"os"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// FieldManager is the server-side-apply field manager every write in this
// package and in the rescan worker (app/import/worker/rescan) uses, on the
// Movie the rescan attributes a file to and on the MediaFile itself. It is
// k8s.ManagerImportarrWorker, NOT k8s.ManagerImportarr, and the difference
// is not cosmetic.
//
// This worker runs on the same replicas as importarr's controllers --
// run.go adds it with mgr.Add(k8s.EveryReplica(...)), inside the same
// manager process -- and those controllers write under ManagerImportarr.
// Server-side apply replaces a manager's whole ownership set on every apply
// rather than merging it, so two writers sharing ONE manager name on one
// object silently release each other's fields. Today they never meet on one
// object, so the name was inert; M3's fileimport consumer writes
// Download.status.import AND MediaFileSpec from these same replicas, which
// is exactly the shape that forced catalogarr-worker to split into
// catalogarr-metadata and catalogarr-grab after both directions of that
// release were reproduced against a real apiserver. Splitting before the
// collision costs one word; splitting after it costs a debugging session.
//
// It is exported so the tests that assert the split -- catalogarr's
// mandatory two-writer gate above all, which has to stand in for this
// worker rather than run it -- name the manager production actually uses
// instead of restating a constant that can drift away from it. It had
// drifted: production wrote as ManagerImportarr while the gate, and
// k8s.ManagerImportarrWorker's own doc comment, described ManagerImportarr-
// Worker.
const FieldManager = k8s.ManagerImportarrWorker

// StaleReadError is an apply to an existing MediaFile that the apiserver
// refused because the object changed after the caller read it: the
// resourceVersion precondition failed. Key and RV name the object and the
// resourceVersion the refused apply carried.
type StaleReadError struct {
	Key types.NamespacedName
	RV  string
	Err error
}

func (e *StaleReadError) Error() string { return e.Err.Error() }
func (e *StaleReadError) Unwrap() error { return e.Err }

// SameFingerprint compares the stored size and mtime with what is on disk.
// Both sides are truncated to whole seconds because metav1.Time serialises as
// RFC 3339, so a stored mtime has already lost its sub-second part and an
// exact comparison would never hold.
func SameFingerprint(mf *catalogv1alpha1.MediaFile, info os.FileInfo) bool {
	if mf.Spec.SizeBytes != info.Size() {
		return false
	}
	stored := mf.Spec.ModTime.Time.UTC().Truncate(time.Second)
	onDisk := info.ModTime().UTC().Truncate(time.Second)
	return !stored.IsZero() && stored.Equal(onDisk)
}

// Frozen is what a first sighting of a file freezes into
// MediaFileSpec beyond the observed path, size and mtime (spec §8.4). A nil
// or empty field is not sent, so this manager never claims a field it has
// nothing to say about.
type Frozen struct {
	Quality        *commonv1.Quality
	Revision       *commonv1.Revision
	ReleaseType    commonv1.ReleaseType
	ReleaseGroup   *string
	Edition        *string
	Languages      []string
	ImportedFrom   *catalogac.ImportSourceApplyConfiguration
	FormatScore    *int32
	MatchedFormats []string
	ProfileHash    string
	Original       *bool
	// Track narrows an album MediaRef to one recording; see
	// commonv1.MediaRef.Track.
	Track string
}

// ReassertFrozen reads every field this manager owns back off an existing
// spec, sending only what is set.
func ReassertFrozen(s *catalogv1alpha1.MediaFileSpec) Frozen {
	var f Frozen
	if s.Quality != (commonv1.Quality{}) {
		f.Quality = &s.Quality
	}
	if s.Revision != (commonv1.Revision{}) {
		f.Revision = &s.Revision
	}
	f.ReleaseType = s.ReleaseType
	if s.ReleaseGroup != "" {
		f.ReleaseGroup = &s.ReleaseGroup
	}
	if s.Edition != "" {
		f.Edition = &s.Edition
	}
	f.Languages = s.Languages
	if s.FormatScore != 0 {
		f.FormatScore = &s.FormatScore
	}
	f.MatchedFormats = s.MatchedFormats
	f.ProfileHash = s.ProfileHash
	f.Original = s.Original
	if src := s.ImportedFrom; src != nil {
		ac := catalogac.ImportSource()
		if src.DownloadRef != "" {
			ac = ac.WithDownloadRef(src.DownloadRef)
		}
		if src.ReleaseTitle != "" {
			ac = ac.WithReleaseTitle(src.ReleaseTitle)
		}
		if src.IndexerName != "" {
			ac = ac.WithIndexerName(src.IndexerName)
		}
		if src.Protocol != "" {
			ac = ac.WithProtocol(src.Protocol)
		}
		if !src.ImportedAt.IsZero() {
			ac = ac.WithImportedAt(src.ImportedAt)
		}
		if src.Manual {
			ac = ac.WithManual(true)
		}
		if src.InfoHash != "" {
			ac = ac.WithInfoHash(src.InfoHash)
		}
		f.ImportedFrom = ac
	}
	return f
}

// FrozenFromSchema is the Frozen an import's plan carries (schema.
// FrozenFields, frozen by the inspect) with from as spec.importedFrom: the
// manager materialises a placed file's MediaFile from it (ADR-0019 §6.9).
func FrozenFromSchema(f schema.FrozenFields, from catalogv1alpha1.ImportSource) Frozen {
	out := Frozen{
		Quality: f.Quality, Revision: f.Revision, ReleaseType: f.ReleaseType,
		Edition: f.Edition, Languages: f.Languages, FormatScore: f.FormatScore,
		MatchedFormats: f.MatchedFormats, ProfileHash: f.ProfileHash, Original: f.Original,
		Track: f.Track,
	}
	if f.ReleaseGroup != "" {
		g := f.ReleaseGroup
		out.ReleaseGroup = &g
	}
	src := catalogac.ImportSource().
		WithImportedAt(from.ImportedAt).
		WithManual(from.Manual)
	if from.DownloadRef != "" {
		src = src.WithDownloadRef(from.DownloadRef)
	}
	if from.ReleaseTitle != "" {
		src = src.WithReleaseTitle(from.ReleaseTitle)
	}
	if from.IndexerName != "" {
		src = src.WithIndexerName(from.IndexerName)
	}
	if from.Protocol != "" {
		src = src.WithProtocol(from.Protocol)
	}
	if from.InfoHash != "" {
		src = src.WithInfoHash(from.InfoHash)
	}
	out.ImportedFrom = src
	return out
}
