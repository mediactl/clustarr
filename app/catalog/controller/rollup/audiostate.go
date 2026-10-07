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
	"context"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/quality"
)

// AudioStateFor is status.audio (anime dual-audio spec §5.3): the profile's
// wanted audio languages resolved to tags ("original" to originalTag, an
// unknown one dropped), the file's probed ones, and what is missing -- only
// when every probed track is known, since an untagged track may be the
// wanted language. While the profile grafts, Graft follows the item's donor
// and AudioGraft (graftState). Nil when there is no file or no audio policy.
func AudioStateFor(p *quality.Profile, originalTag string, mf *catalogv1alpha1.MediaFile, g GraftObservation) *catalogv1alpha1.AudioState {
	if p == nil || len(p.AudioLanguages) == 0 || mf == nil {
		return nil
	}
	st := &catalogv1alpha1.AudioState{}
	for _, l := range p.AudioLanguages {
		if l == "original" {
			l = originalTag
		}
		if t, ok := lang.Normalize(l); ok && !slices.Contains(st.Wanted, string(t)) {
			st.Wanted = append(st.Wanted, string(t)) // an English original is wanted once
		}
	}
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
	if p.AudioGraft {
		st.Graft, st.Reason = graftState(len(st.Missing) > 0, g)
	}
	return st
}

// GraftObservation is what the item's reconciler sees of a graft: an open
// donor Download of the item, and the item's AudioGraft (nil: none).
type GraftObservation struct {
	DonorDownloading bool
	Graft            *transcodev1alpha1.AudioGraft
}

// graftState is status.audio.graft (anime dual-audio spec §6, §9) and, when
// failed, why: searching while languages are missing and nothing is under
// way (the wanted sweep's donor search); grabbed while a donor downloads;
// pending while the graft waits or runs, aligned once it has aligned;
// failed after a failure; done once a graft has left nothing missing.
func graftState(missing bool, g GraftObservation) (string, string) {
	a := g.Graft
	if !missing {
		if a != nil && a.Status.Phase == transcodev1alpha1.AudioGraftSucceeded && a.Status.GraftTag != "" {
			return "done", ""
		}
		if a != nil && a.Status.Phase == transcodev1alpha1.AudioGraftSucceeded && a.Status.Reason == "Grafted" {
			return "done", ""
		}
		return "none", ""
	}
	if g.DonorDownloading {
		return "grabbed", ""
	}
	if a == nil {
		return "searching", ""
	}
	switch a.Status.Phase {
	case transcodev1alpha1.AudioGraftFailed:
		reason := a.Status.Reason
		if a.Status.Message != "" {
			reason += ": " + a.Status.Message
		}
		if len(reason) > 1024 {
			reason = reason[:1024]
		}
		return "failed", strings.ToValidUTF8(reason, "")
	case transcodev1alpha1.AudioGraftRunning:
		if len(a.Status.Segments) > 0 {
			return "aligned", ""
		}
	}
	return "pending", ""
}

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

// ItemOfAudioGraft maps an AudioGraft to its item of kind, for the Episode
// and Movie watches that keep status.audio.graft current.
func ItemOfAudioGraft(kind commonv1.MediaKind) func(context.Context, client.Object) []reconcile.Request {
	return func(_ context.Context, o client.Object) []reconcile.Request {
		g, ok := o.(*transcodev1alpha1.AudioGraft)
		if !ok || g.Spec.ItemRef.Kind != kind || g.Spec.ItemRef.Name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: g.Namespace, Name: g.Spec.ItemRef.Name}}}
	}
}

// AudioGraftState is what of an AudioGraft's status moves graftState.
func AudioGraftState(o client.Object) string {
	g, ok := o.(*transcodev1alpha1.AudioGraft)
	if !ok {
		return ""
	}
	return string(g.Status.Phase) + "|" + g.Status.Reason + "|" + g.Status.GraftTag + "|" + strconv.Itoa(len(g.Status.Segments))
}
