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

package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// ProfileHardware is the class a profile's job plans for: the job's
// spec.hardware when it pins one, else the profile's; auto with no class
// chosen yet plans for the CPU. The TranscodeJob controller and the worker
// both ask here, so their plans hash alike.
func ProfileHardware(spec transcodev1alpha1.TranscodeProfileSpec, hardware *transcodev1alpha1.Hardware) transcode.Hardware {
	hw := spec.Hardware
	if hardware != nil && *hardware != "" && *hardware != transcodev1alpha1.HardwareAuto {
		hw = *hardware
	}
	if hw == transcodev1alpha1.HardwareAuto || hw == "" {
		hw = transcodev1alpha1.HardwareCPU
	}
	return transcode.Hardware(hw)
}

// asSet is a list the hash reads as a set: deduplicated, empty as nil, in
// descending order -- any fixed order makes the hash order-independent, and
// descending leaves the CRD default modifiers {"remux","brdisk"} as they are,
// so no hash already stored and tagged changed when sets came in.
func asSet(l []string) []string {
	if len(l) == 0 {
		return nil
	}
	out := slices.Clone(l)
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(b, a) })
	return slices.Compact(out)
}

// ProfileHash is a profile's status.hash: [ProfileHashAt] the standard's
// current version.
func ProfileHash(spec transcodev1alpha1.TranscodeProfileSpec) string {
	return ProfileHashAt(spec, standard.Version)
}

// ProfileHashAt is the sha256 of what the standard reads from a profile --
// quality (unset as its default), container (unset as mkv), the audio
// languages, the never-transcode modifiers and policy.minDuration -- the
// size limit that decides whether its output is kept
// (policy.maxOutputToSourcePercent; both unset as their defaults), and the
// standard's version. An edit to any of them names new jobs, so a file a
// terminal job skipped or failed under the old values is planned again.
// The scheduling fields (selector, hardware, resources, ...) and
// replaceSource/recycleBin decide where a transcode runs and what happens
// to the source, never whether or what it writes, so they do not reach it.
//
// The hash names each TranscodeJob and is the CLUSTARR_PROFILE tag, but a
// new one re-transcodes nothing: a file that carries any transcode tag, or
// another tool's HEVC encode, is final (catalogv1alpha1.MediaFile.Transcoded).
func ProfileHashAt(spec transcodev1alpha1.TranscodeProfileSpec, version int) string {
	container := spec.Container
	if container == "" {
		container = transcodev1alpha1.ContainerMKV
	}
	b, _ := json.Marshal(struct {
		Version                  int
		Quality                  int32
		Container                transcodev1alpha1.Container
		Languages                []string
		NeverTranscodeModifiers  []string
		MinDuration              time.Duration
		MaxOutputToSourcePercent int32
	}{
		version, spec.QualityOrDefault(), container, asSet(spec.Audio.Languages), asSet(spec.Policy.NeverTranscodeModifiers),
		MinDuration(spec.Policy), MaxOutputToSourcePercent(spec.Policy),
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ReplaceSource is policy.replaceSource with its CRD default applied: unset
// means true. The pointer exists so a Go client can say false; nil must
// still read as the default, because a spec built in Go and never
// round-tripped through the apiserver has nil here, and the kubebuilder
// default is applied only to what the apiserver stores.
func ReplaceSource(p transcodev1alpha1.PolicySpec) bool { return ptr.Deref(p.ReplaceSource, true) }

// RecycleBin is policy.recycleBin with its CRD default applied: unset
// means true.
func RecycleBin(p transcodev1alpha1.PolicySpec) bool { return ptr.Deref(p.RecycleBin, true) }

// DefaultMinDuration mirrors policy.minDuration's +kubebuilder:default="1m".
const DefaultMinDuration = time.Minute

// MinDuration is policy.minDuration with its CRD default applied: unset
// means [DefaultMinDuration], an explicit 0s considers every file.
func MinDuration(p transcodev1alpha1.PolicySpec) time.Duration {
	if p.MinDuration == nil {
		return DefaultMinDuration
	}
	return p.MinDuration.Duration
}

// DefaultMaxOutputToSourcePercent mirrors policy.maxOutputToSourcePercent's
// +kubebuilder:default=100.
const DefaultMaxOutputToSourcePercent = 100

// MaxOutputToSourcePercent is policy.maxOutputToSourcePercent with its CRD
// default applied: unset means 100, an explicit 0 disables the check.
func MaxOutputToSourcePercent(p transcodev1alpha1.PolicySpec) int32 {
	return ptr.Deref(p.MaxOutputToSourcePercent, DefaultMaxOutputToSourcePercent)
}

// DefaultActiveDeadline is the per-task deadline when the profile sets none.
// app/squash/controller/pool/template_test.go:TestFlooredDefaultsMatchTheGeneratedCRD
// holds it to the CRD default.
const DefaultActiveDeadline = 48 * time.Hour

// ActiveDeadline is a profile's per-task deadline, enforced by the worker.
func ActiveDeadline(p transcodev1alpha1.TranscodeProfileSpec) time.Duration {
	if p.ActiveDeadline.Duration > 0 {
		return p.ActiveDeadline.Duration
	}
	return DefaultActiveDeadline
}
