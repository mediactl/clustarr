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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// AnnotationObservedFingerprint is how a library scan tells catalog that a
// post-transcode file changed on disk. Its value is the file's size and
// mtime as the walk found them, "<sizeBytes>@<RFC 3339 mtime, UTC, whole
// seconds>" ([ObservedFingerprint]).
//
// Once catalog incorporates a transcode swap (spec.original false) it owns
// spec.sizeBytes, spec.modTime and spec.original, so a scan never re-applies
// them. When the bytes have changed -- a re-transcode, a person replacing
// the file in place -- the manager's scan applier applies this one
// annotation instead, under k8s.ManagerImport, which owns nothing else on
// any MediaFile, so the apply releases nothing (ADR-0019 §7.6, W17).
// catalog's MediaFile controller declares the same key.
const AnnotationObservedFingerprint = "catalog.clustarr.io/observed-fingerprint"

// ObservedFingerprint renders [AnnotationObservedFingerprint]'s value for a
// file of size bytes last modified at modTime.
func ObservedFingerprint(size int64, modTime time.Time) string {
	return strconv.FormatInt(size, 10) + "@" + modTime.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// SpecHash is the SHA-256, in hex, of a MediaFile's spec as the apiserver
// stores it: import-worker's set (the observed path, size and mtime and the
// fields frozen at import) and the size, mtime, original flag and path
// catalog takes over after a transcode swap. A library scan's observation
// carries it as its basis (schema.MediaFileBasis.SpecHash), and the
// manager's scan applier refuses to apply over a MediaFile whose spec no
// longer hashes to it: another write moved the file since the agent read it
// (ADR-0019 §7.6, "stale basis").
func SpecHash(s catalogv1alpha1.MediaFileSpec) string {
	s.ModTime = metav1.NewTime(s.ModTime.UTC().Truncate(time.Second))
	data, err := json.Marshal(s)
	if err != nil {
		// A MediaFileSpec is plain data; Marshal cannot fail on it. An
		// empty hash matches no basis, so the applier re-observes.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FrozenToSchema is f as a scan observation carries it (schema.FrozenFields).
// ImportedFrom is not carried: a scan's provenance is the observation's
// Manual flag, which the applier renders ([FrozenFromScan]).
func FrozenToSchema(f Frozen) *schema.FrozenFields {
	out := &schema.FrozenFields{
		Quality: f.Quality, Revision: f.Revision, ReleaseType: f.ReleaseType,
		Edition: f.Edition, Languages: f.Languages, FormatScore: f.FormatScore,
		MatchedFormats: f.MatchedFormats, ProfileHash: f.ProfileHash, Original: f.Original,
		Track: f.Track,
	}
	if f.ReleaseGroup != nil {
		out.ReleaseGroup = *f.ReleaseGroup
	}
	return out
}

// FrozenFromScan is the Frozen a scan observation's fields freeze into a new
// MediaFile: f, and for a person's manual assignment spec.importedFrom with
// manual true and importedAt at. A nil f freezes nothing.
func FrozenFromScan(f *schema.FrozenFields, manual bool, at time.Time) Frozen {
	var out Frozen
	if f != nil {
		out = Frozen{
			Quality: f.Quality, Revision: f.Revision, ReleaseType: f.ReleaseType,
			Edition: f.Edition, Languages: f.Languages, FormatScore: f.FormatScore,
			MatchedFormats: f.MatchedFormats, ProfileHash: f.ProfileHash, Original: f.Original,
			Track: f.Track,
		}
		if f.ReleaseGroup != "" {
			g := f.ReleaseGroup
			out.ReleaseGroup = &g
		}
	}
	if manual {
		out.ImportedFrom = catalogac.ImportSource().
			WithManual(true).
			WithImportedAt(metav1.NewTime(at))
	}
	return out
}

// MissingGrace is how long the MediaFile reconciler must have seen a file
// missing (Ready=False, FileMissing) before a scan's missing observation
// removes its record: a share that blinks, or a file mid-move, is not a file
// gone (P46).
const MissingGrace = 10 * time.Minute

// SeenMissing reports whether the MediaFile reconciler has had mf's file
// missing for at least [MissingGrace]. The rescan reports a record only
// when it holds (and the file is ENOENT), and the manager's scan applier
// deletes one only when it still holds on a fresh read.
func SeenMissing(mf *catalogv1alpha1.MediaFile, now time.Time) bool {
	c := meta.FindStatusCondition(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "FileMissing" &&
		now.Sub(c.LastTransitionTime.Time) >= MissingGrace
}
