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

package rollup

import (
	"slices"
	"strings"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/quality"
)

// ReasonMultiItemFile is status.audio.reason when the item's file backs
// several items (spec.mediaRef.keys): such a file is never grafted (loop
// spec §2.6, D10).
const ReasonMultiItemFile = "MultiItemFile"

// GraftObservation is what the item's key sees of a graft (loop spec §2.14):
// an open donor entry, the donor it recorded, the releases it rejected, its
// file's status.graft, and whether that file backs several items.
type GraftObservation struct {
	DonorDownloading bool
	Donor            *catalogv1alpha1.AudioDonor
	Rejected         []string
	File             *catalogv1alpha1.GraftState
	MultiItem        bool
}

// AudioStateFor is status.audio (anime dual-audio spec §5.3; loop spec
// §2.6, §2.14): the profile's wanted audio languages resolved to tags
// ("original" to originalTag, an unknown one dropped), the file's probed
// ones, and what is missing -- only when every probed track is known, since
// an untagged track may be the wanted language. While the profile grafts,
// Graft follows the item's donor and its file's status.graft (graftState).
// With no file or no audio policy it carries only the donor and the
// rejected releases, and is nil when there are none either.
func AudioStateFor(p *quality.Profile, originalTag string, mf *catalogv1alpha1.MediaFile, g GraftObservation) *catalogv1alpha1.AudioState {
	policy := p != nil && len(p.AudioLanguages) > 0
	if mf == nil || !policy {
		if g.Donor == nil && len(g.Rejected) == 0 {
			return nil
		}
		st := &catalogv1alpha1.AudioState{Donor: g.Donor.DeepCopy(), RejectedReleases: slices.Clone(g.Rejected)}
		if policy {
			st.Wanted = wantedTags(p, originalTag)
		}
		return st
	}
	st := &catalogv1alpha1.AudioState{Wanted: wantedTags(p, originalTag), Donor: g.Donor.DeepCopy(), RejectedReleases: slices.Clone(g.Rejected)}
	st.Present = ProbedAudioLanguages(mf)
	if st.Present != nil {
		base := func(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }
		for _, w := range st.Wanted {
			found := false
			for _, h := range st.Present {
				if base(h) == base(w) {
					found = true
					break
				}
			}
			if !found {
				st.Missing = append(st.Missing, w)
			}
		}
	}
	if p.GraftAudio {
		st.Graft, st.Reason = graftState(len(st.Missing) > 0, g, mf.Status.GraftTag)
	}
	return st
}

// GraftAnchor is the language a graft of an item aligns on, the track its
// file and a donor must both carry: the item's original language, or, when
// its file is known to lack that (a.Missing names it), the first wanted
// language the file does carry. Such a file is a dub taken alone as a last
// resort (decision.DubAlone; owner, 2026-11-27: "Allow dubs-only (or vice
// versa - *with* the graft)"): its missing language is the original, and
// its donor a release carrying that beside the dub, the two dubs aligned
// against each other. "" when original is, and when the file carries no
// wanted language at all, which nothing can be aligned on. The donor
// search, the donor's import and the graft planner all take it from here,
// so the three agree on the track.
func GraftAnchor(original string, a *catalogv1alpha1.AudioState) string {
	if original == "" || a == nil {
		return original
	}
	base := func(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }
	has := func(tags []string, l string) bool {
		return slices.ContainsFunc(tags, func(t string) bool { return base(t) == base(l) })
	}
	want := original
	if t, ok := lang.Normalize(original); ok {
		want = string(t)
	}
	if !has(a.Missing, want) {
		return original
	}
	for _, w := range a.Wanted {
		if has(a.Present, w) {
			return w
		}
	}
	return ""
}

// wantedTags is the profile's audio languages as tags, its optional ones
// (Profile.AudioOptional: an English dub still airing) after them:
// "original" is originalTag, an unknown one is dropped, each once (an
// English original is wanted once). An optional language the file lacks is
// missing like any other, so the donor search keeps looking for it, though
// the item meets the cutoff without it.
func wantedTags(p *quality.Profile, originalTag string) []string {
	return append(decision.WantedAudio(*p, originalTag), decision.OptionalAudio(*p, originalTag)...)
}

// GraftDonorSearches is how many donor searches an item whose file lacks a
// wanted language is given before its graft is no longer counted on
// (GraftOpen).
const GraftDonorSearches = 3

// GraftOpen reports an item whose file lacks a wanted language and whose
// graft can still supply it: a donor is downloading or the graft is under
// way, or the donor search has not yet run GraftDonorSearches times without
// one. The grab planner passes a torrent pack over for such an item (owner,
// 2026-11-27: "download the jpn release and an eng release and perform the
// audio graft. If no eng release exists whatsoever that we can download
// (unlikely) - we can then finally fallback to a full season re-download
// from torrent for dual"). A file that backs several items is never
// grafted, so its graft is not open.
func GraftOpen(a *catalogv1alpha1.AudioState, donorSearches commonv1.Attempts) bool {
	if a == nil || len(a.Missing) == 0 {
		return false
	}
	switch a.Graft {
	case "grabbed", "pending", "aligned":
		return true
	case "searching":
		return donorSearches.Count < GraftDonorSearches
	case "failed":
		return a.Reason != ReasonMultiItemFile && donorSearches.Count < GraftDonorSearches
	}
	return false
}

// graftState is status.audio.graft (anime dual-audio spec §6, §9; loop spec
// §2.14) and, when failed, why: searching while languages are missing and
// nothing is under way (the wanted sweep's donor search); grabbed while a
// donor downloads; pending while the file's graft waits or runs, aligned
// once it has aligned; failed after a failure, and for a file that backs
// several items; done once a graft has left nothing missing.
func graftState(missing bool, g GraftObservation, graftTag string) (string, string) {
	b := g.File
	if !missing {
		if graftTag != "" || b != nil && b.Phase == catalogv1alpha1.GraftPhaseSucceeded && b.Reason == grafted {
			return "done", ""
		}
		return "none", ""
	}
	switch {
	case g.MultiItem:
		return "failed", ReasonMultiItemFile
	case g.DonorDownloading:
		return "grabbed", ""
	case b == nil && g.Donor == nil:
		return "searching", ""
	case b == nil:
		return "pending", ""
	case b.Phase == catalogv1alpha1.GraftPhaseFailed:
		reason := b.Reason
		if b.Message != "" {
			reason += ": " + b.Message
		}
		if len(reason) > catalogv1alpha1.MaxAudioReasonLength {
			reason = reason[:catalogv1alpha1.MaxAudioReasonLength]
		}
		return "failed", strings.ToValidUTF8(reason, "")
	case b.Phase == catalogv1alpha1.GraftPhaseRunning && len(b.Segments) > 0:
		return "aligned", ""
	}
	return "pending", ""
}

// grafted is the reason of a graft that swapped the file (grafttask's
// ReasonGrafted, which this package does not import).
const grafted = "Grafted"

// AudioStateAC renders status.audio, the one renderer the Movie and Episode
// reconcilers (the loop's item keys after F4.2) share for their happy paths
// and early returns alike. It sends every field a carries, the donor and
// the rejected releases included (TestAudioStateACIsLossless): a field it
// left out would be released by the item's next apply. It also bounds what
// it sends (loop spec §2.6, §2.11.1): text has the runes a server-side apply
// cannot carry replaced and is cut to its MaxLength, a donor whose path is
// over-long or carries such a rune is left out rather than rewritten into a
// path that does not exist, and only the newest MaxRejectedReleases
// rejected releases are kept.
func AudioStateAC(a *catalogv1alpha1.AudioState) *catalogac.AudioStateApplyConfiguration {
	ac := catalogac.AudioState().WithWanted(a.Wanted...).WithPresent(a.Present...).WithMissing(a.Missing...)
	if a.Graft != "" {
		ac = ac.WithGraft(a.Graft)
	}
	if a.Reason != "" {
		ac = ac.WithReason(audioText(a.Reason, catalogv1alpha1.MaxAudioReasonLength))
	}
	if d := a.Donor; d != nil && len(d.Path) <= catalogv1alpha1.MaxPathLength && !k8s.HasUnapplyable(d.Path) {
		dac := catalogac.AudioDonor().
			WithPath(d.Path).
			WithRelease(audioText(d.Release, catalogv1alpha1.MaxReleaseTitleLength)).
			WithImportedAt(d.ImportedAt)
		if d.DownloadRef != "" {
			dac = dac.WithDownloadRef(d.DownloadRef)
		}
		ac = ac.WithDonor(dac)
	}
	rejected := a.RejectedReleases
	if n := len(rejected) - catalogv1alpha1.MaxRejectedReleases; n > 0 {
		rejected = rejected[n:] // oldest first: drop from the front
	}
	for _, r := range rejected {
		ac = ac.WithRejectedReleases(audioText(r, catalogv1alpha1.MaxReleaseTitleLength))
	}
	return ac
}

// audioText is s safe to apply and within maxBytes.
func audioText(s string, maxBytes int) string {
	return k8s.ClampText(k8s.SanitizeText(s), maxBytes)
}
