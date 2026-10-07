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

package importplan

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// MediaKindOf is an ItemRef kind ("Movie") as a MediaRef kind ("movie").
func MediaKindOf(kind string) commonv1.MediaKind { return commonv1.MediaKind(strings.ToLower(kind)) }

// multiFileKind reports whether one item of kind is a set of files (an
// album's tracks, an audiobook's parts).
func multiFileKind(kind commonv1.MediaKind) bool {
	return kind == commonv1.MediaKindAlbum || kind == commonv1.MediaKindAudiobook
}

// nonVideoKind reports whether files of kind are judged by their extension
// or an audio probe rather than a video probe.
func nonVideoKind(kind commonv1.MediaKind) bool {
	return kind == commonv1.MediaKindAlbum || kind == commonv1.MediaKindBook ||
		kind == commonv1.MediaKindAudiobook || kind == commonv1.MediaKindIssue
}

// candidate is one inspected file the planner may place, with the rank it
// is ordered by (order.go's, moved).
type candidate struct {
	f    schema.InspectedFile
	kind commonv1.MediaKind
	keys []string
	rank candidateRank
}

// candidateRank orders candidates for one item, best first: a quality the
// profile allows before one it does not (or cannot place), then the
// profile's own tier order, then the higher revision, then the larger file.
type candidateRank struct {
	allowed bool
	tier    int
	version int32
	real    int32
	size    int64
}

func rankOf(p quality.Profile, f schema.InspectedFile) candidateRank {
	r := candidateRank{size: f.SizeBytes}
	if f.Frozen.Revision != nil {
		r.version, r.real = f.Frozen.Revision.Version, f.Frozen.Revision.Real
	}
	if q := f.Frozen.Quality; q != nil {
		if idx, ok := p.Index(*q); ok && p.Allowed(*q) {
			r.allowed, r.tier = true, idx
		}
	}
	return r
}

func compareRank(a, b candidateRank) int {
	switch {
	case a.allowed != b.allowed:
		if a.allowed {
			return -1
		}
		return 1
	case a.tier != b.tier:
		return cmp.Compare(a.tier, b.tier) // tiers are listed best first
	case a.version != b.version:
		return cmp.Compare(b.version, a.version)
	case a.real != b.real:
		return cmp.Compare(b.real, a.real)
	default:
		return cmp.Compare(b.size, a.size)
	}
}

// sortCandidates orders cs best first; equal candidates keep walk order.
func sortCandidates(cs []candidate) {
	slices.SortStableFunc(cs, func(a, b candidate) int { return compareRank(a.rank, b.rank) })
}

// lateRejection is the rejection for a candidate ranked after the file that
// filled its item. A file the profile does not allow keeps that reason.
func (c candidate) lateRejection(rel, item, by string) Rejection {
	if q := c.f.Frozen.Quality; q != nil && !c.rank.allowed {
		return notAllowedRejection(rel, *q)
	}
	return filledRejection(rel, item, by)
}

// keysOf is the item names a file covers: its Keys, else its target.
func keysOf(f schema.InspectedFile) []string {
	if len(f.Keys) > 0 {
		return f.Keys
	}
	if f.Proposed != nil {
		return []string{f.Proposed.Name}
	}
	return nil
}

// ownEarlierAttempt reports whether mf is this entry's own file from an
// earlier execute (a re-inspect after the files landed): its importedFrom
// names the entry and it was imported no earlier than the grab, and it is
// still what the import placed -- a file transcoded since stays under the
// gates. Against itself, a file is never an upgrade.
func ownEarlierAttempt(mf *catalogv1alpha1.MediaFile, e *catalogv1alpha1.DownloadEntry) bool {
	src := mf.Spec.ImportedFrom
	if src == nil || src.DownloadRef == "" || src.DownloadRef != e.ID {
		return false
	}
	if src.ImportedAt.IsZero() || src.ImportedAt.Before(&e.GrabbedAt) {
		return false
	}
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		return false
	}
	if mi := mf.Status.MediaInfo; mi != nil && mi.TranscodeProfile != "" {
		return false
	}
	return true
}

// comparedFiles is existing without the entry's own earlier files: the
// files an import's gates judge it against. It never aliases existing.
func comparedFiles(existing []catalogv1alpha1.MediaFile, e *catalogv1alpha1.DownloadEntry) []catalogv1alpha1.MediaFile {
	out := make([]catalogv1alpha1.MediaFile, 0, len(existing))
	for i := range existing {
		if !ownEarlierAttempt(&existing[i], e) {
			out = append(out, existing[i])
		}
	}
	return out
}

// userChosen reports whether the entry's files are a person's choice: an
// interactive grab or a manual import (the entry's manual, or the import
// intent's override).
func userChosen(e *catalogv1alpha1.DownloadEntry, manual bool) bool {
	return manual || e.GrabbedBy == commonv1.GrabSourceInteractive
}

// transcodedRejection is the refusal of a file that would replace existing
// when existing is transcoded and the grab automatic (P58): a transcoded
// file is final, and only a person's choice replaces it.
func transcodedRejection(rel string, existing *catalogv1alpha1.MediaFile, e *catalogv1alpha1.DownloadEntry, manual bool) Rejection {
	if !existing.Transcoded() || userChosen(e, manual) {
		return Rejection{}
	}
	source := string(e.GrabbedBy)
	if source == "" {
		source = "unrecorded"
	}
	r := itemStateRejection("%s: %s %s's existing file (MediaFile %s) is transcoded, and a transcoded file is final: "+
		"an automatic grab (grabbedBy %s) never replaces it; only an interactive grab or a manual import does",
		rel, existing.Spec.MediaRef.Kind, existing.Spec.MediaRef.Name, existing.Name, source)
	r.Transcoded = true
	return r
}

// verdictRejection is a file refused as no upgrade: as a rule the item's
// state, but a file of a worse tier than the quality the release advertised
// is a mislabelled release, its fault.
func verdictRejection(p quality.Profile, e *catalogv1alpha1.DownloadEntry, actual commonv1.Quality, text string) Rejection {
	if advertised := e.Release.Quality; advertised.Name != "" {
		want, okWant := p.Index(advertised)
		got, okGot := p.Index(actual)
		if okWant && okGot && got > want {
			return releaseFaultRejection("%s; the release advertised %s, the file is %s", text, advertised.Name, actual.Name)
		}
	}
	return itemStateRejection("%s", text)
}

// replacesWrongLanguage reports whether an incoming file may replace
// existing although it is no upgrade: existing's probed audio lacks the
// profile's language (decision.LacksLanguage) and the incoming file's own
// probe carries it (anime dual-audio spec §5.3).
func replacesWrongLanguage(p quality.Profile, originalTag string, existing *catalogv1alpha1.MediaFile, incoming *schema.ProbeSummary) bool {
	have := decision.AudioLanguages(existing.Status.MediaInfo)
	if !decision.LacksLanguage(p, originalTag, have) {
		return false
	}
	if incoming == nil || len(incoming.AudioLanguages) == 0 {
		return false
	}
	return !decision.LacksLanguage(p, originalTag, incoming.AudioLanguages)
}

// relPath renders path relative to root for a message, falling back to the
// path itself.
func relPath(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

func baseTag(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }
